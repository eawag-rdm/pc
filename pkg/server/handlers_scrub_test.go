package server

import (
	"context"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// TestConvertScanDiagnostics asserts the server-mode split of raw scan
// diagnostics: one soft, path-free skip acknowledgement per distinct affected
// file (Subject), and subject-less diagnostics log-only. It covers both
// sources, since analysis.Run hands the handler one merged set: diagnostics the
// engine returned by value, and diagnostics it drained from the global.
func TestConvertScanDiagnostics(t *testing.T) {
	output.GlobalLogger.SetJSONMode(true)
	output.GlobalLogger.ClearMessages()

	// Two diagnostics for the SAME file, one for another file, one subject-less.
	output.GlobalLogger.FileWarning("data.csv", "Error getting file info '/nfsmount/ckan/default/resources/aaa/bbb/ccc': stat: permission denied")
	output.GlobalLogger.FileWarning("data.csv", "Error reading file '/nfsmount/ckan/default/resources/aaa/bbb/ccc': read: i/o error")
	output.GlobalLogger.FileError("archive.zip", "Processing archive 'archive.zip' failed: internal error")
	output.GlobalLogger.Warning("CKAN request failed with status code 500")

	// analysis.Run hands the handler the run's WHOLE diagnostic set: what it
	// drained from the global (the four above) plus what the check engine
	// returned by value (the fifth). Both classes must be split the same way.
	diags := append(output.GlobalLogger.Drain(), structs.Diagnostic{
		Level:     structs.DiagError,
		Message:   "Check IsFreeOfKeywords on file 'broken.zip' failed: internal error",
		Subject:   "broken.zip",
		Timestamp: "2026-08-13T00:00:00Z",
	})

	h := NewHandler(&config.Config{}, Config{}, discardLogger(), testPlan(&config.Config{}))
	soft := h.convertScanDiagnostics(context.Background(), "pkg-x", diags)

	if len(soft) != 3 {
		t.Fatalf("expected 3 deduplicated soft skips, got %d: %v", len(soft), soft)
	}
	subjects := map[string]bool{}
	for _, m := range soft {
		if !m.Skipped {
			t.Errorf("soft acknowledgement must be a skip entry, got %+v", m)
		}
		if m.Reason != unscannedReason || m.Content != unscannedReason {
			t.Errorf("soft acknowledgement must use the soft reason, got %+v", m)
		}
		f, ok := m.Source.(structs.File)
		if !ok {
			t.Fatalf("soft acknowledgement source must be a File, got %T", m.Source)
		}
		if f.Path != "" {
			t.Errorf("soft acknowledgement must carry no path, got %q", f.Path)
		}
		if strings.Contains(m.Content, "/nfsmount") || strings.Contains(m.Reason, "Error") {
			t.Errorf("soft acknowledgement leaks raw diagnostic text: %+v", m)
		}
		subjects[f.Name] = true
	}
	if !subjects["data.csv"] || !subjects["archive.zip"] {
		t.Errorf("expected soft skips for data.csv and archive.zip, got %v", subjects)
	}
	// The engine-returned diagnostic must be acknowledged exactly like a
	// global-sourced one - the value channel is not a second-class input.
	if !subjects["broken.zip"] {
		t.Errorf("engine-returned diagnostic produced no soft skip, got %v", subjects)
	}
}

// TestScrubMessagePaths asserts absolute FileStore paths are blanked on
// outgoing messages while non-file sources are untouched.
func TestScrubMessagePaths(t *testing.T) {
	messages := []structs.Message{
		{Content: "issue", Source: structs.File{Name: "a.txt", Path: "/nfsmount/ckan/default/resources/a"}},
		{Content: "repo notice", Source: structs.Repository{}},
	}
	scrubMessagePaths(messages)

	f := messages[0].Source.(structs.File)
	if f.Path != "" {
		t.Errorf("path must be blanked, got %q", f.Path)
	}
	if f.Name != "a.txt" {
		t.Errorf("display identity must survive scrubbing, got %q", f.Name)
	}
	if _, ok := messages[1].Source.(structs.Repository); !ok {
		t.Errorf("non-file source must be untouched, got %T", messages[1].Source)
	}
}
