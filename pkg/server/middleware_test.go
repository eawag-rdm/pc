package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
)

func TestExtractToken_Valid(t *testing.T) {
	handler := ExtractToken(func(w http.ResponseWriter, r *http.Request) {
		token := GetTokenFromContext(r)
		if token != "test-token-123" {
			t.Errorf("Expected token 'test-token-123', got '%s'", token)
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "Bearer test-token-123")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}
}

// A missing Authorization header is OPTIONAL: the handler is still invoked, with
// an empty token (public-package path).
func TestExtractToken_MissingHeader_PublicPath(t *testing.T) {
	called := false
	handler := ExtractToken(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if GetTokenFromContext(r) != "" {
			t.Error("Expected empty token on public path")
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("POST", "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("Handler should be called on missing (optional) token")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}
}

// A present-but-malformed Authorization header is rejected with invalid_request.
func TestExtractToken_MalformedHeader_InvalidRequest(t *testing.T) {
	cases := []string{"Basic test-token", "BearerNoSpace", "Bearer "}
	for _, h := range cases {
		t.Run(h, func(t *testing.T) {
			handler := ExtractToken(func(w http.ResponseWriter, r *http.Request) {
				t.Error("Handler should not be called for malformed auth")
			})

			req := httptest.NewRequest("POST", "/test", nil)
			req.Header.Set("Authorization", h)
			req = withRequestContext(req, "REQ-MALFORMED", DefaultContactMessage)
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("Expected status 400, got %d", rr.Code)
			}
			var resp ErrorResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Error.Code != CodeInvalidRequest {
				t.Errorf("Expected code %q, got %q", CodeInvalidRequest, resp.Error.Code)
			}
		})
	}
}

func TestExtractToken_CaseInsensitiveBearer(t *testing.T) {
	for _, prefix := range []string{"bearer", "BEARER"} {
		req := httptest.NewRequest("POST", "/test", nil)
		req.Header.Set("Authorization", prefix+" my-token")
		rr := httptest.NewRecorder()

		called := false
		ExtractToken(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if GetTokenFromContext(r) != "my-token" {
				t.Errorf("Expected token 'my-token', got '%s'", GetTokenFromContext(r))
			}
			w.WriteHeader(http.StatusOK)
		}).ServeHTTP(rr, req)

		if !called {
			t.Errorf("handler must run for %q bearer (a short-circuit would hide the token assertion)", prefix)
		}
		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200 for %q bearer, got %d", prefix, rr.Code)
		}
	}
}

func TestGetTokenFromContext_NoToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	token := GetTokenFromContext(req)
	if token != "" {
		t.Errorf("Expected empty string, got '%s'", token)
	}
}

// TestRequestContext_GeneratesIDAndHeader verifies the middleware generates a
// request_id, exposes it via context, and echoes it in X-Request-Id.
func TestRequestContext_GeneratesIDAndHeader(t *testing.T) {
	handler := NewHandler(&config.Config{Server: &config.ServerConfig{ContactMessage: "C"}}, Config{}, discardLogger(), testPlan(&config.Config{Server: &config.ServerConfig{ContactMessage: "C"}}))

	var ctxID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxID = GetRequestID(r)
		if contactMessage(r) != "C" {
			t.Errorf("contact not propagated, got %q", contactMessage(r))
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()
	handler.RequestContext(inner).ServeHTTP(rr, req)

	header := rr.Header().Get("X-Request-Id")
	if header == "" {
		t.Fatal("X-Request-Id header not set")
	}
	if header != ctxID {
		t.Errorf("header %q != context id %q", header, ctxID)
	}
	if len(header) != 26 {
		t.Errorf("expected 26-char ULID, got %d chars: %q", len(header), header)
	}
}

// TestAccessLog_NeverLogsToken asserts captured slog access output never
// contains the token value (secret hygiene, §8).
func TestAccessLog_NeverLogsToken(t *testing.T) {
	const secret = "super-secret-ckan-token-XYZ"

	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewJSONHandler(&syncWriter{w: &buf, mu: &mu}, nil))

	handler := NewHandler(&config.Config{Server: &config.ServerConfig{LogClientIP: true}}, Config{}, logger, testPlan(&config.Config{Server: &config.ServerConfig{LogClientIP: true}}))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Token is in the context, as it would be after ExtractToken.
		_ = GetTokenFromContext(r)
		// The handler writes the package_id through the holder AccessLog
		// installed, exactly as Analyze does.
		setPackageID(r, "some-package")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, secret))
	req = withRequestContext(req, "REQ-LOG", DefaultContactMessage)

	rr := httptest.NewRecorder()
	handler.AccessLog(inner).ServeHTTP(rr, req)

	mu.Lock()
	out := buf.String()
	mu.Unlock()

	if out == "" {
		t.Fatal("expected an access log record, got none")
	}
	if strings.Contains(out, secret) {
		t.Errorf("access log leaked the token: %s", out)
	}
	// Sanity: the access record carries the expected non-secret fields.
	for _, want := range []string{`"request_id":"REQ-LOG"`, `"package_id":"some-package"`, `"method":"POST"`, `"status":200`} {
		if !strings.Contains(out, want) {
			t.Errorf("access log missing %q; got %s", want, out)
		}
	}
}

