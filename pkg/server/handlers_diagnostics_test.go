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
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
	jsonformatter "github.com/eawag-rdm/pc/pkg/output/json"
)

// makeBrokenArchiveCKAN is makePDFCKAN's sibling: it serves one uploaded
// resource whose FileStore bytes are NOT the archive their name claims, so the
// archive-file-list phase fails to read it and the check engine emits a run
// diagnostic by value (utils.diagSink -> analysis.Result.Diagnostics). It
// returns the storage root so a caller can assert that path never reaches the
// response - t.TempDir() hands out a NEW directory per call and asserting on a
// second one can never fire.
func makeBrokenArchiveCKAN(t *testing.T) (*httptest.Server, *config.Config, string) {
	t.Helper()

	storage := t.TempDir()
	const resourceID = "abcdef0123456789"
	dir := filepath.Join(storage, "resources", resourceID[:3], resourceID[3:6])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, resourceID[6:]), []byte("not a zip at all"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	resourceURL := fmt.Sprintf("https://ckan.example.org/dataset/x/resource/%s/download/broken.zip", resourceID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, fmt.Sprintf(
			`{"success":true,"result":{"name":"zip-pkg","resources":[`+
				`{"url_type":"upload","url":%q,"name":"broken.zip","size":16}`+
				`]}}`, resourceURL))
	}))

	cfg, err := config.ParseConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatalf("load test config: %v", err)
	}
	cfg.Server = &config.ServerConfig{ContactMessage: DefaultContactMessage, LogClientIP: true}
	cc := cfg.Collectors["CkanCollector"]
	cc.Attrs["url"] = server.URL
	cc.Attrs["verify"] = false
	cc.Attrs["ckan_storage_path"] = storage
	return server, cfg, storage
}

// TestHandler_Analyze_EngineDiagnosticReachesResponse pins the SERVER WIRING of
// the returned diagnostics: a diagnostic the check ENGINE produced by value
// must reach the audience split - its raw text into the operator log, a soft
// path-free acknowledgement into the depositor's response.
//
// It asserts PROVENANCE, not just presence, and that is deliberate. Asserting
// only that the response holds an acknowledgement for broken.zip pins nothing:
// pkg/readers emits its own subject-tagged warning for the same file through
// the global logger, so the acknowledgement appears either way. Only the
// engine's own text ("archive filelist checks") is unique to the value channel.
// The logger is therefore put in the mode server.New uses in production
// (server.go:87), so the global's contribution is buffered and merged exactly
// as it is in a real run.
func TestHandler_Analyze_EngineDiagnosticReachesResponse(t *testing.T) {
	ckan, pcConfig, storage := makeBrokenArchiveCKAN(t)
	defer ckan.Close()

	output.GlobalLogger.SetJSONMode(true)
	output.GlobalLogger.ClearMessages()
	t.Cleanup(func() { output.GlobalLogger.ClearMessages() })

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	handler := NewHandler(pcConfig, Config{}, logger, testPlan(pcConfig))

	req := httptest.NewRequest("POST", "/api/v1/analyze", bytes.NewBufferString(`{"package_id":"zip-pkg"}`))
	req = withRequestContext(req, "REQ-DIAG", DefaultContactMessage)
	req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, "tok"))
	rec := httptest.NewRecorder()
	handler.Analyze(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	// Provenance: the engine's own diagnostic text reached the operator log.
	if !strings.Contains(logBuf.String(), "archive filelist checks") {
		t.Errorf("engine-returned diagnostic never reached the operator log: %s", logBuf.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, unscannedReason) {
		t.Errorf("no soft acknowledgement for the unreadable archive: %s", body)
	}
	if strings.Contains(body, storage) {
		t.Errorf("FileStore path leaked into the response: %s", body)
	}

	// The response carries NO raw diagnostics: the formatter is handed nil, so
	// the arrays are empty as a property of the wiring. Asserting the arrays
	// rather than the absence of one substring is what pins that.
	var result jsonformatter.ScanResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(result.Errors) != 0 || len(result.Warnings) != 0 {
		t.Errorf("raw diagnostics reached the depositor response: errors=%v warnings=%v", result.Errors, result.Warnings)
	}
}

// TestHandler_Analyze_RuleFocusedInResponse pins the SERVER WIRING of the
// details_rule_focused section: the CONFIGURED RULE that produced a finding must
// reach the response, not just the check that implements it.
//
// The assertion is on the RULE NAME on purpose. Asserting only that the section
// exists and is non-empty would still pass if the field were populated from the
// check name, and telling rule from check is the entire reason this section
// exists. "zip-pkg" ships no readme, and testdata/test_config.toml binds rule
// "readme-present" to check "HasReadme", so the pair is unambiguous: a rule-name
// regression shows up as "HasReadme" in both fields.
func TestHandler_Analyze_RuleFocusedInResponse(t *testing.T) {
	ckan, pcConfig, _ := makeBrokenArchiveCKAN(t)
	defer ckan.Close()

	output.GlobalLogger.ClearMessages()
	t.Cleanup(func() { output.GlobalLogger.ClearMessages() })

	handler := NewHandler(pcConfig, Config{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), testPlan(pcConfig))

	req := httptest.NewRequest("POST", "/api/v1/analyze", bytes.NewBufferString(`{"package_id":"zip-pkg"}`))
	req = withRequestContext(req, "REQ-RULES", DefaultContactMessage)
	req = req.WithContext(context.WithValue(req.Context(), CKANTokenKey, "tok"))
	rec := httptest.NewRecorder()
	handler.Analyze(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var response struct {
		DetailsRuleFocused []struct {
			Rule       string `json:"rule"`
			CheckName  string `json:"checkname"`
			IssueCount int    `json:"issue_count"`
			Subjects   []struct {
				Subject    string `json:"subject"`
				IssueCount int    `json:"issue_count"`
			} `json:"subjects"`
		} `json:"details_rule_focused"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(response.DetailsRuleFocused) == 0 {
		t.Fatalf("details_rule_focused is empty: %s", rec.Body.String())
	}

	found := false
	for _, entry := range response.DetailsRuleFocused {
		if entry.Rule != "readme-present" {
			continue
		}
		found = true
		if entry.CheckName != "HasReadme" {
			t.Errorf("rule %q: checkname = %q, want HasReadme", entry.Rule, entry.CheckName)
		}
		if got := entry.Subjects[0].Subject; got != "repository" {
			t.Errorf("rule %q: first subject = %q, want repository", entry.Rule, got)
		}
	}
	if !found {
		t.Errorf("no details_rule_focused entry for rule \"readme-present\": %s", rec.Body.String())
	}
}
