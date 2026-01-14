package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/server"
)

func main() {
	// Parse command line flags
	addr := flag.String("addr", ":8080", "Server listen address (e.g., :8080 or 0.0.0.0:8080)")
	configPath := flag.String("config", "", "Path to PC config file (pc.toml)")
	ckanURL := flag.String("ckan-url", "", "CKAN base URL (overrides config)")
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

	// Create server configuration
	cfg := server.Config{
		Address:     *addr,
		ConfigPath:  *configPath,
		CKANBaseURL: *ckanURL,
		VerifyTLS:   true, // Default to secure
	}

	// Create server
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Set up graceful shutdown
	done := make(chan bool, 1)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Println("Server is shutting down...")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			log.Fatalf("Could not gracefully shutdown the server: %v", err)
		}
		close(done)
	}()

	// Start server
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("Server stopped: %v", err)
	}

	<-done
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
	log.Println("  pc-server -config ./pc.toml")
	log.Println("  pc-server -addr :9000 -config /etc/pc/pc.toml")
	log.Println("")
	log.Println("API Endpoints:")
	log.Println("  GET  /health              - Health check")
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