// syncWriter serializes concurrent writes to the underlying buffer for tests.
type syncWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// corsHandler builds a handler whose CORS allow-list contains origin.
func corsHandler(origin string) *Handler {
	cfg := &config.Config{
		Server: &config.ServerConfig{AllowedOrigins: []string{origin}},
	}
	return NewHandler(cfg, Config{}, discardLogger(), testPlan(cfg))
}

// TestCORS_PreflightAllowedOrigin asserts an OPTIONS preflight from an allowed
// origin returns that origin, allows the Authorization header, and the GET/POST/
// OPTIONS methods, and short-circuits (the inner handler is NOT invoked).
func TestCORS_PreflightAllowedOrigin(t *testing.T) {
	const origin = "https://frontend.example.org"
	h := corsHandler(origin)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("OPTIONS", "/api/v1/analyze", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization")
	rr := httptest.NewRecorder()

	h.CORS(inner).ServeHTTP(rr, req)

	if called {
		t.Error("preflight must short-circuit; inner handler should not run")
	}
	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204 for preflight, got %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, origin)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
		t.Errorf("Allow-Headers %q must include Authorization", got)
	}
	methods := rr.Header().Get("Access-Control-Allow-Methods")
	for _, m := range []string{"GET", "POST", "OPTIONS"} {
		if !strings.Contains(methods, m) {
			t.Errorf("Allow-Methods %q must include %s", methods, m)
		}
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q, want true", got)
	}
}

// TestCORS_DisallowedOrigin asserts a request from an unlisted origin receives
// no allow-origin header (the browser then blocks the cross-origin read).
func TestCORS_DisallowedOrigin(t *testing.T) {
	h := corsHandler("https://frontend.example.org")

	req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rr := httptest.NewRecorder()

	h.CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unlisted origin must not get an allow-origin header, got %q", got)
	}
	// A non-preflight request still reaches the handler.
	if rr.Code != http.StatusOK {
		t.Errorf("non-preflight request should pass through, got %d", rr.Code)
	}
}

// TestCORS_ActualRequestAllowedOrigin asserts that a REAL (non-preflight)
// cross-origin request from an allowed origin carries the CORS allow-origin
// header AND a Vary: Origin header (so shared caches don't serve one origin's
// CORS headers to another), and still reaches the inner handler (§9). The
// preflight path is covered separately; this covers the simple GET/POST that the
// browser actually issues after a successful preflight.
func TestCORS_ActualRequestAllowedOrigin(t *testing.T) {
	const origin = "https://frontend.example.org"
	h := corsHandler(origin)

	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			called := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(method, "/api/v1/analyze", nil)
			req.Header.Set("Origin", origin)
			rr := httptest.NewRecorder()

			h.CORS(inner).ServeHTTP(rr, req)

			if !called {
				t.Errorf("%s: non-preflight request must reach the inner handler", method)
			}
			if rr.Code != http.StatusOK {
				t.Errorf("%s: expected 200 passthrough, got %d", method, rr.Code)
			}
			if got := rr.Header().Get("Access-Control-Allow-Origin"); got != origin {
				t.Errorf("%s: Access-Control-Allow-Origin = %q, want %q", method, got, origin)
			}
			vary := rr.Header().Values("Vary")
			if !containsString(vary, "Origin") {
				t.Errorf("%s: Vary must include Origin (cache-poisoning guard), got %v", method, vary)
			}
		})
	}
}

