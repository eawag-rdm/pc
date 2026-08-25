package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	// CKANTokenKey is the context key for the CKAN API token
	CKANTokenKey contextKey = "ckan_token"
	// requestIDKey is the context key for the per-request ULID.
	requestIDKey contextKey = "request_id"
	// contactKey is the context key for the contact suffix shown in error envelopes.
	contactKey contextKey = "contact_message"
	// packageIDKey is the context key for the request's package_id holder. The
	// holder is a pointer installed by RequestContext BEFORE the handler runs, so
	// the handler can write the id through it and the (outer) access-log
	// middleware reads it back from the same holder. A plain value would not
	// work: the handler's r.WithContext reassignment is local to the handler and
	// never propagates up to the request the access-log middleware holds.
	packageIDKey contextKey = "package_id"
	// alerterKey is the context key for the handler's admin alerter. RequestContext
	// stashes it so renderError can fire a server-fault alert without a reference
	// to the Handler. The stored value may be a typed-nil *alerter (alerts
	// disabled); the getter and Notify are both nil-safe.
	alerterKey contextKey = "alerter"
)

// packageIDHolder is a mutable cell shared between the access-log middleware and
// the handler via the request context. The middleware installs it (empty)
// before invoking the handler; the handler writes the resolved package_id into
// it; the middleware reads it after the handler returns.
type packageIDHolder struct {
	mu sync.Mutex
	id string
}

// ExtractToken extracts the Bearer token from the Authorization header and
// stores it in the request context. The Authorization header is OPTIONAL: a
// missing header is treated as "no token" (public-package path). A present but
// malformed header is rejected with invalid_request (§2).
func ExtractToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			// No token: proceed on the public-package path.
			next(w, r)
			return
		}

		// Parse Bearer token
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeError(w, r, CodeInvalidRequest)
			return
		}

		token := strings.TrimSpace(parts[1])
		if token == "" {
			writeError(w, r, CodeInvalidRequest)
			return
		}

		// Store token in context and proceed
		ctx := context.WithValue(r.Context(), CKANTokenKey, token)
		next(w, r.WithContext(ctx))
	}
}

// GetTokenFromContext retrieves the CKAN token from the request context.
func GetTokenFromContext(r *http.Request) string {
	if token, ok := r.Context().Value(CKANTokenKey).(string); ok {
		return token
	}
	return ""
}

// GetRequestID retrieves the per-request ULID from the request context.
func GetRequestID(r *http.Request) string {
	return GetRequestIDFromContext(r.Context())
}

// GetRequestIDFromContext retrieves the per-request ULID from a context. It is
// used by lifecycle logging that holds a context rather than the *http.Request.
func GetRequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// contactMessage retrieves the contact suffix from the request context.
func contactMessage(r *http.Request) string {
	if c, ok := r.Context().Value(contactKey).(string); ok {
		return c
	}
	return ""
}

// withPackageIDHolder returns a request whose context carries a fresh, empty
// package_id holder. The access-log middleware installs this before invoking the
// handler so both sides share the same cell.
func withPackageIDHolder(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), packageIDKey, &packageIDHolder{}))
}

// setPackageID writes the resolved package_id into the holder on the request
// context (installed by withPackageIDHolder). It is a no-op if no holder is
// present (e.g. a handler exercised in isolation in a test).
func setPackageID(r *http.Request, packageID string) {
	if h, ok := r.Context().Value(packageIDKey).(*packageIDHolder); ok {
		h.mu.Lock()
		h.id = packageID
		h.mu.Unlock()
	}
}

// getPackageID retrieves the package_id from the holder on the request context.
func getPackageID(r *http.Request) string {
	if h, ok := r.Context().Value(packageIDKey).(*packageIDHolder); ok {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.id
	}
	return ""
}

// withAlerter returns a context carrying the handler's admin alerter so
// renderError can fire a server-fault alert. The alerter may be nil (alerts
// disabled); storing it anyway keeps alerterFromContext uniform.
func withAlerter(ctx context.Context, a *alerter) context.Context {
	return context.WithValue(ctx, alerterKey, a)
}

// alerterFromContext retrieves the admin alerter from the request context. It
// tolerates both an absent value and a typed-nil *alerter; Notify is nil-safe,
// so returning nil simply means "no alert".
func alerterFromContext(ctx context.Context) *alerter {
	a, _ := ctx.Value(alerterKey).(*alerter)
	return a
}

