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
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// fakeScanner writes a shell script that logs its args and prints the given
// report on stdout, standing in for the betterleaks binary.
func fakeScanner(t *testing.T, report string) (binPath string, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	binPath = filepath.Join(dir, "fake-betterleaks")
	argsFile = filepath.Join(dir, "args.txt")
	// The redirect target MUST stay quoted: t.TempDir() puts the subtest name
	// into the path, and an unquoted parenthesis or space kills the script
	// silently - which would make every "the scanner never ran" assertion pass
	// for the wrong reason.
	// The child's CPU cap arrives as an environment variable, not an argument,
	// so the log carries it too - it is the only place the cap is observable.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsFile + "\"\nprintf 'GOMAXPROCS=%s\\n' \"$GOMAXPROCS\" >> \"" + argsFile + "\"\ncat <<'EOF'\n" + report + "\nEOF\n"
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
	msgs := runRepoRule(t, "IsFreeOfSecrets", cfg, structs.Repository{Files: []structs.File{file}})

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
	msgs := runRepoRule(t, "IsFreeOfSecrets", cfg, structs.Repository{Files: files})

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
	msgs := runRepoRule(t, "IsFreeOfSecrets", cfg, structs.Repository{Files: []structs.File{archive}})

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

func TestExtractArchivesMkdirFailureKeepsAcks(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "data.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	// Oversized member FIRST so its skip ack is recorded while the iterator
	// buffers the first scannable member (i.e. before the Mkdir failure).
	big, err := zw.Create("big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := big.Write([]byte(strings.Repeat("y", 4096))); err != nil {
		t.Fatal(err)
	}
	small, err := zw.Create("small.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := small.Write([]byte("token = abc\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	// A FILE where the archive's extraction dir would go makes Mkdir fail.
	if err := os.WriteFile(filepath.Join(tmpDir, "a000"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := leakTestConfig("unused", &config.GeneralConfig{
		MaxArchiveFileSize:     1024, // big.txt exceeds this -> ack recorded
		MaxTotalArchiveMemory:  100 * 1024 * 1024,
		MaxContentScanFileSize: 1024 * 1024,
	})
	archive := structs.File{Path: zipPath, Name: "data.zip", Size: structs.GetFileSize(zipPath), IsArchive: true}

	var messages []structs.Message
	sources := map[string]structs.File{}
	// No lists in this config, so the scan's selector filters nothing.
	paths := extractArchivesForLeakScan(archiveLimits(cfg.General), nil, []structs.File{archive}, tmpDir, sources, &messages)

	if len(paths) != 0 {
		t.Errorf("no members can be extracted when the temp dir cannot be created, got %v", paths)
	}
	foundAck := false
	for _, m := range messages {
		src, ok := m.Source.(structs.File)
		if m.Skipped && ok && strings.Contains(src.Name, "big.txt") {
			foundAck = true
		}
	}
	if !foundAck {
		t.Errorf("Mkdir failure must not drop already-recorded skip acknowledgements, got %v", messages)
	}
}

// TestLeakSelectorAdmission pins what the [test.IsFreeOfSecrets] lists mean now
// that one compiled selector serves both leak-scan gates. Rows marked
// "preserved" admit the subject the old per-check filter admitted; rows that
// moved name their declared change.
func TestLeakSelectorAdmission(t *testing.T) {
	tests := []struct {
		name      string
		whitelist []string
		blacklist []string
		subject   string
		admit     bool
	}{
		{"no list admits everything (preserved)", nil, nil, "anything.txt", true},
		{"whitelist regex admits a match (preserved)", []string{`.*\.txt$`}, nil, "notes.txt", true},
		{"whitelist regex rejects a non-match (preserved)", []string{`.*\.txt$`}, nil, "notes.log", false},
		{"whitelist matches case-sensitively (preserved)", []string{`^secret`}, nil, "SECRET.txt", false},
		{"blacklist regex rejects a match (preserved)", nil, []string{`.*\.log$`}, "notes.log", false},
		{"blacklist regex admits a non-match (preserved)", nil, []string{`.*\.log$`}, "notes.txt", true},
		{"blacklist matches case-sensitively (preserved)", nil, []string{`\.LOG$`}, "notes.log", true},
		// Change (d): the member gate reads the same regexes as the file gate,
		// so a metachar pattern filters a member path it could not touch before.
		{"member path filtered by regex, not by literal (change (d))", nil, []string{`.*\.log$`}, "inner/deep/run.log", false},
		// Change (a): the join leaked a leading (?i) into every later entry.
		{"inline flag applies to its own entry (change (a))", []string{`(?i)readme`, `data`}, nil, "README.md", true},
		{"inline flag no longer leaks into later entries (change (a))", []string{`(?i)readme`, `data`}, nil, "DATA.csv", false},
		// Change (f): the file gate matches RelPath, the declared subject, so a
		// path pattern reaches nested files as it reaches archive members.
		{"nested file excluded by a path pattern (change (f))", nil, []string{`^sub/`}, "sub/creds.txt", false},
		{"top-level file unaffected by a path pattern (change (f))", nil, []string{`^sub/`}, "creds.txt", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, err := selector.CompileLegacyRegexLists("IsFreeOfSecrets", "path", tt.whitelist, tt.blacklist)
			if err != nil {
				t.Fatalf("compile failed: %v", err)
			}
			if got := sel.Match(tt.subject); got != tt.admit {
				t.Errorf("Match(%q) = %v, want %v", tt.subject, got, tt.admit)
			}
		})
	}
}

// TestLeakSelectorRejectedAtLoad pins change (c) in its new home: the lists that
// cannot be honoured are refused at LOAD instead of skipping an already-started
// scan. The scan itself no longer compiles anything, so there is no runtime
// fail-closed branch left to test.
//
// The gate is utils.Compile, which validates DISABLED rules too - the leak rule
// ships disabled, and its lists are compiled through exactly the constructor
// exercised here (RuleSpecs + CompileRuleSelectors). The utils side of the same
// contract is pinned by utils.TestCompileValidatesDisabledRules.
func TestLeakSelectorRejectedAtLoad(t *testing.T) {
	tests := []struct {
		name      string
		whitelist []string
		blacklist []string
	}{
		{"uncompilable whitelist pattern", []string{"["}, nil},
		{"uncompilable blacklist pattern", nil, []string{"(unclosed"}},
		{"empty whitelist entry - change e", []string{""}, nil},
		{"empty blacklist entry - change e", nil, []string{""}},
		{"both lists set - change b", []string{`\.txt$`}, []string{`\.log$`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Through the same assembly + selector compilation the boot gate
			// runs, with the scan DISABLED - the shipped state - so the refusal
			// is the one an operator would hit.
			cfg := &config.Config{Tests: testsWithAnchors(map[string]*config.TestConfig{
				"IsFreeOfSecrets": {
					Whitelist: tt.whitelist,
					Blacklist: tt.blacklist,
					Attrs:     map[string]interface{}{"enabled": false},
				},
			}, nil)}
			specs, err := RuleSpecs(cfg, NewRegistry())
			if err != nil {
				t.Fatalf("assemble rule specs: %v", err)
			}
			for _, spec := range specs {
				if spec.Check != "IsFreeOfSecrets" {
					continue
				}
				if spec.Enabled {
					t.Fatal("the fixture ships the scan disabled")
				}
				_, serr := CompileRuleSelectors(spec, []Scope{ScopeRepository})
				if serr == nil {
					t.Fatal("expected the selector compile to refuse the lists")
				}
				if !strings.Contains(serr.Error(), "IsFreeOfSecrets") {
					t.Errorf("the error must name the rule: %v", serr)
				}
				return
			}
			t.Fatal("no IsFreeOfSecrets spec assembled")
		})
	}
}

// TestLeakFilterGatesFilesAndMembers is the wiring test: ONE compiled selector
// filters top-level files and archive members within a single scan, so a
// pattern like `secret.*\.txt$` drops both. The uppercase member pins the
// case-SENSITIVE member matching of change (d), and the nested file pins the
// RelPath subject of change (f) - `^sub/` reaches a nested file exactly as it
// would reach a nested member.
func TestLeakFilterGatesFilesAndMembers(t *testing.T) {
	dir := t.TempDir()
	dropped := filepath.Join(dir, "secret-notes.txt")
	kept := filepath.Join(dir, "public.txt")
	nested := filepath.Join(dir, "creds.txt") // collected as sub/creds.txt
	for _, p := range []string{dropped, kept, nested} {
		if err := os.WriteFile(p, []byte("token = abc\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	zipPath := filepath.Join(dir, "data.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	for _, member := range []string{"inner/secret.txt", "inner/other.txt", "inner/SECRET.txt"} {
		w, err := zw.Create(member)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("token = abc\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}

	bin, argsFile := fakeScanner(t, "null")
	cfg := leakTestConfig(bin, nil)
	cfg.Tests["IsFreeOfSecrets"].Blacklist = []string{`secret.*\.txt$`, `^sub/`}

	// RelPath is what the file gate matches; the collectors set it (here by
	// hand, since the repository is built without one).
	files := []structs.File{
		{Path: dropped, Name: "secret-notes.txt", RelPath: "secret-notes.txt", Size: structs.GetFileSize(dropped)},
		{Path: kept, Name: "public.txt", RelPath: "public.txt", Size: structs.GetFileSize(kept)},
		{Path: nested, Name: "creds.txt", RelPath: "sub/creds.txt", Size: structs.GetFileSize(nested)},
		{Path: zipPath, Name: "data.zip", RelPath: "data.zip", Size: structs.GetFileSize(zipPath), IsArchive: true},
	}
	runRepoRule(t, "IsFreeOfSecrets", cfg, structs.Repository{Files: files})

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("scanner did not run: %v", err)
	}
	args := string(raw)
	// Extracted members are written as "<idx>_<base>", so "_x.txt" identifies a
	// member and the bare path identifies a top-level file.
	for _, want := range []string{"public.txt", "_other.txt", "_SECRET.txt"} {
		if !strings.Contains(args, want) {
			t.Errorf("admitted subject %q missing from scanner args: %s", want, args)
		}
	}
	for _, unwanted := range []string{"secret-notes.txt", "_secret.txt", "creds.txt"} {
		if strings.Contains(args, unwanted) {
			t.Errorf("filtered subject %q reached the scanner: %s", unwanted, args)
		}
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
	a := leakAttrsFrom(nil, &config.GeneralConfig{MaxCores: 64})
	if a.binary != "betterleaks" || a.timeoutSeconds != 120 || a.maxProcs != 3 {
		t.Errorf("unexpected defaults: %+v", a)
	}
	a = leakAttrsFrom(map[string]interface{}{
		"binary": "/opt/bl", "timeoutSeconds": int64(30), "maxProcs": int64(1),
	}, &config.GeneralConfig{MaxCores: 64})
	if a.binary != "/opt/bl" || a.timeoutSeconds != 30 || a.maxProcs != 1 {
		t.Errorf("unexpected parsed attrs: %+v", a)
	}
}

// TestLeakAttrsInheritMaxCores pins that the CHILD scanner obeys the CONFIGURED
// CPU cap: it is a separate process, so it inherits nothing from this one's
// GOMAXPROCS and would otherwise be the single place that ignores the cap.
func TestLeakAttrsInheritMaxCores(t *testing.T) {
	capped := leakAttrsFrom(map[string]interface{}{"maxProcs": int64(8)}, &config.GeneralConfig{MaxCores: 2})
	if capped.maxProcs != 2 {
		t.Errorf("maxProcs %d: the rule asked for 8 on a 2-core budget, want the budget", capped.maxProcs)
	}
	// Below the budget the rule's own, smaller number stands.
	under := leakAttrsFrom(map[string]interface{}{"maxProcs": int64(1)}, &config.GeneralConfig{MaxCores: 8})
	if under.maxProcs != 1 {
		t.Errorf("maxProcs %d: the budget must not RAISE the rule's own limit", under.maxProcs)
	}
	// The rule setting nothing still takes the budget, not its own default.
	silent := leakAttrsFrom(nil, &config.GeneralConfig{MaxCores: 1})
	if silent.maxProcs != 1 {
		t.Errorf("maxProcs %d with no rule param on a 1-core budget, want 1", silent.maxProcs)
	}
}

// TestSecretScanChildInheritsMaxCores pins the wiring, not the clamp: the bind
// must take the cap from the config it is handed, so the child scanner honours
// [general] maxCores however the rule was written.
func TestSecretScanChildInheritsMaxCores(t *testing.T) {
	content := tempFile([]byte("nothing to find\n"))
	defer os.Remove(content)

	bin, argsFile := fakeScanner(t, "null")
	cfg := leakTestConfig(bin, &config.GeneralConfig{
		MaxArchiveFileSize:     10 * 1024 * 1024,
		MaxTotalArchiveMemory:  100 * 1024 * 1024,
		MaxContentScanFileSize: 1024 * 1024 * 1024,
		MaxCores:               2,
	})
	cfg.Tests["IsFreeOfSecrets"].Attrs["maxProcs"] = int64(8)

	file := structs.File{Path: content, Name: "data.txt", Size: 10}
	runRepoRule(t, "IsFreeOfSecrets", cfg, structs.Repository{Files: []structs.File{file}})

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "GOMAXPROCS=2\n") {
		t.Errorf("child scanner ran without the configured 2-core cap (rule asked for 8): %s", args)
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
	msgs := runRepoRule(t, "IsFreeOfSecrets", cfg, structs.Repository{Files: []structs.File{file}})
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

// TestCheckSecretAttrsUnknownKeyLists pins the unknown-key error's allowed-key
// list against the surface it reports for: the legacy attrs table accepts
// "enabled", the [[rule]] parameter set does not.
func TestCheckSecretAttrsUnknownKeyLists(t *testing.T) {
	legacyErr := checkSecretAttrs(map[string]interface{}{"nonsense": true}, true)
	if legacyErr == nil || !strings.Contains(legacyErr.Error(), "enabled, binary") {
		t.Errorf("the legacy allowed-key list must name enabled: %v", legacyErr)
	}
	ruleErr := checkSecretAttrs(map[string]interface{}{"nonsense": true}, false)
	if ruleErr == nil || strings.Contains(ruleErr.Error(), "enabled") {
		t.Errorf("the rule allowed-key list must not name enabled: %v", ruleErr)
	}
}
