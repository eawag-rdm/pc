package checks

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// The rule-fanout benchmarks: one keyword parameter set versus three, over the
// three acquisition paths (text file, archive members, PDF). They are the gate
// for the rules rework - the one-set case must cost what it costs today, and
// three sets must stay close to one, because acquisition happens once per
// (file, check) however many rules match.

var benchMsgSink int

// benchKeywordGroups are three disjoint keyword sets shaped like the shipped
// configs: literal strings, a handful each.
var benchKeywordGroups = [][]string{
	{"password", "id_rsa", "secret_token"},
	{"Q:", "/Users/", `C:\Users\`},
	{"admin", "root", "superuser"},
}

var benchKeywordInfos = []string{
	"Possible credentials in file",
	"Possible internal information in file",
	"Administrative accounts detected",
}

// benchGeneral is the scan-limits config every rule below binds.
func benchGeneral() *config.GeneralConfig {
	return &config.GeneralConfig{
		MaxContentScanFileSize: 1024 * 1024 * 1024,
		MaxArchiveFileSize:     10 * 1024 * 1024,
		MaxTotalArchiveMemory:  100 * 1024 * 1024,
		MaxArchiveMemberCount:  2000,
		MaxPDFPages:            10,
		MaxPDFFileSize:         1024 * 1024,
	}
}

// benchKeywordRules binds n keyword rules, one keyword group each - the fanout
// the gate is stated over. They share one acquisition per (file, check).
func benchKeywordRules(b *testing.B, n int) (CheckDef, *Batch, []*BoundRule) {
	b.Helper()
	def, _ := NewRegistry().Lookup("IsFreeOfKeywords")
	rules := make([]*BoundRule, 0, n)
	for i := 0; i < n; i++ {
		rule, err := def.Bind(config.RuleSpec{
			Name:    benchKeywordInfos[i],
			Check:   "IsFreeOfKeywords",
			Enabled: true,
			Params: map[string]interface{}{ParamSets: []map[string]interface{}{
				{"keywords": benchKeywordGroups[i], "info": benchKeywordInfos[i]},
			}},
		}, benchGeneral())
		if err != nil {
			b.Fatalf("bind rule %d: %v", i, err)
		}
		rules = append(rules, rule)
	}
	// One member-scope rule filters through its own member selector, as Compile
	// wires it: nothing configured here, so the batch admits every member.
	return def, NewBatch(benchGeneral()), rules
}

// benchTextBody builds ~256 KiB of realistic prose lines, seeded with exactly
// one hit for each of the three keyword groups so every set takes both the scan
// and the report path.
func benchTextBody() []byte {
	var b strings.Builder
	b.Grow(300 * 1024)
	line := "2026-08-11T09:14:22Z sensor=alpha site=lake_zurich depth_m=12.5 temperature_c=8.71 conductivity=284 status=ok\n"
	for b.Len() < 256*1024 {
		switch {
		case b.Len() > 60*1024 && b.Len() < 60*1024+len(line):
			b.WriteString("note: connect with password=hunter2 before the run\n")
		case b.Len() > 140*1024 && b.Len() < 140*1024+len(line):
			b.WriteString("note: source copied from /Users/rdm/staging/raw\n")
		case b.Len() > 210*1024 && b.Len() < 210*1024+len(line):
			b.WriteString("note: ingest performed by the admin account\n")
		}
		b.WriteString(line)
	}
	return []byte(b.String())
}

func benchTextFile(b *testing.B) structs.File {
	b.Helper()
	path := filepath.Join(b.TempDir(), "measurements.txt")
	if err := os.WriteFile(path, benchTextBody(), 0o600); err != nil {
		b.Fatalf("write text fixture: %v", err)
	}
	return structs.ToFile(path, "measurements.txt", -1, "")
}

// benchArchiveMembers is the member count the archive gates are stated against.
const benchArchiveMembers = 1000

// benchArchiveFile writes a zip of benchArchiveMembers small text members, one
// in five carrying a keyword hit.
func benchArchiveFile(b *testing.B) structs.File {
	b.Helper()
	body := "station report, 240 bytes of otherwise unremarkable text about the sampling campaign and its instruments; " +
		"the numbers below are placeholders for the real series and exist only to give the scanner something to read.\n"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < benchArchiveMembers; i++ {
		name := fmt.Sprintf("campaign/site_%03d/report_%04d.txt", i%20, i)
		w, err := zw.Create(name)
		if err != nil {
			b.Fatalf("create member %q: %v", name, err)
		}
		content := body
		switch i % 5 {
		case 0:
			content += "credential note: password rotated last week\n"
		case 1:
			content += "path note: staged under /Users/rdm/incoming\n"
		case 2:
			content += "account note: uploaded by admin\n"
		}
		if _, err := w.Write([]byte(content)); err != nil {
			b.Fatalf("write member %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		b.Fatalf("close zip: %v", err)
	}
	path := filepath.Join(b.TempDir(), "campaign.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		b.Fatalf("write archive fixture: %v", err)
	}
	return structs.ToFile(path, "campaign.zip", -1, "")
}

// benchPDFFile writes a five-page PDF, one hit per keyword group.
func benchPDFFile(b *testing.B) structs.File {
	b.Helper()
	filler := strings.Repeat("sampling campaign report text with several words ", 12)
	data := buildTestPDF(
		filler,
		filler+" the password lives here",
		filler,
		filler+" copied from /Users/rdm/staging",
		filler+" signed off by admin",
	)
	path := filepath.Join(b.TempDir(), "report.pdf")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		b.Fatalf("write pdf fixture: %v", err)
	}
	return structs.ToFile(path, "report.pdf", -1, "")
}

// benchQuietStdout muzzles the iterator's per-ten-member memory line: it must
// stay in the measurement but off the terminal, or the benchmark measures the
// console.
func benchQuietStdout(b *testing.B) {
	b.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatalf("open %s: %v", os.DevNull, err)
	}
	stdout := os.Stdout
	os.Stdout = devnull
	b.Cleanup(func() {
		os.Stdout = stdout
		devnull.Close()
	})
}

func benchmarkIsFreeOfKeywords(b *testing.B, ruleCount int) {
	file := benchTextFile(b)
	def, batch, rules := benchKeywordRules(b, ruleCount)

	b.ReportAllocs()
	for b.Loop() {
		msgs := def.RunFile(file, ScopeFile, batch, rules)
		if len(msgs) != ruleCount {
			b.Fatalf("expected one finding per rule, got %d for %d", len(msgs), ruleCount)
		}
		benchMsgSink += len(msgs)
	}
}

func BenchmarkIsFreeOfKeywordsRules1(b *testing.B) { benchmarkIsFreeOfKeywords(b, 1) }
func BenchmarkIsFreeOfKeywordsRules3(b *testing.B) { benchmarkIsFreeOfKeywords(b, 3) }

// benchStreamFile writes a text file past the 1 MiB streaming threshold, so the
// keyword scan takes the chunked path instead of the whole-file one.
func benchStreamFile(b *testing.B) structs.File {
	b.Helper()
	body := benchTextBody()
	var buf bytes.Buffer
	for buf.Len() <= 1024*1024 {
		buf.Write(body)
	}
	path := filepath.Join(b.TempDir(), "series.txt")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		b.Fatalf("write stream fixture: %v", err)
	}
	return structs.ToFile(path, "series.txt", -1, "")
}

// benchmarkIsFreeOfKeywordsStream is the gate on the STREAMED acquisition: the
// file is read once however many rules scan it, and the per-chunk wrappers the
// rules are handed are allocated once for the whole file.
func benchmarkIsFreeOfKeywordsStream(b *testing.B, ruleCount int) {
	file := benchStreamFile(b)
	def, batch, rules := benchKeywordRules(b, ruleCount)

	b.ReportAllocs()
	for b.Loop() {
		msgs := def.RunFile(file, ScopeFile, batch, rules)
		if len(msgs) != ruleCount {
			b.Fatalf("expected one deduplicated finding per rule, got %d for %d", len(msgs), ruleCount)
		}
		benchMsgSink += len(msgs)
	}
}

func BenchmarkIsFreeOfKeywordsStreamRules1(b *testing.B) { benchmarkIsFreeOfKeywordsStream(b, 1) }
func BenchmarkIsFreeOfKeywordsStreamRules3(b *testing.B) { benchmarkIsFreeOfKeywordsStream(b, 3) }

func benchmarkIsArchiveFreeOfKeywords(b *testing.B, ruleCount int) {
	benchQuietStdout(b)
	file := benchArchiveFile(b)
	def, batch, rules := benchKeywordRules(b, ruleCount)

	b.ReportAllocs()
	for b.Loop() {
		msgs := def.RunFile(file, ScopeArchiveMember, batch, rules)
		if len(msgs) == 0 {
			b.Fatal("no finding - the benchmark measures the wrong thing")
		}
		benchMsgSink += len(msgs)
	}
}

func BenchmarkIsArchiveFreeOfKeywordsRules1(b *testing.B) { benchmarkIsArchiveFreeOfKeywords(b, 1) }
func BenchmarkIsArchiveFreeOfKeywordsRules3(b *testing.B) { benchmarkIsArchiveFreeOfKeywords(b, 3) }

func benchmarkPDFRulesFanout(b *testing.B, ruleCount int) {
	file := benchPDFFile(b)
	def, batch, rules := benchKeywordRules(b, ruleCount)

	b.ReportAllocs()
	for b.Loop() {
		msgs := def.RunFile(file, ScopeFile, batch, rules)
		if len(msgs) != ruleCount {
			b.Fatalf("expected one finding per rule, got %d for %d", len(msgs), ruleCount)
		}
		benchMsgSink += len(msgs)
	}
}

func BenchmarkPDFRulesFanout1(b *testing.B) { benchmarkPDFRulesFanout(b, 1) }
func BenchmarkPDFRulesFanout3(b *testing.B) { benchmarkPDFRulesFanout(b, 3) }

// TestArchiveBudgetIndependentOfRuleCount is the acceptance criterion the
// benchmarks cannot state: an archive is opened and walked ONCE per (file,
// check), so its member and memory budgets are charged once however many rules
// match. A per-rule iterator would repeat every skip acknowledgement.
func TestArchiveBudgetIndependentOfRuleCount(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, member := range []struct{ name, content string }{
		{"small_a.txt", "password in a small member\n"},
		{"small_b.txt", "/Users/rdm in a small member\n"},
		{"big_a.txt", strings.Repeat("x", 4096)},
		{"big_b.txt", strings.Repeat("y", 4096)},
	} {
		w, err := zw.Create(member.name)
		if err != nil {
			t.Fatalf("create member %q: %v", member.name, err)
		}
		if _, err := w.Write([]byte(member.content)); err != nil {
			t.Fatalf("write member %q: %v", member.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	path := filepath.Join(dir, "budget.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	archive := structs.ToFile(path, "budget.zip", -1, "")

	// A member cap of 1KB leaves the two big members unscanned, so the iterator
	// records one skip acknowledgement each - the per-archive budget made
	// visible.
	general := benchGeneral()
	general.MaxArchiveFileSize = 1024

	def, _ := NewRegistry().Lookup("IsFreeOfKeywords")
	batch := NewBatch(general)
	count := func(ruleCount int) (skips, findings int) {
		rules := make([]*BoundRule, 0, ruleCount)
		for i := 0; i < ruleCount; i++ {
			rule, err := def.Bind(config.RuleSpec{
				Name:  benchKeywordInfos[i],
				Check: "IsFreeOfKeywords",
				Params: map[string]interface{}{ParamSets: []map[string]interface{}{
					{"keywords": benchKeywordGroups[i], "info": benchKeywordInfos[i]},
				}},
			}, general)
			if err != nil {
				t.Fatalf("bind rule %d: %v", i, err)
			}
			rules = append(rules, rule)
		}
		for _, m := range def.RunFile(archive, ScopeArchiveMember, batch, rules) {
			if m.Skipped {
				skips++
			} else {
				findings++
			}
		}
		return skips, findings
	}

	oneSkips, oneFindings := count(1)
	if oneSkips == 0 || oneFindings == 0 {
		t.Fatalf("fixture must produce both skips and findings, got %d/%d", oneSkips, oneFindings)
	}
	threeSkips, threeFindings := count(3)
	if threeSkips != oneSkips {
		t.Errorf("skip acknowledgements must not scale with the rule count: %d with 1 rule, %d with 3", oneSkips, threeSkips)
	}
	if threeFindings <= oneFindings {
		t.Errorf("more rules must find more, got %d with 1 rule and %d with 3", oneFindings, threeFindings)
	}
}
