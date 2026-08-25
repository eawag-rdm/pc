package utils

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
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

	zipPath := writeZipFixture(t, dir)

	return []structs.File{
		write("notes b.txt", "password in here"),   // whitespace + keyword hit
		write("naïve_data.csv", "a,b\n1,2\n"),      // non-ASCII name
		write(strings.Repeat("x", 70)+".txt", "x"), // over-long name
		write(".Rhistory", "history"),              // disallowed name
		structs.ToFile(zipPath, "", -1, ""),        // archive: name walk + content scan
	}
}

// writeZipFixture writes dir/bundle.zip and returns its path: one member with
// whitespace in its name, one with a keyword in its content, so the archive
// name walk and the content scan both have something to find. testing.TB, so
// the tests and the benchmark share the one fixture.
func writeZipFixture(tb testing.TB, dir string) string {
	tb.Helper()
	zipPath := filepath.Join(dir, "bundle.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		tb.Fatalf("create zip: %v", err)
	}
	// Safety net for the tb.Fatalf paths below, which would otherwise leak the
	// fd; the checked Close at the end is the one whose error matters.
	defer zf.Close()
	zw := zip.NewWriter(zf)
	for name, content := range map[string]string{
		"member one.txt": "harmless",
		"b_member.txt":   "password",
	} {
		w, werr := zw.Create(name)
		if werr != nil {
			tb.Fatalf("create zip member: %v", werr)
		}
		if _, werr = w.Write([]byte(content)); werr != nil {
			tb.Fatalf("write zip member: %v", werr)
		}
	}
	if err := zw.Close(); err != nil {
		tb.Fatalf("close zip writer: %v", err)
	}
	if err := zf.Close(); err != nil {
		tb.Fatalf("close zip: %v", err)
	}
	return zipPath
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

	plan := compilePlan(t, cfg)

	resetGlobalScanState()
	plain, _ := ApplyAllChecks(context.Background(), cfg, plan, files)

	// Ticks are emitted from the pool's collect loop, which is this goroutine,
	// so an unguarded slice is safe here. Ticking from a worker breaks it.
	var calls []progressCall
	resetGlobalScanState()
	withProgress, _ := ApplyAllChecksWithProgress(context.Background(), cfg, plan, files, func(p structs.Progress) {
		calls = append(calls, progressCall{current: p.Current, total: p.Total})
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
// together: ApplyAllChecks and ApplyAllChecksWithProgress share the pool but
// keep separate phase drivers (work-list building, ticking, message assembly)
// that can silently drift, so assert they produce the same message multiset
// over the same file set.
func TestApplyAllChecks_ProgressVariantParity(t *testing.T) {
	cfg, err := config.LoadConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}
	files := buildParityFixture(t)

	// Multi-file: ApplyChecksFilteredByFile takes its parallel branch.
	assertEngineParity(t, *cfg, files)

	// Single file: the same branch goes sequential (threshold is 2 files), which
	// the run above never reaches.
	assertEngineParity(t, *cfg, files[:1])

	// A .tar.gz over the content-scan cap, which dispatch takes away from both
	// archive passes before either runs: its acknowledgement is the one message
	// neither of them produces, so this is where the second engine's half of
	// that classification is pinned.
	capped := *cfg
	general := *cfg.General
	general.MaxContentScanFileSize = 1
	capped.General = &general
	assertEngineParity(t, capped, append(files, tarGzFixture(t)))

	// The same .tar.gz UNDER the cap, with a member-scope keyword rule admitting
	// it: dispatch fuses its content scan and its member-name walk into one
	// decompression and runs both from the member phase, so this is where the
	// second engine's half of the fused path is pinned.
	fused := planConfig([]config.RuleSpec{{
		Name: "keywords", Check: "IsFreeOfKeywords", Enabled: true,
		Params: []map[string]interface{}{
			{"keywords": []string{"password"}, "info": "found"},
		},
	}})
	assertEngineParity(t, fused, []structs.File{tarGzFixture(t)})
}

// TestApplyAllChecksWithProgress_TotalCountsDispatchedItems pins the announced
// total to the DISPATCHED work items: a file every file-scope check filters out
// is never dispatched and must not be counted, or the announced total overshoots
// and the bar sticks below 100% for the whole run.
func TestApplyAllChecksWithProgress_TotalCountsDispatchedItems(t *testing.T) {
	const excluded = "naïve_data.csv"
	exclude := []string{"^" + regexp.QuoteMeta(excluded) + "$"}
	cfg := withRequiredAnchors(planConfig([]config.RuleSpec{
		{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true, Exclude: exclude},
		{Name: "HasNoWhiteSpace", Check: "HasNoWhiteSpace", Enabled: true, Exclude: exclude},
		{Name: "IsValidName", Check: "IsValidName", Enabled: true, Exclude: exclude},
		{Name: "HasFileNameSpecialChars", Check: "HasFileNameSpecialChars", Enabled: true, Exclude: exclude},
		{Name: "IsFileNameTooLong", Check: "IsFileNameTooLong", Enabled: true, Exclude: exclude},
		{Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true, Exclude: exclude,
			Params: []map[string]interface{}{
				{"keywords": []string{"password"}, "info": "Possible credentials in file"},
			}},
	}))
	plan := compilePlan(t, cfg)

	// Anti-vacuity: EVERY file-scope rule must refuse the file - a check entering
	// as an unfiltered default rule, or one more rule beside a filtering one,
	// would dispatch it again. Asked of the compiled gates, not of the config:
	// one check may carry several rules, and only the gates say what each admits.
	naive := structs.File{Name: excluded, RelPath: excluded}
	var subjects checks.Subjects
	subjects.Set(naive)
	for _, entry := range plan.Scope(checks.ScopeFile) {
		for _, rule := range entry.Rules {
			if rule.Match(&subjects) {
				t.Fatalf("rule %q of check %q admits %q - it would still be dispatched", rule.Rule, entry.Def.Name, excluded)
			}
		}
	}

	files := buildParityFixture(t)
	found := false
	for _, file := range files {
		if file.Name == excluded {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("fixture no longer holds %q - no file is filtered out and the case is vacuous", excluded)
	}

	// Ticks come from the collect loop, this goroutine - see assertEngineParity.
	var calls []progressCall
	resetGlobalScanState()
	ApplyAllChecksWithProgress(context.Background(), cfg, plan, files, func(p structs.Progress) {
		calls = append(calls, progressCall{current: p.Current, total: p.Total})
	})
	resetGlobalScanState()

	assertProgressAccounting(t, calls)
}
