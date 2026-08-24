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
	"time"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// TestMain hands over to the PDF worker loop when this binary was re-executed
// as one: the analysis these handlers drive extracts PDFs in a subprocess
// started from the running executable, which under `go test` is this test
// binary. Precautionary - no fixture here reaches extraction today, but without
// the handover the pool refuses to start workers at all, so the first PDF added
// would be acked as an unavailable engine while the suite stayed green.
func TestMain(m *testing.M) {
	readers.HandlePDFWorkerSentinel()
	os.Exit(m.Run())
}

// testPlan compiles the rule plan NewHandler requires, the way server.New does
// at boot. A handler test that never analyses still needs one: a nil plan would
// dispatch nothing and report every package clean. Configs built without a
// [general] section get the shipped defaults, which is what checks.Compile
// refuses to invent for itself.
func testPlan(pcConfig *config.Config) *checks.Plan {
	cfg := config.Config{}
	if pcConfig != nil {
		cfg = *pcConfig
	}
	if cfg.General == nil {
		cfg.General = &config.GeneralConfig{MaxContentScanFileSize: config.DefaultMaxContentScanFileSize}
	}
	// Compile refuses a config silent about the anchored checks; declare a rule
	// for the missing ones, without touching a caller's own rules.
	declared := func(name string) bool {
		for _, rule := range cfg.Rules {
			if rule.Check == name {
				return true
			}
		}
		return false
	}
	rules := append([]config.RuleSpec(nil), cfg.Rules...)
	for _, name := range checks.AnchoredChecks() {
		if !declared(name) {
			rules = append(rules, config.RuleSpec{Name: name, Check: name, Enabled: true})
		}
	}
	cfg.Rules = rules
	plan, err := checks.Compile(&cfg, checks.NewRegistry())
	if err != nil {
		panic("server test plan: " + err.Error())
	}
	return plan
}

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

// TestNewHandler_NilPlanPanics: a nil plan would dispatch no checks and cache
// every package as clean, so construction refuses it.
func TestNewHandler_NilPlanPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewHandler must panic on a nil plan")
		}
	}()
	NewHandler(&config.Config{}, Config{}, discardLogger(), nil)
}

func TestHandler_Health(t *testing.T) {
	handler := NewHandler(&config.Config{}, Config{}, discardLogger(), testPlan(&config.Config{}))

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
	handler := NewHandler(&config.Config{}, Config{}, discardLogger(), testPlan(&config.Config{}))

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
	handler := NewHandler(&config.Config{}, Config{}, discardLogger(), testPlan(&config.Config{}))

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
	handler := NewHandler(&config.Config{}, Config{}, discardLogger(), testPlan(&config.Config{}))

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
// allErrorCodes is every code the server can emit. It must stay in sync with the
// Code* constants in errors.go; TestErrorCatalogue_Complete enforces that each
// has a catalogue entry (and that the catalogue has no orphan entries), so a new
// code can never silently render as a fallback internal_error.
var allErrorCodes = []string{
	CodeMissingPackage, CodeInvalidPackageName, CodeInvalidRequest,
	CodeInvalidToken, CodeTokenRequired, CodeAccessDenied, CodePackageNotFound,
	CodeNotFound, CodeMethodNotAllowed,
	CodeRateLimited, CodeServiceBusy, CodeServiceNotReady, CodeServerRestarting,
	CodeCKANUnavailable, CodeMalformedResource, CodeAnalysisTimeout,
	CodeResourceUnreadable, CodeInternalError,
}

// TestErrorCatalogue_Complete guards the response model: every declared code has
// exactly one catalogue entry and the catalogue carries no codes beyond those.
func TestErrorCatalogue_Complete(t *testing.T) {
	for _, c := range allErrorCodes {
		if _, ok := errorCatalogue[c]; !ok {
			t.Errorf("code %q has no errorCatalogue entry (it would render as a fallback internal_error)", c)
		}
	}
	if len(errorCatalogue) != len(allErrorCodes) {
		t.Errorf("errorCatalogue has %d entries but allErrorCodes lists %d; keep them in sync", len(errorCatalogue), len(allErrorCodes))
	}
}

// TestCountSkipped_ExcludesRepositoryNotice asserts skipped_count reflects only
// per-file skips: the repository-scoped "no files to analyse" notice (also
// Skipped-flagged) must not inflate the count.
func TestCountSkipped_ExcludesRepositoryNotice(t *testing.T) {
	msgs := []structs.Message{
		{Skipped: true, Source: structs.File{Name: "big.csv"}}, // a real per-file skip
		{Skipped: true, Source: structs.Repository{}},          // the no-files notice
		{Skipped: false, Source: structs.File{Name: "ok.csv"}}, // not a skip
	}
	if got := countSkipped(msgs); got != 1 {
		t.Errorf("countSkipped = %d, want 1 (only the file skip, not the repository notice)", got)
	}
}

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

// ckanObservation records, per requested package id, the raw Authorization
// header CKAN received for that id. It is keyed by the unique package id (which
// is itself derived from the request index), so no two concurrent requests
// alias the same slot.
type ckanObservation struct {
	mu   sync.Mutex
	seen map[string]string
}

// observingCKAN returns a fake CKAN that records the Authorization header per
// requested package id into obs. Unlike fakeCKAN's plain map, the caller owns
// obs and can assert per-request isolation after the run.
func observingCKAN(t *testing.T, obs *ckanObservation) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		obs.mu.Lock()
		obs.seen[id] = r.Header.Get("Authorization")
		obs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"result":{"resources":[`+
			`{"url_type":"link","url":"https://example.org/external.csv","name":"external.csv","size":10}`+
			`]}}`)
	}))
}

func ckanPCConfig(ckanURL string) *config.Config {
	return &config.Config{
		General: &config.GeneralConfig{
			MaxArchiveFileSize:     10 * 1024 * 1024,
			MaxTotalArchiveMemory:  100 * 1024 * 1024,
			MaxContentScanFileSize: 20 * 1024 * 1024,
		},
		Server: &config.ServerConfig{ContactMessage: DefaultContactMessage, LogClientIP: true},
		Rules: []config.RuleSpec{{
			Name: "HasReadme", Check: "HasReadme", Enabled: true,
			Params: []map[string]interface{}{
				{"readme_names": []string{"readme.md", "readme.txt"}},
			},
		}},
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

// TestHandler_Analyze_NoUploadResources_OK asserts that a package which EXISTS
// (package_show 200) but has zero analyzable upload resources - here the fixture
// serves only an external-link resource - is NOT an error. The analysis runs and
// returns a normal 200 result (no file issues) carrying request_id. This is the
// fixed behavior: package_not_found is reserved for a genuine CKAN 404, not for
// a real package whose resources happen to be all links.
func TestHandler_Analyze_NoUploadResources_OK(t *testing.T) {
	ckan := fakeCKAN(t, nil, nil)
	defer ckan.Close()

	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

	body := bytes.NewBufferString(`{"package_id": "empty-pkg"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-NOFILES", DefaultContactMessage)
	ctx := context.WithValue(req.Context(), CKANTokenKey, "tok-abc")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for a package with no upload resources, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	// The success body is the scan result (typed ScanResult shape) with
	// request_id injected. Assert the stable top-level keys are present so the
	// success response model is pinned, not just that it's some JSON object.
	var obj map[string]json.RawMessage
	if err := json.NewDecoder(rr.Body).Decode(&obj); err != nil {
		t.Fatalf("failed to decode success body: %v", err)
	}
	for _, key := range []string{"request_id", "timestamp", "scanned", "skipped"} {
		if _, ok := obj[key]; !ok {
			t.Errorf("success body missing expected key %q; got keys %v", key, mapKeys(obj))
		}
	}
	// Server responses never carry raw scan diagnostics: warnings[]/errors[]
	// must be present AND empty (raw detail goes to the server log instead).
	for _, key := range []string{"warnings", "errors"} {
		var arr []json.RawMessage
		if err := json.Unmarshal(obj[key], &arr); err != nil {
			t.Fatalf("%s not decodable: %v", key, err)
		}
		if len(arr) != 0 {
			t.Errorf("server response %s must be empty, got %d entries", key, len(arr))
		}
	}
	var gotID string
	if err := json.Unmarshal(obj["request_id"], &gotID); err != nil {
		t.Fatalf("request_id missing/invalid in success body: %v", err)
	}
	if gotID != "REQ-NOFILES" {
		t.Errorf("expected request_id REQ-NOFILES, got %q", gotID)
	}

	// The result must clearly state there were no files to analyse, surfaced as a
	// skip-style notice (same treatment the CLI gives an empty location).
	var skipped []struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(obj["skipped"], &skipped); err != nil {
		t.Fatalf("skipped not decodable: %v", err)
	}
	found := false
	for _, s := range skipped {
		if strings.Contains(s.Reason, "No files were found to analyse") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'no files to analyse' skip notice in the result, got skipped=%v", skipped)
	}
}

