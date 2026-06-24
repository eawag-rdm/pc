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

		ExtractToken(func(w http.ResponseWriter, r *http.Request) {
			if GetTokenFromContext(r) != "my-token" {
				t.Errorf("Expected token 'my-token', got '%s'", GetTokenFromContext(r))
			}
			w.WriteHeader(http.StatusOK)
		}).ServeHTTP(rr, req)

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
	handler := NewHandler(&config.Config{Server: &config.ServerConfig{ContactMessage: "C"}}, Config{}, discardLogger())

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

	handler := NewHandler(&config.Config{Server: &config.ServerConfig{LogClientIP: true}}, Config{}, logger)

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
