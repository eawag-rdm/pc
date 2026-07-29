package checks

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// fakeScanner writes a shell script that logs its args and prints the given
// report on stdout, standing in for the betterleaks binary.
func fakeScanner(t *testing.T, report string) (binPath string, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	binPath = filepath.Join(dir, "fake-betterleaks")
	argsFile = filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\ncat <<'EOF'\n" + report + "\nEOF\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath, argsFile
}

func leakTestConfig(binary string, generalOverride *config.GeneralConfig) config.Config {
	general := &config.GeneralConfig{
		MaxArchiveFileSize:     10 * 1024 * 1024,
		MaxTotalArchiveMemory:  100 * 1024 * 1024,
		MaxContentScanFileSize: 1024 * 1024 * 1024,
	}
	if generalOverride != nil {
		general = generalOverride
	}
	return config.Config{
		General: general,
		Tests: map[string]*config.TestConfig{
			"IsFreeOfSecrets": {
				Attrs: map[string]interface{}{
					"enabled": true,
					"binary":  binary,
				},
			},
		},
	}
}

func TestIsFreeOfSecretsDisabled(t *testing.T) {
	repo := structs.Repository{Files: []structs.File{{Path: "/tmp/x", Name: "x"}}}

	// No config section at all.
	cfg := config.Config{General: &config.GeneralConfig{}, Tests: map[string]*config.TestConfig{}}
	if msgs := IsFreeOfSecrets(repo, cfg); msgs != nil {
		t.Fatalf("expected nil without config section, got %v", msgs)
	}

	// Section present but not enabled (attrs missing or enabled=false).
	cfg = leakTestConfig("betterleaks", nil)
	cfg.Tests["IsFreeOfSecrets"].Attrs["enabled"] = false
	if msgs := IsFreeOfSecrets(repo, cfg); msgs != nil {
		t.Fatalf("expected nil when disabled, got %v", msgs)
	}
}

func TestIsFreeOfSecretsPlainFileFindings(t *testing.T) {
	content := tempFile([]byte("stripe_key = \"sk_live_whatever\"\n"))
	defer os.Remove(content)

	report := fmt.Sprintf(`[
  {"RuleID": "stripe-access-token", "StartLine": 1, "File": %q},
  {"RuleID": "stripe-access-token", "StartLine": 7, "File": %q},
  {"RuleID": "generic-api-key", "StartLine": 3, "File": %q}
]`, content, content, content)
	bin, argsFile := fakeScanner(t, report)

	cfg := leakTestConfig(bin, nil)
	file := structs.File{Path: content, Name: "data.txt", Size: 10}
	msgs := IsFreeOfSecrets(structs.Repository{Files: []structs.File{file}}, cfg)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 condensed message, got %d: %v", len(msgs), msgs)
	}
	want := "Possible secret(s) detected: generic-api-key (line 3); stripe-access-token (lines 1, 7)"
	if msgs[0].Content != want {
		t.Errorf("content mismatch:\n got: %s\nwant: %s", msgs[0].Content, want)
	}
	if src, ok := msgs[0].Source.(structs.File); !ok || src.Name != "data.txt" {
		t.Errorf("finding not mapped back to source file: %v", msgs[0].Source)
	}

	// The scanner must have been invoked with the fixed safety flags.
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"dir", "--redact=100", "--max-archive-depth", "--exit-code"} {
		if !strings.Contains(string(args), flag) {
			t.Errorf("scanner args missing %q: %s", flag, args)
		}
	}
}