// mapKeys returns the keys of a decoded JSON object, for test diagnostics.
func mapKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestHandler_Analyze_NoTokenBleed proves concurrent requests with different
// tokens never cross: each request's token must reach CKAN as-is. Every request
// is verified INDIVIDUALLY by its own index - each goroutine uses a UNIQUE
// package id ("pkg-<i>") and a UNIQUE token ("token-for-<i>"), and we assert
// that all N requests were observed and each one carried its own token (no slot
// is aliased/overwritten by another index). Run under -race to also catch
// shared-map mutation of the per-request token context.
func TestHandler_Analyze_NoTokenBleed(t *testing.T) {
	obs := &ckanObservation{seen: make(map[string]string)}
	ckan := observingCKAN(t, obs)
	defer ckan.Close()

	pcConfig := ckanPCConfig(ckan.URL)
	handler := NewHandler(pcConfig, Config{}, discardLogger(), testPlan(pcConfig))

	const n = 25
	// pkgs[i] is the unique package id for request i; tokens[i] its expected
	// token. Indexing by i (not by name) guarantees no two requests collide.
	pkgs := make([]string, n)
	tokens := make([]string, n)
	for i := range pkgs {
		pkgs[i] = fmt.Sprintf("pkg-%d", i)
		tokens[i] = fmt.Sprintf("token-for-%d", i)
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := bytes.NewBufferString(`{"package_id":"` + pkgs[i] + `"}`)
			req := httptest.NewRequest("POST", "/api/v1/analyze", body)
			req = withRequestContext(req, "REQ-"+pkgs[i], DefaultContactMessage)
			req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, tokens[i]))
			rr := httptest.NewRecorder()
			handler.Analyze(rr, req)
		}(i)
	}
	wg.Wait()

	obs.mu.Lock()
	defer obs.mu.Unlock()
	// Every request index must have been observed exactly once, carrying ITS OWN
	// token. Verifying per-index (rather than ranging an aliasing map) catches a
	// regression where one request's token bleeds into another's CKAN call.
	if len(obs.seen) != n {
		t.Fatalf("expected %d distinct package_show calls, observed %d: %v", n, len(obs.seen), obs.seen)
	}
	for i := 0; i < n; i++ {
		gotToken, ok := obs.seen[pkgs[i]]
		if !ok {
			t.Errorf("request %d (%q) was never observed at CKAN", i, pkgs[i])
			continue
		}
		if gotToken != tokens[i] {
			t.Errorf("request %d (%q) saw token %q, want %q (token bleed)", i, pkgs[i], gotToken, tokens[i])
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

// TestAnalyzeRequest_JSONParsing enforces the real §2 contract: a client may
// supply ONLY package_id. A client-supplied ckan_url / base_url field must be
// IGNORED so a request can never redirect the server's CKAN base (the removed
// SSRF + token-exfiltration vector). We decode a body that smuggles those
// fields and assert (a) package_id is still parsed, and (b) the decoded struct
// exposes no field that captured the attacker URL - proving AnalyzeRequest has
// no CkanURL/BaseURL field for the server to honor.
func TestAnalyzeRequest_JSONParsing(t *testing.T) {
	const malicious = `{"package_id":"my-package","ckan_url":"https://evil.example.com","base_url":"https://evil.example.com"}`

	var req AnalyzeRequest
	if err := json.Unmarshal([]byte(malicious), &req); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	if req.PackageID != "my-package" {
		t.Errorf("PackageID = %q, want %q", req.PackageID, "my-package")
	}

	// Re-marshal the decoded struct: the smuggled ckan_url/base_url must not
	// survive the round-trip. If AnalyzeRequest ever regains a CkanURL/BaseURL
	// field, the evil host reappears here and this test fails.
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	round := string(out)
	if strings.Contains(round, "evil.example.com") {
		t.Errorf("client-supplied CKAN URL leaked into AnalyzeRequest: %s", round)
	}
	for _, field := range []string{"ckan_url", "base_url", "CkanURL", "BaseURL"} {
		if strings.Contains(round, field) {
			t.Errorf("AnalyzeRequest exposes a client-controllable %q field (SSRF vector); round-trip=%s", field, round)
		}
	}
}

// TestHandler_Analyze_NoToken_PublicPath drives Analyze end-to-end with NO
// token (no Authorization header / empty token in context). The public path
// must reach the collector and NOT 401: the single package_show drives the
// outcome and an empty token is forwarded as an empty Authorization header
// (§2, §5). The fakeCKAN fixture serves only an external-link resource, so the
// package exists with zero upload files and the analysis returns a normal 200 -
// the point is that it is NOT invalid_token (401).
func TestHandler_Analyze_NoToken_PublicPath(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]string)
	ckan := fakeCKAN(t, &mu, seen)
	defer ckan.Close()

	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

	body := bytes.NewBufferString(`{"package_id": "public-pkg"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	// No CKANTokenKey in the context: this is the anonymous / public path.
	req = withRequestContext(req, "REQ-PUBLIC", DefaultContactMessage)

	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("public path must not 401; got 401 (body: %s)", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 on the no-files public path, got %d (body: %s)", rr.Code, rr.Body.String())
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
	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, logger, testPlan(ckanPCConfig(ckan.URL)))

	body := bytes.NewBufferString(`{"package_id": "log-me-pkg"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-ACCESSLOG", DefaultContactMessage)
	req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, "tok"))

	rr := httptest.NewRecorder()
	// AccessLog wraps the real handler - exactly the production layering.
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
// body are serialized by analysisMu, so there is no unsynchronized read of the
// tracker's file slice and no cross-request data bleed (§6, §9).
func TestHandler_Analyze_ConcurrentPDF_RaceClean(t *testing.T) {
	ckan, pcConfig := makePDFCKAN(t)
	defer ckan.Close()

	handler := NewHandler(pcConfig, Config{}, discardLogger(), testPlan(pcConfig))

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
			// The response must list exactly this request's single PDF - proof the
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

// TestHandler_Analyze_ResultCache drives the full analyze flow against a fake
// CKAN and asserts the cache contract: an unchanged metadata_modified serves
// the cached body (X-PC-Cache: hit, fresh request_id, package_show still
// called once per request for auth+freshness), and a bumped metadata_modified
// forces a full re-analysis.
func TestHandler_Analyze_ResultCache(t *testing.T) {
	var mu sync.Mutex
	metadataModified := "2026-07-28T10:00:00.000000"
	var calls int
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mm := metadataModified
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"result":{"name":"cache-pkg","metadata_modified":"`+mm+`","resources":[]}}`)
	}))
	defer ckan.Close()

	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))
	cache, err := newResultCache(t.TempDir(), 10, 0)
	if err != nil {
		t.Fatalf("newResultCache: %v", err)
	}
	handler.cache = cache

	// 1st request: miss, full analysis, result stored.
	rr1 := analyzeWithToken(handler, "cache-pkg", "tok")
	if rr1.Code != http.StatusOK {
		t.Fatalf("first: expected 200, got %d (body: %s)", rr1.Code, rr1.Body.String())
	}
	if got := rr1.Header().Get("X-PC-Cache"); got != "miss" {
		t.Errorf("first: X-PC-Cache = %q, want miss", got)
	}

	// 2nd request, unchanged package: hit - same body except request_id. Use a
	// distinct request id (analyzeWithToken derives it from the package id) so
	// the id-injection assertion below is meaningful.
	body2 := bytes.NewBufferString(`{"package_id":"cache-pkg"}`)
	req2 := httptest.NewRequest("POST", "/api/v1/analyze", body2)
	req2 = withRequestContext(req2, "REQ-SECOND", DefaultContactMessage)
	req2 = req2.WithContext(context.WithValue(req2.Context(), CKANTokenKey, "tok"))
	rr2 := httptest.NewRecorder()
	handler.Analyze(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second: expected 200, got %d (body: %s)", rr2.Code, rr2.Body.String())
	}
	if got := rr2.Header().Get("X-PC-Cache"); got != "hit" {
		t.Errorf("second: X-PC-Cache = %q, want hit", got)
	}
	var b1, b2 map[string]json.RawMessage
	if err := json.Unmarshal(rr1.Body.Bytes(), &b1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &b2); err != nil {
		t.Fatal(err)
	}
	if string(b1["request_id"]) == string(b2["request_id"]) {
		t.Error("cached response must carry the SECOND request's id, not the stored one")
	}
	// Compares scanned, details_metadata and details_rule_focused only - the
	// sections whose content a cache regression would silently change. Other
	// sections (skipped, the detail views, pdf_files, errors, warnings,
	// timestamp) are NOT covered here, so a new section is added to this
	// comparison deliberately rather than assumed covered.
	if string(b1["scanned"]) != string(b2["scanned"]) || string(b1["details_metadata"]) != string(b2["details_metadata"]) || string(b1["details_rule_focused"]) != string(b2["details_rule_focused"]) {
		t.Error("cached response content must equal the original analysis")
	}

	// package_show still runs once per request (auth + freshness signal).
	mu.Lock()
	if calls != 2 {
		t.Errorf("expected 2 package_show calls after 2 requests, got %d", calls)
	}
	metadataModified = "2026-07-28T11:11:11.000000" // package changed upstream
	mu.Unlock()

	// 3rd request: freshness mismatch -> full re-analysis.
	rr3 := analyzeWithToken(handler, "cache-pkg", "tok")
	if rr3.Code != http.StatusOK {
		t.Fatalf("third: expected 200, got %d", rr3.Code)
	}
	if got := rr3.Header().Get("X-PC-Cache"); got != "miss" {
		t.Errorf("third: X-PC-Cache = %q, want miss after metadata_modified bump", got)
	}
}

// TestHandler_Analyze_CancelledChecks_NotCached pins the cache-poisoning guard
// itself: once the request context has fired, runAnalysis must not write a cache
// entry - otherwise a result the handler rejects as 503/504 would be served to
// every later request until CKAN bumps metadata_modified. The handler's
// afterChecks seam fires the context right after the checks phase, making the
// cancellation deterministic (no sleeps). Real truncation is NOT exercised here:
// the fixture has zero resources, so nothing is cut short - only the guard is.
func TestHandler_Analyze_CancelledChecks_NotCached(t *testing.T) {
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"result":{"name":"cancel-cache-pkg","metadata_modified":"2026-07-28T10:00:00.000000","resources":[]}}`)
	}))
	defer ckan.Close()

	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))
	cache, err := newResultCache(t.TempDir(), 10, 0)
	if err != nil {
		t.Fatalf("newResultCache: %v", err)
	}
	handler.cache = cache

	body := bytes.NewBufferString(`{"package_id":"cancel-cache-pkg"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-CANCEL-CACHE", DefaultContactMessage)
	req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, "tok"))
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	// The CKAN fetch succeeds; the context dies only once the checks are done.
	handler.afterChecks = cancel

	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on cancellation during checks, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if resp := decodeEnvelope(t, rr); resp.Error.Code != CodeServiceBusy {
		t.Errorf("expected %q, got %q", CodeServiceBusy, resp.Error.Code)
	}

	if _, ok := cache.get("cancel-cache-pkg", "2026-07-28T10:00:00.000000"); ok {
		t.Error("cancelled analysis must not write a cache entry")
	}

	// A later request for the same (unchanged) package must re-analyse, not be
	// served the rejected body. Fresh handler over the same cache, so each
	// handler's seam is set once and never mutated.
	handler2 := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))
	handler2.cache = cache
	rr2 := analyzeWithToken(handler2, "cancel-cache-pkg", "tok")
	if rr2.Code != http.StatusOK {
		t.Fatalf("second: expected 200, got %d (body: %s)", rr2.Code, rr2.Body.String())
	}
	if got := rr2.Header().Get("X-PC-Cache"); got != "miss" {
		t.Errorf("second: X-PC-Cache = %q, want miss", got)
	}

	// Positive control: this fixture IS cacheable - the uncancelled run stored an
	// entry, so the no-entry assertion after the cancelled run was not vacuous.
	if _, ok := cache.get("cancel-cache-pkg", "2026-07-28T10:00:00.000000"); !ok {
		t.Error("expected a cache entry after the successful analysis")
	}
}

// TestHandler_Analyze_TokenForwarding pins the server's CKAN auth contract
// (spec §2): a request Bearer token is forwarded to CKAN verbatim; a request
// WITHOUT a token produces an anonymous upstream call (no Authorization
// header); and the operator token from the TOML config is NEVER sent by the
// server under either path - it exists for the CLI only.
func TestHandler_Analyze_TokenForwarding(t *testing.T) {
	var mu sync.Mutex
	var gotAuth []string
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		// Header.Get returns "" when the header is absent - exactly the
		// "anonymous request" observation we want to record.
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"result":{"name":"pkg","resources":[]}}`)
	}))
	defer ckan.Close()

	// Simulate an operator who put a real token in the TOML for CLI use.
	cfg := ckanPCConfig(ckan.URL)
	cfg.Collectors["CkanCollector"].Attrs["token"] = "toml-secret"
	handler := NewHandler(cfg, Config{}, discardLogger(), testPlan(cfg))

	// Path 1: Bearer token present -> forwarded verbatim.
	if rr := analyzeWithToken(handler, "pkg-with-token", "user-token"); rr.Code != http.StatusOK {
		t.Fatalf("with token: expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	// Path 2: no token -> anonymous upstream request.
	if rr := analyzeWithToken(handler, "pkg-anonymous", ""); rr.Code != http.StatusOK {
		t.Fatalf("without token: expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotAuth) != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", len(gotAuth))
	}
	if gotAuth[0] != "user-token" {
		t.Errorf("with token: CKAN saw Authorization %q, want %q", gotAuth[0], "user-token")
	}
	if gotAuth[1] != "" {
		t.Errorf("without token: CKAN saw Authorization %q, want none", gotAuth[1])
	}
	for i, a := range gotAuth {
		if a == "toml-secret" {
			t.Errorf("call %d sent the TOML config token upstream; the server must never use it", i)
		}
	}
}

// TestHandler_Analyze_MetadataInResponse asserts the analyze response carries
// the details_metadata section, populated from the SAME package_show fetch the
// file collection uses (no second CKAN call): a package with a non-conforming
// title and missing review fields yields package-entity metadata findings.
func TestHandler_Analyze_MetadataInResponse(t *testing.T) {
	var calls int
	var mu sync.Mutex
	ckan := statusCKAN(t, http.StatusOK,
		`{"success":true,"result":{"name":"meta-pkg","title":"No prefix here",`+
			`"private":false,"status":"complete","resources":[]}}`,
		&calls, &mu)
	defer ckan.Close()
	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

	rr := analyzeWithToken(handler, "meta-pkg", "tok")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}

	var obj struct {
		DetailsMetadata []struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			Issues []struct {
				Checkname string `json:"checkname"`
			} `json:"issues"`
		} `json:"details_metadata"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &obj); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(obj.DetailsMetadata) == 0 {
		t.Fatal("expected details_metadata entries, got none")
	}
	ent := obj.DetailsMetadata[0]
	if ent.Kind != "package" || ent.Name != "meta-pkg" {
		t.Errorf("expected package entity 'meta-pkg', got %s %q", ent.Kind, ent.Name)
	}
	found := map[string]bool{}
	for _, iss := range ent.Issues {
		found[iss.Checkname] = true
	}
	if !found["TitleFormat"] || !found["Required"] {
		t.Errorf("expected TitleFormat and Required findings, got %v", found)
	}

	// The metadata section must come from the single fetch: exactly one
	// package_show call for the whole analysis.
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("expected exactly 1 package_show call, got %d", got)
	}
}

// analyzeWithToken drives the real Analyze handler with the given package id and
// token through a request that carries a populated request context.
func analyzeWithToken(handler *Handler, packageID, token string) *httptest.ResponseRecorder {
	body := bytes.NewBufferString(`{"package_id":` + mustJSON(packageID) + `}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-"+packageID, DefaultContactMessage)
	if token != "" {
		req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, token))
	}
	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)
	return rr
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestHandler_Analyze_ValidPackageNames asserts valid CKAN names and lowercase
// UUIDs pass validation and therefore reach the collector. The fixture serves a
// package with only an external-link resource, so a validated name produces a
// normal 200 result (no uploads to analyze) - NOT a 400 invalid_package_name.
// Asserting the 200 outcome proves the validated name actually reached CKAN
// (an invalid name would be rejected with 400 BEFORE any CKAN call).
func TestHandler_Analyze_ValidPackageNames(t *testing.T) {
	ckan := fakeCKAN(t, nil, nil)
	defer ckan.Close()
	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

	valid := []string{
		"my-dataset",
		"dataset_2024",
		"ab",
		"f46e74be-1c61-4866-81da-9282c37c0c42", // lowercase UUID
		strings.Repeat("a", 100),
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			rr := analyzeWithToken(handler, name, "tok")
			if rr.Code != http.StatusOK {
				t.Fatalf("valid name %q: expected 200 (reached CKAN), got %d (body: %s)", name, rr.Code, rr.Body.String())
			}
		})
	}
}

