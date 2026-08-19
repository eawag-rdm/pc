package checks

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
)

// ruleAnchors declares a [[rule]] for every check ruleSpecs refuses to leave
// undeclared (derived from AnchoredChecks, so fixtures cannot drift from the
// code), so a fixture that configures only the check under test stays a
// complete config.
func ruleAnchors() string {
	var doc strings.Builder
	for _, check := range AnchoredChecks() {
		doc.WriteString("[[rule]]\nname  = \"anchor-" + check + "\"\ncheck = \"" + check + "\"\n\n")
	}
	return doc.String()
}

// loadRuleSpecs drives doc through the REAL loader - LoadConfig, then ruleSpecs
// against the real registry - rather than hand-built specs, so a future
// non-data field on RuleSpec (a func, a pointer, a provenance field) cannot
// silently retire the duplicate refusal without a test noticing.
func loadRuleSpecs(t *testing.T, doc string) ([]config.RuleSpec, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pc.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return ruleSpecs(cfg, NewRegistry())
}

// assembleRuleSpecs runs ruleSpecs over hand-built specs, anchored on the SAME
// [[rule]] surface ruleAnchors declares: one anchor per anchored check the
// caller does not configure itself, so only the rules under test decide the
// verdict and no case has to reason about the legacy surface as well.
func assembleRuleSpecs(rules ...config.RuleSpec) ([]config.RuleSpec, error) {
	declared := func(check string) bool {
		for _, rule := range rules {
			if rule.Check == check {
				return true
			}
		}
		return false
	}
	var cfg config.Config
	for _, check := range AnchoredChecks() {
		if !declared(check) {
			cfg.Rules = append(cfg.Rules, config.RuleSpec{Name: "anchor-" + check, Check: check, Enabled: true})
		}
	}
	cfg.Rules = append(cfg.Rules, rules...)
	return ruleSpecs(&cfg, NewRegistry())
}

// specsForCheck returns the assembled specs of one check.
func specsForCheck(specs []config.RuleSpec, check string) []config.RuleSpec {
	var out []config.RuleSpec
	for _, spec := range specs {
		if spec.Check == check {
			out = append(out, spec)
		}
	}
	return out
}

// TestRuleSpecsRefusesIdenticalRules pins the refusal on the real loader: two
// rules of one check that differ only in their name do the same work twice and
// double every finding, so the config fails to load.
func TestRuleSpecsRefusesIdenticalRules(t *testing.T) {
	_, err := loadRuleSpecs(t, ruleAnchors()+`
[[rule]]
name    = "ascii-a"
check   = "HasOnlyASCII"
include = ["\\.csv$"]

[[rule]]
name    = "ascii-b"
check   = "HasOnlyASCII"
include = ["\\.csv$"]
`)
	if err == nil {
		t.Fatal("two rules differing only in their name must be refused")
	}
	// The message must say WHICH rule repeats which; how it goes on to describe
	// the comparison is not this test's business.
	if want := `rule "ascii-b": resolves to the same rule as "ascii-a"`; !strings.Contains(err.Error(), want) {
		t.Errorf("error must name %q: %v", want, err)
	}
}

