package utils

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// planConfig fills in the [general] section every config needs to compile, so a
// test that only cares about its [test.X] sections can leave it out.
func planConfig(tests map[string]*config.TestConfig) config.Config {
	return config.Config{
		General: &config.GeneralConfig{MaxContentScanFileSize: config.DefaultMaxContentScanFileSize},
		Tests:   tests,
	}
}

// compilePlan compiles cfg against the real registry, the way both frontends do
// at startup.
func compilePlan(t *testing.T, cfg config.Config) *Plan {
	t.Helper()
	if cfg.General == nil {
		cfg.General = &config.GeneralConfig{MaxContentScanFileSize: config.DefaultMaxContentScanFileSize}
	}
	plan, err := Compile(&cfg, checks.NewRegistry())
	if err != nil {
		t.Fatalf("compile rules: %v", err)
	}
	return plan
}

// planEntry returns the plan entry of one check in one scope.
func planEntry(t *testing.T, plan *Plan, name string, scope checks.Scope) checkRules {
	t.Helper()
	for _, entry := range plan.scope(scope) {
		if entry.def.Name == name {
			return entry
		}
	}
	t.Fatalf("%s has no entry in scope %s", name, scope)
	return checkRules{}
}

// planRule returns the single bound rule of one check in one scope.
func planRule(t *testing.T, plan *Plan, name string, scope checks.Scope) *checks.BoundRule {
	t.Helper()
	for _, entry := range plan.scope(scope) {
		if entry.def.Name == name {
			if len(entry.rules) != 1 {
				t.Fatalf("%s@%s: %d rules, want 1", name, scope, len(entry.rules))
			}
			return entry.rules[0]
		}
	}
	t.Fatalf("%s has no rule in scope %s", name, scope)
	return nil
}

func TestCompileRejectsUnknownCheck(t *testing.T) {
	cfg := planConfig(map[string]*config.TestConfig{
		"HasOnlyASCII":       {},
		"HasOnlyAsciiTypo":   {},
		"IsFreeOfKeywordsXX": {},
	})
	_, err := Compile(&cfg, checks.NewRegistry())
	if err == nil {
		t.Fatal("a section naming no known check must fail the compile")
	}
	for _, want := range []string{"HasOnlyAsciiTypo", "IsFreeOfKeywordsXX"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name the orphaned section %q: %v", want, err)
		}
	}
}

func TestCompileRejectsBadParamType(t *testing.T) {
	cases := map[string]config.Config{
		"keywords is not a list": planConfig(map[string]*config.TestConfig{
			"IsFreeOfKeywords": {KeywordArguments: []map[string]interface{}{
				{"keywords": "password", "info": "found"},
			}},
		}),
		"info is not a string": planConfig(map[string]*config.TestConfig{
			"IsFreeOfKeywords": {KeywordArguments: []map[string]interface{}{
				{"keywords": []string{"password"}, "info": []string{"found"}},
			}},
		}),
		"disallowed_names missing": planConfig(map[string]*config.TestConfig{
			"IsValidName": {KeywordArguments: []map[string]interface{}{{"names": []string{".git"}}}},
		}),
		"readme_names is not a list": planConfig(map[string]*config.TestConfig{
			"HasReadme": {KeywordArguments: []map[string]interface{}{{"readme_names": "readme.md"}}},
		}),
		"a check that takes no parameters": planConfig(map[string]*config.TestConfig{
			"HasOnlyASCII": {KeywordArguments: []map[string]interface{}{{"keywords": []string{"x"}}}},
		}),
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(&cfg, checks.NewRegistry()); err == nil {
				t.Fatal("a wrong-typed parameter must fail the compile")
			}
		})
	}
}

