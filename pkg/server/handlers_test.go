package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
)

// discardLogger returns a slog logger that throws away output, for tests that
// don't inspect the access log.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// withRequestContext wires the request_id and contact suffix into the request
// context the way RequestContext middleware does, so handler-level tests see a
// populated request_id and contact.
func withRequestContext(req *http.Request, requestID, contact string) *http.Request {
	ctx := context.WithValue(req.Context(), requestIDKey, requestID)
	ctx = context.WithValue(ctx, contactKey, contact)
	return req.WithContext(ctx)
}

func TestHandler_Health(t *testing.T) {
	handler := NewHandler(&config.Config{}, Config{}, discardLogger())

	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()

	handler.Health(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	var response HealthResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Status != "ok" {
		t.Errorf("Expected status 'ok', got '%s'", response.Status)
	}
	if response.Version != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got '%s'", response.Version)
	}
	if response.Timestamp == "" {
		t.Error("Expected non-empty timestamp")
	}
}

// decodeEnvelope decodes the standard error envelope.
func decodeEnvelope(t *testing.T, rr *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var resp ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode envelope: %v", err)
	}
	return resp
}

func TestHandler_Analyze_MissingPackageID(t *testing.T) {
	handler := NewHandler(&config.Config{}, Config{}, discardLogger())

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-MISSING", DefaultContactMessage)

	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rr.Code)
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeMissingPackage {
		t.Errorf("Expected code %q, got %q", CodeMissingPackage, resp.Error.Code)
	}
}

func TestHandler_Analyze_InvalidJSON(t *testing.T) {
	handler := NewHandler(&config.Config{}, Config{}, discardLogger())

	body := bytes.NewBufferString(`{invalid json}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-BADJSON", DefaultContactMessage)

	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rr.Code)
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeInvalidRequest {
		t.Errorf("Expected code %q, got %q", CodeInvalidRequest, resp.Error.Code)
	}
}

// TestHandler_Analyze_NoCKANURL: with no collector URL configured, the handler
// fails internally (the URL is server-side only) and returns internal_error.
func TestHandler_Analyze_NoCKANURL(t *testing.T) {
	handler := NewHandler(&config.Config{}, Config{}, discardLogger())

	body := bytes.NewBufferString(`{"package_id": "test-package"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-NOURL", DefaultContactMessage)

	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", rr.Code)
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeInternalError {
		t.Errorf("Expected code %q, got %q", CodeInternalError, resp.Error.Code)
	}
}

// TestErrorCatalogue_StatusAndMessage asserts every catalogue code maps to the
// correct HTTP status and message, contact is present except for internal_error,
// and the body request_id equals the X-Request-Id header.
func TestErrorCatalogue_StatusAndMessage(t *testing.T) {
	const contact = "Custom contact suffix."

	for code, entry := range errorCatalogue {
		code, entry := code, entry
		t.Run(code, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req = withRequestContext(req, "REQ-"+code, contact)
			rr := httptest.NewRecorder()

			writeError(rr, req, code)

			if rr.Code != entry.Status {
				t.Errorf("code %q: expected status %d, got %d", code, entry.Status, rr.Code)
			}

			resp := decodeEnvelope(t, rr)
			if resp.Error.Code != code {
				t.Errorf("expected code %q, got %q", code, resp.Error.Code)
			}

			if code == CodeInternalError {
				if resp.Error.Contact != "" {
					t.Errorf("internal_error must omit contact, got %q", resp.Error.Contact)
				}
				if !strings.Contains(resp.Error.Message, "REQ-"+code) {
					t.Errorf("internal_error message must cite request_id, got %q", resp.Error.Message)
				}
			} else {
				if resp.Error.Message != entry.Message {
					t.Errorf("code %q: expected message %q, got %q", code, entry.Message, resp.Error.Message)
				}
				if resp.Error.Contact != contact {
					t.Errorf("code %q: expected contact %q, got %q", code, contact, resp.Error.Contact)
				}
			}

			// Body request_id must equal the X-Request-Id header.
			header := rr.Header().Get("X-Request-Id")
			if header != "REQ-"+code {
				t.Errorf("X-Request-Id header = %q, want %q", header, "REQ-"+code)
			}
			if resp.Error.RequestID != header {
				t.Errorf("body request_id %q != X-Request-Id %q", resp.Error.RequestID, header)
			}
		})
	}
}

// fakeCKAN returns an httptest server that answers package_show with a package
// containing only external-link resources (no uploads). It records, per
// requested package id, the raw Authorization header it received so tests can
// assert per-request token isolation.
func fakeCKAN(t *testing.T, mu *sync.Mutex, seen map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if mu != nil && seen != nil {
			mu.Lock()
			seen[id] = r.Header.Get("Authorization")
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		// success:true with one external-link resource (url_type != "upload"),
		// so no local files are collected.
		io.WriteString(w, `{"success":true,"result":{"resources":[`+
			`{"url_type":"link","url":"https://example.org/external.csv","name":"external.csv","size":10}`+
			`]}}`)
	}))
}