// TestRuleSpecsNormalizesBeforeCompare pins the no-op config edits that would
// evade a raw compare: spelling out a default subject, a scope declared empty
// or spelled out in full, an empty list where the key could have been omitted.
// None of them is a difference.
//
// Each twin is written in TOML and read by the REAL decoder, because the shape
// an empty spelling decodes to is exactly what these cases are about: a
// hand-built []string{} only asserts what the test author believes the decoder
// produces, and that belief is already wrong for an empty [rule.params] table
// (one empty parameter set, not none - which is why it is not among the cases
// below).
func TestRuleSpecsNormalizesBeforeCompare(t *testing.T) {
	// HasOnlyASCII serves the file and archive-file-list scopes, so an omitted
	// subject means "name" there and the spelled-out one is the same subject.
	cases := []struct {
		name string
		key  string
	}{
		{"declared subject equals the default", `subject = "name"`},
		{"empty scope equals the check's own scopes", `scope   = []`},
		{"a spelled-out scope equals the check's own scopes", `scope   = ["file", "archive-file-list"]`},
		{"empty include equals no include", `include = []`},
		{"empty exclude equals no exclude", `exclude = []`},
		{"an empty params array equals no params", `params  = []`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadRuleSpecs(t, ruleAnchors()+`
[[rule]]
name    = "a"
check   = "HasOnlyASCII"

[[rule]]
name    = "b"
check   = "HasOnlyASCII"
`+tc.key+"\n")
			if err == nil {
				t.Fatal("a cosmetic difference must not save a duplicate rule")
			}
			if !strings.Contains(err.Error(), `rule "b": resolves to the same rule as "a"`) {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestRuleSpecsIgnoresScopeDeclarationOrder pins the other half of the scope
// normalization: a rule serves a SET of scopes, so the same set written in two
// orders is one rule said twice.
func TestRuleSpecsIgnoresScopeDeclarationOrder(t *testing.T) {
	_, err := assembleRuleSpecs(
		config.RuleSpec{Name: "a", Check: "HasOnlyASCII", Enabled: true, Scope: []string{"file", "archive-file-list"}},
		config.RuleSpec{Name: "b", Check: "HasOnlyASCII", Enabled: true, Scope: []string{"archive-file-list", "file"}},
	)
	if err == nil {
		t.Fatal("the same scope set in another order must not save a duplicate rule")
	}
	if !strings.Contains(err.Error(), `rule "b": resolves to the same rule as "a"`) {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRuleSpecsAllowsSameSelectorsDifferentParams pins the other side: the same
// files scanned for different things are two rules, not one said twice.
func TestRuleSpecsAllowsSameSelectorsDifferentParams(t *testing.T) {
	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "credentials", Check: "IsFreeOfKeywords", Enabled: true, Scope: []string{"file"}, Include: []string{`\.csv$`},
			Params: []map[string]interface{}{{"keywords": []string{"password"}, "info": "credential keyword"}}},
		config.RuleSpec{Name: "internals", Check: "IsFreeOfKeywords", Enabled: true, Scope: []string{"file"}, Include: []string{`\.csv$`},
			Params: []map[string]interface{}{{"keywords": []string{"Q:"}, "info": "internal keyword"}}},
	)
	if err != nil {
		t.Fatalf("different parameters are different rules: %v", err)
	}
	if got := specsForCheck(specs, "IsFreeOfKeywords"); len(got) != 2 {
		t.Fatalf("both rules must be kept, got %d", len(got))
	}
}

// TestRuleSpecsRefusesTwinsAtThePathDefault pins the subject default in the
// direction that hides a twin: at the archive-member scope an UNDECLARED
// subject is "path", so an omitted subject and a spelled-out subject = "path"
// compile to the same selector and are one rule under two names.
func TestRuleSpecsRefusesTwinsAtThePathDefault(t *testing.T) {
	_, err := assembleRuleSpecs(
		config.RuleSpec{Name: "members", Check: "IsFreeOfKeywords", Enabled: true, Scope: []string{"archive-member"}, Include: []string{"data/"}},
		config.RuleSpec{Name: "paths", Check: "IsFreeOfKeywords", Enabled: true, Scope: []string{"archive-member"}, Subject: "path", Include: []string{"data/"}},
	)
	if err == nil {
		t.Fatal("spelling out the subject the scopes already read must not save a duplicate rule")
	}
	if !strings.Contains(err.Error(), `rule "paths": resolves to the same rule as "members"`) {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRuleSpecsRefusesDisabledTwins pins that the off switch is no exemption: a
// disabled twin does no work today, but the load validates disabled rules
// deliberately, so the duplication is caught now rather than on the day someone
// re-enables both.
func TestRuleSpecsRefusesDisabledTwins(t *testing.T) {
	_, err := assembleRuleSpecs(
		config.RuleSpec{Name: "off-a", Check: "HasOnlyASCII", Include: []string{`\.csv$`}},
		config.RuleSpec{Name: "off-b", Check: "HasOnlyASCII", Include: []string{`\.csv$`}},
	)
	if err == nil {
		t.Fatal("two identical disabled rules must be refused too")
	}
	if !strings.Contains(err.Error(), `rule "off-b": resolves to the same rule as "off-a"`) {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRuleSpecsReportsEveryDuplicateGroup pins the aggregation across groups: a
// config that says two things twice is told about both, each error naming the
// rule its group repeats, rather than one refusal per load.
func TestRuleSpecsReportsEveryDuplicateGroup(t *testing.T) {
	_, err := loadRuleSpecs(t, ruleAnchors()+`
[[rule]]
name    = "csv-a"
check   = "HasOnlyASCII"
include = ["\\.csv$"]

[[rule]]
name    = "csv-b"
check   = "HasOnlyASCII"
include = ["\\.csv$"]

[[rule]]
name    = "txt-a"
check   = "HasOnlyASCII"
include = ["\\.txt$"]

[[rule]]
name    = "txt-b"
check   = "HasOnlyASCII"
include = ["\\.txt$"]
`)
	if err == nil {
		t.Fatal("two duplicate groups must be refused")
	}
	for _, want := range []string{`rule "csv-b": resolves to the same rule as "csv-a"`, `rule "txt-b": resolves to the same rule as "txt-a"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q: %v", want, err)
		}
	}
}

// TestRuleSpecsReportsEveryCopyAgainstTheFirst pins how a group of MORE than two
// copies is reported: each copy is named once, against the one original it
// repeats, rather than against every earlier copy as well. Three copies are
// three rules to delete two of, not three pairs to read.
func TestRuleSpecsReportsEveryCopyAgainstTheFirst(t *testing.T) {
	_, err := loadRuleSpecs(t, ruleAnchors()+`
[[rule]]
name    = "csv-a"
check   = "HasOnlyASCII"
include = ["\\.csv$"]

[[rule]]
name    = "csv-b"
check   = "HasOnlyASCII"
include = ["\\.csv$"]

[[rule]]
name    = "csv-c"
check   = "HasOnlyASCII"
include = ["\\.csv$"]
`)
	if err == nil {
		t.Fatal("three copies of one rule must be refused")
	}
	if got := strings.Count(err.Error(), "resolves to the same rule as"); got != 2 {
		t.Errorf("three copies must be reported twice, got %d: %v", got, err)
	}
	for _, want := range []string{`rule "csv-b": resolves to the same rule as "csv-a"`, `rule "csv-c": resolves to the same rule as "csv-a"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q: %v", want, err)
		}
	}
}

// TestRuleSpecsReportsFaultsBesideTwins pins that a twin does not swallow its
// check's remaining rules: they are assembled and validated anyway, so the
// operator is told every fault of that check at once instead of finding the
// next one only after removing the twin and loading again.
func TestRuleSpecsReportsFaultsBesideTwins(t *testing.T) {
	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "toc-a", Check: "ReadMeContainsTOC", Enabled: true},
		config.RuleSpec{Name: "toc-b", Check: "ReadMeContainsTOC", Enabled: true},
		config.RuleSpec{Name: "toc-c", Check: "ReadMeContainsTOC", Enabled: true,
			Params: []map[string]interface{}{{"readme_names": []string{"readme.md"}}}},
	)
	if err == nil {
		t.Fatal("the twin pair must be refused")
	}
	// The sibling's own fault is whatever the load makes of its parameters; all
	// this case asks is that the sibling was judged at all, so it matches the
	// rule NAMES rather than either verdict's wording.
	for _, want := range []string{`rule "toc-b": resolves to the same rule as "toc-a"`, `rule "toc-c"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q: %v", want, err)
		}
	}
	if got := specsForCheck(specs, "ReadMeContainsTOC"); len(got) != 3 {
		t.Errorf("the check's rules must be assembled despite the twin, got %d", len(got))
	}
}

// TestRuleSpecsReportsFaultsBesideASecondReadmeRule pins the same aggregation
// around the readme's own refusal: a second HasReadme rule is refused - "what
// counts as the readme" has one definition - and the pair is still assembled,
// so every other fault of those rules is reported in the same load rather than
// one refusal per load.
func TestRuleSpecsReportsFaultsBesideASecondReadmeRule(t *testing.T) {
	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "readme-one", Check: "HasReadme", Enabled: true},
		config.RuleSpec{Name: "readme-two", Check: "HasReadme", Enabled: true},
	)
	if err == nil {
		t.Fatal("a second HasReadme rule must be refused")
	}
	if want := `check "HasReadme" allows exactly one rule`; !strings.Contains(err.Error(), want) {
		t.Errorf("error must name %q: %v", want, err)
	}
	// Assembly is what carries the pair on to the parameter binding and the
	// selector compilation, which report the faults ruleSpecs cannot see.
	if got := specsForCheck(specs, "HasReadme"); len(got) != 2 {
		t.Errorf("both readme rules must be assembled, got %d", len(got))
	}
}

// TestRuleSpecsSharesNoNamesFromAmbiguousReadmeRules pins what the refused pair
// must NOT do: two enabled HasReadme rules define the readme twice, so NEITHER
// is elected and the TOC check keeps its own default names. Both rules carry
// parameters AND a file filter - everything a synthesized TOC inherits from the
// readme rule - so electing either of them shows up here. What it guards is the
// election alone: dropping the pair from the assembly instead would satisfy it
// too, because unassembled rules elect nothing either.
func TestRuleSpecsSharesNoNamesFromAmbiguousReadmeRules(t *testing.T) {
	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "readme-one", Check: "HasReadme", Enabled: true,
			Include: []string{"^one/"}, Exclude: []string{"^one/draft/"},
			Subject: "path", IgnoreCase: true,
			Params: []map[string]interface{}{{"readme_names": []string{"one.md"}}}},
		config.RuleSpec{Name: "readme-two", Check: "HasReadme", Enabled: true,
			Include: []string{"^two/"}, Exclude: []string{"^two/draft/"},
			Subject: "name", IgnoreCase: true,
			Params: []map[string]interface{}{{"readme_names": []string{"two.md"}}}},
	)
	if err == nil {
		t.Fatal("a second HasReadme rule must be refused")
	}
	if want := `check "HasReadme" allows exactly one rule`; !strings.Contains(err.Error(), want) {
		t.Errorf("error must name %q: %v", want, err)
	}
	toc := specsForCheck(specs, "ReadMeContainsTOC")
	if len(toc) != 1 {
		t.Fatalf("the TOC check must synthesize its default rule, got %d", len(toc))
	}
	if len(toc[0].Params) > 0 {
		t.Errorf("rule %q inherited an ambiguous readme definition: %v", toc[0].Name, toc[0].Params)
	}
	if len(toc[0].Include) > 0 || len(toc[0].Exclude) > 0 || toc[0].Subject != "" || toc[0].IgnoreCase {
		t.Errorf("rule %q inherited an ambiguous readme rule's filter: %+v", toc[0].Name, toc[0])
	}
}

// TestRuleSpecsRefusesSiblingsBindingOneParameterSet pins the parameter-set
// contract: a [[rule]] block buys a parameter set, not a scan of its own, so
// two rules of one check binding the same parameters where they gate the same
// strings are one scan under two names and fail the load. What their selectors
// say is no part of that verdict - the cases below say it in the shapes the
// config surface has, down to a rule excluding the very pattern its sibling
// includes.
func TestRuleSpecsRefusesSiblingsBindingOneParameterSet(t *testing.T) {
	keywords := []map[string]any{{"keywords": []string{"password"}, "info": "found"}}
	cases := []struct {
		name  string
		rules []config.RuleSpec
		want  string
	}{
		{
			"one rule excludes what the other includes",
			[]config.RuleSpec{
				{Name: "skip-csv", Check: "IsFreeOfKeywords", Enabled: true, Exclude: []string{`\.csv$`}, Params: keywords},
				{Name: "only-csv", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.csv$`}, Params: keywords},
			},
			`rules "skip-csv" and "only-csv"`,
		},
		{
			"a rule filtering nothing beside one that excludes",
			[]config.RuleSpec{
				{Name: "every-file", Check: "IsFreeOfKeywords", Enabled: true, Params: keywords},
				{Name: "skip-csv", Check: "IsFreeOfKeywords", Enabled: true, Exclude: []string{`\.csv$`}, Params: keywords},
			},
			`rules "every-file" and "skip-csv"`,
		},
		{
			"two includes of different patterns",
			[]config.RuleSpec{
				{Name: "csv", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.csv$`}, Params: keywords},
				{Name: "txt", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.txt$`}, Params: keywords},
			},
			`rules "csv" and "txt"`,
		},
		{
			// The repeated [[rule.params]] form declares several sets, and
			// sharing ONE of them is the scan both rules drive.
			"one shared group among several",
			[]config.RuleSpec{
				{Name: "credentials", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.csv$`},
					Params: []map[string]any{{"keywords": []string{"password"}, "info": "found"}, {"keywords": []string{"Q:"}, "info": "internal"}}},
				{Name: "internals", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.txt$`},
					Params: []map[string]any{{"keywords": []string{"Q:"}, "info": "internal"}, {"keywords": []string{"token"}, "info": "token"}}},
			},
			`rules "credentials" and "internals"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := assembleRuleSpecs(tc.rules...)
			if err == nil {
				t.Fatal("two rules binding one parameter set where they gate the same strings must be refused")
			}
			// The message must name both rules and what they share; what it goes
			// on to advise is not this test's business.
			want := tc.want + " bind the same parameters at the same scope"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must name %q: %v", want, err)
			}
		})
	}
}

// TestRuleSpecsRefusesDecodedSiblingsSharingAGroup drives that refusal through
// the REAL decoder, because "the same parameters" is a deep compare of the maps
// a [[rule.params]] table decodes to: a hand-built pair only asserts that two
// values the test author wrote are equal, and would keep passing after a decode
// change made two identical tables decode to maps that no longer compare equal.
func TestRuleSpecsRefusesDecodedSiblingsSharingAGroup(t *testing.T) {
	_, err := loadRuleSpecs(t, ruleAnchors()+`
[[rule]]
name    = "csv-credentials"
check   = "IsFreeOfKeywords"
include = ["\\.csv$"]
  [[rule.params]]
  keywords = ["password"]
  info     = "credential keyword"

[[rule]]
name    = "txt-credentials"
check   = "IsFreeOfKeywords"
include = ["\\.txt$"]
  [[rule.params]]
  keywords = ["password"]
  info     = "credential keyword"
`)
	if err == nil {
		t.Fatal("two decoded rules declaring one parameter group must be refused")
	}
	if want := `rules "csv-credentials" and "txt-credentials" bind the same parameters at the same scope`; !strings.Contains(err.Error(), want) {
		t.Errorf("error must name %q: %v", want, err)
	}
}

// TestRuleSpecsAllowsSiblingsBindingDifferentParameters is that refusal's
// counterweight and its false-positive guard: rules binding DIFFERENT
// parameters - a group neither shares with the other, or a group beside no
// group at all - are different scans, each carrying the file set it was written
// for, so their selectors may say anything about each other, including one rule
// excluding the very pattern the other includes.
func TestRuleSpecsAllowsSiblingsBindingDifferentParameters(t *testing.T) {
	cases := []struct {
		name  string
		rules []config.RuleSpec
	}{
		{
			"one rule excludes the pattern the other includes",
			[]config.RuleSpec{
				{Name: "skip-csv", Check: "IsFreeOfKeywords", Enabled: true, Exclude: []string{`\.csv$`},
					Params: []map[string]any{{"keywords": []string{"secret"}, "info": "found"}}},
				{Name: "only-csv", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.csv$`},
					Params: []map[string]any{{"keywords": []string{"Q:"}, "info": "found"}}},
			},
		},
		{
			// The groups are compared as WRITTEN, not as a scan reads them: a
			// field the two spell differently is two parameter sets, whatever
			// the compiled scans end up sharing.
			"the same keywords under a different info",
			[]config.RuleSpec{
				{Name: "credentials", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.csv$`},
					Params: []map[string]any{{"keywords": []string{"password"}, "info": "credential keyword"}}},
				{Name: "internals", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.txt$`},
					Params: []map[string]any{{"keywords": []string{"password"}, "info": "internal keyword"}}},
			},
		},
		{
			// "No group at all" is one side's answer, not a set the other can
			// share: the pair below gates the very same files under the very
			// same folding, and only the check taking NO parameters would make
			// the two of them one scan.
			"a rule declaring no group beside one that declares one",
			[]config.RuleSpec{
				{Name: "credentials", Check: "IsFreeOfKeywords", Enabled: true, Scope: []string{"file"}, Include: []string{`\.csv$`},
					Params: []map[string]any{{"keywords": []string{"password"}, "info": "credential keyword"}}},
				{Name: "no-parameters", Check: "IsFreeOfKeywords", Enabled: true, Scope: []string{"file"}, Include: []string{`\.csv$`}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			specs, err := assembleRuleSpecs(tc.rules...)
			if err != nil {
				t.Fatalf("rules binding different parameters are different scans: %v", err)
			}
			if got := specsForCheck(specs, "IsFreeOfKeywords"); len(got) != 2 {
				t.Fatalf("both rules must be kept, got %d", len(got))
			}
		})
	}
}

