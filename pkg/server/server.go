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

	// Liveness: cheap static 200, no auth, exempt from rate limiting and the
	// concurrency semaphore (§1/§4).
	mux.HandleFunc("GET /health", handler.Health)

	// Readiness: CKAN reachable (cached ~5s) AND storage mount readable (§1).
	// Also exempt from the limiter and semaphore so health-check polling is free.
	mux.HandleFunc("GET /ready", handler.Ready)

	// Analyze endpoint. The draining, rate-limit and concurrency gates wrap THIS
	// route only (§4/§9): outer -> inner the analyze chain is
	// draining -> rate-limit(global) -> rate-limit(per-IP) ->
	// concurrency-semaphore -> token extraction (optional) -> handler. /health
	// and /ready bypass it entirely (a draining server must still answer
	// healthchecks).
	analyze := http.Handler(ExtractToken(handler.Analyze))
	analyze = handler.Concurrency(analyze)
	analyze = handler.RateLimitPerIP(analyze)
	analyze = handler.RateLimitGlobal(analyze)
	analyze = handler.Draining(analyze)
	mux.Handle("POST /api/v1/analyze", analyze)

	// Full middleware chain (outer -> inner, §9):
	//   recover -> request_id -> access-log -> CORS -> routes
	// The per-analyze gates (draining/limiters/semaphore) are applied to the
	// analyze route above, inside the mux.
	chain := handler.RequestContext(handler.AccessLog(handler.CORS(mux)))
	chain = handler.Recover(chain)

	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.Address,
			Handler:           chain,
			ReadTimeout:       30 * time.Second,
			ReadHeaderTimeout: 10 * time.Second,  // slowloris guard (§9)
			WriteTimeout:      300 * time.Second, // long timeout for analysis
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20, // 1 MiB header cap (§9)
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

// Shutdown gracefully shuts down the server (§9). It first flips the draining
// flag so newly arriving /analyze requests are rejected with server_restarting
// (503), then calls http.Server.Shutdown, which stops accepting new connections
// and waits for in-flight requests to finish (bounded by the caller's ctx,
// which should allow at least the analysis timeout to drain).
func (s *Server) Shutdown(ctx context.Context) error {
	s.handler.BeginDraining()
	return s.httpServer.Shutdown(ctx)
}