// RequestContext generates a per-request ULID, propagates it (and the contact
// suffix) through the request context, and echoes the id in the X-Request-Id
// header. It is the outermost application middleware so downstream handlers and
// error responses can reference the same id.
func (h *Handler) RequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		w.Header().Set("X-Request-Id", requestID)

		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		ctx = context.WithValue(ctx, contactKey, h.contactMsg)
		ctx = withAlerter(ctx, h.alerter)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code written,
// for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	return sr.ResponseWriter.Write(b)
}

// AccessLog emits exactly one structured (slog JSON) access record per request
// at completion: ts, level, request_id, method, path, package_id, status,
// latency_ms, client_ip, and real_ip when the request carries a present and
// non-blank X-Real-IP header, whitespace-trimmed and capped at 45 bytes (both
// IP fields gated by logClientIP). Successful /health and /ready probes are
// not logged (only failing ones). It never routes check Messages and never
// logs the token.
func (h *Handler) AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 0}

		// Install a mutable package_id holder before invoking the handler so the
		// handler's setPackageID write is visible here after it returns. A plain
		// context value written downstream via r.WithContext would not propagate
		// back up to this request.
		r = withPackageIDHolder(r)

		next.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}

		// Successful probe hits are pure noise (a healthcheck every 30s would
		// dominate the log); probes are logged only when they fail.
		if (r.URL.Path == "/health" || r.URL.Path == "/ready") && status < 400 {
			return
		}

		attrs := []slog.Attr{
			slog.String("request_id", GetRequestID(r)),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("package_id", getPackageID(r)),
			slog.Int("status", status),
			slog.Int64("latency_ms", time.Since(start).Milliseconds()),
		}
		if h.logClientIP {
			attrs = append(attrs, slog.String("client_ip", clientIP(r)))
			// Untrusted: any client can set this header. Not the limiter key - see rateLimiter.clientIP.
			if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
				// 45 = longest IPv6 text form; caps attacker-controlled log volume.
				if len(realIP) > 45 {
					realIP = realIP[:45]
				}
				attrs = append(attrs, slog.String("real_ip", realIP))
			}
		}

		h.logger.LogAttrs(r.Context(), slog.LevelInfo, "access", attrs...)
	})
}

// clientIP returns the connection's remote address (host portion), for access
// logging. Rate-limit keying uses the proxy-aware rateLimiter.clientIPKey
// instead; this function records only what is on the connection.
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i != -1 {
		return addr[:i]
	}
	return addr
}

