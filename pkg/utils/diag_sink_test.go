package utils

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// bufferedGlobal puts the process logger in buffering mode and empties it, so a
// test can assert where a diagnostic did NOT go. Without this the logger
// defaults to stream mode, where log() prints and buffers nothing - and any
// "it is not in the global" assertion would hold even if the diagnostic had
// gone there. Not parallel-safe: SetJSONMode has no lock, and the buffer is
// process-wide.
func bufferedGlobal(t *testing.T) {
	t.Helper()
	output.GlobalLogger.SetJSONMode(true)
	output.GlobalLogger.ClearMessages()
	t.Cleanup(func() { output.GlobalLogger.ClearMessages() })
}

// assertNotInGlobal fails if any buffered diagnostic contains substr.
//
// It matches on CONTENT rather than asserting the buffer is empty, and that is
// not fussiness: sibling tests in this package deliberately leave a cancelled
// scan running in a background goroutine (check_utils_ctx_test.go), and those
// stragglers write to this process-global buffer at unpredictable moments. An
// emptiness assertion is therefore flaky by construction - it was, and it
// failed in full-package runs while passing in isolation. Content matching
// still fails if the emission under test goes to the global.
func assertNotInGlobal(t *testing.T, substr string) {
	t.Helper()
	for _, d := range output.GlobalLogger.GetMessages() {
		if strings.Contains(d.Message, substr) {
			t.Errorf("pkg/utils wrote this diagnostic to the global logger: %+v", d)
		}
	}
}

// TestResultValueEquivalence pins the whole point of the diagnostics return:
// an unreadable archive is reported to the CALLER, by value, and nothing about
// it is written to the process global.
//
// The plan is deliberately restricted to the archive-file-list scope: a
// member-scope check would drive the archive iterator, which does still write
// to the global, and the assertion would then fail for an unrelated reason.
func TestResultValueEquivalence(t *testing.T) {
	bufferedGlobal(t)

	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.zip")
	if err := os.WriteFile(broken, []byte("this is not a zip file"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []structs.File{{Path: broken, Name: "broken.zip", IsArchive: true}}

	entry := mockEntry("listCheck", func(structs.File) []structs.Message { return nil })
	sink := &diagSink{}
	applyChecksFilteredByFileOnArchiveFileList(context.Background(), sink, config.Config{}, []checkRules{entry}, files)

	diags := sink.drain()
	found := false
	for _, d := range diags {
		if d.Level == structs.DiagWarning && strings.Contains(d.Message, "reading archive file list") {
			found = true
			if d.Subject != "broken.zip" {
				t.Errorf("diagnostic must be tagged with the archive's display name, got %q", d.Subject)
			}
			if d.Timestamp == "" {
				t.Error("diagnostic must be stamped when emitted")
			}
		}
	}
	if !found {
		t.Fatalf("unreadable archive produced no returned diagnostic, got %v", diags)
	}
	assertNotInGlobal(t, "reading archive file list")
}

// TestDiagnosticsSurviveCancellation covers the one behaviour the value channel
// actually risks: a diagnostic emitted by a worker during a run that is being
// cancelled must still come back.
//
// It does NOT pin where the drain sits: that rests on the join order (the
// deferred stop runs wg.Wait before the phase returns), not on this test.
func TestDiagnosticsSurviveCancellation(t *testing.T) {
	bufferedGlobal(t)

	files := writeTempFiles(t, 8)
	ctx, cancel := context.WithCancel(context.Background())

	var once sync.Once
	panicking := mockEntry("panicking", func(structs.File) []structs.Message {
		// Cancel from inside the first check, so the cancellation lands while
		// the pool is running, then panic to emit a diagnostic.
		once.Do(cancel)
		panic("boom: simulated check bug")
	})

	plan := &Plan{}
	plan.scopes[checks.ScopeFile] = []checkRules{panicking}
	_, diags := ApplyAllChecks(ctx, config.Config{}, plan, files)

	if len(diags) == 0 {
		t.Fatal("a cancelled run dropped the diagnostics its workers had already produced")
	}
	for _, d := range diags {
		if !strings.Contains(d.Message, "internal error") {
			t.Errorf("unexpected diagnostic: %+v", d)
		}
	}
	assertNotInGlobal(t, "internal error")
}

// TestDiagSinkDrainTransfersOwnership pins that drain TAKES the diagnostics
// rather than copying them out: the sink is empty afterwards, so a second drain
// cannot report the same diagnostic twice, and a later add cannot write into
// the backing array the caller now holds.
//
// Asserting only "the returned slice was not mutated" would pin nothing - with
// items left in place, the next add usually grows the slice into a fresh array
// and the old one survives by luck of capacity.
func TestDiagSinkDrainTransfersOwnership(t *testing.T) {
	sink := &diagSink{}
	sink.add(structs.DiagWarning, "a.txt", "first")
	first := sink.drain()
	if len(first) != 1 || first[0].Message != "first" {
		t.Fatalf("drain did not return the diagnostic: %v", first)
	}

	// Empty afterwards: a second drain must not re-report what the first took.
	if again := sink.drain(); len(again) != 0 {
		t.Fatalf("drain left the sink loaded, second drain returned %v", again)
	}

	// And a later add starts a fresh run's worth, invisible to the old owner.
	sink.add(structs.DiagError, "b.txt", "second")
	if len(first) != 1 || first[0].Message != "first" {
		t.Fatalf("drained slice was mutated by a later add: %v", first)
	}
	second := sink.drain()
	if len(second) != 1 || second[0].Message != "second" {
		t.Fatalf("expected only the later diagnostic, got %v", second)
	}
}

// TestDiagnosticsFromParallelArchives exercises the one path where several
// goroutines call sink.add concurrently: two or more archives select
// applyArchiveFileListChecksParallel, and each worker reports its own
// unreadable archive. Worth running under -race; the single-archive test above
// only covers the sequential branch.
func TestDiagnosticsFromParallelArchives(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("the parallel archive branch is gated on GOMAXPROCS > 1; this run would silently take the sequential one")
	}
	bufferedGlobal(t)

	dir := t.TempDir()
	var files []structs.File
	const n = 4
	for i := range n {
		name := fmt.Sprintf("broken%d.zip", i)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("not a zip file"), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, structs.File{Path: path, Name: name, IsArchive: true})
	}

	entry := mockEntry("listCheck", func(structs.File) []structs.Message { return nil })
	sink := &diagSink{}
	applyChecksFilteredByFileOnArchiveFileList(context.Background(), sink, config.Config{}, []checkRules{entry}, files)

	subjects := map[string]bool{}
	for _, d := range sink.drain() {
		subjects[d.Subject] = true
	}
	if len(subjects) != n {
		t.Errorf("expected one diagnostic per unreadable archive, got %d: %v", len(subjects), subjects)
	}
}