// containsString reports whether s is present in vs.
func containsString(vs []string, s string) bool {
	for _, v := range vs {
		if v == s {
			return true
		}
	}
	return false
}

// TestRecover_PanicBecomesInternalError asserts a panic in a downstream handler
// is turned into a clean internal_error envelope (500) by the Recover
// middleware, and the process does not crash. It exercises the REAL production
// chain order Recover(RequestContext(panicHandler)) (spec §9) rather than
// injecting a request_id by hand: Recover is the outermost middleware, so its
// deferred handler holds the original request whose context predates
// RequestContext. The envelope must still cite the request_id (taken from the
// X-Request-Id header RequestContext sets) and it must equal the header value
// (spec §3).
func TestRecover_PanicBecomesInternalError(t *testing.T) {
	h := NewHandler(&config.Config{Server: &config.ServerConfig{}}, Config{}, discardLogger(), testPlan(&config.Config{Server: &config.ServerConfig{}}))

	boom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom")
	})

	// Real chain order: Recover wraps RequestContext (which generates the id),
	// matching production wiring in server.New.
	chain := h.Recover(h.RequestContext(boom))

	req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
	rr := httptest.NewRecorder()

	chain.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 after recovered panic, got %d", rr.Code)
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeInternalError {
		t.Errorf("expected %q, got %q", CodeInternalError, resp.Error.Code)
	}

	headerID := rr.Header().Get("X-Request-Id")
	if headerID == "" {
		t.Fatal("expected a non-empty X-Request-Id header")
	}
	if resp.Error.RequestID == "" {
		t.Error("internal_error envelope must carry a non-empty request_id")
	}
	if resp.Error.RequestID != headerID {
		t.Errorf("envelope request_id %q must equal X-Request-Id header %q", resp.Error.RequestID, headerID)
	}
	// internal_error cites the request_id in its message and must not leave the
	// reference blank.
	if !strings.Contains(resp.Error.Message, headerID) {
		t.Errorf("internal_error message must quote the request_id %q, got %q", headerID, resp.Error.Message)
	}
}

// TestRecover_PanicLogCarriesRequestID locks in D1: the panic_recovered slog
// event must be keyed by the SAME request_id the envelope and admin alert cite,
// not an empty string. Recover is outermost (its r predates RequestContext), so
// the id is only available via the X-Request-Id header fallback.
func TestRecover_PanicLogCarriesRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := NewHandler(&config.Config{Server: &config.ServerConfig{}}, Config{}, logger, testPlan(&config.Config{Server: &config.ServerConfig{}}))

	boom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom")
	})
	chain := h.Recover(h.RequestContext(boom))

	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, httptest.NewRequest("POST", "/api/v1/analyze", nil))

	headerID := rr.Header().Get("X-Request-Id")
	if headerID == "" {
		t.Fatal("expected a non-empty X-Request-Id header")
	}

	// Find the panic_recovered record in the captured log and assert its id.
	var loggedID string
	var found bool
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line is not JSON: %v (%s)", err, line)
		}
		if rec["msg"] == "panic_recovered" {
			found = true
			loggedID, _ = rec["request_id"].(string)
		}
	}
	if !found {
		t.Fatalf("expected a panic_recovered log record; got:\n%s", buf.String())
	}
	if loggedID != headerID {
		t.Errorf("panic_recovered request_id %q must equal X-Request-Id %q (D1 correlation)", loggedID, headerID)
	}
}

// TestDraining_RejectsNewRequests asserts that once BeginDraining is called, the
// Draining middleware rejects new requests with server_restarting (503) and the
// inner handler is not invoked.
// TestEnforceKnownRoutes_UnknownPath asserts an unregistered path yields the
// not_found envelope instead of the mux's plain-text 404.
func TestEnforceKnownRoutes_UnknownPath(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler must not be called for an unknown path")
	})

	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	req = withRequestContext(req, "REQ-404", DefaultContactMessage)
	rr := httptest.NewRecorder()
	EnforceKnownRoutes(inner).ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeNotFound {
		t.Errorf("expected code %q, got %q", CodeNotFound, resp.Error.Code)
	}
	if resp.Error.RequestID != "REQ-404" {
		t.Errorf("expected request_id in envelope, got %q", resp.Error.RequestID)
	}
}

