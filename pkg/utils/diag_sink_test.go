package utils

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
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
	applyChecksFilteredByFileOnArchiveFileList(context.Background(), sink, config.Config{}, []checks.PlanEntry{entry}, files)

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

	plan := checks.NewPlan(map[checks.Scope][]checks.PlanEntry{checks.ScopeFile: {panicking}})
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
	applyChecksFilteredByFileOnArchiveFileList(context.Background(), sink, config.Config{}, []checks.PlanEntry{entry}, files)

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

// assertPanicSkip fails unless m acknowledges, for reason, that check panicked
// on source: a non-transient skip, so the result is cached like any verdict.
func assertPanicSkip(t *testing.T, m structs.Message, check string, source structs.Source, reason string) {
	t.Helper()
	if !m.Skipped || m.Transient || m.TestName != check || m.Content != reason || m.Reason != reason || !reflect.DeepEqual(m.Source, source) {
		t.Errorf("expected a non-transient skip of %s on %+v, got %+v", check, source, m)
	}
}

// panicSkips returns the skips among messages.
func panicSkips(messages []structs.Message) []structs.Message {
	var skips []structs.Message
	for _, m := range messages {
		if m.Skipped {
			skips = append(skips, m)
		}
	}
	return skips
}

// panickingPlan is a check that always panics, declared for one scope, so a
// phase can be driven into its recover path and asked for the diagnostic.
func panickingPlan(scope checks.Scope) *checks.Plan {
	entry := mockEntry("panicking", func(structs.File) []structs.Message {
		panic("boom: simulated check bug")
	})
	entry.Def.Scopes = checks.ScopesOf(scope)
	return checks.NewPlan(map[checks.Scope][]checks.PlanEntry{scope: {entry}})
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
// repository phase: shadowing its sink used to leave every test green. The
// panicking check is acknowledged as skipped on the repository.
func TestRepositoryPhaseReturnsDiagnostics(t *testing.T) {
	bufferedGlobal(t)

	def := checks.CheckDef{
		Name:   "panickingRepo",
		Scopes: checks.ScopesOf(checks.ScopeRepository),
		RunRepository: func(context.Context, structs.Repository, *checks.Batch, []*checks.BoundRule) []structs.Message {
			panic("boom: simulated repository check bug")
		},
	}
	plan := checks.NewPlan(map[checks.Scope][]checks.PlanEntry{checks.ScopeRepository: {{
		Def:   &def,
		Batch: &checks.Batch{},
		Rules: []*checks.BoundRule{{Rule: "panickingRepo"}},
	}}})

	files := writeTempFiles(t, 1)
	messages, diags := ApplyAllChecks(context.Background(), config.Config{}, plan, files)
	assertPanicDiagnostic(t, diags)
	assertNotInGlobal(t, "internal error")
	skips := panicSkips(messages)
	if len(skips) != 1 {
		t.Fatalf("expected one skip of the panicking repository check, got %v", messages)
	}
	assertPanicSkip(t, skips[0], "panickingRepo", structs.Repository{}, checkPanicReason)
}

// TestArchiveMemberPhaseReturnsDiagnostics pins the sink threading of the
// archive-member phase, unpinned for the same reason. That phase hands the
// check the archive itself, so its skip is sourced there.
func TestArchiveMemberPhaseReturnsDiagnostics(t *testing.T) {
	bufferedGlobal(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "member.zip")
	if err := os.WriteFile(path, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []structs.File{{Path: path, Name: "member.zip", IsArchive: true}}

	messages, diags := ApplyAllChecks(context.Background(), config.Config{},
		panickingPlan(checks.ScopeArchiveMember), files)
	assertPanicDiagnostic(t, diags)
	assertNotInGlobal(t, "internal error")
	var skips []structs.Message
	for _, m := range panicSkips(messages) {
		if m.TestName == "panicking" {
			skips = append(skips, m)
		}
	}
	if len(skips) != 1 {
		t.Fatalf("expected one skip of the panicking check, got %v", messages)
	}
	assertPanicSkip(t, skips[0], "panicking", files[0], checkPanicReason)
}

// TestArchiveFileListPanicSkipsOncePerCheck pins the fold of member skips: a
// check panicking on every member of an archive is acknowledged once, on the
// archive, while its healthy sibling still runs on every member.
func TestArchiveFileListPanicSkipsOncePerCheck(t *testing.T) {
	bufferedGlobal(t)

	panicking := mockEntry("panicking", func(structs.File) []structs.Message {
		panic("boom: simulated check bug")
	})
	healthy := mockEntry("healthy", func(file structs.File) []structs.Message {
		return []structs.Message{{Content: "healthy ran", Source: file}}
	})
	archive := structs.File{Path: "../../testdata/archives/ten_valid_files.zip", Name: "ten_valid_files.zip", IsArchive: true}

	sink := &diagSink{}
	messages := applyChecksFilteredByFileOnArchiveFileList(context.Background(), sink, config.Config{},
		[]checks.PlanEntry{panicking, healthy}, []structs.File{archive})
	assertPanicDiagnostic(t, sink.drain())

	skips := panicSkips(messages)
	if len(skips) != 1 {
		t.Fatalf("expected one skip of the panicking check, got %v", skips)
	}
	assertPanicSkip(t, skips[0], "panicking", archive, archivePanicReason)
	healthyRan := 0
	for _, m := range messages {
		if m.Content == "healthy ran" {
			healthyRan++
		}
	}
	if healthyRan != 10 {
		t.Errorf("expected the healthy check to run on all 10 members, got %v", messages)
	}
}

// TestArchiveFileListPanicSkipSurvivesCancel pins that a cancel arriving with
// the panic still leaves the folded skip: the member walk stops, it does not
// drop what it has collected.
func TestArchiveFileListPanicSkipSurvivesCancel(t *testing.T) {
	bufferedGlobal(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	panicking := mockEntry("panicking", func(structs.File) []structs.Message {
		cancel()
		panic("boom: simulated check bug")
	})
	archive := structs.File{Path: "../../testdata/archives/ten_valid_files.zip", Name: "ten_valid_files.zip", IsArchive: true}

	messages := applyChecksFilteredByFileOnArchiveFileList(ctx, &diagSink{}, config.Config{},
		[]checks.PlanEntry{panicking}, []structs.File{archive})
	if skips := panicSkips(messages); len(skips) != 1 {
		t.Fatalf("expected the panicking check's skip despite the cancel, got %v", messages)
	}
}

// TestArchiveGuardPanicSkipsEveryCheckOfThePass drives each whole-archive guard
// into its recover path. Every check of the pass is acknowledged once on the
// archive, including one listed in both entry lists of the fused walk.
func TestArchiveGuardPanicSkipsEveryCheckOfThePass(t *testing.T) {
	bufferedGlobal(t)

	// A nil rule panics in (*checks.BoundRule).Match, called by matchRules
	// while archiveFileListMemberChecks selects the checks for a member -
	// before safeRunCheck runs, so only the whole-archive guard can recover it.
	// It sits in file-list entries only: the member phase's own selection would
	// hit it outside any guard.
	broken := mockEntry("broken", nil)
	broken.Rules = []*checks.BoundRule{nil}
	healthy := mockEntry("healthy", func(structs.File) []structs.Message { return nil })
	memberOnly := mockEntry("memberOnly", func(structs.File) []structs.Message { return nil })

	tests := []struct {
		name    string
		archive structs.File
		run     func(sink *diagSink, archive structs.File) []structs.Message
		want    []string
	}{
		{
			name:    "file-list pass",
			archive: structs.File{Path: "../../testdata/archives/test.zip", Name: "test.zip", IsArchive: true},
			run: func(sink *diagSink, archive structs.File) []structs.Message {
				return applyChecksFilteredByFileOnArchiveFileList(context.Background(), sink, config.Config{},
					[]checks.PlanEntry{broken, healthy}, []structs.File{archive})
			},
			want: []string{"broken", "healthy"},
		},
		{
			name:    "stream-list pass",
			archive: structs.File{Path: "../../testdata/archives/test.tar.gz", Name: "test.tar.gz", IsArchive: true},
			run: func(sink *diagSink, archive structs.File) []structs.Message {
				return applyChecksFilteredByFileOnArchive(context.Background(), sink, config.Config{},
					[]checks.PlanEntry{broken, healthy}, []checks.PlanEntry{healthy, memberOnly}, []structs.File{archive})
			},
			want: []string{"broken", "healthy", "memberOnly"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &diagSink{}
			messages := tt.run(sink, tt.archive)
			assertPanicDiagnostic(t, sink.drain())

			if len(messages) != len(tt.want) {
				t.Fatalf("expected one skip per distinct check %v, got %v", tt.want, messages)
			}
			for _, check := range tt.want {
				if !slices.ContainsFunc(messages, func(m structs.Message) bool { return m.TestName == check }) {
					t.Errorf("expected a skip of %s, got %v", check, messages)
				}
			}
			for _, m := range messages {
				assertPanicSkip(t, m, m.TestName, tt.archive, archivePanicReason)
			}
		})
	}
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