// TestHandler_Analyze_InvalidPackageNames asserts injection / illegal-char ids
// are rejected as invalid_package_name (400) BEFORE any CKAN call. To prove no
// CKAN call happens, the fixture counts requests and must stay at zero.
func TestHandler_Analyze_InvalidPackageNames(t *testing.T) {
	var calls int
	var mu sync.Mutex
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		io.WriteString(w, `{"success":true,"result":{"resources":[]}}`)
	}))
	defer ckan.Close()
	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

	invalid := []string{
		"Has-Uppercase",
		"with space",
		"semi;colon",
		"slash/injection",
		"../etc/passwd",
		"name?id=other",
		"name&q=1",
		"a",                      // too short (1 char)
		strings.Repeat("a", 101), // too long
		`bad"quote`,
		"unicode-é",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			rr := analyzeWithToken(handler, name, "tok")
			if rr.Code != http.StatusBadRequest {
				t.Errorf("name %q: expected 400, got %d", name, rr.Code)
			}
			resp := decodeEnvelope(t, rr)
			if resp.Error.Code != CodeInvalidPackageName {
				t.Errorf("name %q: expected %q, got %q", name, CodeInvalidPackageName, resp.Error.Code)
			}
		})
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("invalid names must not reach CKAN, but %d call(s) were made", calls)
	}
}

