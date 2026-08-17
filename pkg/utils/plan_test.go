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
func compilePlan(t *testing.T, cfg config.Config) *Plan {
	t.Helper()
	if cfg.General == nil {
		cfg.General = &config.GeneralConfig{MaxContentScanFileSize: config.DefaultMaxContentScanFileSize}
	}
	cfg = withRequiredAnchors(cfg)
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
	cfg := planConfig([]config.RuleSpec{
		{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true},
		{Name: "IsFreeOfKeywordsXX", Check: "IsFreeOfKeywordsXX", Enabled: true},
	})
	_, err := Compile(&cfg, checks.NewRegistry())
	if err == nil {
		t.Fatal("a declaration naming no known check must fail the compile")
	}
	if want := "IsFreeOfKeywordsXX"; !strings.Contains(err.Error(), want) {
		t.Errorf("error must name the orphan %q: %v", want, err)
	}
}

// TestCompileRejectsBadParamType asserts the FAULT each config carries, not
// merely that the compile failed: the anchors are filled in, so a config whose
// parameter is corrected compiles clean and the case cannot pass on an
// unrelated error.
func TestCompileRejectsBadParamType(t *testing.T) {
	cases := map[string]struct {
		config config.Config
		want   string
	}{
		"keywords is not a list": {
			config: planConfig([]config.RuleSpec{
				{Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true, Params: []map[string]interface{}{
					{"keywords": "password", "info": "found"},
				}},
			}),
			want: `"keywords" must be a list of strings`,
		},
		"info is not a string": {
			config: planConfig([]config.RuleSpec{
				{Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true, Params: []map[string]interface{}{
					{"keywords": []string{"password"}, "info": []string{"found"}},
				}},
			}),
			want: `"info" must be a string`,
		},
		"disallowed_names missing": {
			config: planConfig([]config.RuleSpec{
				{Name: "IsValidName", Check: "IsValidName", Enabled: true, Params: []map[string]interface{}{{"names": []string{".git"}}}},
			}),
			want: `unknown key "names"`,
		},
		"readme_names is not a list": {
			config: planConfig([]config.RuleSpec{
				{Name: "HasReadme", Check: "HasReadme", Enabled: true, Params: []map[string]interface{}{{"readme_names": "readme.md"}}},
			}),
			want: `"readme_names" must be a list of strings`,
		},
		"a check that takes no parameters": {
			config: planConfig([]config.RuleSpec{
				{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true, Params: []map[string]interface{}{{"keywords": []string{"x"}}}},
			}),
			want: `check "HasOnlyASCII" takes no parameters`,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := withRequiredAnchors(test.config)
			_, err := Compile(&cfg, checks.NewRegistry())
			if err == nil {
				t.Fatal("a wrong-typed parameter must fail the compile")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("the compile must fail on %s: %v", test.want, err)
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
// at once - all bad patterns, and every rule that names no check - rather than
// one per run.
func TestCompileAggregatesLoadErrors(t *testing.T) {
	cfg := planConfig([]config.RuleSpec{
		{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true, Include: []string{"("}},
		{Name: "HasNoWhiteSpace", Check: "HasNoWhiteSpace", Enabled: true, Exclude: []string{"[a-"}},
		{Name: "IsValidName", Check: "IsValidName", Enabled: true, Exclude: []string{"keep.txt", ""}},
		{Name: "NoSuchCheck", Check: "NoSuchCheck", Enabled: true},
	})
	_, err := Compile(&cfg, checks.NewRegistry())
	if err == nil {
		t.Fatal("expected a load error")
	}
	for _, want := range []string{"HasOnlyASCII", "HasNoWhiteSpace", "IsValidName", "NoSuchCheck"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregated error must name %q: %v", want, err)
		}
	}
}

// TestCompileReportsTwinsWithSiblingFaults is the same contract one level down,
// where a check's own rules are assembled: a twin - two rules of one check that
// differ only in their name - does not take the check's remaining rules out of
// the load, so a sibling's fault is reported in the SAME run rather than on the
// day the twin is removed and the load rerun. Only Compile can show it: the
// sibling's fault here is its selector's, and selectors are compiled here.
func TestCompileReportsTwinsWithSiblingFaults(t *testing.T) {
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("ascii-a", []string{"file"}, `\.csv$`),
		asciiRule("ascii-b", []string{"file"}, `\.csv$`),
		asciiRule("ascii-bad", []string{"file"}, "("),
	}
	cfg = withRequiredAnchors(cfg)
	_, err := Compile(&cfg, checks.NewRegistry())
	if err == nil {
		t.Fatal("a twin rule and an uncompilable pattern must both refuse the load")
	}
	for _, want := range []string{
		`rule "ascii-b": resolves to the same rule as "ascii-a"`,
		`selector for rule "ascii-bad"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the aggregate must report %q alongside the check's other fault: %v", want, err)
		}
	}
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
		// No rule for ReadMeContainsTOC, none for HasNoWhiteSpace.
		{Name: "HasReadme", Check: "HasReadme", Enabled: true, Params: []map[string]interface{}{
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
	msgs := applyChecksFilteredByRepository(context.Background(), &diagSink{}, plan.scope(checks.ScopeRepository), repo.Files)
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
// scan runs off its rule's enabled flag, so a disabled scan is simply not in
// the plan.
func TestCompileSkipsDisabledSecretScan(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := planConfig([]config.RuleSpec{
			{Name: "IsFreeOfSecrets", Check: "IsFreeOfSecrets", Enabled: enabled},
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

// TestCompileRejectsMissingGeneral pins that the scan bounds are never
// fabricated: a zero GeneralConfig means maxContentScanFileSize = 0, i.e. every
// file skipped as oversized, which must be a load error rather than a silent
// no-scan.
func TestCompileRejectsMissingGeneral(t *testing.T) {
	cfg := config.Config{Rules: []config.RuleSpec{{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true}}}
	if _, err := Compile(&cfg, checks.NewRegistry()); err == nil {
		t.Fatal("a config without [general] must fail the compile")
	}
}

// TestCompileRejectsEmptyRegistry pins that a zero-value Registry{} never
// compiles: a plan with no entries runs no checks, so every package would scan
// clean and the server would cache that as authoritative.
func TestCompileRejectsEmptyRegistry(t *testing.T) {
	cfg := planConfig(nil)
	if _, err := Compile(&cfg, checks.Registry{}); err == nil {
		t.Fatal("an empty check registry must fail the compile")
	}
}

// TestCompileRejectsNilConfig pins that a nil config is an error, not a panic.
func TestCompileRejectsNilConfig(t *testing.T) {
	if _, err := Compile(nil, checks.NewRegistry()); err == nil {
		t.Fatal("a nil config must fail the compile")
	}
}

// TestCompileErrorExposesSelectorFault pins that a pattern fault stays
// MATCHABLE through the aggregate: callers that want the faulty patterns rather
// than the message text match *selector.CompileError with errors.As.
func TestCompileErrorExposesSelectorFault(t *testing.T) {
	cfg := planConfig([]config.RuleSpec{{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true, Include: []string{"("}}})
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

// TestBuildMemberAdmissionTwoRules pins the plan-wide member decision on the
// PRODUCTION path - two [[rule]] specs through Compile, never hand-built
// selectors: with two member rules the iterator gets the UNION of their
// literals - matched against the member PATH, like the per-rule gates - and
// the per-rule gates must still run, because the union is nobody's own filter.
func TestBuildMemberAdmissionTwoRules(t *testing.T) {
	// Case-SENSITIVE literals: a case-folding selector may never gate a union
	// skip (selector.UnionLiterals), pinned by the ignoreCase case below.
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		memberRule("data-members", []string{"data/"}, false),
		memberRule("raw-members", []string{"raw/"}, false),
	}
	plan := compilePlan(t, cfg)

	entry := planEntry(t, plan, "IsFreeOfKeywords", checks.ScopeArchiveMember)
	if len(entry.rules) != 2 {
		t.Fatalf("expected 2 member rules, got %d", len(entry.rules))
	}
	batch := entry.batch
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
	// A member rule's dispatch gate admits every ARCHIVE: the selector
	// addresses members, so the container is never gated on it.
	if !entry.rules[0].Unfiltered() {
		t.Error("a member rule's dispatch gate must admit every archive")
	}

	// An ignoreCase selector may never gate a union skip: FastMatcher-style
	// folding is narrower than RE2 (?i), so the pass is disabled entirely.
	cfg = planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		memberRule("data-members", []string{"data/"}, false),
		memberRule("raw-members", []string{"raw/"}, true),
	}
	plan = compilePlan(t, cfg)
	entry = planEntry(t, plan, "IsFreeOfKeywords", checks.ScopeArchiveMember)
	if entry.batch.Admit != nil {
		t.Error("an ignoreCase member rule must disable the union pre-filter")
	}
	if !entry.batch.PerRule {
		t.Error("without a union, the per-rule gates are the only member filter")
	}
}

// TestMemberRulesUnionScansPerRule is the union's behaviour test over a real
// archive: two member rules, admission = the union of their literals, and each
// rule still sees ONLY the members its own gate admits - a member both
// keywords would hit is reported by the rule whose selector covers it, never
// by the other.
func TestMemberRulesUnionScansPerRule(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bundle.zip")
	buf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(buf)
	members := map[string]string{
		"data/one.csv":   "a password here",
		"raw/two.csv":    "a password here too",
		"docs/three.csv": "a password everywhere",
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
	cfg.Rules = []config.RuleSpec{
		memberRule("data-members", []string{"data/"}, false),
		memberRule("raw-members", []string{"raw/"}, false),
	}
	plan := compilePlan(t, cfg)

	archive := structs.ToFile(zipPath, "bundle.zip", -1, "")
	messages := applyChecksFilteredByFileOnArchive(context.Background(), &diagSink{}, plan.scope(checks.ScopeArchiveMember), []structs.File{archive})

	found := map[string]string{} // member -> rule that reported it
	for _, m := range messages {
		if m.Skipped {
			continue
		}
		src, ok := m.Source.(structs.File)
		if !ok {
			t.Fatalf("finding without a file source: %+v", m)
		}
		if previous, twice := found[src.Name]; twice && previous != m.Rule {
			t.Errorf("member %q reported by two rules: %q and %q", src.Name, previous, m.Rule)
		}
		found[src.Name] = m.Rule
	}
	want := map[string]string{
		"data/one.csv": "data-members",
		"raw/two.csv":  "raw-members",
	}
	for member, rule := range want {
		if found[member] != rule {
			t.Errorf("member %q: reported by %q, want %q", member, found[member], rule)
		}
	}
	if rule, scanned := found["docs/three.csv"]; scanned {
		t.Errorf("docs/three.csv matches no rule and must not be scanned, got a finding from %q", rule)
	}
}

// TestCompileValidatesDisabledRules pins the gate a disabled rule still passes
// through: Compile binds its parameters and compiles its selectors exactly like
// a live rule's - a config the scan could not honour must fail at LOAD, not on
// the day the dormant rule is re-enabled - and only then leaves it out of the
// plan (TestCompileSkipsDisabledSecretScan).
func TestCompileValidatesDisabledRules(t *testing.T) {
	cfg := planConfig([]config.RuleSpec{
		{Name: "IsFreeOfSecrets", Check: "IsFreeOfSecrets", Enabled: false, Include: []string{"("}},
	})
	if _, err := Compile(&cfg, checks.NewRegistry()); err == nil {
		t.Fatal("a disabled rule's uncompilable list must refuse the load")
	}
	cfg = planConfig([]config.RuleSpec{
		{Name: "IsFreeOfSecrets", Check: "IsFreeOfSecrets", Enabled: false,
			Params: []map[string]interface{}{{"nonsense": true}}},
	})
	if _, err := Compile(&cfg, checks.NewRegistry()); err == nil {
		t.Fatal("a disabled rule's unknown parameter must refuse the load")
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
	report := func(plan *Plan) map[string]string {
		out := map[string]string{}
		for _, m := range applyChecksFilteredByRepository(context.Background(), &diagSink{}, plan.scope(checks.ScopeRepository), files) {
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

// TestRepositoryRuleRejectsBadPattern is the other half of the same contract:
// now that the lists are live, an uncompilable one refuses the boot instead of
// sitting inert.
func TestRepositoryRuleRejectsBadPattern(t *testing.T) {
	cfg := planConfig([]config.RuleSpec{
		{
			Name: "HasReadme", Check: "HasReadme", Enabled: true,
			Include: []string{"^raw/("},
			Params:  []map[string]interface{}{{"readme_names": []string{"readme.md"}}},
		},
	})
	if _, err := Compile(&cfg, checks.NewRegistry()); err == nil {
		t.Fatal("an uncompilable HasReadme pattern must refuse the boot")
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
				entry.def.RunFile = func(ctx context.Context, file structs.File, s checks.Scope, batch *checks.Batch, rules []*checks.BoundRule) []structs.Message {
					record(key)
					return run(ctx, file, s, batch, rules)
				}
			}
			if run := entry.def.RunRepository; run != nil {
				entry.def.RunRepository = func(ctx context.Context, repo structs.Repository, batch *checks.Batch, rules []*checks.BoundRule) []structs.Message {
					record(key)
					return run(ctx, repo, batch, rules)
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