// TestRuleSpecsAllowsSiblingsFoldingCaseDifferently pins ignoreCase as part of
// what the pair binds: it folds every pattern of a rule alike, so the two file
// sets below cannot be had from one rule however their pattern lists are
// merged, and the parameters they share - a keyword set, or the empty set of a
// check taking none - are no answer about them.
func TestRuleSpecsAllowsSiblingsFoldingCaseDifferently(t *testing.T) {
	cases := []struct {
		name  string
		check string
		rules []config.RuleSpec
	}{
		{
			"the same keywords folded differently",
			"IsFreeOfKeywords",
			[]config.RuleSpec{
				{Name: "folded", Check: "IsFreeOfKeywords", Enabled: true, IgnoreCase: true, Include: []string{`\.csv$`},
					Params: []map[string]any{{"keywords": []string{"password"}, "info": "found"}}},
				{Name: "exact", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.CSV$`},
					Params: []map[string]any{{"keywords": []string{"password"}, "info": "found"}}},
			},
		},
		{
			"a check taking no parameters folded differently",
			"HasOnlyASCII",
			[]config.RuleSpec{
				{Name: "folded", Check: "HasOnlyASCII", Enabled: true, IgnoreCase: true, Include: []string{`\.csv$`}},
				{Name: "exact", Check: "HasOnlyASCII", Enabled: true, Include: []string{`\.CSV$`}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			specs, err := assembleRuleSpecs(tc.rules...)
			if err != nil {
				t.Fatalf("rules folding case differently gate different files: %v", err)
			}
			if got := specsForCheck(specs, tc.check); len(got) != 2 {
				t.Fatalf("both rules must be kept, got %d", len(got))
			}
		})
	}
}

// TestRuleSpecsAllowsSharedParamsAcrossScopes is the refusal's other limit: two
// rules binding the same (empty) parameter set gate different phases of the
// scan - the files a collection carries, and the members inside an archive - so
// they never meet and the config loads, patterns and all.
func TestRuleSpecsAllowsSharedParamsAcrossScopes(t *testing.T) {
	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "files", Check: "IsFreeOfKeywords", Enabled: true, Scope: []string{"file"}, Exclude: []string{"^data/"}},
		config.RuleSpec{Name: "members", Check: "IsFreeOfKeywords", Enabled: true, Scope: []string{"archive-member"}, Include: []string{"^data/"}},
	)
	if err != nil {
		t.Fatalf("rules of different scopes address different phases: %v", err)
	}
	if got := specsForCheck(specs, "IsFreeOfKeywords"); len(got) != 2 {
		t.Fatalf("both rules must be kept, got %d", len(got))
	}
}

// TestRuleSpecsAllowsSharedParamsAcrossSubjects is the same limit one step
// down: rules of one scope whose patterns are matched against DIFFERENT strings
// - the collection-relative path and the base name - gate different file sets,
// so the parameter set they share is no answer about them and the config loads.
func TestRuleSpecsAllowsSharedParamsAcrossSubjects(t *testing.T) {
	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "skip-raw-paths", Check: "HasOnlyASCII", Enabled: true, Subject: "path", Exclude: []string{"^raw/"}},
		config.RuleSpec{Name: "only-raw-names", Check: "HasOnlyASCII", Enabled: true, Subject: "name", Include: []string{"^raw/"}},
	)
	if err != nil {
		t.Fatalf("a path pattern and a name pattern address different strings: %v", err)
	}
	if got := specsForCheck(specs, "HasOnlyASCII"); len(got) != 2 {
		t.Fatalf("both rules must be kept, got %d", len(got))
	}
}

// TestRuleSpecsRefusesSiblingsMeetingAtOneScope pins how far "the same strings"
// reaches: a check serving both scope classes reads its rules' subjects once
// per class, and ONE class they read alike is enough. Each pair below differs
// in the other class, and the parameter set both bind - the empty one - is
// still bound twice where they meet.
func TestRuleSpecsRefusesSiblingsMeetingAtOneScope(t *testing.T) {
	cases := []struct {
		name  string
		rules []config.RuleSpec
		want  string
	}{
		{
			// At the file scope an undeclared subject means the name, which is
			// what the sibling spells out; at the archive-member scope the first
			// rule reads the path instead.
			"they meet where an undeclared subject means the name",
			[]config.RuleSpec{
				{Name: "by-path", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{"data/"}},
				{Name: "by-name", Check: "IsFreeOfKeywords", Enabled: true, Subject: "name", Include: []string{"data/"}},
			},
			`rules "by-path" and "by-name"`,
		},
		{
			// The mirror image: the two read different strings at the file
			// scope, and the member path both read is where they meet.
			"they meet where an undeclared subject means the path",
			[]config.RuleSpec{
				{Name: "default-subject", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{"data/"}},
				{Name: "path-subject", Check: "IsFreeOfKeywords", Enabled: true, Subject: "path", Include: []string{"data/"}},
			},
			`rules "default-subject" and "path-subject"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := assembleRuleSpecs(tc.rules...)
			if err == nil {
				t.Fatal("one scope read alike is enough to make the pair one scan there")
			}
			want := tc.want + " both drive its only scan"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must name %q: %v", want, err)
			}
		})
	}
}

