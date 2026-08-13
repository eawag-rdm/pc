package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/metadata"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/eawag-rdm/pc/pkg/utils"
)

// buildFiles writes a tiny PDF-free file set that makes the file pipeline emit
// messages: a whitespace + keyword hit and a non-ASCII name. PDF-free on
// purpose - the wasm reader's cold compile would dominate the runtime.
func buildFiles(t *testing.T) []structs.File {
	t.Helper()
	dir := t.TempDir()

	write := func(name, content string) structs.File {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture %q: %v", name, err)
		}
		return structs.ToFile(path, "", -1, "")
	}

	return []structs.File{
		write("notes b.txt", "password in here"), // whitespace + keyword hit
		write("naïve_data.csv", "a,b\n1,2\n"),    // non-ASCII name
	}
}

// buildMetadata returns metadata whose checks all fail, so RunChecks yields a
// known, non-empty message set.
func buildMetadata() *metadata.Metadata {
	md := &metadata.Metadata{}
	md.Entity("package", "pkg-one").Field("author", nil, metadata.Required())
	md.Entity("resource", "res-one").Field("description", nil, metadata.Required())
	return md
}

// testConfig loads the shared test config; both engines read their per-check
// settings from it.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.ParseConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}
	return *cfg
}

// planFor compiles the rules the way each frontend does at startup.
func planFor(t *testing.T, cfg config.Config) *utils.Plan {
	t.Helper()
	plan, err := utils.Compile(&cfg, checks.NewRegistry())
	if err != nil {
		t.Fatalf("compile rules: %v", err)
	}
	return plan
}

// resetGlobalScanState clears the process-global state the checks accumulate
// into, so each Run starts from the same point.
func resetGlobalScanState() {
	output.GlobalLogger.ClearMessages()
	helpers.PDFTracker.Reset()
}

func isMetadataMessage(m structs.Message) bool {
	_, ok := m.Source.(*metadata.Entity)
	return ok
}

func countFileMessages(messages []structs.Message) int {
	n := 0
	for _, m := range messages {
		if _, ok := m.Source.(structs.File); ok {
			n++
		}
	}
	return n
}

// TestRun_NilMetadata pins that md == nil is tolerated (the local collector
// never produces metadata) and that the file messages still come back.
func TestRun_NilMetadata(t *testing.T) {
	cfg := testConfig(t)
	files := buildFiles(t)

	resetGlobalScanState()
	messages := Run(context.Background(), cfg, planFor(t, cfg), files, nil, nil).Messages
	resetGlobalScanState()

	if countFileMessages(messages) == 0 {
		t.Fatal("no File-sourced messages - the file pipeline did not run")
	}
	for _, m := range messages {
		if isMetadataMessage(m) {
			t.Fatalf("metadata message from a nil metadata: %q", m.Content)
		}
	}
}

// TestRun_MetadataMessagesFirst pins the documented order: every metadata
// message precedes every file message.
func TestRun_MetadataMessagesFirst(t *testing.T) {
	cfg := testConfig(t)
	files := buildFiles(t)
	md := buildMetadata()

	want := metadata.RunChecks(md)
	if len(want) == 0 {
		t.Fatal("fixture metadata produced no messages - it no longer pins the order")
	}

	resetGlobalScanState()
	messages := Run(context.Background(), cfg, planFor(t, cfg), files, md, nil).Messages
	resetGlobalScanState()

	if len(messages) <= len(want) {
		t.Fatalf("got %d messages, want more than the %d metadata ones", len(messages), len(want))
	}
	for i, m := range want {
		if messages[i].Content != m.Content || messages[i].TestName != m.TestName {
			t.Fatalf("message %d: got %q/%q, want the metadata message %q/%q",
				i, messages[i].TestName, messages[i].Content, m.TestName, m.Content)
		}
	}
	for i, m := range messages[len(want):] {
		if isMetadataMessage(m) {
			t.Fatalf("metadata message %d after the file messages: %q", i+len(want), m.Content)
		}
	}
	if countFileMessages(messages) == 0 {
		t.Fatal("no File-sourced messages - the file pipeline did not run")
	}
}

// TestRun_ProgressRoutesToProgressEngine pins the routing: a non-nil progress
// must reach ApplyAllChecksWithProgress. Only that engine reports progress, so
// an invocation is proof of the route; the final current == total pins that the
// whole engine ran, not just its opening callback.
func TestRun_ProgressRoutesToProgressEngine(t *testing.T) {
	cfg := testConfig(t)
	files := buildFiles(t)

	// The progress engine's file phase is single-threaded and the callback only
	// runs on that goroutine, so an unguarded slice is safe here.
	type call struct {
		current int
		total   int
		message string
	}
	var calls []call

	resetGlobalScanState()
	messages := Run(context.Background(), cfg, planFor(t, cfg), files, nil, func(current, total int, message string) {
		calls = append(calls, call{current: current, total: total, message: message})
	}).Messages
	resetGlobalScanState()

	if len(calls) == 0 {
		t.Fatal("progress callback never fired - Run did not route to ApplyAllChecksWithProgress")
	}
	last := calls[len(calls)-1]
	if last.total == 0 || last.current != last.total {
		t.Fatalf("progress did not finish: final current %d, total %d", last.current, last.total)
	}
	if strings.TrimSpace(last.message) == "" {
		t.Fatal("final progress message is empty")
	}
	if countFileMessages(messages) == 0 {
		t.Fatal("no File-sourced messages - the file pipeline did not run")
	}
}

// TestRun_MergesGlobalAndEngineDiagnostics pins Run as the run's single merge
// point: what other packages still write to output.GlobalLogger and what the
// engine returns by value both come back in one Result.Diagnostics, and the
// buffer is left empty so a second run cannot inherit the first one's notes.
func TestRun_MergesGlobalAndEngineDiagnostics(t *testing.T) {
	cfg := testConfig(t)
	files := buildFiles(t)

	resetGlobalScanState()
	output.GlobalLogger.SetJSONMode(true)
	t.Cleanup(resetGlobalScanState)

	// Stands in for the packages that still emit through the global (readers,
	// collectors, checks): emitted before Run, exactly as a collector's would be.
	output.GlobalLogger.FileWarning("data.csv", "planted by a collector")

	// And a file the ENGINE itself will report on: bytes that are not the
	// archive the name claims, so the archive-file-list phase emits into the
	// sink and that diagnostic comes back by value. Both halves of the merge
	// must be present - asserting only the global half leaves "Run drops
	// everything the engine returned" green.
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.zip")
	if err := os.WriteFile(broken, []byte("not a zip at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	files = append(files, structs.ToFile(broken, "", -1, ""))

	res := Run(context.Background(), cfg, planFor(t, cfg), files, nil, nil)

	var fromGlobal, fromEngine bool
	for _, d := range res.Diagnostics {
		if d.Message == "planted by a collector" && d.Subject == "data.csv" {
			fromGlobal = true
		}
		if strings.Contains(d.Message, "archive filelist checks") && d.Subject == "broken.zip" {
			fromEngine = true
		}
	}
	if !fromGlobal {
		t.Errorf("Run did not merge the buffered global diagnostics, got %v", res.Diagnostics)
	}
	if !fromEngine {
		t.Errorf("Run did not merge the diagnostics the engine returned, got %v", res.Diagnostics)
	}
	if buffered := output.GlobalLogger.GetMessages(); len(buffered) != 0 {
		t.Errorf("Run must drain the buffer, it still holds %v", buffered)
	}
}