// statusCKAN returns a fake CKAN that answers package_show with a fixed HTTP
// status and body, recording how many package_show calls it received.
func statusCKAN(t *testing.T, status int, body string, calls *int, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil && mu != nil {
			mu.Lock()
			*calls++
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
}

// TestHandler_Analyze_CKANOutcomeMapping covers the §3/§5 status mapping for the
// single package_show call.
func TestHandler_Analyze_CKANOutcomeMapping(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantHTTP int
		wantCode string
	}{
		{"404 -> package_not_found", http.StatusNotFound, ``, http.StatusNotFound, CodePackageNotFound},
		{"401 -> invalid_token", http.StatusUnauthorized, ``, http.StatusUnauthorized, CodeInvalidToken},
		{"403 -> access_denied", http.StatusForbidden, ``, http.StatusForbidden, CodeAccessDenied},
		{"500 -> ckan_unavailable", http.StatusInternalServerError, ``, http.StatusBadGateway, CodeCKANUnavailable},
		{"429 -> ckan_unavailable", http.StatusTooManyRequests, ``, http.StatusBadGateway, CodeCKANUnavailable},
		{
			name:     "200+success:false -> package_not_found",
			status:   http.StatusOK,
			body:     `{"success":false,"error":{"__type":"Authorization Error"}}`,
			wantHTTP: http.StatusNotFound,
			wantCode: CodePackageNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			var mu sync.Mutex
			ckan := statusCKAN(t, tt.status, tt.body, &calls, &mu)
			defer ckan.Close()
			handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

			rr := analyzeWithToken(handler, "some-pkg", "tok")
			if rr.Code != tt.wantHTTP {
				t.Errorf("expected HTTP %d, got %d (body: %s)", tt.wantHTTP, rr.Code, rr.Body.String())
			}
			resp := decodeEnvelope(t, rr)
			if resp.Error.Code != tt.wantCode {
				t.Errorf("expected code %q, got %q", tt.wantCode, resp.Error.Code)
			}

			// Exactly one package_show per analyze (single CKAN call, §5).
			mu.Lock()
			got := calls
			mu.Unlock()
			if got != 1 {
				t.Errorf("expected exactly 1 package_show call, got %d", got)
			}
		})
	}
}

