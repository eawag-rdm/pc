package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
)

// Server wraps the HTTP server with PC functionality
type Server struct {
	httpServer *http.Server
	pcConfig   *config.Config
	serverCfg  Config
	handler    *Handler
}

// New creates a new server instance
func New(cfg Config) (*Server, error) {
	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid server configuration: %w", err)
	}

	// Load PC configuration
	pcConfig, err := cfg.LoadPCConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load PC config: %w", err)
	}

	// Create handler
	handler := NewHandler(pcConfig, cfg)

	// Set up routes
	mux := http.NewServeMux()

	// Health endpoint (no auth required)
	mux.HandleFunc("GET /health", handler.Health)

	// Analyze endpoint (auth required - token extraction middleware)
	mux.HandleFunc("POST /api/v1/analyze", ExtractToken(handler.Analyze))

	// Wrap with logging middleware
	loggedMux := LoggingMiddleware(mux)

	return &Server{
		httpServer: &http.Server{
			Addr:         cfg.Address,
			Handler:      loggedMux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 300 * time.Second, // Long timeout for analysis
			IdleTimeout:  120 * time.Second,
		},
		pcConfig:  pcConfig,
		serverCfg: cfg,
		handler:   handler,
	}, nil
}

// ListenAndServe starts the HTTP server
func (s *Server) ListenAndServe() error {
	log.Printf("PC Server starting on %s", s.serverCfg.Address)
	log.Printf("PC Config loaded from: %s", s.serverCfg.ConfigPath)

	ckanURL := s.serverCfg.GetCKANBaseURL(s.pcConfig)
	if ckanURL != "" {
		log.Printf("CKAN URL: %s", ckanURL)
	}

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
