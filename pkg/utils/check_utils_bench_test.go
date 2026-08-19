package utils

import (
	"fmt"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// benchFilterConfig is the filter workload: two of the six file checks carry
// patterns (one exclude, one include), the rest carry none - the shape of a real
// config block.
func benchFilterConfig() config.Config {
	return planConfig([]config.RuleSpec{
		{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true, Exclude: []string{`\.png$`, `\.jpg$`}},
		{Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.txt$`, `\.csv$`}},
		{Name: "IsValidName", Check: "IsValidName", Enabled: true},
	})
}

// benchFoldedFilterConfig is benchFilterConfig's workload with gates that take
// the FOLDED literal path: ignoreCase plus a plain literal is what a selector
// matches against the caller's folded subject instead of the regex engine. It is
// the only selection config here whose patterns take that path -
// benchFilterConfig's are anchored regexes, which no scratch can speed up - and
// it exists to keep the path measurable: a gate that went back through the regex
// engine once per (file, rule) moves that number.
func benchFoldedFilterConfig() config.Config {
	return planConfig([]config.RuleSpec{
		{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true, IgnoreCase: true, Exclude: []string{`\.png`, `\.jpg`}},
		{Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true, IgnoreCase: true, Include: []string{`\.txt`, `\.csv`}},
		{Name: "IsValidName", Check: "IsValidName", Enabled: true},
	})
}

// benchUnfilteredConfig is the shipped-config shape: rules present, all lists
// empty, so nothing is filtered at all.
func benchUnfilteredConfig() config.Config {
	return planConfig([]config.RuleSpec{
		{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true},
		{Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true},
		{Name: "IsValidName", Check: "IsValidName", Enabled: true},
	})
}

// benchFilterFiles returns n files whose names hit both sides of every pattern.
func benchFilterFiles(n int) []structs.File {
	exts := [...]string{"txt", "png", "csv", "bin"}
	files := make([]structs.File, n)
	for i := range files {
		name := fmt.Sprintf("file_%04d.%s", i, exts[i%len(exts)])
		files[i] = structs.File{Name: name, Path: "/data/" + name, RelPath: name}
	}
	return files
}

// benchFileScope compiles cfg and returns its file-scope plan entries, as
// startup does once.
func benchFileScope(b *testing.B, cfg config.Config) []checks.PlanEntry {
	b.Helper()
	cfg = withRequiredAnchors(cfg)
	plan, err := checks.Compile(&cfg, checks.NewRegistry())
	if err != nil {
		b.Fatalf("compile rules: %v", err)
	}
	return plan.Scope(checks.ScopeFile)
}

// BenchmarkFilterChecksForFiles measures the selection pass over 5000 files x the
// six file checks, with the plan compiled once outside the loop - as startup does.
//
//   - decision: the rule match alone (what the 0-allocs gate covers) - no
//     work-item building, so it must not allocate at all.
//   - decision-folded: the same decision over ignoreCase literal gates, which
//     read their subject folded off the scratch instead of running the regex
//     engine per rule. It is NOT an A/B against decision: those patterns differ
//     in anchoring as well as in case folding, so the gap between the two mixes
//     substring-versus-regex with the scratch win. Compare this arm with itself
//     over time.
//   - unfiltered: the same pass over the shipped all-empty-lists config, where
//     every rule admits every file.
//   - workitems: the whole pass, whose allocations are the per-file rule and
//     check slices the worker pool consumes, not the filtering.
//   - workitems-reported / workitems-unfiltered-reported: the same two passes
//     with the rule report on, so the A/B against them is what the reporting
//     costs. The buffer is taken inside the loop because production takes one
//     per phase.
func BenchmarkFilterChecksForFiles(b *testing.B) {
	const fileCount = 5000
	files := benchFilterFiles(fileCount)

	entries := benchFileScope(b, benchFilterConfig())
	folded := benchFileScope(b, benchFoldedFilterConfig())
	noFilter := benchFileScope(b, benchUnfilteredConfig())

	decide := func(b *testing.B, entries []checks.PlanEntry, wantSkips bool) {
		b.ReportAllocs()
		skipped := 0
		// One file's subjects, set once for every gate that reads them - the
		// selection pass's own shape.
		var subjects checks.Subjects
		for b.Loop() {
			for i := range files {
				subjects.Set(files[i])
				for _, entry := range entries {
					for _, rule := range entry.Rules {
						if !rule.Match(&subjects) {
							skipped++
						}
					}
				}
			}
		}
		if (skipped != 0) != wantSkips {
			b.Fatalf("skipped %d files - the benchmark measures the wrong thing", skipped)
		}
	}

	// The folded gates must really read their literals FOLDED, or the arm below
	// measures the regex engine instead: a case-mismatched include literal must
	// still admit, and a case-mismatched exclude literal must still refuse a name
	// the include gates do admit. Dropping IgnoreCase from benchFoldedFilterConfig
	// - from either gate - fails here.
	foldsCase := func(name string, want bool) {
		var subjects checks.Subjects
		subjects.Set(structs.File{Name: name, Path: "/data/" + name, RelPath: name})
		got := true
		for _, entry := range folded {
			for _, rule := range entry.Rules {
				got = got && rule.Match(&subjects)
			}
		}
		if got != want {
			b.Fatalf("the folded gates decide %q as %v, want %v - they are not reading their literals folded", name, got, want)
		}
	}
	foldsCase("file_9999.CSV", true)
	foldsCase("file_9999.CSV.PNG", false)

	b.Run("decision", func(b *testing.B) { decide(b, entries, true) })
	b.Run("decision-folded", func(b *testing.B) { decide(b, folded, true) })
	b.Run("unfiltered", func(b *testing.B) { decide(b, noFilter, false) })

	b.Run("workitems", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if items := filterChecksForFiles(entries, checks.ScopeFile, files, nil); len(items) == 0 {
				b.Fatal("no work items built")
			}
		}
	})

	// The shipped-config fast path: selection is the identity, so the pass
	// must allocate the work list and nothing else - no arenas, no Match
	// calls. The item count and the shared entries pin that the fast path
	// actually ran.
	b.Run("workitems-unfiltered", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			items := filterChecksForFiles(noFilter, checks.ScopeFile, files, nil)
			if len(items) != fileCount || &items[0].Checks[0] != &noFilter[0] {
				b.Fatal("the unfiltered pass must share the plan's entries")
			}
		}
	})

	b.Run("workitems-reported", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			marks := newRuleMarks(entries)
			if items := filterChecksForFiles(entries, checks.ScopeFile, files, marks); len(items) == 0 {
				b.Fatal("no work items built")
			}
		}
	})

	b.Run("workitems-unfiltered-reported", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			marks := newRuleMarks(noFilter)
			items := filterChecksForFiles(noFilter, checks.ScopeFile, files, marks)
			if len(items) != fileCount || &items[0].Checks[0] != &noFilter[0] {
				b.Fatal("the unfiltered pass must share the plan's entries")
			}
		}
	})
}
