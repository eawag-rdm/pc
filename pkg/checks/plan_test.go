package checks

import (
	"errors"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/selector"
)

// anchoredConfig fills in what every config needs to compile - the [general]
// section and a [[rule]] for each anchored check - so a test that only cares
// about its own rules can leave both out.
func anchoredConfig(rules []config.RuleSpec) config.Config {
	return config.Config{
		General: &config.GeneralConfig{MaxContentScanFileSize: config.DefaultMaxContentScanFileSize},
		Rules:   rulesWithAnchors(rules),
	}
}

// compileAnchored compiles an anchoredConfig against the real registry, the way
// both frontends do at startup.
func compileAnchored(t *testing.T, cfg config.Config) *Plan {
	t.Helper()
	plan, err := Compile(&cfg, NewRegistry())
	if err != nil {
		t.Fatalf("compile rules: %v", err)
	}
	return plan
}

// planEntry returns the plan entry of one check in one scope.
func planEntry(t *testing.T, plan *Plan, name string, scope Scope) PlanEntry {
	t.Helper()
	for _, entry := range plan.Scope(scope) {
		if entry.Def.Name == name {
			return entry
		}
	}
	t.Fatalf("%s has no entry in scope %s", name, scope)
	return PlanEntry{}
}

func TestCompileRejectsUnknownCheck(t *testing.T) {
	cfg := anchoredConfig([]config.RuleSpec{
		{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true},
		{Name: "IsFreeOfKeywordsXX", Check: "IsFreeOfKeywordsXX", Enabled: true},
	})
	_, err := Compile(&cfg, NewRegistry())
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
			config: anchoredConfig([]config.RuleSpec{
				{Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true, Params: []map[string]interface{}{
					{"keywords": "password", "info": "found"},
				}},
			}),
			want: `"keywords" must be a list of strings`,
		},
		"info is not a string": {
			config: anchoredConfig([]config.RuleSpec{
				{Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true, Params: []map[string]interface{}{
					{"keywords": []string{"password"}, "info": []string{"found"}},
				}},
			}),
			want: `"info" must be a string`,
		},
		"disallowed_names missing": {
			config: anchoredConfig([]config.RuleSpec{
				{Name: "IsValidName", Check: "IsValidName", Enabled: true, Params: []map[string]interface{}{{"names": []string{".git"}}}},
			}),
			want: `unknown key "names"`,
		},
		"readme_names is not a list": {
			config: anchoredConfig([]config.RuleSpec{
				{Name: "HasReadme", Check: "HasReadme", Enabled: true, Params: []map[string]interface{}{{"readme_names": "readme.md"}}},
			}),
			want: `"readme_names" must be a list of strings`,
		},
		"a check that takes no parameters": {
			config: anchoredConfig([]config.RuleSpec{
				{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true, Params: []map[string]interface{}{{"keywords": []string{"x"}}}},
			}),
			want: `check "HasOnlyASCII" takes no parameters`,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := test.config
			_, err := Compile(&cfg, NewRegistry())
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
	keywords, _ := NewRegistry().Lookup("IsFreeOfKeywords")

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
	cfg := anchoredConfig([]config.RuleSpec{
		{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true, Include: []string{"("}},
		{Name: "HasNoWhiteSpace", Check: "HasNoWhiteSpace", Enabled: true, Exclude: []string{"[a-"}},
		{Name: "IsValidName", Check: "IsValidName", Enabled: true, Exclude: []string{"keep.txt", ""}},
		{Name: "NoSuchCheck", Check: "NoSuchCheck", Enabled: true},
	})
	_, err := Compile(&cfg, NewRegistry())
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
	cfg := anchoredConfig([]config.RuleSpec{
		{Name: "ascii-a", Check: "HasOnlyASCII", Scope: []string{"file"}, Enabled: true, Include: []string{`\.csv$`}},
		{Name: "ascii-b", Check: "HasOnlyASCII", Scope: []string{"file"}, Enabled: true, Include: []string{`\.csv$`}},
		{Name: "ascii-bad", Check: "HasOnlyASCII", Scope: []string{"file"}, Enabled: true, Include: []string{"("}},
	})
	_, err := Compile(&cfg, NewRegistry())
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

// TestCompileSkipsDisabledSecretScan pins the phase gate's new home: the leak
// scan runs off its rule's enabled flag, so a disabled scan is simply not in
// the plan.
func TestCompileSkipsDisabledSecretScan(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := anchoredConfig([]config.RuleSpec{
			{Name: "IsFreeOfSecrets", Check: "IsFreeOfSecrets", Enabled: enabled},
		})
		plan := compileAnchored(t, cfg)
		found := false
		for _, entry := range plan.Scope(ScopeRepository) {
			found = found || entry.Def.Name == "IsFreeOfSecrets"
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
	if _, err := Compile(&cfg, NewRegistry()); err == nil {
		t.Fatal("a config without [general] must fail the compile")
	}
}

// TestCompileRejectsEmptyRegistry pins that a zero-value Registry{} never
// compiles: a plan with no entries runs no checks, so every package would scan
// clean and the server would cache that as authoritative.
func TestCompileRejectsEmptyRegistry(t *testing.T) {
	cfg := anchoredConfig(nil)
	if _, err := Compile(&cfg, Registry{}); err == nil {
		t.Fatal("an empty check registry must fail the compile")
	}
}

// TestCompileRejectsNilConfig pins that a nil config is an error, not a panic.
func TestCompileRejectsNilConfig(t *testing.T) {
	if _, err := Compile(nil, NewRegistry()); err == nil {
		t.Fatal("a nil config must fail the compile")
	}
}

// TestCompileErrorExposesSelectorFault pins that a pattern fault stays
// MATCHABLE through the aggregate: callers that want the faulty patterns rather
// than the message text match *selector.CompileError with errors.As.
func TestCompileErrorExposesSelectorFault(t *testing.T) {
	cfg := anchoredConfig([]config.RuleSpec{{Name: "HasOnlyASCII", Check: "HasOnlyASCII", Enabled: true, Include: []string{"("}}})
	_, err := Compile(&cfg, NewRegistry())
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
	plan := compileAnchored(t, anchoredConfig([]config.RuleSpec{
		memberRule("data-members", []string{"data/"}, false),
		memberRule("raw-members", []string{"raw/"}, false),
	}))

	entry := planEntry(t, plan, "IsFreeOfKeywords", ScopeArchiveMember)
	if len(entry.Rules) != 2 {
		t.Fatalf("expected 2 member rules, got %d", len(entry.Rules))
	}
	batch := entry.Batch
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
	if !entry.Rules[0].Unfiltered() {
		t.Error("a member rule's dispatch gate must admit every archive")
	}

	// An ignoreCase selector may never gate a union skip: FastMatcher-style
	// folding is narrower than RE2 (?i), so the pass is disabled entirely.
	plan = compileAnchored(t, anchoredConfig([]config.RuleSpec{
		memberRule("data-members", []string{"data/"}, false),
		memberRule("raw-members", []string{"raw/"}, true),
	}))
	entry = planEntry(t, plan, "IsFreeOfKeywords", ScopeArchiveMember)
	if entry.Batch.Admit != nil {
		t.Error("an ignoreCase member rule must disable the union pre-filter")
	}
	if !entry.Batch.PerRule {
		t.Error("without a union, the per-rule gates are the only member filter")
	}
}

// TestCompileValidatesDisabledRules pins the gate a disabled rule still passes
// through: Compile binds its parameters and compiles its selectors exactly like
// a live rule's - a config the scan could not honour must fail at LOAD, not on
// the day the dormant rule is re-enabled - and only then leaves it out of the
// plan (TestCompileSkipsDisabledSecretScan).
func TestCompileValidatesDisabledRules(t *testing.T) {
	cfg := anchoredConfig([]config.RuleSpec{
		{Name: "IsFreeOfSecrets", Check: "IsFreeOfSecrets", Enabled: false, Include: []string{"("}},
	})
	if _, err := Compile(&cfg, NewRegistry()); err == nil {
		t.Fatal("a disabled rule's uncompilable list must refuse the load")
	}
	cfg = anchoredConfig([]config.RuleSpec{
		{Name: "IsFreeOfSecrets", Check: "IsFreeOfSecrets", Enabled: false,
			Params: []map[string]interface{}{{"nonsense": true}}},
	})
	if _, err := Compile(&cfg, NewRegistry()); err == nil {
		t.Fatal("a disabled rule's unknown parameter must refuse the load")
	}
}

// TestRepositoryRuleRejectsBadPattern is the other half of the repository
// rule's contract (the file set it narrows is pinned in pkg/utils, where the
// dispatch runs): now that the lists are live, an uncompilable one refuses the
// boot instead of sitting inert.
func TestRepositoryRuleRejectsBadPattern(t *testing.T) {
	cfg := anchoredConfig([]config.RuleSpec{
		{
			Name: "HasReadme", Check: "HasReadme", Enabled: true,
			Include: []string{"^raw/("},
			Params:  []map[string]interface{}{{"readme_names": []string{"readme.md"}}},
		},
	})
	if _, err := Compile(&cfg, NewRegistry()); err == nil {
		t.Fatal("an uncompilable HasReadme pattern must refuse the boot")
	}
}