func ckanPCConfig(ckanURL string) *config.Config {
	return &config.Config{
		Server: &config.ServerConfig{ContactMessage: DefaultContactMessage, LogClientIP: true},
		Collectors: map[string]*config.CollectorConfig{
			"CkanCollector": {
				Attrs: map[string]interface{}{
					"url":               ckanURL,
					"verify":            false,
					"ckan_storage_path": "/tmp/ckan-test-storage",
				},
			},
		},
	}
}

// TestHandler_Analyze_RequestIDInBody verifies a successful analysis includes
// request_id in the body. (Zero upload resources -> package_not_found path is
// exercised separately; here we use a package that yields no files but assert
// the request_id appears in the error envelope too.)
func TestHandler_Analyze_NoFiles_PackageNotFound(t *testing.T) {
	ckan := fakeCKAN(t, nil, nil)
	defer ckan.Close()

	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger())

	body := bytes.NewBufferString(`{"package_id": "empty-pkg"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-NOFILES", DefaultContactMessage)
	ctx := context.WithValue(req.Context(), CKANTokenKey, "tok-abc")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodePackageNotFound {
		t.Errorf("expected %q, got %q", CodePackageNotFound, resp.Error.Code)
	}
	if resp.Error.RequestID != "REQ-NOFILES" {
		t.Errorf("expected request_id REQ-NOFILES, got %q", resp.Error.RequestID)
	}
}

// TestHandler_Analyze_NoTokenBleed proves concurrent requests with different
// tokens never cross: each request's token must reach CKAN as-is. Run under
// -race to also catch shared-map mutation.
func TestHandler_Analyze_NoTokenBleed(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]string)
	ckan := fakeCKAN(t, &mu, seen)
	defer ckan.Close()

	pcConfig := ckanPCConfig(ckan.URL)
	handler := NewHandler(pcConfig, Config{}, discardLogger())

	const n = 25
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pkg := "pkg-" + string(rune('A'+i%26)) + strings.Repeat("x", i%5)
			token := "token-for-" + pkg
			body := bytes.NewBufferString(`{"package_id":"` + pkg + `"}`)
			req := httptest.NewRequest("POST", "/api/v1/analyze", body)
			req = withRequestContext(req, "REQ-"+pkg, DefaultContactMessage)
			req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, token))
			rr := httptest.NewRecorder()
			handler.Analyze(rr, req)
		}(i)
	}
	wg.Wait()

	// Each package's CKAN call must have carried that package's own token.
	mu.Lock()
	defer mu.Unlock()
	for pkg, gotToken := range seen {
		want := "token-for-" + pkg
		if gotToken != want {
			t.Errorf("package %q saw token %q, want %q (token bleed)", pkg, gotToken, want)
		}
	}
}

func TestRespondJSON(t *testing.T) {
	rr := httptest.NewRecorder()

	data := map[string]string{"key": "value"}
	respondJSON(rr, http.StatusOK, data)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got '%s'", contentType)
	}

	var response map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response["key"] != "value" {
		t.Errorf("Expected key='value', got '%s'", response["key"])
	}
}

func TestAnalyzeRequest_JSONParsing(t *testing.T) {
	// Only package_id is parsed; the client cannot supply a CKAN base URL.
	var req AnalyzeRequest
	if err := json.Unmarshal([]byte(`{"package_id":"my-package"}`), &req); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	if req.PackageID != "my-package" {
		t.Errorf("PackageID = %q, want %q", req.PackageID, "my-package")
	}
}

// TestHandler_Analyze_NoToken_PublicPath drives Analyze end-to-end with NO
// token (no Authorization header / empty token in context). The public path
// must reach the collector and NOT 401: VerifyCKANAccess no longer short-
// circuits on an empty token (§2). The fakeCKAN fixture serves only an
// external-link resource, so zero files are collected and the result is
// package_not_found (404) — the point is that it is NOT invalid_token (401).
func TestHandler_Analyze_NoToken_PublicPath(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]string)
	ckan := fakeCKAN(t, &mu, seen)
	defer ckan.Close()

	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger())

	body := bytes.NewBufferString(`{"package_id": "public-pkg"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	// No CKANTokenKey in the context: this is the anonymous / public path.
	req = withRequestContext(req, "REQ-PUBLIC", DefaultContactMessage)

	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("public path must not 401; got 401 (body: %s)", rr.Body.String())
	}
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 package_not_found on the no-files public path, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code == CodeInvalidToken {
		t.Errorf("public path produced invalid_token; the empty-token guard is still present")
	}
	if resp.Error.Code != CodePackageNotFound {
		t.Errorf("expected %q, got %q", CodePackageNotFound, resp.Error.Code)
	}

	// CKAN must have been called with an empty Authorization header.
	mu.Lock()
	defer mu.Unlock()
	if got, ok := seen["public-pkg"]; !ok || got != "" {
		t.Errorf("expected empty Authorization forwarded to CKAN, ok=%v got=%q", ok, got)
	}
}

