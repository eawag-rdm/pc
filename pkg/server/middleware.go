package server

import (
	"context"
	"log/slog"
	"net/http"
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
	if id, ok := r.Context().Value(requestIDKey).(string); ok {
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
// latency_ms, and client_ip (gated by logClientIP). It never routes check
// Messages and never logs the token. It replaces the previous LoggingMiddleware
// stub.
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

		attrs := []any{
			slog.String("request_id", GetRequestID(r)),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("package_id", getPackageID(r)),
			slog.Int("status", status),
			slog.Int64("latency_ms", time.Since(start).Milliseconds()),
		}
		if h.logClientIP {
			attrs = append(attrs, slog.String("client_ip", clientIP(r)))
		}

		h.logger.LogAttrs(r.Context(), slog.LevelInfo, "access", toLogAttrs(attrs)...)
	})
}

// toLogAttrs converts the variadic []any (slog.Attr values) used above into the
// []slog.Attr expected by LogAttrs.
func toLogAttrs(attrs []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if attr, ok := a.(slog.Attr); ok {
			out = append(out, attr)
		}
	}
	return out
}

// clientIP returns the connection's remote address (host portion). Proxy-aware
// X-Real-IP handling is introduced with rate limiting in a later step; here we
// only record what is on the connection.
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i != -1 {
		return addr[:i]
	}
	return addr
}
