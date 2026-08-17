package utils

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// Sink: the engines' result must not be elided.
var benchPipelineMessages []structs.Message

// benchPipelineDiagnostics keeps the second return live, so the benchmark
// exercises the diagnostics path the way a caller does.
var benchPipelineDiagnostics []structs.Diagnostic

// benchPipelineConfig is the end-to-end workload: the parameterised checks are
// declared here, the registry's remaining ones enter as synthesized default
// rules (6 file checks in the compiled plan). No path filters, so every file
// meets every check. Anchored rules are filled in, as a complete config
// carries them.
func benchPipelineConfig() config.Config {
	return withRequiredAnchors(planConfig([]config.RuleSpec{
		{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true},
		{Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true, Params: []map[string]interface{}{
			{"keywords": []string{"password"}, "info": "Possible credentials in file"},
		}},
		{Name: "IsValidName", Check: "IsValidName", Enabled: true, Params: []map[string]interface{}{
			{"disallowed_names": []string{".Rhistory", "__pycache__"}},
		}},
	}))
}

// benchPipelinePlan compiles cfg against the real registry, as startup does once.
func benchPipelinePlan(b *testing.B, cfg config.Config) *Plan {
	b.Helper()
	plan, err := Compile(&cfg, checks.NewRegistry())
	if err != nil {
		b.Fatalf("compile rules: %v", err)
	}
	return plan
}

// benchPipelineTree writes n real files: the content checks read from disk, so
// the fixture cannot be synthetic. Bodies stay a few hundred bytes so the
// measurement is dispatch and checking, not I/O. The first files carry the
// finding paths (keyword content + whitespace name, non-ASCII name, disallowed
// name) and the zip pulls in the archive phases.
func benchPipelineTree(b *testing.B, n int) []structs.File {
	b.Helper()
	dir := b.TempDir()

	files := make([]structs.File, 0, n)
	write := func(name, content string) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			b.Fatalf("write fixture %q: %v", name, err)
		}
		files = append(files, structs.ToFile(path, "", -1, ""))
	}

	body := strings.Repeat("lorem ipsum dolor sit amet ", 12) // ~324 B
	write("secrets b.txt", "password = hunter2\n"+body)
	write("naïve_data.csv", "a,b\n1,2\n"+body)
	write(".Rhistory", body)

	files = append(files, structs.ToFile(writeZipFixture(b, dir), "", -1, "")) // member-name walk + content scan

	exts := [...]string{"txt", "csv", "md", "log"}
	for i := len(files); i < n; i++ {
		write(fmt.Sprintf("file_%04d.%s", i, exts[i%len(exts)]), body)
	}
	return files
}

// BenchmarkApplyAllChecks gates the check-pipeline rework: all four phases (file,
// archive file list, archive member, repository) over a 1000-file tree through
// both entry points, so a benchstat A/B compares them and pins the allocation
// budget. The tree is written once outside the timed loop - fixture I/O is
// setup, not workload.
//
//   - plain: ApplyAllChecks, the parallel path both frontends take.
//   - progress: ApplyAllChecksWithProgress, same pool plus the rate-limited
//     progress path - the sub-benchmark exists for the A/B against plain.
func BenchmarkApplyAllChecks(b *testing.B) {
	const fileCount = 1000
	files := benchPipelineTree(b, fileCount)
	cfg := benchPipelineConfig()
	plan := benchPipelinePlan(b, cfg)

	run := func(b *testing.B, engine func() []structs.Message) {
		b.ReportAllocs()
		for b.Loop() {
			// Untimed: the sinks must not grow across iterations, but the clear
			// and the tracker's mutex round-trips are setup, not workload.
			b.StopTimer()
			resetGlobalScanState()
			b.StartTimer()
			benchPipelineMessages = engine()
		}
		if len(benchPipelineMessages) == 0 {
			b.Fatal("no messages - the benchmark measures the empty path")
		}
	}

	b.Run("plain", func(b *testing.B) {
		run(b, func() []structs.Message {
			messages, diags := ApplyAllChecks(context.Background(), cfg, plan, files)
			benchPipelineDiagnostics = diags
			return messages
		})
	})

	b.Run("progress", func(b *testing.B) {
		run(b, func() []structs.Message {
			messages, diags := ApplyAllChecksWithProgress(context.Background(), cfg, plan, files, func(structs.Progress) {})
			benchPipelineDiagnostics = diags
			return messages
		})
	})
}
