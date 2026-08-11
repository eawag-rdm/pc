package utils

import (
	"fmt"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// benchFilterConfig is the filter workload: two of the six BY_FILE checks carry
// patterns (one exclude, one include), the rest carry none - the shape of a real
// [test.*] block.
func benchFilterConfig() config.Config {
	return config.Config{Tests: map[string]*config.TestConfig{
		"HasOnlyASCII":     {Blacklist: []string{`\.png$`, `\.jpg$`}},
		"IsFreeOfKeywords": {Whitelist: []string{`\.txt$`, `\.csv$`}},
		"IsValidName":      {},
	}}
}

// benchUnfilteredConfig is the shipped-config shape: sections present, all lists
// empty, so nothing is filtered at all.
func benchUnfilteredConfig() config.Config {
	return config.Config{Tests: map[string]*config.TestConfig{
		"HasOnlyASCII":     {},
		"IsFreeOfKeywords": {},
		"IsValidName":      {},
	}}
}

// benchFilterFiles returns n files whose names hit both sides of every pattern.
func benchFilterFiles(n int) []structs.File {
	exts := [...]string{"txt", "png", "csv", "bin"}
	files := make([]structs.File, n)
	for i := range files {
		name := fmt.Sprintf("file_%04d.%s", i, exts[i%len(exts)])
		files[i] = structs.File{Name: name, Path: "/data/" + name}
	}
	return files
}

// BenchmarkFilterChecksForFiles measures the selection pass over 5000 files x the
// 6 BY_FILE checks, with the table compiled and resolved once outside the loop -
// as startup and each dispatch call do.
//
//   - decision: the skip decision alone (what the 0-allocs gate covers) - no
//     work-item building, so it must not allocate at all.
//   - unfiltered: the same pass over the shipped all-empty-lists config, where
//     resolve yields no selectors and every check runs.
//   - workitems: the whole pass, whose allocations are the per-file check slices
//     the worker pool consumes (rebuilt in R7), not the filtering.
func BenchmarkFilterChecksForFiles(b *testing.B) {
	const fileCount = 5000
	files := benchFilterFiles(fileCount)

	selectors, err := CompileCheckSelectors(benchFilterConfig())
	if err != nil {
		b.Fatalf("compile check selectors: %v", err)
	}
	sels := selectors.resolve(BY_FILE)
	if sels == nil {
		b.Fatal("no check resolved to a selector - the benchmark measures the wrong thing")
	}

	unfiltered, err := CompileCheckSelectors(benchUnfilteredConfig())
	if err != nil {
		b.Fatalf("compile unfiltered selectors: %v", err)
	}
	noSels := unfiltered.resolve(BY_FILE)

	b.Run("decision", func(b *testing.B) {
		b.ReportAllocs()
		skipped := 0
		for b.Loop() {
			for i := range files {
				for j := range BY_FILE {
					if skipFileCheck(sels, j, files[i]) {
						skipped++
					}
				}
			}
		}
		if skipped == 0 {
			b.Fatal("no file was filtered - the benchmark measures the wrong thing")
		}
	})

	b.Run("unfiltered", func(b *testing.B) {
		b.ReportAllocs()
		skipped := 0
		for b.Loop() {
			for i := range files {
				for j := range BY_FILE {
					if skipFileCheck(noSels, j, files[i]) {
						skipped++
					}
				}
			}
		}
		if skipped != 0 {
			b.Fatal("an empty list must filter nothing")
		}
	})

	b.Run("workitems", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if items := filterChecksForFiles(sels, BY_FILE, files); len(items) == 0 {
				b.Fatal("no work items built")
			}
		}
	})
}
