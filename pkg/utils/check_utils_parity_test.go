package utils

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// buildParityFixture writes a PDF-free file set covering the check groups both
// engines dispatch: plain-file name/content checks, an archive's member-name
// walk, its content scan, and the repository-wide checks. NOT covered:
// BY_REPOSITORY_SECRETS - IsFreeOfSecrets is disabled in test_config.toml, so
// both engines skip that branch identically and parity there is untested.
// PDF-free on purpose - the wasm reader's cold compile would dominate the
// runtime without testing anything here.
func buildParityFixture(t *testing.T) []structs.File {
	t.Helper()
	dir := t.TempDir()

	write := func(name, content string) structs.File {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture %q: %v", name, err)
		}
		return structs.ToFile(path, "", -1, "")
	}

	zipPath := filepath.Join(dir, "bundle.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	// Safety net for the t.Fatalf paths below, which would otherwise leak the
	// fd; the checked Close at the end is the one whose error matters.
	defer zf.Close()
	zw := zip.NewWriter(zf)
	for name, content := range map[string]string{
		"member one.txt": "harmless",
		"b_member.txt":   "password",
	} {
		w, werr := zw.Create(name)
		if werr != nil {
			t.Fatalf("create zip member: %v", werr)
		}
		if _, werr = w.Write([]byte(content)); werr != nil {
			t.Fatalf("write zip member: %v", werr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := zf.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	return []structs.File{
		write("notes b.txt", "password in here"),   // whitespace + keyword hit
		write("naïve_data.csv", "a,b\n1,2\n"),      // non-ASCII name
		write(strings.Repeat("x", 70)+".txt", "x"), // over-long name
		write(".Rhistory", "history"),              // disallowed name
		structs.ToFile(zipPath, "", -1, ""),        // archive: name walk + content scan
	}
}

// messageKey canonicalises a Message for multiset comparison. Message itself is
// not a map key: Source is an interface that may hold a Repository, whose Files
// slice makes it non-comparable. Repository identity is deliberately out of
// scope - both engines build the Repository from the same file slice, so the
// file count is all this comparison needs from it.
func messageKey(m structs.Message) string {
	var source string
	switch s := m.Source.(type) {
	case structs.File:
		source = strings.Join([]string{s.Path, s.Name, s.DisplayName, s.ArchiveName}, "\x1f")
	case structs.Repository:
		source = fmt.Sprintf("repository(%d files)", len(s.Files))
	default:
		source = fmt.Sprintf("%T", m.Source)
	}
	return strings.Join([]string{m.TestName, m.Content, m.Reason, fmt.Sprint(m.Skipped), source}, "\x1e")
}

func messageMultiset(messages []structs.Message) map[string]int {
	counts := make(map[string]int, len(messages))
	for _, m := range messages {
		counts[messageKey(m)]++
	}
	return counts
}

// resetGlobalScanState clears the process-global state both engines accumulate
// into, so the second run starts from the first run's starting point.
func resetGlobalScanState() {
	output.GlobalLogger.ClearMessages()
	helpers.PDFTracker.Reset()
}

// progressCall records one ProgressCallback invocation.
type progressCall struct {
	current int
	total   int
}

// assertEngineParity runs both engines over files and asserts they agree.
// Message order is not part of the contract - the parallel path finishes files
// out of order - so the comparison is over multisets.
func assertEngineParity(t *testing.T, cfg config.Config, files []structs.File) {
	t.Helper()

	selectors, err := CompileCheckSelectors(cfg)
	if err != nil {
		t.Fatalf("compile check selectors: %v", err)
	}

	resetGlobalScanState()
	plain := ApplyAllChecks(context.Background(), cfg, selectors, files, true)

	// The callback runs on the caller's goroutine only (the progress engine's
	// file phase is sequential), so an unguarded slice is safe here.
	var calls []progressCall
	resetGlobalScanState()
	withProgress := ApplyAllChecksWithProgress(context.Background(), cfg, selectors, files, true, func(current, total int, _ string) {
		calls = append(calls, progressCall{current: current, total: total})
	})

	resetGlobalScanState()

	// Anti-vacuity: the repository checks alone (HasReadme fires on any fixture)
	// must not keep this test green - the file pipeline has to produce messages.
	fileMessages := 0
	for _, m := range plain {
		if _, ok := m.Source.(structs.File); ok {
			fileMessages++
		}
	}
	if fileMessages == 0 {
		t.Fatal("fixture produced no File-sourced messages - it no longer exercises the file pipeline")
	}

	assertProgressAccounting(t, calls)

	want, got := messageMultiset(plain), messageMultiset(withProgress)
	var diffs []string
	for key, n := range want {
		if got[key] != n {
			diffs = append(diffs, fmt.Sprintf("ApplyAllChecks=%d ApplyAllChecksWithProgress=%d: %s", n, got[key], key))
		}
	}
	for key, n := range got {
		if _, seen := want[key]; !seen {
			diffs = append(diffs, fmt.Sprintf("ApplyAllChecks=0 ApplyAllChecksWithProgress=%d: %s", n, key))
		}
	}
	if len(diffs) > 0 {
		sort.Strings(diffs)
		t.Fatalf("engine entrypoints drifted:\n%s", strings.Join(diffs, "\n"))
	}
}

// assertProgressAccounting pins the progress contract the TUI renders: the
// counter never goes backwards, never exceeds the announced total, and ends
// exactly on it (a stuck-below-total bar is a drift symptom).
func assertProgressAccounting(t *testing.T, calls []progressCall) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("progress callback never fired")
	}
	previous := 0
	for i, c := range calls {
		if c.current < previous {
			t.Fatalf("progress went backwards at call %d: %d after %d", i, c.current, previous)
		}
		if c.current > c.total {
			t.Fatalf("progress overran at call %d: current %d > total %d", i, c.current, c.total)
		}
		previous = c.current
	}
	last := calls[len(calls)-1]
	if last.total == 0 || last.current != last.total {
		t.Fatalf("progress did not finish: final current %d, total %d", last.current, last.total)
	}
}

// TestApplyAllChecks_ProgressVariantParity pins the two engine entrypoints
// together: ApplyAllChecks and ApplyAllChecksWithProgress have separate dispatch
// code (parallel vs per-test-progress) that can silently drift, so assert they
// produce the same message multiset over the same file set.
func TestApplyAllChecks_ProgressVariantParity(t *testing.T) {
	cfg, err := config.ParseConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}
	files := buildParityFixture(t)

	// Multi-file: ApplyChecksFilteredByFile takes its parallel branch.
	assertEngineParity(t, *cfg, files)

	// Single file: the same branch goes sequential (threshold is 2 files), which
	// the run above never reaches.
	assertEngineParity(t, *cfg, files[:1])
}