// TestHandler_Analyze_AnonymousForbidden asserts a CKAN 403 maps to
// token_required (401) when the request carried NO token, and stays
// access_denied (403) when a token was sent.
func TestHandler_Analyze_AnonymousForbidden(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		wantHTTP int
		wantCode string
	}{
		{"no token -> token_required", "", http.StatusUnauthorized, CodeTokenRequired},
		{"with token -> access_denied", "tok", http.StatusForbidden, CodeAccessDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ckan := statusCKAN(t, http.StatusForbidden, ``, nil, nil)
			defer ckan.Close()
			handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

			rr := analyzeWithToken(handler, "some-pkg", tt.token)
			if rr.Code != tt.wantHTTP {
				t.Errorf("expected HTTP %d, got %d (body: %s)", tt.wantHTTP, rr.Code, rr.Body.String())
			}
			resp := decodeEnvelope(t, rr)
			if resp.Error.Code != tt.wantCode {
				t.Errorf("expected code %q, got %q", tt.wantCode, resp.Error.Code)
			}
		})
	}
}

// TestHandler_Analyze_TransportError asserts a CKAN connection failure maps to
// ckan_unavailable (502).
func TestHandler_Analyze_TransportError(t *testing.T) {
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ckan.URL
	ckan.Close() // refuse connections

	cfg := ckanPCConfig(url)
	handler := NewHandler(cfg, Config{}, discardLogger(), testPlan(cfg))

	rr := analyzeWithToken(handler, "some-pkg", "tok")
	if rr.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeCKANUnavailable {
		t.Errorf("expected %q, got %q", CodeCKANUnavailable, resp.Error.Code)
	}
}

