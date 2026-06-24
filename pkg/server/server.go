package server

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
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

	// slog JSON handler to stdout for request/access logging (§8). Check
	// Messages are NOT routed through this; they stay in GlobalLogger, which is
	// switched to JSON mode so per-request messages are buffered (and cleared at
	// the top of each request) instead of printed to stdout.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	output.GlobalLogger.SetJSONMode(true)

	// Create handler
	handler := NewHandler(pcConfig, cfg, logger)

	// Set up routes
	mux := http.NewServeMux()

	// Health endpoint (no auth required, exempt from rate limiting and the
	// concurrency semaphore, §1/§4).
	mux.HandleFunc("GET /health", handler.Health)

	// Analyze endpoint. The rate-limit and concurrency gates wrap THIS route
	// only (§4/§9): outer -> inner the analyze chain is
	// rate-limit(global) -> rate-limit(per-IP) -> concurrency-semaphore ->
	// token extraction (optional, see ExtractToken) -> handler. /health (and a
	// future /ready) bypass it entirely.
	analyze := http.Handler(ExtractToken(handler.Analyze))
	analyze = handler.Concurrency(analyze)
	analyze = handler.RateLimitPerIP(analyze)
	analyze = handler.RateLimitGlobal(analyze)
	mux.Handle("POST /api/v1/analyze", analyze)

	// Middleware chain (outer -> inner): request_id -> access-log -> routes.
	loggedMux := handler.RequestContext(handler.AccessLog(mux))

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
