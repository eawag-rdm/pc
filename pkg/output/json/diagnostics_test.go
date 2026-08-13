package json

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// TestFormatResults_DiagnosticsComeFromTheArgument pins that the formatter is a
// function of what it is handed: the diagnostics it renders are the ones passed
// in, and a message sitting in the process-global logger is NOT rendered.
//
// The planted message must be BUFFERED to pin anything - the logger defaults to
// stream mode, where log() prints and buffers nothing, so a plant made without
// SetJSONMode(true) would be invisible whether or not the formatter reads the
// global. Not parallel: SetJSONMode has no lock and the buffer is process-wide.
func TestFormatResults_DiagnosticsComeFromTheArgument(t *testing.T) {
	output.GlobalLogger.SetJSONMode(true)
	output.GlobalLogger.ClearMessages()
	t.Cleanup(func() { output.GlobalLogger.ClearMessages() })

	output.GlobalLogger.Error("planted in the global, must not be rendered")

	diagnostics := []structs.Diagnostic{
		{Level: structs.DiagError, Message: "passed as an argument", Timestamp: "2026-08-13T00:00:00Z"},
		{Level: structs.DiagWarning, Message: "also passed", Subject: "data.csv", Timestamp: "2026-08-13T00:00:00Z"},
	}

	out, err := NewJSONFormatter().FormatResults("loc", "coll", []structs.Message{}, 0, nil, diagnostics)
	if err != nil {
		t.Fatalf("FormatResults: %v", err)
	}

	var result ScanResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(result.Errors) != 1 || result.Errors[0].Message != "passed as an argument" {
		t.Errorf("errors[] must hold exactly the argument's error diagnostic, got %v", result.Errors)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Subject != "data.csv" {
		t.Errorf("warnings[] must hold exactly the argument's warning diagnostic, got %v", result.Warnings)
	}
	// Self-check: the plant has to be BUFFERED for this test to pin anything.
	// Matched by content rather than by buffer length - length would make the
	// test hostage to anything else the process left in the buffer.
	planted := false
	for _, d := range output.GlobalLogger.GetMessages() {
		if strings.Contains(d.Message, "planted in the global") {
			planted = true
		}
	}
	if !planted {
		t.Fatal("the plant did not buffer - the test would pin nothing")
	}
}