// TestRuleSpecsRefusesParameterlessSiblingsAtTheDefaultedSubject pins the
// verdict on a check that takes NO parameters: it has one scan, so every rule
// of it binds the same set and two of them are refused wherever their gates
// meet - here through the subject the rest of the load defaults to, which makes
// an omitted subject and a spelled-out subject = "name" ONE reading.
func TestRuleSpecsRefusesParameterlessSiblingsAtTheDefaultedSubject(t *testing.T) {
	_, err := assembleRuleSpecs(
		config.RuleSpec{Name: "skip-csv", Check: "HasOnlyASCII", Enabled: true, Exclude: []string{`\.csv$`}},
		config.RuleSpec{Name: "only-csv", Check: "HasOnlyASCII", Enabled: true, Subject: "name", Include: []string{`\.csv$`}},
	)
	if err == nil {
		t.Fatal("two rules of a check taking no parameters bind its one scan twice")
	}
	if want := `rules "skip-csv" and "only-csv" both drive its only scan (no parameters declared)`; !strings.Contains(err.Error(), want) {
		t.Errorf("error must name %q: %v", want, err)
	}
}

// TestRuleSpecsRefusesARepeatedParamsGroup pins the same contract inside ONE
// rule: a [[rule.params]] group declared twice buys that rule the second scan a
// sibling would have bought it. Two DIFFERENT groups are the repeated form
// doing its job, so they load.
func TestRuleSpecsRefusesARepeatedParamsGroup(t *testing.T) {
	_, err := assembleRuleSpecs(
		config.RuleSpec{Name: "twice", Check: "IsFreeOfKeywords", Enabled: true,
			Params: []map[string]any{
				{"keywords": []string{"password"}, "info": "found"},
				{"keywords": []string{"password"}, "info": "found"},
			}},
	)
	if err == nil {
		t.Fatal("one rule declaring the same parameter group twice must be refused")
	}
	if want := `rule "twice" declares [[rule.params]] group 1 again as group 2`; !strings.Contains(err.Error(), want) {
		t.Errorf("error must name %q: %v", want, err)
	}

	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "both", Check: "IsFreeOfKeywords", Enabled: true,
			Params: []map[string]any{
				{"keywords": []string{"password"}, "info": "found"},
				{"keywords": []string{"Q:"}, "info": "found"},
			}},
	)
	if err != nil {
		t.Fatalf("two different groups are what the repeated form is for: %v", err)
	}
	if got := specsForCheck(specs, "IsFreeOfKeywords"); len(got) != 1 {
		t.Fatalf("the rule must be kept, got %d", len(got))
	}
}

