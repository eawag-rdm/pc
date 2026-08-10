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

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/server"
)

func main() {
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

	// Create server configuration. The listen address comes from the TOML
	// ([server] listenAddress), not a flag, so Address is left empty here.
	// VerifyTLS is left nil so it falls back to the PC config's CkanCollector
	// "verify" attr (and finally the secure default of true).
	cfg := server.Config{
		ConfigPath: *configPath,
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
		log.Println("Server is shutting down...")

		ctx, cancel := context.WithTimeout(context.Background(), srv.DrainTimeout())
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Could not gracefully shutdown the server: %v", err)
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