// TestHandler_Analyze_UnusableCKANBody asserts a CKAN 200 whose body cannot be
// used (malformed JSON, or valid JSON with no "result" object) is treated as an
// UPSTREAM condition, not a server fault: 502 ckan_unavailable, no admin alert,
// and a ckan_upstream_outcome log line whose class is unusable_body - distinct
// from the "transport" class, so ops can tell "CKAN is down" from "CKAN is
// speaking garbage". Before F7 this path produced internal_error (500) and
// mailed the admin list.
func TestHandler_Analyze_UnusableCKANBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		// wantDetail is the collector's fixed, non-secret diagnostic. It is
		// spelled out here because it belongs to the log contract (ops dashboards
		// read it), not just to an internal constant in pkg/collectors.
		wantDetail string
	}{
		{"malformed JSON", `{"success":true,"result":{`, "malformed JSON in package_show response"},
		{"no result object", `{"success":true}`, "package_show response has no 'result' object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tt.body)
			}))
			defer ckan.Close()

			var buf bytes.Buffer
			var mu sync.Mutex
			logger := slog.New(slog.NewJSONHandler(&syncWriter{w: &buf, mu: &mu}, nil))
			handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, logger, testPlan(ckanPCConfig(ckan.URL)))

			a, alerts := newRecordingAlerter(t, nil)

			body := bytes.NewBufferString(`{"package_id":"garbage-pkg"}`)
			req := httptest.NewRequest("POST", "/api/v1/analyze", body)
			req = withRequestContext(req, "REQ-UNUSABLE", DefaultContactMessage)
			req = req.WithContext(withAlerter(req.Context(), a))
			req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, "tok"))
			rr := httptest.NewRecorder()
			handler.Analyze(rr, req)

			if rr.Code != http.StatusBadGateway {
				t.Errorf("expected 502, got %d (body: %s)", rr.Code, rr.Body.String())
			}
			envelope := rr.Body.String() // captured before decodeEnvelope consumes it
			resp := decodeEnvelope(t, rr)
			if resp.Error.Code != CodeCKANUnavailable {
				t.Errorf("expected code %q, got %q", CodeCKANUnavailable, resp.Error.Code)
			}
			// The diagnostic is for ops, not for the client: it must stay out of
			// the envelope, which carries the fixed catalogue message.
			if strings.Contains(envelope, tt.wantDetail) {
				t.Errorf("envelope leaked the CKAN error detail %q; got %s", tt.wantDetail, envelope)
			}

			mu.Lock()
			out := buf.String()
			mu.Unlock()
			if !strings.Contains(out, `"transport_error_class":"unusable_body"`) {
				t.Errorf("expected the unusable_body log class in the CKAN outcome record; got %s", out)
			}
			if strings.Contains(out, `"transport_error_class":"transport"`) {
				t.Errorf("an unusable body must not be logged as a transport failure; got %s", out)
			}
			if !strings.Contains(out, `"ckan_error_detail":"`+tt.wantDetail+`"`) {
				t.Errorf("expected ckan_error_detail %q in the CKAN outcome record; got %s", tt.wantDetail, out)
			}

			// An upstream condition is not a server fault: no admin mail.
			expectNoAlert(t, alerts)
		})
	}
}

// TestHandler_Analyze_MissingUpload_ResourceUnreadable asserts a url_type=
// "upload" resource whose backing file is absent on disk maps to
// resource_unreadable (500), not a panic or silent skip (§5).
func TestHandler_Analyze_MissingUpload_ResourceUnreadable(t *testing.T) {
	const resID = "abcdef0123456789" // -> resources/abc/def/0123456789 (no file written)
	resourceURL := fmt.Sprintf("https://ckan.example.org/dataset/x/resource/%s/download/data.csv", resID)
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, fmt.Sprintf(
			`{"success":true,"result":{"name":"p","resources":[`+
				`{"url_type":"upload","url":%q,"name":"data.csv","size":10}]}}`, resourceURL))
	}))
	defer ckan.Close()

	cfg := ckanPCConfig(ckan.URL)
	cfg.Collectors["CkanCollector"].Attrs["ckan_storage_path"] = t.TempDir()
	handler := NewHandler(cfg, Config{}, discardLogger(), testPlan(cfg))

	rr := analyzeWithToken(handler, "upload-pkg", "tok")
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeResourceUnreadable {
		t.Errorf("expected %q, got %q", CodeResourceUnreadable, resp.Error.Code)
	}
}