// TestAccessLog_RecordsPackageID wraps the REAL Analyze handler with AccessLog
// (as the server does) and asserts the emitted access record carries a non-empty
// package_id. This is the production chain that the previous context-value
// approach broke: the handler's package_id write must be visible to the access
// log via the shared holder.
func TestAccessLog_RecordsPackageID(t *testing.T) {
	ckan := fakeCKAN(t, nil, nil)
	defer ckan.Close()

	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewJSONHandler(&syncWriter{w: &buf, mu: &mu}, nil))
	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, logger)

	body := bytes.NewBufferString(`{"package_id": "log-me-pkg"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-ACCESSLOG", DefaultContactMessage)
	req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, "tok"))

	rr := httptest.NewRecorder()
	// AccessLog wraps the real handler — exactly the production layering.
	handler.AccessLog(http.HandlerFunc(handler.Analyze)).ServeHTTP(rr, req)

	mu.Lock()
	out := buf.String()
	mu.Unlock()

	if out == "" {
		t.Fatal("expected an access log record, got none")
	}
	if !strings.Contains(out, `"package_id":"log-me-pkg"`) {
		t.Errorf("access record missing non-empty package_id; got %s", out)
	}
}

// makePDFCKAN returns a fakeCKAN whose package_show serves a single url_type=
// "upload" PDF resource that resolves to a real file on disk under a temp
// storage root, plus a PC config pointing at that storage root. This exercises
// the PDFTracker append path (helpers.PDFTracker.AddFileIfPDF) and the snapshot
// read in the handler, which is where Reviewer 1's data race lives.
func makePDFCKAN(t *testing.T) (*httptest.Server, *config.Config) {
	t.Helper()

	storage := t.TempDir()
	// CKAN FileStore sharded layout: resources/<id[:3]>/<id[3:6]>/<id[6:]>.
	const resourceID = "abcdef0123456789"
	dir := filepath.Join(storage, "resources", resourceID[:3], resourceID[3:6])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	// Minimal but valid-enough PDF bytes; checks only need a readable file.
	pdfPath := filepath.Join(dir, resourceID[6:])
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\n%%EOF\n"), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	resourceURL := fmt.Sprintf("https://ckan.example.org/dataset/x/resource/%s/download/report.pdf", resourceID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, fmt.Sprintf(
			`{"success":true,"result":{"name":"pdf-pkg","resources":[`+
				`{"url_type":"upload","url":%q,"name":"report.pdf","size":15}`+
				`]}}`, resourceURL))
	}))

	// Load the real default config so every check (IsValidName, IsFreeOfKeywords,
	// ...) has its populated Tests/General entries, then point the CkanCollector
	// at this fixture's URL and temp storage root.
	cfg, err := config.ParseConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatalf("load test config: %v", err)
	}
	cfg.Server = &config.ServerConfig{ContactMessage: DefaultContactMessage, LogClientIP: true}
	cc := cfg.Collectors["CkanCollector"]
	cc.Attrs["url"] = server.URL
	cc.Attrs["verify"] = false
	cc.Attrs["ckan_storage_path"] = storage
	return server, cfg
}

// TestHandler_Analyze_ConcurrentPDF_RaceClean drives many concurrent analyses of
// a PDF-bearing package through the real handler. It must be green under
// `go test -race`: the per-request reset (PDFTracker.Reset / GlobalLogger.Clear),
// the AddFileIfPDF append during checks, and the snapshot read for the response
// body are serialized by analysisMu, so there is no unsynchronized read of
// PDFTracker.Files and no cross-request data bleed (§6, §9).
func TestHandler_Analyze_ConcurrentPDF_RaceClean(t *testing.T) {
	ckan, pcConfig := makePDFCKAN(t)
	defer ckan.Close()

	handler := NewHandler(pcConfig, Config{}, discardLogger())

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := bytes.NewBufferString(`{"package_id":"pdf-pkg"}`)
			req := httptest.NewRequest("POST", "/api/v1/analyze", body)
			req = withRequestContext(req, fmt.Sprintf("REQ-PDF-%d", i), DefaultContactMessage)
			req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, "tok"))
			rr := httptest.NewRecorder()
			handler.Analyze(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("expected 200 for PDF-bearing package, got %d (body: %s)", rr.Code, rr.Body.String())
				return
			}
			// The response must list exactly this request's single PDF — proof the
			// snapshot is consistent and not bleeding another request's state.
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(rr.Body.Bytes(), &obj); err != nil {
				t.Errorf("unmarshal response: %v", err)
				return
			}
			var pdfs []string
			if err := json.Unmarshal(obj["pdf_files"], &pdfs); err != nil {
				t.Errorf("unmarshal pdf_files: %v", err)
				return
			}
			if len(pdfs) != 1 {
				t.Errorf("expected exactly 1 pdf in response, got %d: %v", len(pdfs), pdfs)
			}
		}(i)
	}
	wg.Wait()
}

func TestWithRequestID_Additive(t *testing.T) {
	in := `{"timestamp":"t","scanned":[],"skipped":[]}`
	out, err := withRequestID(in, "REQ-XYZ")
	if err != nil {
		t.Fatalf("withRequestID error: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := obj["timestamp"]; !ok {
		t.Error("existing field 'timestamp' was dropped")
	}
	if _, ok := obj["skipped"]; !ok {
		t.Error("existing field 'skipped' was dropped")
	}
	var id string
	if err := json.Unmarshal(obj["request_id"], &id); err != nil || id != "REQ-XYZ" {
		t.Errorf("request_id = %q (err %v), want REQ-XYZ", id, err)
	}
}
