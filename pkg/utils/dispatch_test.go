package utils

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// planConfig fills in the [general] section every config needs to compile, so a
// test that only cares about its rules can leave it out.
func planConfig(rules []config.RuleSpec) config.Config {
	return config.Config{
		General: &config.GeneralConfig{MaxContentScanFileSize: config.DefaultMaxContentScanFileSize},
		Rules:   rules,
	}
}

// withRequiredAnchors copies cfg's rules and fills the anchored checks (which
// Compile refuses to leave undeclared) with bare rules, so a fixture that
// configures only the check under test stays a complete config.
func withRequiredAnchors(cfg config.Config) config.Config {
	declared := func(name string) bool {
		for _, rule := range cfg.Rules {
			if rule.Check == name {
				return true
			}
		}
		return false
	}
	anchored := checks.AnchoredChecks()
	rules := make([]config.RuleSpec, 0, len(cfg.Rules)+len(anchored))
	rules = append(rules, cfg.Rules...)
	for _, name := range anchored {
		if !declared(name) {
			rules = append(rules, config.RuleSpec{Name: name, Check: name, Enabled: true})
		}
	}
	cfg.Rules = rules
	return cfg
}

// compilePlan compiles cfg against the real registry, the way both frontends do
// at startup.
func compilePlan(t *testing.T, cfg config.Config) *checks.Plan {
	t.Helper()
	if cfg.General == nil {
		cfg.General = &config.GeneralConfig{MaxContentScanFileSize: config.DefaultMaxContentScanFileSize}
	}
	cfg = withRequiredAnchors(cfg)
	plan, err := checks.Compile(&cfg, checks.NewRegistry())
	if err != nil {
		t.Fatalf("compile rules: %v", err)
	}
	return plan
}

// planEntry returns the plan entry of one check in one scope.
func planEntry(t *testing.T, plan *checks.Plan, name string, scope checks.Scope) checks.PlanEntry {
	t.Helper()
	for _, entry := range plan.Scope(scope) {
		if entry.Def.Name == name {
			return entry
		}
	}
	t.Fatalf("%s has no entry in scope %s", name, scope)
	return checks.PlanEntry{}
}

// planRule returns the single bound rule of one check in one scope.
func planRule(t *testing.T, plan *checks.Plan, name string, scope checks.Scope) *checks.BoundRule {
	t.Helper()
	for _, entry := range plan.Scope(scope) {
		if entry.Def.Name == name {
			if len(entry.Rules) != 1 {
				t.Fatalf("%s@%s: %d rules, want 1", name, scope, len(entry.Rules))
			}
			return entry.Rules[0]
		}
	}
	t.Fatalf("%s has no rule in scope %s", name, scope)
	return nil
}

// TestCompileSynthesizesDefaultRules is the guard against silently deleting a
// check no rule names: the shipped configs leave ReadMeContainsTOC (and, in
// testdata, three name checks) undeclared. It asserts the BOUND PARAMETERS,
// not just that a rule exists - a default rule bound to nothing would pass an
// existence check and check nothing.
func TestCompileSynthesizesDefaultRules(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "myreadme.md")
	if err := os.WriteFile(readme, []byte("only lists itself\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := planConfig([]config.RuleSpec{
		// No rule for ReadMeContainsTOC, none for HasOnlyASCII.
		{Name: "HasReadme", Check: "HasReadme", Enabled: true, Params: []map[string]interface{}{
			{"readme_names": []string{"myreadme.md"}},
		}},
	})
	plan := compilePlan(t, cfg)

	// The parameterless check is synthesized into both of its scopes and runs.
	naive := structs.File{Name: "naïve.csv", RelPath: "naïve.csv"}
	var subjects checks.Subjects
	subjects.Set(naive)
	for _, scope := range []checks.Scope{checks.ScopeFile, checks.ScopeArchiveFileList} {
		rule := planRule(t, plan, "HasOnlyASCII", scope)
		if !rule.Match(&subjects) {
			t.Errorf("a synthesized rule must carry an empty selector that admits everything (%s)", scope)
		}
	}

	// The synthesized TOC rule is bound to the CONFIGURED readme name, not to
	// some default: it must recognise myreadme.md as the readme and report the
	// file missing from it.
	repo := structs.Repository{Files: []structs.File{
		{Path: readme, Name: "myreadme.md", RelPath: "myreadme.md"},
		{Path: filepath.Join(dir, "data.csv"), Name: "data.csv", RelPath: "data.csv"},
	}}
	msgs := applyChecksFilteredByRepository(context.Background(), &diagSink{}, plan.Scope(checks.ScopeRepository), repo.Files)
	toc := 0
	for _, m := range msgs {
		if m.TestName == "ReadMeContainsTOC" {
			toc++
			if !strings.Contains(m.Content, "data.csv") {
				t.Errorf("the TOC rule must report the file missing from the readme: %q", m.Content)
			}
		}
		if m.TestName == "HasReadme" {
			t.Errorf("myreadme.md is the configured readme, so HasReadme must stay silent: %q", m.Content)
		}
	}
	if toc != 1 {
		t.Fatalf("expected exactly one ReadMeContainsTOC message, got %d: %v", toc, msgs)
	}
}