// TestHandler_Analyze_TokenForwardedRaw asserts the token reaches CKAN as a RAW
// Authorization header (no "Bearer " prefix), and exactly one package_show call
// happens per analyze (§2 token forwarding, §5 single call).
func TestHandler_Analyze_TokenForwardedRaw(t *testing.T) {
	var mu sync.Mutex
	var calls int
	var gotAuth string
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"result":{"resources":[]}}`)
	}))
	defer ckan.Close()
	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

	_ = analyzeWithToken(handler, "tok-pkg", "raw-ckan-token-123")

	mu.Lock()
	defer mu.Unlock()
	if gotAuth != "raw-ckan-token-123" {
		t.Errorf("expected raw token forwarded, got Authorization %q", gotAuth)
	}
	if strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("token forwarded with Bearer prefix: %q", gotAuth)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 package_show call, got %d", calls)
	}
}

// TestHandler_Analyze_BodyTooLarge asserts an oversized body is rejected as
// invalid_request (the MaxBytesReader cap, §2).
func TestHandler_Analyze_BodyTooLarge(t *testing.T) {
	handler := NewHandler(&config.Config{}, Config{}, discardLogger(), testPlan(&config.Config{}))

	huge := `{"package_id":"` + strings.Repeat("a", 8000) + `"}`
	req := httptest.NewRequest("POST", "/api/v1/analyze", bytes.NewBufferString(huge))
	req = withRequestContext(req, "REQ-HUGE", DefaultContactMessage)
	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for oversized body, got %d", rr.Code)
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeInvalidRequest {
		t.Errorf("expected %q, got %q", CodeInvalidRequest, resp.Error.Code)
	}
}

// TestHandler_Analyze_Timeout asserts a request whose hard timeout fires while
// the analysis is still running returns a clean 504 envelope carrying the
// analysis_timeout code (spec §2) - NOT ckan_unavailable, since the deadline
// bounds the whole analysis rather than indicating CKAN is unreachable. It uses
// a CKAN that parks until released rather than a fixed wall-clock sleep, so the
// test does not burn real time and the 504 is driven purely by the 1s
// requestTimeoutSeconds firing on the in-flight call.
// (TestHandler_Analyze_Timeout_HungUpstream additionally bounds the elapsed time.)
func TestHandler_Analyze_Timeout(t *testing.T) {
	release := make(chan struct{})
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // park until released, well past the 1s timeout
	}))
	// LIFO defers: release the parked handler BEFORE Close() so Close does not
	// block waiting on the still-parked goroutine.
	defer ckan.Close()
	defer close(release)

	cfg := ckanPCConfig(ckan.URL)
	cfg.Server.RequestTimeoutSeconds = 1
	handler := NewHandler(cfg, Config{}, discardLogger(), testPlan(cfg))

	rr := analyzeWithToken(handler, "slow-pkg", "tok")
	if rr.Code != http.StatusGatewayTimeout {
		t.Errorf("expected 504 on timeout, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeAnalysisTimeout {
		t.Errorf("expected %q on timeout, got %q", CodeAnalysisTimeout, resp.Error.Code)
	}
}

// TestHandler_Analyze_Timeout_HungUpstream is the case the timeout exists for:
// CKAN accepts the TCP connection but NEVER responds. The hard requestTimeout
// must abort the in-flight call and return a clean 504 envelope within ~the
// configured budget, rather than blocking until the server WriteTimeout (spec
// §2). With the previous context-unaware collector this test would hang.
func TestHandler_Analyze_Timeout_HungUpstream(t *testing.T) {
	release := make(chan struct{})
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // never respond until released
	}))
	// Defers run LIFO: close(release) must happen BEFORE ckan.Close(), otherwise
	// Close() blocks forever waiting on the still-parked handler goroutine.
	defer ckan.Close()
	defer close(release)

	cfg := ckanPCConfig(ckan.URL)
	cfg.Server.RequestTimeoutSeconds = 1
	handler := NewHandler(cfg, Config{}, discardLogger(), testPlan(cfg))

	done := make(chan *httptest.ResponseRecorder, 1)
	start := time.Now()
	go func() { done <- analyzeWithToken(handler, "hung-pkg", "tok") }()

	select {
	case rr := <-done:
		elapsed := time.Since(start)
		if rr.Code != http.StatusGatewayTimeout {
			t.Errorf("expected 504 on hung upstream, got %d (body: %s)", rr.Code, rr.Body.String())
		}
		resp := decodeEnvelope(t, rr)
		if resp.Error.Code != CodeAnalysisTimeout {
			t.Errorf("expected %q, got %q", CodeAnalysisTimeout, resp.Error.Code)
		}
		// Must finish near the 1s budget. A 5s ceiling is generous for CI
		// scheduling jitter yet tight enough to catch a regression that waited on
		// a longer timeout (e.g. a 5s/300s WriteTimeout) instead of the 1s one.
		if elapsed > 5*time.Second {
			t.Errorf("handler did not honor the 1s request timeout: took %v", elapsed)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("handler hung on a non-responding CKAN: request timeout was not enforced on the in-flight call")
	}
}

// TestHandler_Analyze_ClientCancelled_ServiceBusy drives Analyze with a request
// context that is ALREADY cancelled (the client went away). This exercises the
// ctx.Err()==context.Canceled branch - distinct from the DeadlineExceeded (504)
// branch - which must render service_busy (503), not gateway_timeout. The
// collector's in-flight call aborts immediately on the cancelled context, so the
// handler sees a non-nil ctx.Err() that is NOT a deadline.
func TestHandler_Analyze_ClientCancelled_ServiceBusy(t *testing.T) {
	ckan := fakeCKAN(t, nil, nil)
	defer ckan.Close()

	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

	body := bytes.NewBufferString(`{"package_id":"cancel-pkg"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-CANCEL", DefaultContactMessage)
	req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, "tok"))

	// Pre-cancel the request context: simulate the client disconnecting before /
	// while the analysis runs. The handler's own WithTimeout wraps this cancelled
	// parent, so the derived ctx is cancelled (Canceled, not DeadlineExceeded).
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on client cancellation, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeServiceBusy {
		t.Errorf("expected %q on client cancellation (not the 504 deadline branch), got %q", CodeServiceBusy, resp.Error.Code)
	}
}

