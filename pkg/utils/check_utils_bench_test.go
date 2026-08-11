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
// [test.*] block.
func benchFilterConfig() config.Config {
	return planConfig(map[string]*config.TestConfig{
		"HasOnlyASCII":     {Blacklist: []string{`\.png$`, `\.jpg$`}},
		"IsFreeOfKeywords": {Whitelist: []string{`\.txt$`, `\.csv$`}},
		"IsValidName":      {},
	})
}

// benchUnfilteredConfig is the shipped-config shape: sections present, all lists
// empty, so nothing is filtered at all.
func benchUnfilteredConfig() config.Config {
	return planConfig(map[string]*config.TestConfig{
		"HasOnlyASCII":     {},
		"IsFreeOfKeywords": {},
		"IsValidName":      {},
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
func benchFileScope(b *testing.B, cfg config.Config) []checkRules {
	b.Helper()
	plan, err := Compile(&cfg, checks.NewRegistry())
	if err != nil {
		b.Fatalf("compile rules: %v", err)
	}
	return plan.scope(checks.ScopeFile)
}

// BenchmarkFilterChecksForFiles measures the selection pass over 5000 files x the
// six file checks, with the plan compiled once outside the loop - as startup does.
//
//   - decision: the rule match alone (what the 0-allocs gate covers) - no
//     work-item building, so it must not allocate at all.
//   - unfiltered: the same pass over the shipped all-empty-lists config, where
//     every rule admits every file.
//   - workitems: the whole pass, whose allocations are the per-file rule and
//     check slices the worker pool consumes, not the filtering.
func BenchmarkFilterChecksForFiles(b *testing.B) {
	const fileCount = 5000
	files := benchFilterFiles(fileCount)

	entries := benchFileScope(b, benchFilterConfig())
	noFilter := benchFileScope(b, benchUnfilteredConfig())

	decide := func(b *testing.B, entries []checkRules, wantSkips bool) {
		b.ReportAllocs()
		skipped := 0
		for b.Loop() {
			for i := range files {
				for _, entry := range entries {
					for _, rule := range entry.rules {
						if !rule.Match(files[i]) {
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

	b.Run("decision", func(b *testing.B) { decide(b, entries, true) })
	b.Run("unfiltered", func(b *testing.B) { decide(b, noFilter, false) })

	b.Run("workitems", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if items := filterChecksForFiles(entries, checks.ScopeFile, files); len(items) == 0 {
				b.Fatal("no work items built")
			}
		}
	})
}