// memberRule builds one [[rule]] spec for the keyword check's archive-member
// scope, as the config surface produces it.
func memberRule(name string, include []string, ignoreCase bool) config.RuleSpec {
	return config.RuleSpec{
		Name:       name,
		Check:      "IsFreeOfKeywords",
		Scope:      []string{"archive-member"},
		Enabled:    true,
		IgnoreCase: ignoreCase,
		Include:    include,
		Subject:    "path",
		Params:     []map[string]interface{}{{"keywords": []string{"password"}, "info": "found"}},
	}
}

// TestMemberRulesUnionScansEachMemberOnce is the union's behaviour test over a
// real archive: two member rules, admission = the union of their literals, and
// each rule still sees ONLY the members its own gate admits. A member one gate
// admits is reported by that rule alone; a member both admit is scanned ONCE
// and carries ONE finding naming both, the two having bound the same parameters
// and therefore one unit.
func TestMemberRulesUnionScansEachMemberOnce(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bundle.zip")
	buf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(buf)
	members := map[string]string{
		"data/one.csv":      "a password here",
		"raw/two.csv":       "a password here too",
		"docs/three.csv":    "a password everywhere",
		"data/raw/four.csv": "a password both gates admit",
	}
	for name, content := range members {
		w, werr := zw.Create(name)
		if werr != nil {
			t.Fatal(werr)
		}
		if _, werr = w.Write([]byte(content)); werr != nil {
			t.Fatal(werr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := buf.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := planConfig(nil)
	// One parameter set over two gates, which the loader refuses where the two
	// read the same string - and both have to stay case-SENSITIVE, or their
	// literals contribute nothing and there is no union to test. Reading one
	// gate against the member PATH and the other against its BASE NAME is that
	// difference: the union takes both rules' literals over the path either way,
	// so it over-admits for the second one and its own gate decides.
	byName := memberRule("name-members", []string{`two\.csv`, `four\.csv`}, false)
	byName.Subject = "name"
	cfg.Rules = []config.RuleSpec{
		memberRule("data-members", []string{"data/"}, false),
		byName,
	}
	plan := compilePlan(t, cfg)

	archive := structs.ToFile(zipPath, "bundle.zip", -1, "")
	messages := applyChecksFilteredByFileOnArchive(context.Background(), &diagSink{}, plan.Scope(checks.ScopeArchiveMember), []structs.File{archive})

	found := map[string][][]string{} // member -> the rules each of its findings names
	for _, m := range messages {
		if m.Skipped {
			continue
		}
		src, ok := m.Source.(structs.File)
		if !ok {
			t.Fatalf("finding without a file source: %+v", m)
		}
		found[src.Name] = append(found[src.Name], m.Rules)
	}
	want := map[string][]string{
		"data/one.csv":      {"data-members"},
		"raw/two.csv":       {"name-members"},
		"data/raw/four.csv": {"data-members", "name-members"},
	}
	for member, rules := range want {
		reported := found[member]
		if len(reported) != 1 {
			t.Errorf("member %q: %d findings, want the one its single scan produces: %v", member, len(reported), reported)
			continue
		}
		if !slices.Equal(reported[0], rules) {
			t.Errorf("member %q: reported by %v, want %v", member, reported[0], rules)
		}
	}
	if rules, scanned := found["docs/three.csv"]; scanned {
		t.Errorf("docs/three.csv matches no rule and must not be scanned, got findings from %v", rules)
	}
}

// TestRepositoryRuleNarrowsFileSet pins the contract the previously-inert
// HasReadme lists now have (plan §3.1): a repository rule's selector narrows
// the file set the check sees, for HasReadme AND for ReadMeContainsTOC, which
// shares that rule.
func TestRepositoryRuleNarrowsFileSet(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "readme.md")
	if err := os.WriteFile(readme, []byte("lists nothing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []structs.File{
		{Path: readme, Name: "readme.md", RelPath: "docs/readme.md"},
		{Path: filepath.Join(dir, "data.csv"), Name: "data.csv", RelPath: "raw/data.csv"},
	}

	// Unnarrowed: the readme is seen, and the excluded file is missing from it.
	wide := compilePlan(t, planConfig([]config.RuleSpec{
		{Name: "HasReadme", Check: "HasReadme", Enabled: true,
			Params: []map[string]interface{}{{"readme_names": []string{"readme.md"}}}},
	}))
	report := func(plan *checks.Plan) map[string]string {
		out := map[string]string{}
		for _, m := range applyChecksFilteredByRepository(context.Background(), &diagSink{}, plan.Scope(checks.ScopeRepository), files) {
			out[m.TestName] = m.Content
		}
		return out
	}
	if got := report(wide); got["HasReadme"] != "" || !strings.Contains(got["ReadMeContainsTOC"], "data.csv") {
		t.Fatalf("without a selector both checks must see every file: %v", got)
	}

	// Narrowed to raw/: the readme is no longer in the set, so HasReadme reports
	// it missing and the TOC check has nothing to check.
	narrow := compilePlan(t, planConfig([]config.RuleSpec{
		{
			Name: "HasReadme", Check: "HasReadme", Enabled: true,
			Include: []string{"^raw/"},
			Params:  []map[string]interface{}{{"readme_names": []string{"readme.md"}}},
		},
	}))
	got := report(narrow)
	if got["HasReadme"] == "" {
		t.Error("a selector that excludes the readme must make HasReadme report it missing")
	}
	if _, reported := got["ReadMeContainsTOC"]; reported {
		t.Error("ReadMeContainsTOC shares HasReadme's rule, so it must see the same narrowed set")
	}
}

// multisetFixture is the file set the executed-check multiset is counted over:
// two plain files and two two-member archives, with "b" in exactly the names
// testdata/test_config.toml's keyword-rule include admits.
//
// "BUNDLE.zip" pins the case-SENSITIVITY of the selector: it holds a "b" only
// case-insensitively, so at file scope it is not selected, and at
// archive-member scope it is opened (member rules gate members, not
// containers) but none of its members passes the member gate - neither
// "note two.txt" nor "B_MEMBER.txt" holds a lowercase "b". An engine that
// folded case would scan it at file scope, and this multiset would grow.
func multisetFixture(t *testing.T) []structs.File {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) structs.File {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture %q: %v", name, err)
		}
		return structs.ToFile(path, name, -1, "")
	}
	writeZip := func(name string, members ...string) structs.File {
		zipPath := filepath.Join(dir, name)
		buf, err := os.Create(zipPath)
		if err != nil {
			t.Fatalf("create zip %q: %v", name, err)
		}
		zw := zip.NewWriter(buf)
		for _, member := range members {
			w, werr := zw.Create(member)
			if werr != nil {
				t.Fatalf("create member: %v", werr)
			}
			if _, werr = w.Write([]byte("harmless")); werr != nil {
				t.Fatalf("write member: %v", werr)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("close zip writer: %v", err)
		}
		if err := buf.Close(); err != nil {
			t.Fatalf("close zip: %v", err)
		}
		return structs.ToFile(zipPath, name, -1, "")
	}

	return []structs.File{
		write("notes b.txt", "harmless"),
		write("clean.txt", "harmless"),
		writeZip("bundle b.zip", "member one.txt", "b_member.txt"),
		writeZip("BUNDLE.zip", "note two.txt", "B_MEMBER.txt"),
	}
}

// countInvocations wraps every runner in the plan, so a real ApplyAllChecks run
// records the (check, scope) multiset the dispatch actually produced - rather
// than a second implementation of the selection rules inside the test.
func countInvocations(plan *checks.Plan) map[string]int {
	counts := map[string]int{}
	var mu sync.Mutex
	record := func(key string) {
		mu.Lock()
		counts[key]++
		mu.Unlock()
	}
	for scope := checks.Scope(0); scope < checks.NumScopes; scope++ {
		entries := plan.Scope(scope)
		for i := range entries {
			entry := &entries[i]
			key := entry.Def.Name + "@" + scope.String()
			if run := entry.Def.RunFile; run != nil {
				entry.Def.RunFile = func(ctx context.Context, file structs.File, s checks.Scope, batch *checks.Batch, rules []*checks.BoundRule) []structs.Message {
					record(key)
					return run(ctx, file, s, batch, rules)
				}
			}
			if run := entry.Def.RunRepository; run != nil {
				entry.Def.RunRepository = func(ctx context.Context, repo structs.Repository, batch *checks.Batch, rules []*checks.BoundRule) []structs.Message {
					record(key)
					return run(ctx, repo, batch, rules)
				}
			}
		}
	}
	return counts
}

// TestExecutedCheckMultisetUnchanged is R7's guard: over every shipped config,
// the engine must invoke exactly the (check, scope) pairs the registry
// declares - the pairs the five dispatch tables invoked before the rework plus
// HasFileNameSpecialChars and IsFileNameTooLong over the archive file list,
// where a member's name is checked for special characters and for length like
// any other name, minus HasNoWhiteSpace at file scope, which CKAN's own name
// munging makes moot. That is five file checks, the five name checks over the
// archive file list (once per member), the keyword check over archive members,
// two repository checks, and the leak scan disabled everywhere -
// IsArchiveFreeOfKeywords appearing where it now belongs, as IsFreeOfKeywords
// in the archive-member scope.
//
// The section-less checks are the point: ReadMeContainsTOC has no section in
// pc.toml or pc.toml.example, and HasNoWhiteSpace, HasFileNameSpecialChars and
// IsFileNameTooLong have none in testdata/test_config.toml. A translation
// without default-rule synthesis deletes them silently, and this test is what
// notices.
func TestExecutedCheckMultisetUnchanged(t *testing.T) {
	const files, members, archives = 4, 4, 2
	unfiltered := map[string]int{
		"HasOnlyASCII@file":                         files,
		"IsValidName@file":                          files,
		"HasFileNameSpecialChars@file":              files,
		"IsFileNameTooLong@file":                    files,
		"IsFreeOfKeywords@file":                     files,
		"HasOnlyASCII@archive-file-list":            members,
		"HasNoWhiteSpace@archive-file-list":         members,
		"IsValidName@archive-file-list":             members,
		"HasFileNameSpecialChars@archive-file-list": members,
		"IsFileNameTooLong@archive-file-list":       members,
		"IsFreeOfKeywords@archive-member":           archives,
		"HasReadme@repository":                      1,
		"ReadMeContainsTOC@repository":              1,
	}
	// testdata/test_config.toml carries the only non-empty lists: the keyword
	// rule includes names holding "b" - matched case-sensitively as a regex, so
	// at file scope "notes b.txt" and "bundle b.zip" qualify and "BUNDLE.zip"
	// does not - and the HasOnlyASCII rule excludes "test.txt", which no
	// fixture name matches.
	//
	// AMENDED at R9 (the config migrated to [[rule]]): a member-scope rule's
	// selector addresses MEMBERS under the one selector semantics, so every
	// archive is opened (2 invocations, where the legacy two-readings split
	// gated the container on the same list and opened only "bundle b.zip") and
	// the member gate decides inside - BUNDLE.zip's members hold no
	// case-sensitive "b", so it is opened and nothing in it is scanned.
	testdata := map[string]int{}
	for key, n := range unfiltered {
		testdata[key] = n
	}
	testdata["IsFreeOfKeywords@file"] = 2
	testdata["IsFreeOfKeywords@archive-member"] = 2

	cases := map[string]map[string]int{
		"../../pc.toml":                   unfiltered,
		"../../pc.toml.example":           unfiltered,
		"../../testdata/test_config.toml": testdata,
	}
	for path, want := range cases {
		t.Run(path, func(t *testing.T) {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("%s is not in the tree (pc.toml is a local, gitignored config)", path)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			plan := compilePlan(t, *cfg)
			counts := countInvocations(plan)

			resetGlobalScanState()
			ApplyAllChecks(context.Background(), *cfg, plan, multisetFixture(t))
			resetGlobalScanState()

			var diffs []string
			for key, n := range want {
				if counts[key] != n {
					diffs = append(diffs, fmt.Sprintf("%s: got %d, want %d", key, counts[key], n))
				}
			}
			for key, n := range counts {
				if _, expected := want[key]; !expected {
					diffs = append(diffs, fmt.Sprintf("%s: got %d, want none", key, n))
				}
			}
			if len(diffs) > 0 {
				sort.Strings(diffs)
				t.Fatalf("executed-check multiset drifted:\n%s", strings.Join(diffs, "\n"))
			}
		})
	}
}

// TestOversizedFileAcknowledgedOnce pins the content-scan size gates against
// the dispatch that decides which of them a file meets: the file pass
// acknowledges an over-cap plain file, the archive-member pass an over-cap
// archive it may list cheaply, and dispatch itself the over-cap .tar.gz it
// hands to neither archive pass - and no file is acknowledged twice. Only the
// whole pipeline can pin it: the gates sit in three places, and which files
// reach which is the dispatch's decision (IsArchive, and the listing cost of the
// format), not the check's.
func TestOversizedFileAcknowledgedOnce(t *testing.T) {
	dir := t.TempDir()
	var files []structs.File
	for _, name := range []string{"data.zip", "data.tar", "data.tar.gz", "data.gz", "data.txt"} {
		path := filepath.Join(dir, name)
		// Both gates read os.Stat before any reader opens the file, so the
		// archives reach them without being valid containers.
		if err := os.WriteFile(path, []byte("password"), 0o600); err != nil {
			t.Fatalf("write fixture %q: %v", name, err)
		}
		files = append(files, structs.ToFile(path, name, -1, ""))
	}

	cfg := planConfig([]config.RuleSpec{{
		Name: "keywords", Check: "IsFreeOfKeywords", Enabled: true,
		Params: []map[string]interface{}{
			{"keywords": []string{"password"}, "info": "found"},
		},
	}})
	// A one-byte cap puts every fixture over the gate.
	cfg.General.MaxContentScanFileSize = 1
	plan := compilePlan(t, cfg)

	resetGlobalScanState()
	messages, _ := ApplyAllChecks(context.Background(), cfg, plan, files)
	resetGlobalScanState()

	// The unreadable archives draw unrelated findings and diagnostics; only the
	// skip acknowledgements are this test's business - both wordings of them,
	// the content gates' and dispatch's refusal of a tar.gz.
	acknowledged := map[string][]string{}
	for _, m := range messages {
		if !strings.HasPrefix(m.Content, "Skipped ") {
			continue
		}
		src, ok := m.Source.(structs.File)
		if !ok {
			t.Fatalf("skip acknowledgement without a file source: %+v", m)
		}
		acknowledged[src.Name] = append(acknowledged[src.Name], m.Content)
	}
	want := map[string]string{
		"data.zip": "Skipped content scan of archive:",
		"data.tar": "Skipped content scan of archive:",
		// Listing a tar.gz costs a decompression, so over the cap dispatch refuses
		// the archive outright: the one message it emits covers the member-name
		// checks as well, and the content gate is never reached.
		"data.tar.gz": "Skipped archive checks (member-name checks and content scan):",
		"data.gz":     "Skipped content scan of archive:",
		"data.txt":    "Skipped content scan of file:",
	}
	for name, prefix := range want {
		got := acknowledged[name]
		if len(got) != 1 {
			t.Errorf("%s: %d skip acknowledgements, want exactly 1: %v", name, len(got), got)
			continue
		}
		if !strings.HasPrefix(got[0], prefix) {
			t.Errorf("%s: acknowledged as %q, want %q", name, got[0], prefix)
		}
	}
}

// tarGzMember is the fixture archive's only member: the whitespace in its NAME
// is a file-list finding and the keyword in its BODY a member-pass one, so a
// pass that opens the archive against the refusal leaves a visible trace.
const tarGzMember = "notes file.txt"

// tarGzFixture writes a VALID one-member .tar.gz. Valid on purpose: an archive
// refused on its SIZE has to be one both archive passes could otherwise walk -
// corrupt bytes would stop each of them for the wrong reason and prove nothing.
func tarGzFixture(t *testing.T) structs.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.tar.gz")
	writeTarGzFixture(t, path, tarGzMember, []byte("a password inside"))
	return structs.ToFile(path, "data.tar.gz", -1, "")
}

// writeTarGzFixture writes a one-member .tar.gz at path. testing.TB, so the
// tests and the pipeline benchmark share the one writer.
func writeTarGzFixture(tb testing.TB, path, member string, body []byte) {
	tb.Helper()
	f, err := os.Create(path)
	if err != nil {
		tb.Fatalf("create tar.gz: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: member, Mode: 0o600, Size: int64(len(body))}); err != nil {
		tb.Fatalf("write member header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		tb.Fatalf("write member: %v", err)
	}
	for _, closer := range []io.Closer{tw, gz, f} {
		if err := closer.Close(); err != nil {
			tb.Fatalf("close tar.gz: %v", err)
		}
	}
}

// TestOverCapTarGzRefusedByBothArchivePasses pins the refusal end to end: a
// .tar.gz over the content-scan cap is opened by NEITHER archive pass - its
// member list costs a decompression - so its member draws no finding and its
// bytes no diagnostic, and it says so ONCE, in a message covering the
// member-name checks and the content scan together. The content clause is there
// only when a member-scope rule had admitted the container, the only case where
// a content scan was ever scheduled. The refusal reaches no further than those
// two passes: the file and repository passes keep the full file set (the
// dormant repository-scope secret scan has its own size gate).
func TestOverCapTarGzRefusedByBothArchivePasses(t *testing.T) {
	cases := []struct {
		name    string
		scope   []string
		skipped string
	}{
		{
			name:    "a member rule admits the container",
			scope:   nil, // the keyword check's own scopes: file and archive-member
			skipped: "member-name checks and content scan",
		},
		{
			name:    "the only keyword rule is file-scoped",
			scope:   []string{"file"},
			skipped: "member-name checks",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := tarGzFixture(t)
			info, err := os.Stat(archive.Path)
			if err != nil {
				t.Fatalf("stat fixture: %v", err)
			}
			cfg := planConfig([]config.RuleSpec{{
				Name: "keywords", Check: "IsFreeOfKeywords", Enabled: true, Scope: tc.scope,
				Params: []map[string]interface{}{
					{"keywords": []string{"password"}, "info": "found"},
				},
			}})
			// A one-byte cap puts the fixture over the gate.
			cfg.General.MaxContentScanFileSize = 1
			plan := compilePlan(t, cfg)

			resetGlobalScanState()
			messages, diags := ApplyAllChecks(context.Background(), cfg, plan, []structs.File{archive})
			resetGlobalScanState()

			var got, opened []structs.Message
			for _, m := range messages {
				src, ok := m.Source.(structs.File)
				if !ok || src.Path != archive.Path {
					continue
				}
				// A member carries the ARCHIVE's path under its own name, so
				// anything filed under the member name is a walk that happened.
				if src.Name == tarGzMember {
					opened = append(opened, m)
					continue
				}
				got = append(got, m)
			}
			if len(opened) > 0 {
				t.Errorf("neither archive pass may open it, yet its member drew findings: %v", opened)
			}
			for _, d := range diags {
				if strings.Contains(d.Message, archive.Name) {
					t.Errorf("neither archive pass may read it, yet a diagnostic reports on it: %s", d.Message)
				}
			}
			if len(got) != 1 {
				t.Fatalf("a refused archive must produce exactly one message, got %d: %v", len(got), got)
			}
			want := fmt.Sprintf("Skipped archive checks (%s): listing this archive means decompressing its stream; file size (%d bytes) exceeds maximum (1 bytes).", tc.skipped, info.Size())
			if got[0].Content != want {
				t.Errorf("acknowledgement is\n %q\nwant %q", got[0].Content, want)
			}
			if got[0].TestName != "ArchiveFileList" {
				t.Errorf("the acknowledgement is filed under %q, want ArchiveFileList", got[0].TestName)
			}
			if !got[0].Skipped || got[0].Reason != got[0].Content {
				t.Errorf("the refusal must be a skip acknowledgement carrying its reason: %+v", got[0])
			}
		})
	}
}

// TestRefuseStreamListArchivesNothingScheduled pins the third case of the
// refusal, the one the end-to-end test cannot reach: when neither archive pass
// had scheduled anything for the over-cap archive, no acknowledgement is owed
// and none is emitted - and the archive is withheld from both passes all the
// same.
func TestRefuseStreamListArchivesNothingScheduled(t *testing.T) {
	general := &config.GeneralConfig{MaxContentScanFileSize: 1}

	refusals, remaining := refuseStreamListArchives(context.Background(), general, nil, nil, []structs.File{tarGzFixture(t)})

	if len(refusals) != 0 {
		t.Errorf("nothing was scheduled for the archive, so nothing is owed an acknowledgement, got: %v", refusals)
	}
	if len(remaining) != 0 {
		t.Errorf("the refused archive must be withheld from the archive passes even without a message, got: %v", remaining)
	}
}

// TestAtCapTarGzWalkedByBothPasses is the other side of the refusal: at (not
// over) the cap the same .tar.gz reaches both archive passes exactly as it did
// before - the name checks see its member list, the content scan its member
// bodies - and nothing about it is acknowledged as skipped.
func TestAtCapTarGzWalkedByBothPasses(t *testing.T) {
	archive := tarGzFixture(t)
	info, err := os.Stat(archive.Path)
	if err != nil {
		t.Fatalf("stat fixture: %v", err)
	}

	cfg := planConfig([]config.RuleSpec{{
		Name: "keywords", Check: "IsFreeOfKeywords", Enabled: true,
		Params: []map[string]interface{}{
			{"keywords": []string{"password"}, "info": "found"},
		},
	}})
	// At exactly the cap the archive is still walked: the refusal gate is
	// strictly greater-than, in step with the content gate in
	// pkg/checks/checks_by_file.go (fileInfo.Size() > cap).
	cfg.General.MaxContentScanFileSize = info.Size()
	plan := compilePlan(t, cfg)

	resetGlobalScanState()
	messages, _ := ApplyAllChecks(context.Background(), cfg, plan, []structs.File{archive})
	resetGlobalScanState()

	found := map[string]int{}
	for _, m := range messages {
		src, ok := m.Source.(structs.File)
		if !ok {
			continue
		}
		if m.Skipped {
			t.Errorf("an archive under the cap must not be acknowledged as skipped: %q", m.Content)
			continue
		}
		if src.Name == tarGzMember {
			found[m.TestName]++
		}
	}
	// The whitespace in the member name is the file-list pass's finding, the
	// keyword in its body the member pass's: one walk each, as before.
	if found["HasNoWhiteSpace"] != 1 {
		t.Errorf("expected the member name to be checked once, got %d findings: %v", found["HasNoWhiteSpace"], found)
	}
	if found["IsFreeOfKeywords"] != 1 {
		t.Errorf("expected the member body to be scanned once, got %d findings: %v", found["IsFreeOfKeywords"], found)
	}
}

// TestOverCapTarGzJudgesNoFileListRule pins the bookkeeping half of the
// refusal: a member list that was never walked proves nothing about the rules
// that would have selected over it, so none of them may be marked alive or
// dead. The rule matches no member of anything, so the run that DOES walk an
// archive is the control - without it, a silent scope and a silent rule name
// look the same. The bookkeeping is per scope, not per archive: a walked
// archive beside a refused one still marks the scope exercised, so a rule whose
// only match sat inside the refused archive can still be reported dead - that
// is accepted, the report is advisory.
func TestOverCapTarGzJudgesNoFileListRule(t *testing.T) {
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{asciiRule("list-dead", []string{"archive-file-list"}, "zzz-no-such-member")}
	cfg.General.MaxContentScanFileSize = 1
	plan := compilePlan(t, cfg)

	run := func(file structs.File) []structs.Diagnostic {
		resetGlobalScanState()
		_, diags := ApplyAllChecks(context.Background(), cfg, plan, []structs.File{file})
		resetGlobalScanState()
		return diags
	}

	// The refused archive leaves the scope unexercised: no verdict on its rules.
	assertRuleDiags(t, run(tarGzFixture(t)), structs.DiagWarning, 0, "list-dead")
	// A zip of the same size is listed without decompressing anything, so it IS
	// walked past the cap - and the very same rule is then reported dead.
	zip := structs.ToFile(buildNamedZip(t, []string{"alpha.txt"}), "data.zip", -1, "")
	assertRuleDiags(t, run(zip), structs.DiagWarning, 1, "list-dead")
}

// TestBenchFixturesCompile keeps the benchmark fixtures in the normal suite's
// reach: nothing else runs them, so a fixture Compile refuses would surface
// only on the day someone takes a measurement - and the measurements rest on
// exactly these three configs. It pins that each still compiles, nothing about
// the plan they compile to.
func TestBenchFixturesCompile(t *testing.T) {
	fixtures := map[string]func() config.Config{
		"benchFilterConfig":     benchFilterConfig,
		"benchUnfilteredConfig": benchUnfilteredConfig,
		"benchPipelineConfig":   benchPipelineConfig,
	}
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			compilePlan(t, fixture())
		})
	}
}