// TestEnforceKnownRoutes_WrongMethod asserts a known path with an unsupported
// method yields the method_not_allowed envelope plus the RFC 9110 Allow header.
func TestEnforceKnownRoutes_WrongMethod(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler must not be called for a wrong method")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/analyze", nil)
	req = withRequestContext(req, "REQ-405", DefaultContactMessage)
	rr := httptest.NewRecorder()
	EnforceKnownRoutes(inner).ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
	if allow := rr.Header().Get("Allow"); allow != "POST" {
		t.Errorf("expected Allow: POST, got %q", allow)
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeMethodNotAllowed {
		t.Errorf("expected code %q, got %q", CodeMethodNotAllowed, resp.Error.Code)
	}
}

// TestEnforceKnownRoutes_KnownRoutePassesThrough asserts registered
// path+method pairs reach the inner handler untouched.
func TestEnforceKnownRoutes_KnownRoutePassesThrough(t *testing.T) {
	for path, methods := range knownRoutes {
		for _, method := range methods {
			called := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(method, path, nil)
			rr := httptest.NewRecorder()
			EnforceKnownRoutes(inner).ServeHTTP(rr, req)

			if !called {
				t.Errorf("%s %s: inner handler not called", method, path)
			}
		}
	}
}

func TestDraining_RejectsNewRequests(t *testing.T) {
	h := NewHandler(&config.Config{Server: &config.ServerConfig{}}, Config{}, discardLogger(), testPlan(&config.Config{Server: &config.ServerConfig{}}))

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	guarded := h.Draining(inner)

	// Before draining: request passes through.
	req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
	req = withRequestContext(req, "REQ-OK", DefaultContactMessage)
	rr := httptest.NewRecorder()
	guarded.ServeHTTP(rr, req)
	if !called || rr.Code != http.StatusOK {
		t.Fatalf("pre-drain request should pass: called=%v code=%d", called, rr.Code)
	}

	// After BeginDraining: rejected with server_restarting.
	h.BeginDraining()
	called = false
	req = httptest.NewRequest("POST", "/api/v1/analyze", nil)
	req = withRequestContext(req, "REQ-DRAIN", DefaultContactMessage)
	rr = httptest.NewRecorder()
	guarded.ServeHTTP(rr, req)

	if called {
		t.Error("draining must not invoke the inner handler")
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while draining, got %d", rr.Code)
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeServerRestarting {
		t.Errorf("expected %q, got %q", CodeServerRestarting, resp.Error.Code)
	}
}

// TestAccessLog_SkipsSuccessfulProbes asserts successful /health and /ready
// hits emit no access record (probe noise), while failing probes and normal
// routes are still logged.
func TestAccessLog_SkipsSuccessfulProbes(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		status  int
		wantLog bool
	}{
		{"health 200 skipped", "/health", http.StatusOK, false},
		{"ready 200 skipped", "/ready", http.StatusOK, false},
		{"ready 503 logged", "/ready", http.StatusServiceUnavailable, true},
		{"health 503 logged", "/health", http.StatusServiceUnavailable, true},
		{"analyze 200 logged", "/api/v1/analyze", http.StatusOK, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			var mu sync.Mutex
			logger := slog.New(slog.NewJSONHandler(&syncWriter{w: &buf, mu: &mu}, nil))
			handler := NewHandler(&config.Config{Server: &config.ServerConfig{}}, Config{}, logger, testPlan(&config.Config{Server: &config.ServerConfig{}}))

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			})

			req := httptest.NewRequest("GET", tt.path, nil)
			req = withRequestContext(req, "REQ-PROBE", DefaultContactMessage)
			rr := httptest.NewRecorder()
			handler.AccessLog(inner).ServeHTTP(rr, req)

			mu.Lock()
			out := buf.String()
			mu.Unlock()

			if tt.wantLog && !strings.Contains(out, `"msg":"access"`) {
				t.Errorf("expected an access record, got: %q", out)
			}
			if !tt.wantLog && strings.Contains(out, `"msg":"access"`) {
				t.Errorf("expected no access record, got: %q", out)
			}
		})
	}
}