func TestIsFreeOfSecretsSizeGate(t *testing.T) {
	small := tempFile([]byte("small"))
	big := tempFile([]byte(strings.Repeat("x", 2048)))
	defer os.Remove(small)
	defer os.Remove(big)

	bin, argsFile := fakeScanner(t, "null")
	general := &config.GeneralConfig{
		MaxArchiveFileSize:     10 * 1024 * 1024,
		MaxTotalArchiveMemory:  100 * 1024 * 1024,
		MaxContentScanFileSize: 1024, // big file exceeds this
	}
	cfg := leakTestConfig(bin, general)

	files := []structs.File{
		{Path: small, Name: "small.txt", Size: 5},
		{Path: big, Name: "big.txt", Size: 2048},
	}
	msgs := IsFreeOfSecrets(structs.Repository{Files: files}, cfg)

	if len(msgs) != 1 || !msgs[0].Skipped {
		t.Fatalf("expected exactly one aggregate skip message, got %v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "1 file(s)") {
		t.Errorf("skip message should count 1 oversized file: %s", msgs[0].Content)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), big) {
		t.Errorf("oversized file must not reach the scanner: %s", args)
	}
	if !strings.Contains(string(args), small) {
		t.Errorf("small file should reach the scanner: %s", args)
	}
}