func TestCompileRejectsUnsupportedScope(t *testing.T) {
	keywords, _ := checks.NewRegistry().Lookup("IsFreeOfKeywords")

	// A scope the check does not serve.
	if _, err := ruleScopes(config.RuleSpec{Check: "IsFreeOfKeywords", Scope: []string{"repository"}}, keywords); err == nil {
		t.Error("a scope the check does not support must be a load error")
	}
	// A scope that does not exist at all.
	if _, err := ruleScopes(config.RuleSpec{Check: "IsFreeOfKeywords", Scope: []string{"nonsense"}}, keywords); err == nil {
		t.Error("an unknown scope name must be a load error")
	}
	// The scopes it does serve resolve.
	scopes, err := ruleScopes(config.RuleSpec{Check: "IsFreeOfKeywords", Scope: []string{"file", "archive-member"}}, keywords)
	if err != nil || len(scopes) != 2 {
		t.Errorf("supported scopes must resolve, got (%v, %v)", scopes, err)
	}
	// Naming none takes the check's own.
	if scopes, err = ruleScopes(config.RuleSpec{Check: "IsFreeOfKeywords"}, keywords); err != nil || len(scopes) != 2 {
		t.Errorf("a rule naming no scope takes the check's own, got (%v, %v)", scopes, err)
	}
}

// TestCompileAggregatesLoadErrors pins that a config author is told every fault
// at once - all bad patterns, and every section that names no check - rather
// than one per run.
func TestCompileAggregatesLoadErrors(t *testing.T) {
	cfg := planConfig(map[string]*config.TestConfig{
		"HasOnlyASCII":    {Whitelist: []string{"("}},
		"HasNoWhiteSpace": {Blacklist: []string{"[a-"}},
		"IsValidName":     {Blacklist: []string{"keep.txt", ""}},
		"IsFreeOfKeywords": {
			Whitelist: []string{"keep.txt"},
			Blacklist: []string{"drop.txt"},
		},
		"NoSuchCheck": {},
	})
	_, err := Compile(&cfg, checks.NewRegistry())
	if err == nil {
		t.Fatal("expected a load error")
	}
	for _, want := range []string{"HasOnlyASCII", "HasNoWhiteSpace", "IsValidName", "IsFreeOfKeywords", "NoSuchCheck"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregated error must name %q: %v", want, err)
		}
	}
}