// TestAccessLog_RealIP asserts the access record carries the X-Real-IP header
// trimmed and length-capped but otherwise unparsed as real_ip - only when the
// header is present and non-blank, and only under the same logClientIP privacy
// gate as client_ip.
func TestAccessLog_RealIP(t *testing.T) {
	// Exactly 45 bytes, the cap the middleware applies, so a longer header must
	// be recorded as this prefix alone.
	const longestIPv6 = "ffff:ffff:ffff:ffff:ffff:ffff:255.255.255.255"

	cases := []struct {
		name        string
		logClientIP bool
		header      string
		wantRealIP  string // "" = the attribute must be absent
	}{
		{"header trimmed", true, "  203.0.113.7  ", "203.0.113.7"},
		{"blank header, no attribute", true, "  \t ", ""},
		{"no header, no attribute", true, "", ""},
		{"over-long header truncated", true, longestIPv6 + ":and-more-attacker-bytes", longestIPv6},
		{"privacy gate closed", false, "203.0.113.7", ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			var mu sync.Mutex
			logger := slog.New(slog.NewJSONHandler(&syncWriter{w: &buf, mu: &mu}, nil))
			cfg := &config.Config{Server: &config.ServerConfig{LogClientIP: tt.logClientIP}}
			handler := NewHandler(cfg, Config{}, logger, testPlan(cfg))

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
			if tt.header != "" {
				req.Header.Set("X-Real-IP", tt.header)
			}
			req = withRequestContext(req, "REQ-REALIP", DefaultContactMessage)
			handler.AccessLog(inner).ServeHTTP(httptest.NewRecorder(), req)

			mu.Lock()
			out := buf.Bytes()
			mu.Unlock()

			var rec map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(out), &rec); err != nil {
				t.Fatalf("access record is not JSON: %v (%s)", err, out)
			}
			realIP, hasRealIP := rec["real_ip"]
			if tt.wantRealIP == "" {
				if hasRealIP {
					t.Errorf("real_ip must be absent, got %v", realIP)
				}
			} else if realIP != tt.wantRealIP {
				t.Errorf("real_ip = %v, want %q", realIP, tt.wantRealIP)
			}
			if _, hasClientIP := rec["client_ip"]; hasClientIP != tt.logClientIP {
				t.Errorf("client_ip present = %v, want %v", hasClientIP, tt.logClientIP)
			}
		})
	}
}

// allowlistHandler builds a handler whose allow-list holds the given CIDRs. srv
// is required (NewHandler builds a limiter only from a present [server] section)
// and its proxy trust is wired into the limiter exactly as server.New does it,
// so the rows that send a header run against LIVE header trust: a gate that
// consulted the limiter's derivation would admit the header address there.
func allowlistHandler(t *testing.T, srv *config.ServerConfig, cidrs ...string) *Handler {
	t.Helper()
	if srv == nil {
		t.Fatal("allowlistHandler needs a [server] config; without one no limiter is built")
	}
	h := NewHandler(&config.Config{Server: srv}, Config{}, discardLogger(), testPlan(&config.Config{Server: srv}))
	if h.limiter != nil {
		proxies, err := parsePrefixList("trustedProxies", srv.TrustedProxies)
		if err != nil {
			t.Fatalf("parsePrefixList(trustedProxies, %q): %v", srv.TrustedProxies, err)
		}
		h.limiter.trustedProxies = proxies
	}
	prefixes, err := parsePrefixList("allowedClients", cidrs)
	if err != nil {
		t.Fatalf("parsePrefixList(allowedClients, %q): %v", cidrs, err)
	}
	h.allowedClients = prefixes
	return h
}