// assertPanicDiagnostic fails unless the panic notice is among diags. Asserting
// merely that SOME diagnostic came back is not enough: a broken archive fixture
// also trips the file-list phase, which would mask a member phase that reports
// nothing at all.
func assertPanicDiagnostic(t *testing.T, diags []structs.Diagnostic) {
	t.Helper()
	for _, d := range diags {
		if d.Level == structs.DiagError && strings.Contains(d.Message, "internal error") {
			return
		}
	}
	t.Fatalf("no panic diagnostic came back from this phase, got %v", diags)
}

// panickingPlan is a check that always panics, declared for one scope, so a
// phase can be driven into its recover path and asked for the diagnostic.
func panickingPlan(scope checks.Scope) *Plan {
	entry := mockEntry("panicking", func(structs.File) []structs.Message {
		panic("boom: simulated check bug")
	})
	entry.def.Scopes = checks.ScopesOf(scope)
	plan := &Plan{}
	plan.scopes[scope] = []checkRules{entry}
	return plan
}

// TestProgressEngineReturnsDiagnostics pins the DEFAULT CLI/TUI path. Its twin
// (ApplyAllChecks) was pinned from the start; without this, replacing the
// progress engine's whole diagnostics return with nil leaves the module green.
func TestProgressEngineReturnsDiagnostics(t *testing.T) {
	bufferedGlobal(t)

	files := writeTempFiles(t, 3)
	_, diags := ApplyAllChecksWithProgress(context.Background(), config.Config{},
		panickingPlan(checks.ScopeFile), files, func(structs.Progress) {})

	assertPanicDiagnostic(t, diags)
	assertNotInGlobal(t, "internal error")
}

// TestRepositoryPhaseReturnsDiagnostics pins the sink threading of the
// repository phase: shadowing its sink used to leave every test green.
func TestRepositoryPhaseReturnsDiagnostics(t *testing.T) {
	bufferedGlobal(t)

	def := checks.CheckDef{
		Name:   "panickingRepo",
		Scopes: checks.ScopesOf(checks.ScopeRepository),
		RunRepository: func(context.Context, structs.Repository, *checks.Batch, []*checks.BoundRule) []structs.Message {
			panic("boom: simulated repository check bug")
		},
	}
	plan := &Plan{}
	plan.scopes[checks.ScopeRepository] = []checkRules{{
		def:   &def,
		batch: &checks.Batch{},
		rules: []*checks.BoundRule{{Rule: "panickingRepo"}},
	}}

	_, diags := ApplyAllChecks(context.Background(), config.Config{}, plan, writeTempFiles(t, 1))
	assertPanicDiagnostic(t, diags)
	assertNotInGlobal(t, "internal error")
}

// TestArchiveMemberPhaseReturnsDiagnostics pins the sink threading of the
// archive-member phase, unpinned for the same reason.
func TestArchiveMemberPhaseReturnsDiagnostics(t *testing.T) {
	bufferedGlobal(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "member.zip")
	if err := os.WriteFile(path, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []structs.File{{Path: path, Name: "member.zip", IsArchive: true}}

	_, diags := ApplyAllChecks(context.Background(), config.Config{},
		panickingPlan(checks.ScopeArchiveMember), files)
	assertPanicDiagnostic(t, diags)
	assertNotInGlobal(t, "internal error")
}

// TestNewWorkerPoolRejectsNilSink pins the constructor guard: without a sink the
// first recovered panic would nil-dereference inside a deferred recover, which
// no recover catches.
func TestNewWorkerPoolRejectsNilSink(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("newWorkerPool accepted a nil sink")
		}
	}()
	newWorkerPool(context.Background(), nil, 1)
}