// TestHandler_Analyze_UnknownCKANType_InternalNoLeak: CKAN answers HTTP 200 with
// success:false and an UNRECOGNISED error.__type ("Validation Error"). This is
// not a 401/403/404/transport-5xx, so it maps to internal_error (500) - CKAN was
// reachable, so it is NOT ckan_unavailable. Critically, the verbose CKAN body
// text (including the __type and any message) must NOT leak into the client
// envelope (spec §3: raw CKAN bodies are kept out and logged instead).
func TestHandler_Analyze_UnknownCKANType_InternalNoLeak(t *testing.T) {
	const leakyType = "Validation Error"
	const leakySecret = "internal-ckan-detail-do-not-leak"
	body := `{"success":false,"error":{"__type":"` + leakyType + `","message":"` + leakySecret + `"}}`
	ckan := statusCKAN(t, http.StatusOK, body, nil, nil)
	defer ckan.Close()

	handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

	rr := analyzeWithToken(handler, "unknown-type-pkg", "tok")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 internal_error for an unknown __type, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeInternalError {
		t.Errorf("expected %q for an unknown CKAN __type, got %q", CodeInternalError, resp.Error.Code)
	}
	// The verbose CKAN body must not surface to the client.
	raw := rr.Body.String()
	if strings.Contains(raw, leakyType) {
		t.Errorf("CKAN __type leaked into the client envelope: %s", raw)
	}
	if strings.Contains(raw, leakySecret) {
		t.Errorf("CKAN body detail leaked into the client envelope: %s", raw)
	}
}

// TestHandler_Analyze_MalformedResource_Surfaced: a malformed resource is a
// user-fixable data problem, not a server fault. CKAN returns a package with a
// malformed resource (either missing BOTH url_type and url, or an upload missing
// required metadata); the collector returns a MalformedResourceError and the
// handler maps it to malformed_resource (422). Unlike a server-fault envelope,
// the collector's authored, user-facing message IS surfaced verbatim so the user
// learns the exact resource + package to fix (spec §3). The message names only
// the resource and package - no token, URL or internal path.
func TestHandler_Analyze_MalformedResource_Surfaced(t *testing.T) {
	cases := []struct {
		name      string
		pkg       string
		resources string
		wantLabel string // resource label that must appear in the surfaced message
		wantHints []string
	}{
		{
			name:      "missing url_type and url",
			pkg:       "malformed-pkg",
			resources: `{"name":"broken-resource","size":10}`,
			wantLabel: "broken-resource",
			wantHints: []string{"url_type", "recreate", "reupload"},
		},
		{
			name:      "upload missing required metadata",
			pkg:       "malformed-upload-pkg",
			resources: `{"url_type":"upload","name":"half-upload","size":10}`, // no url
			wantLabel: "half-upload",
			wantHints: []string{"missing required", "recreate", "reupload"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"success":true,"result":{"name":"`+tc.pkg+`","resources":[`+tc.resources+`]}}`)
			}))
			defer ckan.Close()

			handler := NewHandler(ckanPCConfig(ckan.URL), Config{}, discardLogger(), testPlan(ckanPCConfig(ckan.URL)))

			rr := analyzeWithToken(handler, tc.pkg, "tok")
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422 malformed_resource, got %d (body: %s)", rr.Code, rr.Body.String())
			}
			resp := decodeEnvelope(t, rr)
			if resp.Error.Code != CodeMalformedResource {
				t.Errorf("expected %q, got %q", CodeMalformedResource, resp.Error.Code)
			}
			// The authored message MUST be surfaced: it names the resource and
			// package and tells the user what to do.
			msg := resp.Error.Message
			if !strings.Contains(msg, tc.wantLabel) {
				t.Errorf("surfaced message must name the resource %q; got %q", tc.wantLabel, msg)
			}
			if !strings.Contains(msg, tc.pkg) {
				t.Errorf("surfaced message must name the package %q; got %q", tc.pkg, msg)
			}
			for _, hint := range tc.wantHints {
				if !strings.Contains(msg, hint) {
					t.Errorf("surfaced message missing guidance %q; got %q", hint, msg)
				}
			}
		})
	}
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

// TestHandler_Analyze_SlowCKAN_TimesOutAsUnavailable asserts the dedicated CKAN
// call deadline ([server] ckanRequestTimeoutSeconds) is enforced: a CKAN that
// does not answer package_show within it is treated as unavailable
// (ckan_unavailable, 502) at ~the configured bound - it does not eat the
// whole-analysis (requestTimeoutSeconds) budget.
func TestHandler_Analyze_SlowCKAN_TimesOutAsUnavailable(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold the response until the test ends or the client gives up.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release) // runs BEFORE slow.Close (LIFO), unblocking the stub

	pcConfig := ckanPCConfig(slow.URL)
	pcConfig.Server.CkanRequestTimeoutSeconds = 1
	handler := NewHandler(pcConfig, Config{}, discardLogger(), testPlan(pcConfig))

	body := bytes.NewBufferString(`{"package_id": "slow-pkg"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req = withRequestContext(req, "REQ-SLOWCKAN", DefaultContactMessage)

	start := time.Now()
	rr := httptest.NewRecorder()
	handler.Analyze(rr, req)
	elapsed := time.Since(start)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 ckan_unavailable, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeCKANUnavailable {
		t.Errorf("expected code %q, got %q", CodeCKANUnavailable, resp.Error.Code)
	}
	if elapsed > 5*time.Second {
		t.Errorf("handler took %s; the 1s CKAN deadline was not enforced", elapsed)
	}
}