// TestEnforceClientAllowlist_PeerAddress asserts which client the gate admits.
// The address it matches is the connection's peer (RemoteAddr), never X-Real-IP:
// the four proxy-trust rows below run under live trust, and the header still
// moves nothing - it can neither admit an unlisted peer nor deny a listed one.
func TestEnforceClientAllowlist_PeerAddress(t *testing.T) {
	const allowedV4 = "192.0.2.0/24"

	tests := []struct {
		name        string
		trustProxy  bool
		proxies     []string
		allowed     []string
		remoteAddr  string
		realIP      string
		wantAllowed bool
	}{
		{name: "listed peer", allowed: []string{allowedV4}, remoteAddr: "192.0.2.9:1111", wantAllowed: true},
		{name: "unlisted peer", allowed: []string{allowedV4}, remoteAddr: "198.51.100.9:1111"},
		// An empty list matches nobody, which is why server.New installs the gate
		// only when the list is non-empty.
		{name: "empty list denies", allowed: nil, remoteAddr: "192.0.2.9:1111"},
		{name: "listed IPv6 peer", allowed: []string{"2001:db8::/32"}, remoteAddr: "[2001:db8::5]:1111", wantAllowed: true},
		// A /128 entry admits exactly that host: an implementation matching on the
		// limiter's /64-masked key would let the whole subnet in.
		{name: "IPv6 host entry excludes its neighbour", allowed: []string{"2001:db8::/128"}, remoteAddr: "[2001:db8::1]:1111"},
		// The peer is a fully trusted proxy and its header names a listed address,
		// which the limiter would key on - the gate still matches the proxy itself
		// and denies. Anyone able to reach that proxy could otherwise set the
		// header and let themselves in.
		{
			name: "trusted proxy, header inside the list", trustProxy: true, proxies: []string{"127.0.0.1/32"},
			allowed: []string{allowedV4}, remoteAddr: "127.0.0.1:1111", realIP: "192.0.2.9",
		},
		// The header has no effect from an untrusted peer either.
		{
			name: "untrusted peer, spoofed header inside the list", trustProxy: true, proxies: []string{"127.0.0.1/32"},
			allowed: []string{allowedV4}, remoteAddr: "198.51.100.9:1111", realIP: "192.0.2.9",
		},
		// The header cannot deny a listed peer either: it is not consulted at all.
		{
			name: "listed peer, header naming an unlisted address", trustProxy: true, proxies: []string{"192.0.2.0/24"},
			allowed: []string{allowedV4}, remoteAddr: "192.0.2.9:1111", realIP: "198.51.100.9", wantAllowed: true,
		},
		// The families are disjoint after unmapping: an IPv4-only list admits no
		// IPv6 client, however wide its prefixes are.
		{name: "IPv6 peer against an IPv4-only list", allowed: []string{allowedV4, "0.0.0.0/0"}, remoteAddr: "[2001:db8::5]:1111"},
		// Listing the proxy admits it, and with it every client connecting through
		// it: behind a reverse proxy the list controls which HOSTS may reach the
		// endpoint.
		{
			name: "trusted and listed proxy without header", trustProxy: true, proxies: []string{"127.0.0.1/32"},
			allowed: []string{"127.0.0.1/32"}, remoteAddr: "127.0.0.1:1111", wantAllowed: true,
		},
		// The zone makes the peer address unparseable, so the gate fails closed -
		// even against a list that admits every parseable address of both families.
		{name: "unparseable zoned peer", allowed: []string{"::/0", "0.0.0.0/0"}, remoteAddr: "[fe80::1%eth0]:1111"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &config.ServerConfig{
				TrustProxyHeaders:  tt.trustProxy,
				TrustedProxies:     tt.proxies,
				MaxTrackedRateKeys: 100,
			}
			h := allowlistHandler(t, srv, tt.allowed...)

			called := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.realIP != "" {
				req.Header.Set("X-Real-IP", tt.realIP)
			}
			req = withRequestContext(req, "REQ-ALLOWLIST", DefaultContactMessage)
			rr := httptest.NewRecorder()
			h.enforceClientAllowlist(inner).ServeHTTP(rr, req)

			if called != tt.wantAllowed {
				t.Errorf("inner handler called = %v, want %v", called, tt.wantAllowed)
			}
			wantStatus := http.StatusForbidden
			if tt.wantAllowed {
				wantStatus = http.StatusOK
			}
			if rr.Code != wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, wantStatus)
			}
		})
	}

	// The gate does not depend on the rate limiter (server.New no longer refuses
	// that combination): a handler assembled without one must still admit the
	// listed peer and deny the unlisted one.
	t.Run("nil limiter", func(t *testing.T) {
		h := allowlistHandler(t, &config.ServerConfig{MaxTrackedRateKeys: 100}, allowedV4)
		h.limiter = nil

		do := func(remoteAddr string) (bool, int) {
			called := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
			req.RemoteAddr = remoteAddr
			req = withRequestContext(req, "REQ-NOLIMITER", DefaultContactMessage)
			rr := httptest.NewRecorder()
			h.enforceClientAllowlist(inner).ServeHTTP(rr, req)
			return called, rr.Code
		}

		if called, code := do("192.0.2.9:1111"); !called || code != http.StatusOK {
			t.Errorf("listed peer without a limiter: called = %v, status = %d, want true and 200", called, code)
		}
		if called, code := do("198.51.100.9:1111"); called || code != http.StatusForbidden {
			t.Errorf("unlisted peer without a limiter: called = %v, status = %d, want false and 403", called, code)
		}
	})
}