// TestRuleSpecsRefusesAParkedDuplicate pins that the off switch is no exemption
// here either: a rule parked with enabled = false still binds its parameters,
// and the load validates disabled rules deliberately, so the duplicate is
// refused now rather than on the day someone re-enables it.
func TestRuleSpecsRefusesAParkedDuplicate(t *testing.T) {
	keywords := []map[string]any{{"keywords": []string{"password"}, "info": "found"}}
	_, err := assembleRuleSpecs(
		config.RuleSpec{Name: "parked-skip", Check: "IsFreeOfKeywords", Exclude: []string{`\.csv$`}, Params: keywords},
		config.RuleSpec{Name: "live-only", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.csv$`}, Params: keywords},
	)
	if err == nil {
		t.Fatal("a duplicate a disabled rule takes part in must be refused too")
	}
	if want := `rules "parked-skip" and "live-only" bind the same parameters at the same scope`; !strings.Contains(err.Error(), want) {
		t.Errorf("error must name %q: %v", want, err)
	}
}

// TestRuleSpecsReportsEveryDuplicatePairAtOnce pins the aggregation the rest of
// this file promises: a check binding two of its parameter sets twice is told
// about both pairs, rather than one refusal per load. The four rules make six
// pairs, of which only these two share a parameter set, so the count is the
// over-reporting guard as well.
func TestRuleSpecsReportsEveryDuplicatePairAtOnce(t *testing.T) {
	credentials := []map[string]any{{"keywords": []string{"password"}, "info": "found"}}
	internals := []map[string]any{{"keywords": []string{"Q:"}, "info": "found"}}
	_, err := assembleRuleSpecs(
		config.RuleSpec{Name: "csv-credentials", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.csv$`}, Params: credentials},
		config.RuleSpec{Name: "txt-credentials", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.txt$`}, Params: credentials},
		config.RuleSpec{Name: "csv-internals", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.csv$`}, Params: internals},
		config.RuleSpec{Name: "txt-internals", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{`\.txt$`}, Params: internals},
	)
	if err == nil {
		t.Fatal("two duplicate pairs must be refused")
	}
	for _, want := range []string{
		`check "IsFreeOfKeywords": rules "csv-credentials" and "txt-credentials" bind the same parameters at the same scope`,
		`check "IsFreeOfKeywords": rules "csv-internals" and "txt-internals" bind the same parameters at the same scope`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q: %v", want, err)
		}
	}
	if got := strings.Count(err.Error(), "bind the same parameters"); got != 2 {
		t.Errorf("only the two pairs sharing a parameter set may be reported, got %d: %v", got, err)
	}
}

// TestRuleSpecFieldNamesAreDecidedAbout is the tripwire on ruleIdentity, which
// names the fields it compares and therefore cannot notice a new one by itself:
// a field added to config.RuleSpec has to be decided about - does it make two
// rules different? a loader-set provenance field, distinct per rule, would
// retire the refusal outright - and then listed here. The declaration ORDER is
// not the contract, the set of names is.
func TestRuleSpecFieldNamesAreDecidedAbout(t *testing.T) {
	want := []string{"Check", "Enabled", "Exclude", "IgnoreCase", "Include", "Name", "Params", "Scope", "Subject"}
	specType := reflect.TypeOf(config.RuleSpec{})
	got := make([]string, 0, specType.NumField())
	for i := 0; i < specType.NumField(); i++ {
		got = append(got, specType.Field(i).Name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("config.RuleSpec fields are %v, want %v: decide whether the new one belongs in ruleIdentity", got, want)
	}
}
