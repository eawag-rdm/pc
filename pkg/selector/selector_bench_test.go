package selector

import (
	"fmt"
	"testing"
)

var benchSink int

// benchSubjects builds member-path shaped subjects: nested directories and
// realistic names, 60-130 bytes, the range archive member paths actually fall
// in. Short base names flatter every scan; the gate is read from these.
func benchSubjects(b *testing.B, n int, forms []string) []string {
	b.Helper()
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(forms[i%len(forms)], i, i)
		if len(out[i]) < 60 || len(out[i]) > 130 {
			b.Fatalf("subject %q is %d bytes, want 60-130", out[i], len(out[i]))
		}
	}
	return out
}

var pathForms = []string{
	"campaign_2024/site_alpha/raw_measurements/sensor_%04d/timeseries_%04d.csv",
	"deliverables/reports/quarterly/2024_q3/analysis_summary_%04d_v%04d.pdf",
	"processing/intermediate/scratch/temp_run_%04d/checkpoint_%04d.log",
	"documentation/methods/supplementary/protocol_%04d/section_%04d.md",
	"archive/scans/high_resolution/plate_%04d/microscopy_capture_%04d.tif",
}

// nonASCIIForms are the same paths with accented characters, which must not
// push the subject off the fold fast path.
var nonASCIIForms = []string{
	"campagne_2024/site_créé/mesures_brutes/capteur_%04d/série_temporelle_%04d.csv",
	"livrables/rapports/trimestriel/2024_q3/résumé_analyse_%04d_v%04d.pdf",
	"traitement/intermédiaire/scratch/temp_exécution_%04d/point_%04d.log",
	"documentation/méthodes/supplément/protocole_%04d/séction_%04d.md",
	"archive/scans/haute_résolution/plaque_%04d/capture_microscopé_%04d.tif",
}

var shortForms = []string{
	"data_%04d_%04d.csv", "report_%04d_%04d.pdf", "temp_%04d_%04d.log",
}

func benchSelector(b *testing.B, spec Spec) Selector {
	b.Helper()
	sel, err := Compile(spec)
	if err != nil {
		b.Fatalf("Compile(%+v) failed: %v", spec, err)
	}
	return sel
}

// reportPerMatch converts the per-iteration cost into the per-match cost the
// gate is stated in.
func reportPerMatch(b *testing.B, matches int) {
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(matches), "ns/match")
}

var benchInclude = []string{"temp", `\.log`, "backup"}

// BenchmarkSelectorLiteral1000 is the gate: 1000 literal matches per iteration
// over member-path shaped subjects, 0 allocs/op and <= 150 ns per match.
func BenchmarkSelectorLiteral1000(b *testing.B) {
	sel := benchSelector(b, Spec{Rule: "bench", Include: benchInclude})
	subjects := benchSubjects(b, 1000, pathForms)
	hits := 0
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range subjects {
			if sel.Match(s) {
				hits++
			}
		}
	}
	reportPerMatch(b, len(subjects))
	benchSink = hits
}

// BenchmarkSelectorLiteralFold1000 is the same selector with ignoreCase: the
// literal scan over the caller's scratch, including the lazy fold.
func BenchmarkSelectorLiteralFold1000(b *testing.B) {
	sel := benchSelector(b, Spec{Rule: "bench", IgnoreCase: true, Include: benchInclude})
	subjects := benchSubjects(b, 1000, pathForms)
	hits := 0
	var sc Scratch
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range subjects {
			sc.Set(s)
			if sel.MatchScratch(&sc) {
				hits++
			}
		}
	}
	reportPerMatch(b, len(subjects))
	benchSink = hits
}

// BenchmarkSelectorLiteralFoldNonASCII1000 must stay near the ASCII numbers: an
// accented rune is not one of the two that fold onto ASCII, so it must not send
// the subject to the regex engine.
func BenchmarkSelectorLiteralFoldNonASCII1000(b *testing.B) {
	sel := benchSelector(b, Spec{Rule: "bench", IgnoreCase: true, Include: benchInclude})
	subjects := benchSubjects(b, 1000, nonASCIIForms)
	hits := 0
	var sc Scratch
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range subjects {
			sc.Set(s)
			if sel.MatchScratch(&sc) {
				hits++
			}
		}
	}
	reportPerMatch(b, len(subjects))
	benchSink = hits
}

// BenchmarkSelectorRegex1000 is the same filter expressed non-literally, so the
// cost of the engine path stays visible next to the fast path. No gate.
func BenchmarkSelectorRegex1000(b *testing.B) {
	sel := benchSelector(b, Spec{Rule: "bench", Include: []string{"^temp", `\.log$`, "backup.*"}})
	subjects := benchSubjects(b, 1000, pathForms)
	hits := 0
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range subjects {
			if sel.Match(s) {
				hits++
			}
		}
	}
	reportPerMatch(b, len(subjects))
	benchSink = hits
}

// BenchmarkSelectorLiteralShort1000 keeps the short-base-name shape visible for
// file-scope callers, which match base names rather than member paths.
func BenchmarkSelectorLiteralShort1000(b *testing.B) {
	sel := benchSelector(b, Spec{Rule: "bench", Include: benchInclude})
	subjects := make([]string, 1000)
	for i := range subjects {
		subjects[i] = fmt.Sprintf(shortForms[i%len(shortForms)], i, i)
	}
	hits := 0
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range subjects {
			if sel.Match(s) {
				hits++
			}
		}
	}
	reportPerMatch(b, len(subjects))
	benchSink = hits
}
