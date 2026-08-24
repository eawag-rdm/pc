package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/eawag-rdm/pc/internal/cpucap"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/server"
)

func main() {
	// Before anything else, including flag parsing: the PDF pool extracts in
	// worker processes that are this binary re-executed with an argv sentinel,
	// and such a process runs the worker loop instead of a server.
	readers.HandlePDFWorkerSentinel()

	// The server is configured entirely from the TOML file - no tunable flags.
	// The only argument is the optional config-file location (with a sensible
	// search fallback); everything else, including the listen address, lives in
	// the [server] section of pc.toml.
	configPath := flag.String("config", "", "Path to PC config file (pc.toml); if omitted, standard locations are searched")
	help := flag.Bool("help", false, "Show usage information")
	flag.Parse()

	if *help {
		printUsage()
		return
	}

	// Find config file if not specified
	if *configPath == "" {
		*configPath = config.FindConfigFile()
		if *configPath == "" {
			log.Fatal("Error: No config file found. Please specify with -config flag.")
		}
	}

	// Pin the CPU budget HERE, at the composition root, before anything sizes
	// itself from it: every pool reads the budget per pass. The cap applies
	// whether or not the operator set one, so it is logged rather than left to
	// be discovered on a machine with more cores than the default. Both numbers
	// are logged: the machine, a cgroup quota or GOMAXPROCS can bind below the cap.
	pcConfig, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	maxCores := pcConfig.General.EffectiveMaxCores()
	budget := cpucap.Apply(maxCores)
	log.Printf("CPU budget: %d core(s) ([general] maxCores %d)", budget, maxCores)

	// Create server configuration. The listen address comes from the TOML
	// ([server] listenAddress), not a flag, so Address is left empty here.
	// VerifyTLS is left nil so it falls back to the PC config's CkanCollector
	// "verify" attr (and finally the secure default of true). The config read
	// above is handed in so the cap and the rules come from one read; ConfigPath
	// stays set for logging and any path-based re-read.
	cfg := server.Config{
		ConfigPath: *configPath,
		PCConfig:   pcConfig,
	}

	// Create server
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Set up graceful shutdown (§9). A SIGINT/SIGTERM triggers Shutdown, which
	// flips the draining flag (new requests get server_restarting) and drains
	// in-flight analyses. srv.DrainTimeout() is derived from the configured
	// request timeout, so the drain outlasts any analysis that observes its
	// deadline (cancellation is polled between files/checks; the orchestrator's
	// SIGKILL remains the backstop).
	shutdownComplete := make(chan struct{})
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		// A drain can outlast the operator's patience, so a second signal must
		// end the process. It is handled rather than left to the OS default:
		// the shipped container runs this binary as PID 1, where the default
		// disposition of a signal is a no-op, and an installed handler is
		// delivered all the same. The channel therefore stays subscribed, with
		// no window in which a second signal lands unread.
		go func() {
			<-quit
			log.Println("second signal received, forcing exit")
			os.Exit(1)
		}()
		log.Println("Server is shutting down... (a second signal forces quit)")

		ctx, cancel := context.WithTimeout(context.Background(), srv.DrainTimeout())
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Could not gracefully shutdown the server: %v", err)
		} else {
			// Only on a drain that finished: then no worker is mid-job. This
			// merely ends idle workers a moment before the process exit that
			// closes their pipes anyway - the EOF that reaps them on every
			// path, with the Linux parent-death signal as the backstop for a
			// parent killed outright.
			readers.ClosePDFWorkers()
		}
		close(shutdownComplete)
	}()

	// Start the server. ListenAndServe returns http.ErrServerClosed only on a
	// graceful Shutdown; any OTHER error (e.g. a failed bind because the port is
	// already in use or the address is invalid) is fatal and must exit non-zero
	// instead of blocking on the shutdown channel forever (the previous bug).
	err = srv.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Server failed to start: %v", err)
	}

	// Graceful path: ListenAndServe returned ErrServerClosed because Shutdown was
	// called. Wait for the drain to finish before exiting.
	<-shutdownComplete
	log.Println("Server stopped")
}

func printUsage() {
	log.Println("PC Server - REST API for Package Checker")
	log.Println("")
	log.Println("Usage:")
	log.Println("  pc-server [flags]")
	log.Println("")
	log.Println("Flags:")
	flag.PrintDefaults()
	log.Println("")
	log.Println("Environment Variables:")
	log.Println("  (none currently)")
	log.Println("")
	log.Println("Examples:")
	log.Println("  pc-server                       # search standard locations for pc.toml")
	log.Println("  pc-server -config /etc/pc/pc.toml")
	log.Println("")
	log.Println("The listen address and all other settings come from the [server]")
	log.Println("section of the config file; the server takes no tunable flags.")
	log.Println("")
	log.Println("API Endpoints:")
	log.Println("  GET  /health              - Liveness check (cheap static 200)")
	log.Println("  GET  /ready               - Readiness check (CKAN + storage)")
	log.Println("  POST /api/v1/analyze      - Analyze a CKAN package")
	log.Println("")
	log.Println("Authentication:")
	log.Println("  Use your CKAN API token in the Authorization header:")
	log.Println("  Authorization: Bearer <your-ckan-api-token>")
	log.Println("")
	log.Println("Example Request:")
	log.Println("  curl -X POST http://localhost:8080/api/v1/analyze \\")
	log.Println("    -H 'Authorization: Bearer <your-ckan-api-token>' \\")
	log.Println("    -H 'Content-Type: application/json' \\")
	log.Println("    -d '{\"package_id\": \"my-package\"}'")
}
