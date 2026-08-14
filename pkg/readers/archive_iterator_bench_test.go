package readers

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

var benchMemberSink int

// benchMemberForms are member-path shaped names, 60-130 bytes, the range real
// archive member paths fall in - short base names flatter every scan. One form
// in five carries "temp" and one carries ".log", so the filter decision below
// takes both the hit and the miss path.
var benchMemberForms = []string{
	"campaign_2024/site_alpha/raw_measurements/sensor_%04d/timeseries_%04d.csv",
	"deliverables/reports/quarterly/2024_q3/analysis_summary_%04d_v%04d.pdf",
	"processing/intermediate/scratch/temp_run_%04d/checkpoint_%04d.log",
	"documentation/methods/supplementary/protocol_%04d/section_%04d.md",
	"archive/scans/high_resolution/plate_%04d/microscopy_capture_%04d.tif",
}

// benchMemberNames builds n member paths from benchMemberForms.
func benchMemberNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf(benchMemberForms[i%len(benchMemberForms)], i, i)
	}
	return names
}

// benchMemberLists is the legacy [test.X] filter shape: a handful of literal
// substrings, exactly what the shipped configs carry.
var benchMemberLists = []string{"temp", ".log", "backup"}

// benchWalkZip writes a zip holding one tiny member per name.
func benchWalkZip(b *testing.B, names []string) string {
	b.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			b.Fatalf("create member %q: %v", name, err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			b.Fatalf("write member %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		b.Fatalf("close zip: %v", err)
	}
	path := filepath.Join(b.TempDir(), "walk1000.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		b.Fatalf("write zip: %v", err)
	}
	return path
}

// BenchmarkArchiveNameWalk1000 is the reference cost the member filter is
// judged against: one file-list walk over a 1000-member archive.
func BenchmarkArchiveNameWalk1000(b *testing.B) {
	names := benchMemberNames(1000)
	path := benchWalkZip(b, names)
	archive := structs.ToFile(path, "walk1000.zip", -1, "")

	b.ReportAllocs()
	for b.Loop() {
		list, truncated, err := ReadArchiveFileList(archive, 10000, 100*1024*1024)
		if err != nil || truncated || len(list) != len(names) {
			b.Fatalf("walk returned %d members (truncated=%v): %v", len(list), truncated, err)
		}
		benchMemberSink += len(list)
	}
}

// benchMemberFilter translates the legacy lists the way the callers do.
func benchMemberFilter(b *testing.B, whitelist, blacklist []string) *selector.Selector {
	b.Helper()
	sel, admitNone, err := selector.CompileLegacyLists("bench", whitelist, blacklist)
	if err != nil || admitNone || sel == nil {
		b.Fatalf("translate legacy lists: (%v, %v, %v)", sel, admitNone, err)
	}
	return sel
}

// BenchmarkMemberNameFilter1000 is the gate: the filter decision for 1000
// members, as the archive walks make it - one iterator, its own scratch, the
// translated legacy lists as an include list. 0 allocs/op.
//
// The legacy translation forces ignoreCase, so every decision folds the member
// name once into the scratch; that fold is roughly half the cost measured here
// and is the price of preserving the old case-insensitive meaning, not of the
// Selector itself.
func BenchmarkMemberNameFilter1000(b *testing.B) {
	names := benchMemberNames(1000)
	u := InitArchiveIterator("bench.zip", "bench.zip", ArchiveLimits{}, benchMemberFilter(b, benchMemberLists, nil))
	admitted := 0

	b.ReportAllocs()
	for b.Loop() {
		for _, name := range names {
			if u.admitMember(name) {
				admitted++
			}
		}
	}
	if admitted == 0 {
		b.Fatal("no member was admitted - the benchmark measures the wrong thing")
	}
	benchMemberSink += admitted
}

// BenchmarkMemberNameFilterExclude1000 is the same decision for the shape the
// shipped configs actually carry when they carry one: a blacklist only, where
// every admitted member has to be checked against every pattern.
func BenchmarkMemberNameFilterExclude1000(b *testing.B) {
	names := benchMemberNames(1000)
	u := InitArchiveIterator("bench.zip", "bench.zip", ArchiveLimits{}, benchMemberFilter(b, nil, benchMemberLists))
	admitted := 0

	b.ReportAllocs()
	for b.Loop() {
		for _, name := range names {
			if u.admitMember(name) {
				admitted++
			}
		}
	}
	if admitted == 0 {
		b.Fatal("no member was admitted - the benchmark measures the wrong thing")
	}
	benchMemberSink += admitted
}

// BenchmarkArchiveIterationFiltered drains a whole 1000-member archive through
// the filter, which is where the per-member decision is really paid: zip
// consults it twice per member (the candidate-count preview, then the unpack
// walk), and the per-archive translation is amortized over the whole drain.
func BenchmarkArchiveIterationFiltered(b *testing.B) {
	names := benchMemberNames(1000)
	path := benchWalkZip(b, names)

	b.ReportAllocs()
	for b.Loop() {
		filter, admitNone, err := selector.CompileLegacyLists("bench", benchMemberLists, nil)
		if err != nil || admitNone {
			b.Fatalf("translate legacy lists: (%v, %v)", admitNone, err)
		}
		u := InitArchiveIterator(path, "walk1000.zip",
			ArchiveLimits{MaxMemberSize: 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 10000}, filter)
		scanned := 0
		for u.HasFilesToUnpack() && u.HasNext() {
			u.Next()
			scanned++
		}
		u.Close()
		if scanned == 0 {
			b.Fatal("no member was scanned - the benchmark measures the wrong thing")
		}
		benchMemberSink += scanned
	}
}
