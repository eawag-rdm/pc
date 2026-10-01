package checks

import (
	"errors"
	"fmt"
	"os"
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
		"maxLength is zero": {
			config: anchoredConfig([]config.RuleSpec{
				{Name: "IsFileNameTooLong", Check: "IsFileNameTooLong", Enabled: true, Params: []map[string]interface{}{{"maxLength": int64(0)}}},
			}),
			want: `"maxLength" has the wrong type or an invalid value (0)`,
		},
		"maxLength is a string": {
			config: anchoredConfig([]config.RuleSpec{
				{Name: "IsFileNameTooLong", Check: "IsFileNameTooLong", Enabled: true, Params: []map[string]interface{}{{"maxLength": "abc"}}},
			}),
			want: `"maxLength" has the wrong type or an invalid value ("abc")`,
		},
		"IsFileNameTooLong with two parameter sets": {
			config: anchoredConfig([]config.RuleSpec{
				{Name: "IsFileNameTooLong", Check: "IsFileNameTooLong", Enabled: true, Params: []map[string]interface{}{{"maxLength": int64(10)}, {"maxLength": int64(20)}}},
			}),
			want: `check "IsFileNameTooLong" takes one parameter set`,
		},
		"IsFileNameTooLong with an unknown key": {
			config: anchoredConfig([]config.RuleSpec{
				{Name: "IsFileNameTooLong", Check: "IsFileNameTooLong", Enabled: true, Params: []map[string]interface{}{{"maxlength": int64(64)}}},
			}),
			want: `unknown key "maxlength"`,
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

// TestScopeResolutionAgreesAcrossIdentityAndPlan pins the single reading of a
// declared scope list: the duplicate refusal and the plan resolve it through
// the same ruleScopes, so a name no scope answers to is reported once, as the
// unknown scope it is, and never as a duplicate rule beside it. An identity
// pass that skipped the bad name found the two rules identical and told the
// operator to remove one that does its own work.
func TestScopeResolutionAgreesAcrossIdentityAndPlan(t *testing.T) {
	// One pair of rules, spelled two ways. They differ in a scope-DEPENDENT
	// field - the subject, which at file scope reads the name when undeclared
	// and the path when spelled out - so under a scope name that resolves to
	// nothing neither reading is decided.
	pair := func(scope string) config.Config {
		return anchoredConfig([]config.RuleSpec{
			{Name: "keywords-by-name", Check: "IsFreeOfKeywords", Scope: []string{scope}, Enabled: true, Include: []string{"data/"}},
			{Name: "keywords-by-path", Check: "IsFreeOfKeywords", Scope: []string{scope}, Enabled: true, Include: []string{"data/"}, Subject: "path"},
		})
	}

	cfg := pair("fil")
	_, err := Compile(&cfg, NewRegistry())
	if err == nil {
		t.Fatal("a scope name no scope answers to must fail the compile")
	}
	if want := `unknown scope "fil"`; !strings.Contains(err.Error(), want) {
		t.Errorf("the load must report %s: %v", want, err)
	}
	if strings.Contains(err.Error(), "resolves to the same rule as") {
		t.Errorf("two rules the misspelled scope left unresolved must not be reported as duplicates: %v", err)
	}

	// The premise of the arm above: spelled correctly, the scope decides the
	// subject reading and the pair is two DIFFERENT rules. Were they one rule
	// under every reading, the silence above would be a genuine twin's report
	// swallowed rather than a fault reported once.
	cfg = pair("file")
	if _, err := Compile(&cfg, NewRegistry()); err != nil {
		t.Errorf("the same pair under a scope that resolves gates two different subjects, so both must load: %v", err)
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
	bin, _ := fakeScanner(t, "[]")
	for _, enabled := range []bool{false, true} {
		cfg := anchoredConfig([]config.RuleSpec{
			{Name: "IsFreeOfSecrets", Check: "IsFreeOfSecrets", Enabled: enabled,
				Params: []map[string]interface{}{{"binary": bin}}},
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
	// skip (selector.UnionLiterals), pinned by the ignoreCase case below. Two
	// case-sensitive rules of one check are only a config at all where they bind
	// DIFFERENT parameters, so the second scans for another keyword.
	raw := memberRule("raw-members", []string{"raw/"}, false)
	raw.Params = []map[string]interface{}{{"keywords": []string{"token"}, "info": "found"}}
	plan := compileAnchored(t, anchoredConfig([]config.RuleSpec{
		memberRule("data-members", []string{"data/"}, false),
		raw,
	}))

	entry := planEntry(t, plan, "IsFreeOfKeywords", ScopeArchiveMember)
	if len(entry.Rules) != 2 {
		t.Fatalf("expected 2 member rules, got %d", len(entry.Rules))
	}
	batch := entry.Batch
	if !batch.perRule {
		t.Fatal("a union admission is nobody's own filter: the per-rule gates must still run")
	}
	if batch.admit == nil {
		t.Fatal("two literal member selectors must produce a union pre-filter")
	}
	// Subject "path": the union carries member-path literals, so matching it
	// against a base name would skip every nested member.
	if !batch.admit.Match("data/one.csv") || !batch.admit.Match("raw/two.csv") {
		t.Error("the union must admit the members of both rules, matched by PATH")
	}
	if batch.admit.Match("docs/three.csv") {
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
	if entry.Batch.admit != nil {
		t.Error("an ignoreCase member rule must disable the union pre-filter")
	}
	if !entry.Batch.perRule {
		t.Error("without a union, the per-rule gates are the only member filter")
	}
}

// TestRuleMemberScopeGatesMembers pins §3.2's one semantics at archive-member
// scope: the selector addresses MEMBERS, so the dispatch gate admits every
// archive and the member gate carries the pattern - matched per the rule's
// subject, which defaults to "path" in this scope.
func TestRuleMemberScopeGatesMembers(t *testing.T) {
	plan := compileAnchored(t, anchoredConfig([]config.RuleSpec{{
		Name: "csv-members", Check: "IsFreeOfKeywords", Scope: []string{"archive-member"}, Enabled: true,
		Include: []string{`\.csv$`},
		Params:  []map[string]interface{}{{"keywords": []string{"password"}, "info": "found"}},
	}}))
	entry := planEntry(t, plan, "IsFreeOfKeywords", ScopeArchiveMember)
	if len(entry.Rules) != 1 {
		t.Fatalf("expected 1 member rule, got %d", len(entry.Rules))
	}
	rule := entry.Rules[0]
	if !rule.Unfiltered() {
		t.Error("a member rule's dispatch gate must admit every archive")
	}
	if rule.Member == nil {
		t.Fatal("the member gate must carry the rule's selector")
	}
	if !rule.Member.Match("one.csv") || rule.Member.Match("two.txt") {
		t.Error("the member gate must match the pattern against members")
	}
	// One member rule: the iterator filters through that rule's own selector.
	if entry.Batch.admit != rule.Member || entry.Batch.perRule {
		t.Error("with one member rule the iterator must use its selector directly")
	}
}

// TestCompileShippedConfigs is R9's gate: every shipped config through
// LoadConfig + Compile. It must live HERE, not in pkg/config -
// TestParseShippedConfigsLoad cannot reach Compile, and LoadConfig alone
// rejects neither an unknown check name, nor a bad param type, nor an
// unsupported scope.
func TestCompileShippedConfigs(t *testing.T) {
	for _, path := range []string{
		"../../pc.toml",
		"../../pc.toml.example",
		"../../testdata/test_config.toml",
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("%s is not in the tree (pc.toml is a local, gitignored config)", path)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			plan, err := Compile(cfg, NewRegistry())
			if err != nil {
				t.Fatalf("compile config: %v", err)
			}
			// Every shipped config carries ONE keyword rule (its groups ride as
			// [[rule.params]] sets), so the archive path takes the
			// single-member-rule admission: the iterator gets that rule's own
			// selector and the per-rule gates stay off.
			entry := planEntry(t, plan, "IsFreeOfKeywords", ScopeArchiveMember)
			if len(entry.Rules) != 1 || entry.Batch.perRule {
				t.Errorf("shipped configs must keep the single-member-rule fast path: %d rules, perRule %v", len(entry.Rules), entry.Batch.perRule)
			}
		})
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

// TestMergeBitMatchesReportIndex pins what the merge may not disturb: a plan
// entry's rule list stays the one Compile planned - every rule, in declared
// order - and the merge node references those rules by exactly the index
// pkg/utils' rule report keys its per-rule marks and its triangular pair table
// by, rule i being bit 1<<i. Reorder, filter or renumber that list and the
// dead-rule and overlap diagnostics name the wrong rules, silently.
func TestMergeBitMatchesReportIndex(t *testing.T) {
	// Three rules of a check that reads no parameters: they all bind its one
	// scan, so what keeps the loader from refusing them as one rule said three
	// times is the two fields that do no work here - the case folding and the
	// subject a pattern is read from (see benchNameRuleSpecs).
	declared := []config.RuleSpec{
		{Name: "ascii-csv", Check: "HasOnlyASCII", Enabled: true, Include: []string{`\.csv$`}},
		{Name: "ascii-names", Check: "HasOnlyASCII", Enabled: true, IgnoreCase: true},
		{Name: "ascii-logs", Check: "HasOnlyASCII", Enabled: true, Subject: "path", Include: []string{`\.log$`}},
	}
	entry := planEntry(t, compileAnchored(t, anchoredConfig(declared)), "HasOnlyASCII", ScopeFile)
	if len(entry.Rules) != len(declared) {
		t.Fatalf("the entry holds %d rules, want the %d declared", len(entry.Rules), len(declared))
	}
	for i, spec := range declared {
		if entry.Rules[i].Rule != spec.Name {
			t.Fatalf("rule %d is %q, want %q - this list is what the rule report indexes by", i, entry.Rules[i].Rule, spec.Name)
		}
		if want := uint64(1) << i; entry.Rules[i].bit != want {
			t.Errorf("rule %q carries bit %b, want %b", spec.Name, entry.Rules[i].bit, want)
		}
	}
	// A check that reads no parameters binds one unit for all of them, so its
	// contributor mask is every rule's bit.
	units := entry.Batch.merged.units
	if len(units) != 1 {
		t.Fatalf("expected the three rules to bind one unit, got %d", len(units))
	}
	if want := uint64(1)<<len(declared) - 1; units[0].mask != want {
		t.Errorf("the merged unit's contributor mask is %b, want %b", units[0].mask, want)
	}
}

// TestPairScansDiffer pins the predicate the overlap notice is gated on: it
// answers from the merged units, so what decides a pair is the parameter sets
// its two rules bound and nothing else. The fixture binds "password" twice,
// "token" once and "shared" three times, and its last rule binds an empty
// keyword list - which the config surface accepts and which binds no unit at
// all.
func TestPairScansDiffer(t *testing.T) {
	set := func(keywords ...string) map[string]interface{} {
		return map[string]interface{}{"keywords": keywords, "info": "found"}
	}
	// The first three rules each share a parameter set with the other two, which
	// the loader refuses where their gates meet: the subject each reads its
	// patterns from - and, between the two that read the same one, the case
	// folding - is what keeps the three apart without touching the sets they
	// bind, the only thing this predicate answers from.
	entry := planEntry(t, compileAnchored(t, anchoredConfig([]config.RuleSpec{
		{Name: "csv-secrets", Check: "IsFreeOfKeywords", Enabled: true, Subject: "name", Include: []string{`\.csv$`},
			Params: []map[string]interface{}{set("password"), set("shared")}},
		{Name: "alpha-secrets", Check: "IsFreeOfKeywords", Enabled: true, Subject: "path", Include: []string{`^alpha`},
			Params: []map[string]interface{}{set("password"), set("shared")}},
		{Name: "alpha-tokens", Check: "IsFreeOfKeywords", Enabled: true, Subject: "name", IgnoreCase: true, Include: []string{`^alpha`},
			Params: []map[string]interface{}{set("token"), set("shared")}},
		{Name: "no-keywords", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`^beta`},
			Params: []map[string]interface{}{set()}},
	})), "IsFreeOfKeywords", ScopeFile)

	for _, tc := range []struct {
		name  string
		entry PlanEntry
		a, b  int
		want  bool
	}{
		// The node also holds "token", which only the third rule bound, and
		// "shared", which all three did: neither is a scan one of these two
		// drives without the other.
		{"the same sets are one scan", entry, 0, 1, false},
		{"different sets are two, whatever they share", entry, 0, 2, true},
		{"a rule that bound no unit drives no scan", entry, 0, 3, false},
		// A hand-built entry answers rather than panicking, and it answers the
		// way the unmerged repository scope does.
		{"no merge node", PlanEntry{Batch: &Batch{}}, 0, 1, true},
		{"no batch", PlanEntry{}, 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.PairScansDiffer(tc.a, tc.b); got != tc.want {
				t.Errorf("rules %d and %d: PairScansDiffer is %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestCompileRefusesMoreThan64RulesOfOneCheck pins the merge mask's edge: an
// entry's contributor bits are a uint64, so a 65th rule of one check in one
// scope is a LOAD ERROR rather than a second, untested pass on the
// acquisition's hot path. 64 still compiles, so what the load refuses is that
// boundary and not some smaller limit nobody stated.
func TestCompileRefusesMoreThan64RulesOfOneCheck(t *testing.T) {
	// Rules of one check that bind the same parameters where their gates meet
	// are refused, so each rule scans for a keyword of its own - which a
	// parameter-less check has no room for, its rules all binding its one scan.
	specs := func(n int) []config.RuleSpec {
		rules := make([]config.RuleSpec, 0, n)
		for i := 0; i < n; i++ {
			rules = append(rules, config.RuleSpec{
				Name:    fmt.Sprintf("keywords-%02d", i),
				Check:   "IsFreeOfKeywords",
				Enabled: true,
				Include: []string{fmt.Sprintf(`_%02d\.csv$`, i)},
				Params:  []map[string]interface{}{{"keywords": []string{fmt.Sprintf("kw_%02d", i)}, "info": "found"}},
			})
		}
		return rules
	}

	cfg := anchoredConfig(specs(64))
	if _, err := Compile(&cfg, NewRegistry()); err != nil {
		t.Fatalf("64 rules of one check must compile: %v", err)
	}
	cfg = anchoredConfig(specs(65))
	_, err := Compile(&cfg, NewRegistry())
	if err == nil {
		t.Fatal("a 65th rule of one check must refuse the load")
	}
	for _, want := range []string{`check "IsFreeOfKeywords"`, "65"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %s: %v", want, err)
		}
	}
}

// TestMergeKeepsKeylessUnitsApart covers the guard behind "every bind that
// emits a unit sets a key": two units that bound none are compared as nil ==
// nil, so folding them would silently run one check's scan in place of the
// other's. No configuration reaches this - the rules are assembled here, not
// compiled - which is exactly why the guard needs a pin of its own.
func TestMergeKeepsKeylessUnitsApart(t *testing.T) {
	keyless := func() *BoundRule { return &BoundRule{units: []unit{{}}} }
	node, err := mergeUnits([]*BoundRule{keyless(), keyless()})
	if err != nil {
		t.Fatalf("merge units: %v", err)
	}
	if len(node.units) != 2 {
		t.Fatalf("units that bound no key must not merge, got %d", len(node.units))
	}
	for i := range node.units {
		if want := uint64(1) << i; node.units[i].mask != want {
			t.Errorf("unit %d carries the contributor mask %b, want %b", i, node.units[i].mask, want)
		}
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
