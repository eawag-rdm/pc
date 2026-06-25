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

// writeTimeoutMargin is added to the configured request timeout to derive the
// http.Server WriteTimeout. It gives the handler enough slack to render and
// fully flush its own analysis_timeout (504) envelope AFTER the analysis context
// deadline fires but BEFORE the socket's write deadline tears the connection
// down — so the client receives the clean 504 instead of a dropped connection.
const writeTimeoutMargin = 30 * time.Second

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

	// Fail fast at boot if the required CkanCollector attrs are missing or
	// wrong-typed (spec §5): otherwise a bad/absent TOML key only surfaces as an
	// opaque internal_error 500 on the FIRST /analyze request. Catching it here
	// gives the operator a clear, actionable error before the server starts.
	if err := validateCkanCollector(pcConfig); err != nil {
		return nil, fmt.Errorf("invalid PC config: %w", err)
	}

	// Resolve the listen address from the [server] config (the server takes no
	// flags) and fail fast if it — or any other [server] setting — is invalid,
	// so a bad value is caught at boot rather than at bind time or per request.
	listenAddr := cfg.ListenAddress(pcConfig)
	if err := validateServerSettings(pcConfig, listenAddr); err != nil {
		return nil, fmt.Errorf("invalid PC config: %w", err)
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
	// draining -> rate-limit(per-IP) -> rate-limit(global) ->
	// concurrency-gate (single slot, 2s busy-wait) -> token extraction
	// (optional) -> handler. /health and /ready bypass it entirely (a draining
	// server must still answer healthchecks).
	//
	// Per-IP is the OUTER (primary) limit and global is the INNER (backstop), so
	// per-IP is checked FIRST. This ordering matters because the limiter uses a
	// fixed window and allow() increments the counter even when it rejects: if
	// global ran first, a single IP already over its per-IP cap would still
	// consume (and exhaust) the shared global budget on every rejected request,
	// locking out everyone else. Putting per-IP first means a per-IP rejection
	// short-circuits before global is ever touched — IP-primary, global-backstop.
	analyze := http.Handler(ExtractToken(handler.Analyze))
	analyze = handler.Concurrency(analyze)
	analyze = handler.RateLimitGlobal(analyze)
	analyze = handler.RateLimitPerIP(analyze)
	analyze = handler.Draining(analyze)
	mux.Handle("POST /api/v1/analyze", analyze)

	// Full middleware chain (outer -> inner, §9):
	//   recover -> request_id -> access-log -> CORS -> routes
	// The per-analyze gates (draining/limiters/semaphore) are applied to the
	// analyze route above, inside the mux.
	chain := handler.RequestContext(handler.AccessLog(handler.CORS(mux)))
	chain = handler.Recover(chain)

	// Derive the socket WriteTimeout from the SAME request-timeout source the
	// handler uses for its analysis context deadline, plus a margin. The handler
	// caps the whole analysis at requestTimeout and writes a clean
	// analysis_timeout (504) envelope when that fires; the WriteTimeout must be
	// strictly longer so that 504 can be fully written before the socket deadline
	// tears the connection down. A hardcoded value would break this invariant once
	// an operator raises requestTimeoutSeconds above it.
	writeTimeout := configuredRequestTimeout(pcConfig) + writeTimeoutMargin

	return &Server{
		httpServer: &http.Server{
			Addr:              listenAddr,
			Handler:           chain,
			ReadTimeout:       30 * time.Second,
			ReadHeaderTimeout: 10 * time.Second, // slowloris guard (§9)
			WriteTimeout:      writeTimeout,     // requestTimeout + margin: handler's 504 must flush before this fires
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
	log.Printf("PC Server starting on %s", s.httpServer.Addr)
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
	err := s.httpServer.Shutdown(ctx)
	// Close the admin alerter only AFTER Shutdown returns, so in-flight requests
	// that fault during the drain can still enqueue their alerts. Close is
	// nil-safe (alerts disabled). The Shutdown error is what we return.
	s.handler.alerter.Close()
	return err
}
