package checks

import (
	"archive/zip"
	"bytes"
	"context"
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

// benchKeywordSecondSet rides as a second param set on the first rule - the
// shipped configs bind multi-set rules. "credential" hits the same archive
// members as "password", so those members carry TWO findings for one rule; no
// keyword here hits the text or PDF fixtures.
var benchKeywordSecondSet = []string{"credential", "passphrase"}

const benchKeywordSecondInfo = "Possible credential material in file"

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

// benchKeywordRules binds n keyword rules for one scope, one keyword group
// each - the fanout the gate is stated over. They share one acquisition per
// (file, check). The first rule carries benchKeywordSecondSet as a second param
// set, so an archive member both sets hit yields several findings for ONE rule
// and per-finding costs (message build, Source boxing) stay visible to the
// archive benchmarks.
//
// The binding goes through the REAL Compile, so the batch carries the merge
// node the dispatch walks: bound by hand, these benchmarks measured a path
// production never takes.
func benchKeywordRules(b *testing.B, n int, scope Scope) (CheckDef, *Batch, []*BoundRule) {
	b.Helper()
	specs := make([]config.RuleSpec, 0, n)
	for i := 0; i < n; i++ {
		sets := []map[string]interface{}{
			{"keywords": benchKeywordGroups[i], "info": benchKeywordInfos[i]},
		}
		if i == 0 {
			sets = append(sets, map[string]interface{}{
				"keywords": benchKeywordSecondSet, "info": benchKeywordSecondInfo,
			})
		}
		specs = append(specs, config.RuleSpec{
			Name:    benchKeywordInfos[i],
			Check:   "IsFreeOfKeywords",
			Enabled: true,
			Params:  sets,
		})
	}
	def, rules, batch := bindTestRule(b, "IsFreeOfKeywords", config.Config{General: benchGeneral(), Rules: specs}, scope)
	if len(rules) != n {
		b.Fatalf("expected %d bound rules in scope %s, got %d", n, scope, len(rules))
	}
	return def, batch, rules
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

func benchmarkIsFreeOfKeywords(b *testing.B, ruleCount int) {
	file := benchTextFile(b)
	def, batch, rules := benchKeywordRules(b, ruleCount, ScopeFile)

	b.ReportAllocs()
	for b.Loop() {
		msgs := def.RunFile(context.Background(), file, ScopeFile, batch, rules)
		if len(msgs) != ruleCount {
			b.Fatalf("expected one finding per rule, got %d for %d", len(msgs), ruleCount)
		}
		benchMsgSink += len(msgs)
	}
}

func BenchmarkIsFreeOfKeywordsRules1(b *testing.B) { benchmarkIsFreeOfKeywords(b, 1) }
func BenchmarkIsFreeOfKeywordsRules3(b *testing.B) { benchmarkIsFreeOfKeywords(b, 3) }

// benchStreamFile writes a text file of at least size bytes, past the
// streamChunkSize streaming threshold, so the keyword scan takes the chunked
// path instead of the whole-file one. nonASCII plants a valid UTF-8 'ä' every
// ~100 KiB, so every chunk leaves the ASCII fast path of the lowercasing step.
func benchStreamFile(b *testing.B, size int, nonASCII bool) structs.File {
	b.Helper()
	body := benchTextBody()
	var buf bytes.Buffer
	for buf.Len() <= size {
		if !nonASCII {
			buf.Write(body)
			continue
		}
		for off := 0; off < len(body); off += 100 * 1024 {
			buf.Write(body[off:min(off+100*1024, len(body))])
			buf.WriteString("ä\n")
		}
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
func benchmarkIsFreeOfKeywordsStream(b *testing.B, ruleCount, size int, nonASCII bool) {
	file := benchStreamFile(b, size, nonASCII)
	def, batch, rules := benchKeywordRules(b, ruleCount, ScopeFile)

	b.ReportAllocs()
	for b.Loop() {
		msgs := def.RunFile(context.Background(), file, ScopeFile, batch, rules)
		if len(msgs) != ruleCount {
			b.Fatalf("expected one deduplicated finding per rule, got %d for %d", len(msgs), ruleCount)
		}
		benchMsgSink += len(msgs)
	}
}

func BenchmarkIsFreeOfKeywordsStreamRules1(b *testing.B) {
	benchmarkIsFreeOfKeywordsStream(b, 1, streamChunkSize, false)
}
func BenchmarkIsFreeOfKeywordsStreamRules3(b *testing.B) {
	benchmarkIsFreeOfKeywordsStream(b, 3, streamChunkSize, false)
}

// The Large pair pins the size-INDEPENDENT acquisition budget: at 8 chunks the
// chunk count is ~6x the fixture above's, so a per-chunk allocation regression
// multiplies here while staying invisible at two chunks. The non-ASCII twin
// must stay near its ASCII sibling - the two diverged materially (the ratio is
// machine-dependent) before the chunk buffers were reused.
func BenchmarkIsFreeOfKeywordsStreamLarge(b *testing.B) {
	benchmarkIsFreeOfKeywordsStream(b, 1, 8*streamChunkSize, false)
}
func BenchmarkIsFreeOfKeywordsStreamLargeNonASCII(b *testing.B) {
	benchmarkIsFreeOfKeywordsStream(b, 1, 8*streamChunkSize, true)
}

func benchmarkIsArchiveFreeOfKeywords(b *testing.B, ruleCount int) {
	file := benchArchiveFile(b)
	def, batch, rules := benchKeywordRules(b, ruleCount, ScopeArchiveMember)

	// Each keyword group hits benchArchiveMembers/5 members once; the first
	// rule's second set hits the group-0 members again, so those members carry
	// two findings for one rule.
	want := (ruleCount + 1) * (benchArchiveMembers / 5)
	b.ReportAllocs()
	for b.Loop() {
		msgs := def.RunFile(context.Background(), file, ScopeArchiveMember, batch, rules)
		if len(msgs) != want {
			b.Fatalf("expected %d findings, got %d - the benchmark measures the wrong thing", want, len(msgs))
		}
		benchMsgSink += len(msgs)
	}
}

func BenchmarkIsArchiveFreeOfKeywordsRules1(b *testing.B) { benchmarkIsArchiveFreeOfKeywords(b, 1) }
func BenchmarkIsArchiveFreeOfKeywordsRules3(b *testing.B) { benchmarkIsArchiveFreeOfKeywords(b, 3) }

// benchMergedMemberSelectors are member gates that all admit every member of
// benchArchiveFile while the loader still accepts them as different rules.
// None of them is a literal, so several of them get NO union pre-filter
// (unionMemberAdmission) and the per-member gates decide - the member path the
// merge has to serve.
var benchMergedMemberSelectors = [...]string{
	`campaign/.*\.txt$`,
	`site_[0-9]{3}/`,
	`report_[0-9]{4}\.txt$`,
}

// benchMergedMemberRules binds n member rules over ONE keyword parameter set:
// binding the same parameters is what folds them into a single unit, so a
// member several of them admit is scanned once and reported once, naming all
// of them.
func benchMergedMemberRules(b *testing.B, n int) (CheckDef, *Batch, []*BoundRule) {
	b.Helper()
	specs := make([]config.RuleSpec, 0, n)
	for i := 0; i < n; i++ {
		specs = append(specs, config.RuleSpec{
			Name:    fmt.Sprintf("shared members %d", i),
			Check:   "IsFreeOfKeywords",
			Scope:   []string{"archive-member"},
			Enabled: true,
			Subject: "path",
			Include: []string{benchMergedMemberSelectors[i]},
			Params: []map[string]interface{}{
				{"keywords": benchKeywordGroups[0], "info": benchKeywordInfos[0]},
			},
		})
	}
	def, rules, batch := bindTestRule(b, "IsFreeOfKeywords", config.Config{General: benchGeneral(), Rules: specs}, ScopeArchiveMember)
	if len(rules) != n {
		b.Fatalf("expected %d bound rules at the archive-member scope, got %d", n, len(rules))
	}
	if got := len(batch.merged.units); got != 1 {
		b.Fatalf("rules that bound the same parameters must be one unit, got %d", got)
	}
	return def, batch, rules
}

func benchmarkArchiveMemberMerged(b *testing.B, ruleCount int) {
	file := benchArchiveFile(b)
	def, batch, rules := benchMergedMemberRules(b, ruleCount)

	// One finding per member carrying the keyword, however many gates admitted
	// it: the count does not scale with the rule count once the rules' units
	// merged, which is what this pair measures around. The NAMES still scale -
	// a finding names every rule that contributed it - so a merge that
	// collapsed the scan and lost the attribution fails here.
	wantFound, wantNamed := benchArchiveMembers/5, ruleCount*(benchArchiveMembers/5)
	b.ReportAllocs()
	for b.Loop() {
		msgs := def.RunFile(context.Background(), file, ScopeArchiveMember, batch, rules)
		named := 0
		for _, m := range msgs {
			named += len(m.Rules)
		}
		if len(msgs) != wantFound || named != wantNamed {
			b.Fatalf("expected %d findings naming %d rules in total, got %d and %d - the benchmark measures the wrong thing", wantFound, wantNamed, len(msgs), named)
		}
		benchMsgSink += len(msgs)
	}
}

func BenchmarkIsArchiveFreeOfKeywordsMergedRules1(b *testing.B) { benchmarkArchiveMemberMerged(b, 1) }
func BenchmarkIsArchiveFreeOfKeywordsMergedRules3(b *testing.B) { benchmarkArchiveMemberMerged(b, 3) }

func benchmarkPDFRulesFanout(b *testing.B, ruleCount int) {
	file := benchPDFFile(b)
	def, batch, rules := benchKeywordRules(b, ruleCount, ScopeFile)

	b.ReportAllocs()
	for b.Loop() {
		msgs := def.RunFile(context.Background(), file, ScopeFile, batch, rules)
		if len(msgs) != ruleCount {
			b.Fatalf("expected one finding per rule, got %d for %d", len(msgs), ruleCount)
		}
		benchMsgSink += len(msgs)
	}
}

func BenchmarkPDFRulesFanout1(b *testing.B) { benchmarkPDFRulesFanout(b, 1) }
func BenchmarkPDFRulesFanout3(b *testing.B) { benchmarkPDFRulesFanout(b, 3) }

// The name-rule fanout: one rule of a PARAMETER-LESS check versus three, over
// one file set. The three rules bind the SAME unit, so the entry scans a name
// ONCE and reports one finding naming all three - where a unit per rule scanned
// it three times and reported the same fault three times. The pair therefore no
// longer measures three times the work against one: both produce the same
// findings, and what 3 costs over 1 is what the rule count still costs around a
// single scan - the contributor mask, the wider attribution, and the selection
// that got here.
//
// benchNameCheck is the check the pair is stated over. Its finding path
// allocates - the offending runes are accumulated into the message - so a
// repeated finding costs what it costs; the selection pass
// (BenchmarkFilterChecksForFiles) and the end-to-end pipeline
// (BenchmarkApplyAllChecks) declare the same check, so the three sets of
// numbers are about one check.
const benchNameCheck = "HasOnlyASCII"

// benchNameRuleSpecs are rules of ONE parameter-less check that the loader
// accepts as DIFFERENT rules while all of them match the same files. A check
// reading no parameters leaves nothing but the selector fields in a rule's
// identity (see ruleIdentity), so what separates these three is the two fields
// that do no work without a pattern to apply them to: the case folding, and the
// subject a pattern would be read from. None of them declares an include or an
// exclude, so every one of these gates admits every file.
var benchNameRuleSpecs = []config.RuleSpec{
	{Name: "ascii file names", Check: benchNameCheck, Enabled: true},
	{Name: "ascii file names, folded", Check: benchNameCheck, Enabled: true, IgnoreCase: true},
	{Name: "ascii file names, by path", Check: benchNameCheck, Enabled: true, Subject: "path"},
}

// benchNameFiles is a data publication's order of magnitude, and enough names
// that one pass outweighs the timer. One in five of them carries a non-ASCII
// character, so benchNameFindings - the findings ONE rule produces over the set
// - keeps the finding path part of what is measured.
const (
	benchNameFiles    = 200
	benchNameFindings = benchNameFiles / 5
)

// benchNameFileSet builds names, not files: a name check reads structs.File and
// never the disk.
func benchNameFileSet() []structs.File {
	exts := [...]string{"csv", "txt", "xlsx", "md"}
	sites := [...]string{"lake_zurich", "greifensee", "sempachersee", "hallwilersee"}
	files := make([]structs.File, benchNameFiles)
	for i := range files {
		name := fmt.Sprintf("%s_profile_%04d.%s", sites[i%len(sites)], i, exts[i%len(exts)])
		if i%5 == 0 {
			name = fmt.Sprintf("messwerte_grösse_%s_%04d.%s", sites[i%len(sites)], i, exts[i%len(exts)])
		}
		files[i] = structs.File{Name: name, Path: "/data/" + name, RelPath: name}
	}
	return files
}

// benchNameRules binds the first n rule specs through the REAL Compile, so the
// fanout runs what a config saying that would dispatch - and n rules of one
// check reach the plan only because the loader finds them distinct.
func benchNameRules(b *testing.B, n int) (CheckDef, *Batch, []*BoundRule) {
	b.Helper()
	def, rules, batch := bindTestRule(b, benchNameCheck, config.Config{Rules: benchNameRuleSpecs[:n]}, ScopeFile)
	if len(rules) != n {
		b.Fatalf("expected %d bound rules at the file scope, got %d", n, len(rules))
	}
	return def, batch, rules
}

func benchmarkNameRules(b *testing.B, ruleCount int) {
	files := benchNameFileSet()
	def, batch, rules := benchNameRules(b, ruleCount)

	// One finding per non-ASCII name however many rules matched it: the count
	// stopped scaling with ruleCount when the rules' units merged. The NAMES on
	// those findings still scale, because the finding names every rule that
	// contributed it - so a merge that collapsed the scan and lost the
	// attribution would satisfy the finding count and fail here.
	wantFound, wantNamed := benchNameFindings, ruleCount*benchNameFindings
	b.ReportAllocs()
	for b.Loop() {
		found, named := 0, 0
		for _, file := range files {
			messages := def.RunFile(context.Background(), file, ScopeFile, batch, rules)
			found += len(messages)
			for _, message := range messages {
				named += len(message.Rules)
			}
		}
		if found != wantFound || named != wantNamed {
			b.Fatalf("expected %d findings naming %d rules in total, got %d and %d - the benchmark measures the wrong thing", wantFound, wantNamed, found, named)
		}
		benchMsgSink += found
	}
}

func BenchmarkNameRules1(b *testing.B) { benchmarkNameRules(b, 1) }
func BenchmarkNameRules3(b *testing.B) { benchmarkNameRules(b, 3) }

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
	count := func(ruleCount int) (skips, findings int) {
		rules := make([]*BoundRule, 0, ruleCount)
		for i := 0; i < ruleCount; i++ {
			rule, err := def.Bind(config.RuleSpec{
				Name:  benchKeywordInfos[i],
				Check: "IsFreeOfKeywords",
				Params: []map[string]interface{}{
					{"keywords": benchKeywordGroups[i], "info": benchKeywordInfos[i]},
				},
			}, general)
			if err != nil {
				t.Fatalf("bind rule %d: %v", i, err)
			}
			rules = append(rules, rule)
		}
		batch := mergedBatch(t, general, rules...)
		for _, m := range def.RunFile(context.Background(), archive, ScopeArchiveMember, batch, rules) {
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