// TestCompileSynthesizesDefaultRules is the guard against silently deleting a
// check no [test.X] section names: the shipped configs leave ReadMeContainsTOC
// (and, in testdata, three name checks) section-less. It asserts the BOUND
// PARAMETERS, not just that a rule exists - a default rule bound to nothing
// would pass an existence check and check nothing.
func TestCompileSynthesizesDefaultRules(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "myreadme.md")
	if err := os.WriteFile(readme, []byte("only lists itself\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := planConfig(map[string]*config.TestConfig{
		// No [test.ReadMeContainsTOC], no [test.HasNoWhiteSpace].
		"HasReadme": {KeywordArguments: []map[string]interface{}{
			{"readme_names": []string{"myreadme.md"}},
		}},
	})
	plan := compilePlan(t, cfg)

	// The parameterless check is synthesized into both of its scopes and runs.
	spaced := structs.File{Name: "has space.txt", RelPath: "has space.txt"}
	for _, scope := range []checks.Scope{checks.ScopeFile, checks.ScopeArchiveFileList} {
		rule := planRule(t, plan, "HasNoWhiteSpace", scope)
		if !rule.Match(spaced) {
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
	msgs := applyChecksFilteredByRepository(context.Background(), plan.scope(checks.ScopeRepository), repo.Files)
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

// TestCompileSkipsDisabledSecretScan pins the phase gate's new home: the leak
// scan's attrs.enabled is the rule's enabled flag, so a disabled scan is simply
// not in the plan.
func TestCompileSkipsDisabledSecretScan(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := planConfig(map[string]*config.TestConfig{
			"IsFreeOfSecrets": {Attrs: map[string]interface{}{"enabled": enabled}},
		})
		plan := compilePlan(t, cfg)
		found := false
		for _, entry := range plan.scope(checks.ScopeRepository) {
			found = found || entry.def.Name == "IsFreeOfSecrets"
		}
		if found != enabled {
			t.Errorf("enabled = %v: leak rule in plan = %v", enabled, found)
		}
	}
}

// TestCompileMemberAdmission pins §3.2's correction: the iterator's member
// pre-filter is the single rule's own selector, and nothing is built when there
// is only one - which is every shipped config.
func TestCompileMemberAdmission(t *testing.T) {
	cfg := planConfig(map[string]*config.TestConfig{
		"IsFreeOfKeywords": {Blacklist: []string{".log"}},
	})
	plan := compilePlan(t, cfg)
	entry := planEntry(t, plan, "IsFreeOfKeywords", checks.ScopeArchiveMember)
	rule := planRule(t, plan, "IsFreeOfKeywords", checks.ScopeArchiveMember)
	if entry.batch.Admit != rule.Member {
		t.Error("with one member rule the iterator must filter through that rule's own member selector")
	}
	if entry.batch.PerRule {
		t.Error("with one member rule the per-rule member gates are redundant")
	}
	// The legacy member reading: literal, case-insensitive, over the member
	// path. Asserted through the admission filter the iterator is handed, which
	// the assertion above pinned to this rule's own member selector.
	if rule.Member == nil {
		t.Fatal("member selector must be compiled")
	}
	if entry.batch.Admit.Match("deep/run.LOG") {
		t.Error("the member gate must keep the case-insensitive literal reading")
	}
	// The dispatch gate keeps the OTHER legacy reading: regex over the archive's
	// own name, case-sensitive. ".log" as a regex matches any character before
	// "log", so an archive named "catalog.zip" is excluded by it - and a
	// container named "RUN.LOG.zip" is not, because the regex reading is case
	// sensitive where the member reading is not.
	if rule.Match(structs.File{Name: "catalog.zip", RelPath: "catalog.zip"}) {
		t.Error("the dispatch gate must keep the regex reading of the same list")
	}
	if !rule.Match(structs.File{Name: "RUN.LOG.zip", RelPath: "RUN.LOG.zip"}) {
		t.Error("the dispatch gate must keep the CASE-SENSITIVE regex reading")
	}
}

// TestCompileRejectsMissingGeneral pins that the scan bounds are never
// fabricated: a zero GeneralConfig means maxContentScanFileSize = 0, i.e. every
// file skipped as oversized, which must be a load error rather than a silent
// no-scan.
func TestCompileRejectsMissingGeneral(t *testing.T) {
	cfg := config.Config{Tests: map[string]*config.TestConfig{"HasOnlyASCII": {}}}
	if _, err := Compile(&cfg, checks.NewRegistry()); err == nil {
		t.Fatal("a config without [general] must fail the compile")
	}
}

// TestCompileErrorExposesSelectorFault pins that a pattern fault stays
// MATCHABLE through the aggregate: callers that want the faulty patterns rather
// than the message text match *selector.CompileError with errors.As.
func TestCompileErrorExposesSelectorFault(t *testing.T) {
	cfg := planConfig(map[string]*config.TestConfig{"HasOnlyASCII": {Whitelist: []string{"("}}})
	_, err := Compile(&cfg, checks.NewRegistry())
	if err == nil {
		t.Fatal("an uncompilable pattern must fail the compile")
	}
	var compileErr *selector.CompileError
	if !errors.As(err, &compileErr) {
		t.Fatalf("the aggregate must stay matchable as *selector.CompileError: %v", err)
	}
	if compileErr.Rule != "HasOnlyASCII" || len(compileErr.Faults) == 0 {
		t.Errorf("the fault must name its rule and its patterns: %+v", compileErr)
	}
}

// TestBuildMemberAdmissionTwoRules pins the plan-wide member decision: with two
// member rules the iterator gets the UNION of their literals - matched against
// the member PATH, like the per-rule gates - and the per-rule gates must still
// run, because the union is nobody's own filter.
func TestBuildMemberAdmissionTwoRules(t *testing.T) {
	def := checks.CheckDef{Name: "IsFreeOfKeywords", Scopes: checks.ScopesOf(checks.ScopeArchiveMember)}
	rules := make([]*checks.BoundRule, 0, 2)
	// Case-SENSITIVE literals: a case-folding selector may never gate a union
	// skip (selector.UnionLiterals), which is why the translated legacy lists
	// never build one.
	for _, list := range [][]string{{"data/"}, {"raw/"}} {
		member, err := selector.Compile(selector.Spec{Rule: list[0], Subject: "path", Include: list})
		if err != nil {
			t.Fatalf("compile member selector: %v", err)
		}
		rule := &checks.BoundRule{Rule: list[0]}
		rule.SetSelectors(selector.Selector{}, &member)
		rules = append(rules, rule)
	}
	plan := &Plan{}
	plan.scopes[checks.ScopeArchiveMember] = []checkRules{{def: &def, batch: &checks.Batch{}, rules: rules}}
	plan.buildMemberAdmission()

	batch := plan.scopes[checks.ScopeArchiveMember][0].batch
	if !batch.PerRule {
		t.Fatal("a union admission is nobody's own filter: the per-rule gates must still run")
	}
	if batch.Admit == nil {
		t.Fatal("two literal member selectors must produce a union pre-filter")
	}
	// Subject "path": the union carries member-path literals, so matching it
	// against a base name would skip every nested member.
	if !batch.Admit.Match("data/one.csv") || !batch.Admit.Match("raw/two.csv") {
		t.Error("the union must admit the members of both rules, matched by PATH")
	}
	if batch.Admit.Match("docs/three.csv") {
		t.Error("the union must skip a member no rule can match")
	}
}

// TestCompileKeepsDisabledSecretListsUncompiled pins the division of labour a
// disabled rule creates: utils.Compile leaves it out of the plan and therefore
// never compiles its lists, so the gate that refuses a leak config the scan
// could not honour is config.ValidateChecksConfig, at boot.
func TestCompileKeepsDisabledSecretListsUncompiled(t *testing.T) {
	cfg := planConfig(map[string]*config.TestConfig{
		"IsFreeOfSecrets": {Whitelist: []string{"("}, Attrs: map[string]interface{}{"enabled": false}},
	})
	if _, err := Compile(&cfg, checks.NewRegistry()); err != nil {
		t.Fatalf("a disabled rule leaves the plan before its lists are compiled: %v", err)
	}
}

// TestRepositoryRuleNarrowsFileSet pins the contract the previously-inert
// [test.HasReadme] lists now have (plan §3.1): a repository rule's selector
// narrows the file set the check sees, for HasReadme AND for ReadMeContainsTOC,
// which shares that section.
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
	wide := compilePlan(t, planConfig(map[string]*config.TestConfig{
		"HasReadme": {KeywordArguments: []map[string]interface{}{{"readme_names": []string{"readme.md"}}}},
	}))
	report := func(plan *Plan) map[string]string {
		out := map[string]string{}
		for _, m := range applyChecksFilteredByRepository(context.Background(), plan.scope(checks.ScopeRepository), files) {
			out[m.TestName] = m.Content
		}
		return out
	}
	if got := report(wide); got["HasReadme"] != "" || !strings.Contains(got["ReadMeContainsTOC"], "data.csv") {
		t.Fatalf("without a selector both checks must see every file: %v", got)
	}

	// Narrowed to raw/: the readme is no longer in the set, so HasReadme reports
	// it missing and the TOC check has nothing to check.
	narrow := compilePlan(t, planConfig(map[string]*config.TestConfig{
		"HasReadme": {
			Whitelist:        []string{"^raw/"},
			KeywordArguments: []map[string]interface{}{{"readme_names": []string{"readme.md"}}},
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

// TestRepositoryRuleRejectsBadPattern is the other half of the same contract:
// now that the lists are live, an uncompilable one refuses the boot instead of
// sitting inert.
func TestRepositoryRuleRejectsBadPattern(t *testing.T) {
	cfg := planConfig(map[string]*config.TestConfig{
		"HasReadme": {
			Whitelist:        []string{"^raw/("},
			KeywordArguments: []map[string]interface{}{{"readme_names": []string{"readme.md"}}},
		},
	})
	if _, err := Compile(&cfg, checks.NewRegistry()); err == nil {
		t.Fatal("an uncompilable [test.HasReadme] pattern must refuse the boot")
	}
}

// multisetFixture is the file set the executed-check multiset is counted over:
// two plain files and two two-member archives, with "b" in exactly the names
// testdata/test_config.toml's whitelist admits.
//
// "BUNDLE.zip" is the guard for the two legacy readings of one list: it holds a
// "b" only under the CASE-INSENSITIVE literal reading the archive MEMBER gate
// uses, not under the case-sensitive regex reading the DISPATCH gate uses. An
// engine that gated the container with the member reading would scan it, and
// this multiset would grow.
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
		writeZip("BUNDLE.zip", "member two.txt", "B_MEMBER.txt"),
	}
}

// countInvocations wraps every runner in the plan, so a real ApplyAllChecks run
// records the (check, scope) multiset the dispatch actually produced - rather
// than a second implementation of the selection rules inside the test.
func countInvocations(plan *Plan) map[string]int {
	counts := map[string]int{}
	var mu sync.Mutex
	record := func(key string) {
		mu.Lock()
		counts[key]++
		mu.Unlock()
	}
	for scope := checks.Scope(0); scope < checks.NumScopes; scope++ {
		for i := range plan.scopes[scope] {
			entry := &plan.scopes[scope][i]
			key := entry.def.Name + "@" + scope.String()
			if run := entry.def.RunFile; run != nil {
				entry.def.RunFile = func(file structs.File, s checks.Scope, batch *checks.Batch, rules []*checks.BoundRule) []structs.Message {
					record(key)
					return run(file, s, batch, rules)
				}
			}
			if run := entry.def.RunRepository; run != nil {
				entry.def.RunRepository = func(repo structs.Repository, batch *checks.Batch, rules []*checks.BoundRule) []structs.Message {
					record(key)
					return run(repo, batch, rules)
				}
			}
		}
	}
	return counts
}

// TestExecutedCheckMultisetUnchanged is R7's guard: over every shipped config,
// the engine must invoke exactly the (check, scope) pairs the five dispatch
// tables invoked before the rework. The expectations are derived from those
// tables - six file checks, three over the archive file list (once per member),
// the keyword check over archive members, two repository checks, and the leak
// scan disabled everywhere - and IsArchiveFreeOfKeywords appears where it now
// belongs, as IsFreeOfKeywords in the archive-member scope.
//
// The section-less checks are the point: ReadMeContainsTOC has no section in
// pc.toml or pc.toml.example, and HasNoWhiteSpace, HasFileNameSpecialChars and
// IsFileNameTooLong have none in testdata/test_config.toml. A translation
// without default-rule synthesis deletes them silently, and this test is what
// notices.
func TestExecutedCheckMultisetUnchanged(t *testing.T) {
	const files, members, archives = 4, 4, 2
	unfiltered := map[string]int{
		"HasOnlyASCII@file":                 files,
		"HasNoWhiteSpace@file":              files,
		"IsValidName@file":                  files,
		"HasFileNameSpecialChars@file":      files,
		"IsFileNameTooLong@file":            files,
		"IsFreeOfKeywords@file":             files,
		"HasOnlyASCII@archive-file-list":    members,
		"HasNoWhiteSpace@archive-file-list": members,
		"IsValidName@archive-file-list":     members,
		"IsFreeOfKeywords@archive-member":   archives,
		"HasReadme@repository":              1,
		"ReadMeContainsTOC@repository":      1,
	}
	// testdata/test_config.toml carries the only non-empty lists: the keyword
	// check is whitelisted to names holding "b" - matched case-sensitively as a
	// regex, so "notes b.txt" and "bundle b.zip" qualify and "BUNDLE.zip" does
	// not - and HasOnlyASCII blacklists "test.txt", which no fixture name
	// matches.
	testdata := map[string]int{}
	for key, n := range unfiltered {
		testdata[key] = n
	}
	testdata["IsFreeOfKeywords@file"] = 2
	testdata["IsFreeOfKeywords@archive-member"] = 1

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
			ApplyAllChecks(context.Background(), *cfg, plan, multisetFixture(t), true)
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