func TestIsFreeOfSecretsArchiveExtraction(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "data.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	smallMember, err := zw.Create("inner/secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := smallMember.Write([]byte("token = abc\n")); err != nil {
		t.Fatal(err)
	}
	bigMember, err := zw.Create("inner/too-big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bigMember.Write([]byte(strings.Repeat("y", 4096))); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}

	// The fake scanner echoes a finding for whatever path it got: capture args
	// first, then rerun assertions on the mapping via a report crafted after
	// the fact is impossible - so instead scan with a null report and assert
	// extraction behavior via args, then test mapping separately.
	bin, argsFile := fakeScanner(t, "null")
	general := &config.GeneralConfig{
		MaxArchiveFileSize:     1024, // big member exceeds this
		MaxTotalArchiveMemory:  100 * 1024 * 1024,
		MaxContentScanFileSize: 1024 * 1024,
	}
	cfg := leakTestConfig(bin, general)

	archive := structs.File{Path: zipPath, Name: "data.zip", Size: structs.GetFileSize(zipPath), IsArchive: true}
	msgs := IsFreeOfSecrets(structs.Repository{Files: []structs.File{archive}}, cfg)

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "secret.txt") {
		t.Errorf("small member should be extracted and scanned: %s", args)
	}
	if strings.Contains(string(args), "too-big") {
		t.Errorf("oversized member must not be extracted for scanning: %s", args)
	}
	// The iterator reports the oversized member as a skip acknowledgement
	// (the member is identified via the message Source).
	foundSkip := false
	for _, m := range msgs {
		src, isFile := m.Source.(structs.File)
		if m.Skipped && isFile && strings.Contains(src.Name, "too-big") {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Errorf("expected a skip acknowledgement for the oversized member, got %v", msgs)
	}
	// Temp extraction dir must be gone afterwards.
	entries, _ := filepath.Glob(os.TempDir() + "/pc-leakcheck-*")
	if len(entries) != 0 {
		t.Errorf("temp extraction dirs left behind: %v", entries)
	}
}

func TestCondenseLeakFindingsMemberMapping(t *testing.T) {
	member := structs.ToFileWithDisplay("/data/data.zip", "inner/secret.txt", "inner/secret.txt", 12, "", "data.zip")
	sources := map[string]structs.File{"/tmp/pc-leakcheck-x/a000/0000_secret.txt": member}
	findings := []blFinding{{RuleID: "generic-api-key", StartLine: 2, File: "/tmp/pc-leakcheck-x/a000/0000_secret.txt"}}

	msgs := condenseLeakFindings(findings, sources)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	src, ok := msgs[0].Source.(structs.File)
	if !ok || src.ArchiveName != "data.zip" || src.Name != "inner/secret.txt" {
		t.Errorf("member finding should map to archive member source, got %+v", msgs[0].Source)
	}
	if !strings.Contains(msgs[0].Format(), "data.zip > inner/secret.txt") {
		t.Errorf("formatted message should show archive context: %s", msgs[0].Format())
	}
}

func TestFormatLeakLinesCap(t *testing.T) {
	got := formatLeakLines([]int{9, 1, 5, 3, 7, 11, 13})
	want := "lines 1, 3, 5, 7, 9 +2 more"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got := formatLeakLines([]int{4}); got != "line 4" {
		t.Errorf("single line format wrong: %q", got)
	}
}

func TestLeakAttrsDefaults(t *testing.T) {
	a := leakAttrsFrom(&config.TestConfig{})
	if a.enabled || a.binary != "betterleaks" || a.timeoutSeconds != 120 || a.maxProcs != 3 {
		t.Errorf("unexpected defaults: %+v", a)
	}
	a = leakAttrsFrom(&config.TestConfig{Attrs: map[string]interface{}{
		"enabled": true, "binary": "/opt/bl", "timeoutSeconds": int64(30), "maxProcs": int64(1),
	}})
	if !a.enabled || a.binary != "/opt/bl" || a.timeoutSeconds != 30 || a.maxProcs != 1 {
		t.Errorf("unexpected parsed attrs: %+v", a)
	}
}

func TestRunBetterleaksEdgeCases(t *testing.T) {
	// Relative paths are refused.
	if _, err := runBetterleaks(context.Background(), "true", []string{"relative.txt"}, 1); err == nil {
		t.Error("expected error for relative path")
	}
	// Empty path list is a no-op.
	if fs, err := runBetterleaks(context.Background(), "/nonexistent-binary", nil, 1); err != nil || fs != nil {
		t.Errorf("empty path list should be a no-op, got %v, %v", fs, err)
	}
	// A failing binary surfaces stderr.
	dir := t.TempDir()
	failing := filepath.Join(dir, "failing")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\necho boom >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runBetterleaks(context.Background(), failing, []string{dir}, 1); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected stderr in error, got %v", err)
	}
	// Junk stdout is a parse error, valid JSON parses.
	junk := filepath.Join(dir, "junk")
	if err := os.WriteFile(junk, []byte("#!/bin/sh\necho 'not json'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runBetterleaks(context.Background(), junk, []string{dir}, 1); err == nil {
		t.Error("expected parse error for junk output")
	}
	good := filepath.Join(dir, "good")
	report := `[{"RuleID": "r", "StartLine": 1, "File": "/f"}]`
	if err := os.WriteFile(good, []byte("#!/bin/sh\ncat <<'EOF'\n"+report+"\nEOF\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := runBetterleaks(context.Background(), good, []string{dir}, 1)
	if err != nil || len(fs) != 1 || fs[0].RuleID != "r" {
		t.Errorf("expected one finding, got %v, %v", fs, err)
	}
	// JSON `null` means no findings.
	null := filepath.Join(dir, "null")
	if err := os.WriteFile(null, []byte("#!/bin/sh\necho null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if fs, err := runBetterleaks(context.Background(), null, []string{dir}, 1); err != nil || fs != nil {
		t.Errorf("null report should be no findings, got %v, %v", fs, err)
	}
}

// TestIsFreeOfSecretsRealBinary exercises the full path against a real
// betterleaks binary when one is on PATH; skipped otherwise (CI-safe).
func TestIsFreeOfSecretsRealBinary(t *testing.T) {
	if _, err := os.Stat("/usr/local/bin/betterleaks"); err != nil {
		if _, err := exec.LookPath("betterleaks"); err != nil {
			t.Skip("betterleaks binary not available")
		}
	}
	content := tempFile([]byte("aws_secret_access_key = \"" + strings.Repeat("A", 20) + "9xK2mQ7vLpZ4wR8tJ6nB\"\n"))
	defer os.Remove(content)
	cfg := leakTestConfig("betterleaks", nil)
	file := structs.File{Path: content, Name: "config.txt", Size: structs.GetFileSize(content)}
	msgs := IsFreeOfSecrets(structs.Repository{Files: []structs.File{file}}, cfg)
	// Findings depend on scanner rules; assert only that no secret VALUE leaks
	// into any message and no error/skip message appeared.
	for _, m := range msgs {
		if strings.Contains(m.Content, "9xK2mQ7vLpZ4wR8tJ6nB") {
			t.Errorf("secret value leaked into message: %s", m.Content)
		}
		if m.Skipped {
			t.Errorf("unexpected skip message with real binary: %s", m.Content)
		}
	}
}