// RateLimitGlobal enforces the global (all-clients) fixed-hourly-window cap
// (§4). It is the INNER limiter (backstop): it runs AFTER the per-IP check, so a
// request already rejected by its per-IP cap never reaches - and never consumes
// - the shared global counter. It runs before any CKAN call; the token is never
// consulted. On rejection it emits the rate_limited slog event and renders the
// rate_limited envelope with a Retry-After header. It wraps /analyze only -
// /health and /ready never see it.
func (h *Handler) RateLimitGlobal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		ok, count, limit, retryAfter := h.limiter.allow(globalKey, scopeGlobal)
		if !ok {
			// Log the GLOBAL bucket key (the key the limiter actually counts on for
			// this scope), not the per-IP key - a scope=global event carrying a
			// per-IP key is misleading. The per-IP path (RateLimitPerIP) logs its
			// own clientIPKey.
			h.logRateLimited(r, scopeGlobal, hashIPKey(globalKey), count, limit, retryAfter)
			writeRateLimited(w, r, retryAfter)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimitPerIP enforces the per-client-IP fixed-hourly-window cap (§4). The
// key is the proxy-aware client IP (X-Real-IP only from trustedProxies; IPv6 on
// the /64 prefix); the token is never a key. It is the OUTER (primary) limiter:
// it runs BEFORE the global check, so a per-IP rejection short-circuits without
// consuming the shared global budget. It runs before the concurrency semaphore.
func (h *Handler) RateLimitPerIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		key := h.limiter.clientIPKey(r)
		ok, count, limit, retryAfter := h.limiter.allow(key, scopeIP)
		if !ok {
			h.logRateLimited(r, scopeIP, hashIPKey(key), count, limit, retryAfter)
			writeRateLimited(w, r, retryAfter)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// enforceClientAllowlist refuses requests from clients outside the [server]
// allowedClients CIDRs. It is the OUTERMOST gate on the analyze route, ahead of
// RateLimitPerIP, so a denied request never creates a limiter entry and cannot
// evict an honest client's counter. server.New installs it only when the
// allow-list is non-empty; an empty list matches nothing, so an installed gate
// would deny every request. /health and /ready are never wrapped by it.
//
// A POST from an unlisted client renders the same not_found envelope an unknown
// path gets; other methods still draw the route guard's 405 + Allow: POST, which
// is outside the mux and so outside this gate. The denial appears in the access
// log as an ordinary 404; there is no separate event.
func (h *Handler) enforceClientAllowlist(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.clientAllowed(r) {
			writeError(w, r, CodeNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientAllowed reports whether r's client IP falls inside an allow-list prefix.
// The IP is the one the rate limiter derives (X-Real-IP only from a trusted
// proxy, otherwise the connection address), so the allow-list is exactly as
// strong as the trustProxyHeaders/trustedProxies configuration and no stronger.
// It fails closed: an address that cannot be derived is not allowed.
func (h *Handler) clientAllowed(r *http.Request) bool {
	if h.limiter == nil {
		return false
	}
	addr, ok := netip.AddrFromSlice(h.limiter.clientIP(r))
	if !ok {
		return false
	}
	// net.IP carries IPv4 in its 16-byte mapped form, which no IPv4 prefix
	// contains; Unmap turns it back into a 4-byte address.
	addr = addr.Unmap()
	for _, prefix := range h.allowedClients {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Concurrency is the single analysis serialization gate (concurrency = 1,
// §4/§9). It is the front door to the same single slot guarded by analysisMu:
// only one analysis at a time may touch the process-global GlobalLogger/
// PDFTracker. When the slot is busy, a request WAITS up to analysisBusyWait
// (default 2s, from [server] analysisBusyWaitSeconds) for it to free; if it
// frees in time the request proceeds, otherwise it is rejected with the
// service_busy envelope (503) plus a Retry-After header so the frontend can
// render a busy state and clients can back off. The slot is released when the
// handler returns. There is no unbounded queue - the wait is capped.
func (h *Handler) Concurrency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.sem == nil {
			next.ServeHTTP(w, r)
			return
		}
		// Fast path: slot free right now.
		select {
		case h.sem <- struct{}{}:
			defer func() { <-h.sem }()
			next.ServeHTTP(w, r)
			return
		default:
		}
		// Busy: wait up to analysisBusyWait for the slot, honouring client
		// cancellation. The timer is always stopped so it cannot leak.
		timer := time.NewTimer(h.analysisBusyWait)
		defer timer.Stop()
		select {
		case h.sem <- struct{}{}:
			defer func() { <-h.sem }()
			next.ServeHTTP(w, r)
		case <-r.Context().Done():
			// Client went away while waiting: nothing to serve.
			writeError(w, r, CodeServiceBusy)
		case <-timer.C:
			// Still busy after the grace period: reject with a backoff hint.
			writeServiceBusy(w, r)
		}
	})
}

// busyRetryAfterSeconds is the Retry-After advertised on a service_busy
// rejection. It matches the catalogue message ("try again in a minute").
const busyRetryAfterSeconds = 60

// writeServiceBusy renders the service_busy envelope (503) with a Retry-After
// header so clients/frontends can back off. The header is set before writeError
// writes the status/body.
func writeServiceBusy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", strconv.Itoa(busyRetryAfterSeconds))
	writeError(w, r, CodeServiceBusy)
}

// writeRateLimited renders the rate_limited envelope (429) and the Retry-After
// header (whole seconds until the next hour boundary, §4). Retry-After is set
// before writeError writes the status/body.
func writeRateLimited(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	secs := int(retryAfter.Seconds())
	if retryAfter > 0 && secs < 1 {
		secs = 1
	}
	if secs < 0 {
		secs = 0
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, r, CodeRateLimited)
}

// logRateLimited emits the rate_limited slog event (§8): request_id, scope,
// key (a hashed/truncated IP - never the token), count, limit, retry_after.
func (h *Handler) logRateLimited(r *http.Request, scope rateScope, key string, count, limit int, retryAfter time.Duration) {
	h.logger.LogAttrs(r.Context(), slog.LevelWarn, "rate_limited",
		slog.String("request_id", GetRequestID(r)),
		slog.String("scope", string(scope)),
		slog.String("key", key),
		slog.Int("count", count),
		slog.Int("limit", limit),
		slog.Int("retry_after", int(retryAfter.Seconds())),
	)
}

// Recover is the OUTERMOST middleware (§9). It turns a panic in any downstream
// handler/middleware into a clean internal_error envelope instead of crashing
// the process or leaking a stack trace to the client. The panic value and stack
// are logged (keyed by request_id) but never sent to the client. If the
// response was already partially written there is nothing safe to do but log;
// writeError will then be a no-op on the headers it could not set.
func (h *Handler) Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// Recover is the OUTERMOST middleware, so r's context predates
				// RequestContext and carries no request_id. Fall back to the
				// X-Request-Id response header (set by RequestContext on the shared
				// ResponseWriter) so the panic stack is keyed by the SAME id the
				// client envelope and admin alert cite - the same fallback renderError
				// uses. Without this the only record carrying the stack would have an
				// empty request_id, breaking log correlation.
				rid := GetRequestID(r)
				if rid == "" {
					rid = w.Header().Get("X-Request-Id")
				}
				h.logger.LogAttrs(r.Context(), slog.LevelError, "panic_recovered",
					slog.String("request_id", rid),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)
				// Recover is the OUTERMOST middleware, so r's context predates
				// RequestContext and carries no alerter. Stash it here so renderError
				// fires the internal_error alert exactly once (no second direct
				// Notify, to avoid double-sending).
				r = r.WithContext(withAlerter(r.Context(), h.alerter))
				writeError(w, r, CodeInternalError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// CORS applies the cross-origin policy (§9): it allows the configured
// allowedOrigins (an allow-list of exact origin URLs), the methods GET/POST/
// OPTIONS, and the Authorization (plus Content-Type) request headers. A request
// whose Origin is in the allow-list receives the matching
// Access-Control-Allow-Origin (echoing the request's origin, never "*", so
// credentials are permitted) and Access-Control-Allow-Credentials: true. An
// OPTIONS preflight short-circuits with 204 and the CORS headers. A request from
// an unlisted origin is NOT blocked here (CORS is a browser-enforced policy); it
// simply receives no allow headers, so the browser blocks the cross-origin read.
func (h *Handler) CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// Vary on Origin UNCONDITIONALLY so a shared cache keys on the request
		// Origin for every CORS-handled request. Setting it only for allowed
		// origins would let a cache store a header-less response for a disallowed
		// origin and replay it to an allowed one (or vice-versa) - a
		// cache-poisoning vector. It must be set whether or not the origin passes
		// the allow-list check below.
		w.Header().Add("Vary", "Origin")
		if origin != "" && h.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Max-Age", "600")
		}

		// Preflight: answer OPTIONS here without invoking the route handler.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// originAllowed reports whether origin is in the configured CORS allow-list.
func (h *Handler) originAllowed(origin string) bool {
	for _, allowed := range h.allowedOrigins {
		if allowed == origin {
			return true
		}
	}
	return false
}

// knownRoutes maps every path registered on the mux (server.go New) to the
// methods it serves, so requests that match no route get catalogue envelopes -
// not_found (404) / method_not_allowed (405) - instead of the mux's plain-text
// defaults (§3: every failure uses the envelope). HEAD is listed alongside GET
// because the mux serves it implicitly; OPTIONS never reaches this (the CORS
// middleware answers preflights upstream). MUST be kept in sync with the mux
// registrations in New.
var knownRoutes = map[string][]string{
	"/health":         {http.MethodGet, http.MethodHead},
	"/ready":          {http.MethodGet, http.MethodHead},
	"/api/v1/analyze": {http.MethodPost},
}

// EnforceKnownRoutes wraps the mux and replaces its plain-text 404/405
// defaults with catalogue envelopes. It sits INSIDE the CORS middleware (so
// preflights are already answered) and delegates every known path+method to
// the mux untouched.
func EnforceKnownRoutes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, ok := knownRoutes[r.URL.Path]
		if !ok {
			writeError(w, r, CodeNotFound)
			return
		}
		if !methodAllowed(allowed, r.Method) {
			// RFC 9110: a 405 must carry Allow listing the supported methods.
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			writeError(w, r, CodeMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// methodAllowed reports whether method is in the route's allowed list.
func methodAllowed(allowed []string, method string) bool {
	for _, m := range allowed {
		if m == method {
			return true
		}
	}
	return false
}

// Draining rejects new requests with server_restarting (503) once graceful
// shutdown has begun (§9). It wraps the application routes so that, while the
// server drains in-flight analyses, freshly arriving requests get a clean
// envelope instead of being accepted into a shutting-down server. In-flight
// requests are unaffected; they continue under the shutdown drain timeout.
func (h *Handler) Draining(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.draining.Load() {
			writeError(w, r, CodeServerRestarting)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BeginDraining marks the server as draining so the Draining middleware starts
// rejecting new requests with server_restarting (§9). Called from the graceful
// shutdown path before httpServer.Shutdown.
func (h *Handler) BeginDraining() {
	h.draining.Store(true)
}
