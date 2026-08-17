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

// TestRuleSpecsAllowsSameParamsDifferentSelectors pins the other side: the same
// parameters over different files are two rules, not one said twice.
func TestRuleSpecsAllowsSameParamsDifferentSelectors(t *testing.T) {
	params := []map[string]interface{}{{"keywords": []string{"password"}, "info": "found"}}
	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "csv", Check: "IsFreeOfKeywords", Enabled: true, Scope: []string{"file"}, Include: []string{`\.csv$`}, Params: params},
		config.RuleSpec{Name: "txt", Check: "IsFreeOfKeywords", Enabled: true, Scope: []string{"file"}, Include: []string{`\.txt$`}, Params: params},
	)
	if err != nil {
		t.Fatalf("different selectors are different rules: %v", err)
	}
	if got := specsForCheck(specs, "IsFreeOfKeywords"); len(got) != 2 {
		t.Fatalf("both rules must be kept, got %d", len(got))
	}
}

// TestRuleSpecsAllowsSameSelectorsDifferentParams is its twin: the same files
// scanned for different things are two rules as well.
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

// TestRuleSpecsAllowsSubjectAgainstThePathDefault is its counterweight: where
// the scope set makes the default "path", a spelled-out subject = "name" is a
// DIFFERENT rule - it gates archive members by their base name rather than by
// their path - so the pair must load.
func TestRuleSpecsAllowsSubjectAgainstThePathDefault(t *testing.T) {
	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "by-path", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{"data/"}},
		config.RuleSpec{Name: "by-name", Check: "IsFreeOfKeywords", Enabled: true, Subject: "name", Include: []string{"data/"}},
	)
	if err != nil {
		t.Fatalf("a name subject is not the path default: %v", err)
	}
	if got := specsForCheck(specs, "IsFreeOfKeywords"); len(got) != 2 {
		t.Fatalf("both rules must be kept, got %d", len(got))
	}
}

// TestRuleSpecsAllowsSubjectPathAcrossScopeClasses pins the reading a rule
// serving BOTH scope classes has: IsFreeOfKeywords runs over files and over
// archive members, where an undeclared subject means the file's NAME and the
// member's PATH respectively. Spelling out subject = "path" therefore keeps the
// member gate and changes the file gate to the collection-relative path - two
// different rules, however identical the rest is, and a config saying both is
// valid. The path-only scope set of TestRuleSpecsRefusesTwinsAtThePathDefault is
// the same pair's other verdict: there nothing but the path is read, so the
// spelling is a spelling.
func TestRuleSpecsAllowsSubjectPathAcrossScopeClasses(t *testing.T) {
	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "default-subject", Check: "IsFreeOfKeywords", Enabled: true, Include: []string{"data/"}},
		config.RuleSpec{Name: "path-subject", Check: "IsFreeOfKeywords", Enabled: true, Subject: "path", Include: []string{"data/"}},
	)
	if err != nil {
		t.Fatalf("the two rules gate files differently, so both must load: %v", err)
	}
	if got := specsForCheck(specs, "IsFreeOfKeywords"); len(got) != 2 {
		t.Fatalf("both rules must be kept, got %d", len(got))
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

// TestRuleSpecsAllowsADisabledRuleBesideItsEnabledTwin is that refusal's
// counterweight, and the ordinary config it must not break: the off switch is a
// DIFFERENCE, so a rule parked with enabled = false beside the one that
// replaced it loads. Only one of the two is ever dispatched, so nothing is
// reported twice.
func TestRuleSpecsAllowsADisabledRuleBesideItsEnabledTwin(t *testing.T) {
	specs, err := assembleRuleSpecs(
		config.RuleSpec{Name: "parked", Check: "HasOnlyASCII", Include: []string{`\.csv$`}},
		config.RuleSpec{Name: "live", Check: "HasOnlyASCII", Enabled: true, Include: []string{`\.csv$`}},
	)
	if err != nil {
		t.Fatalf("a disabled rule beside its enabled twin does no work twice: %v", err)
	}
	if got := specsForCheck(specs, "HasOnlyASCII"); len(got) != 2 {
		t.Fatalf("both rules must be kept, got %d", len(got))
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

// TestRuleSpecsRefusesSiblingIncludeOfAnExclude pins the file-set contract: the
// files a check runs on are the CHECK's business, so a rule excluding what a
// sibling rule includes answers that one question twice and fails the load.
// Either declaration order says the same two things, so both are refused, and
// the message names the excluding rule first whichever came first in the file.
func TestRuleSpecsRefusesSiblingIncludeOfAnExclude(t *testing.T) {
	skips := config.RuleSpec{Name: "skip-csv", Check: "HasOnlyASCII", Enabled: true, Exclude: []string{`\.csv$`}}
	targets := config.RuleSpec{Name: "only-csv", Check: "HasOnlyASCII", Enabled: true, Include: []string{`\.csv$`}}
	cases := []struct {
		name  string
		rules []config.RuleSpec
	}{
		{"the excluding rule is declared first", []config.RuleSpec{skips, targets}},
		{"the including rule is declared first", []config.RuleSpec{targets, skips}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := assembleRuleSpecs(tc.rules...)
			if err == nil {
				t.Fatal("one rule's exclude of a pattern a sibling includes must be refused")
			}
			// The message must name both rules and the pattern they disagree
			// about; what it goes on to advise is not this test's business.
			if want := `check "HasOnlyASCII": rule "skip-csv" excludes "\\.csv$" while rule "only-csv" includes it`; !strings.Contains(err.Error(), want) {
				t.Errorf("error must name %q: %v", want, err)
			}
		})
	}
}

// TestRuleSpecsAllowsCrossScopeIncludeExclude is that refusal's counterweight
// and its false-positive guard: a pattern excluded at one scope and included at
// another addresses two different phases of the scan - the files a collection
// carries, and the members inside an archive - so the two rules disagree about
// nothing and the config loads.
func TestRuleSpecsAllowsCrossScopeIncludeExclude(t *testing.T) {
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

// TestRuleSpecsAllowsCrossSubjectIncludeExclude is the same counterweight one
// step down: rules of one scope whose patterns are matched against DIFFERENT
// strings - the collection-relative path and the base name - gate different
// file sets, so an exclude of one is no answer to the other's include and the
// config loads.
func TestRuleSpecsAllowsCrossSubjectIncludeExclude(t *testing.T) {
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

// TestRuleSpecsRefusesSiblingIncludeAtTheDefaultedSubject is that allowance's
// limit: the subject is read as the rest of the load reads it, so at these
// scopes an omitted subject and a spelled-out subject = "name" are ONE subject,
// and the pair contradicts itself exactly as two spelled-out ones would.
func TestRuleSpecsRefusesSiblingIncludeAtTheDefaultedSubject(t *testing.T) {
	_, err := assembleRuleSpecs(
		config.RuleSpec{Name: "skip-csv", Check: "HasOnlyASCII", Enabled: true, Exclude: []string{`\.csv$`}},
		config.RuleSpec{Name: "only-csv", Check: "HasOnlyASCII", Enabled: true, Subject: "name", Include: []string{`\.csv$`}},
	)
	if err == nil {
		t.Fatal("spelling out the subject both rules already read must not save a contradiction")
	}
	if want := `rule "skip-csv" excludes "\\.csv$" while rule "only-csv" includes it`; !strings.Contains(err.Error(), want) {
		t.Errorf("error must name %q: %v", want, err)
	}
}

// TestRuleSpecsComparesPatternsVerbatim pins the refusal's reach: it recognizes
// one rule's pattern in a sibling's list, not a pattern related to it. Nothing
// trims or anchors these strings between the decode and the selector compile,
// and whether two regexes overlap is undecidable in general, so a pattern that
// merely contains another - in either direction - is a different pattern and
// loads.
func TestRuleSpecsComparesPatternsVerbatim(t *testing.T) {
	cases := []struct {
		name             string
		exclude, include string
	}{
		{"the include contains the exclude", `\.csv$`, `^raw/.*\.csv$`},
		{"the exclude contains the include", `^raw/.*\.csv$`, `\.csv$`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			specs, err := assembleRuleSpecs(
				config.RuleSpec{Name: "skips", Check: "HasOnlyASCII", Enabled: true, Exclude: []string{tc.exclude}},
				config.RuleSpec{Name: "targets", Check: "HasOnlyASCII", Enabled: true, Include: []string{tc.include}},
			)
			if err != nil {
				t.Fatalf("two different patterns are no contradiction: %v", err)
			}
			if got := specsForCheck(specs, "HasOnlyASCII"); len(got) != 2 {
				t.Fatalf("both rules must be kept, got %d", len(got))
			}
		})
	}
}

// TestRuleSpecsRefusesAParkedContradiction pins that the off switch is no
// exemption here either: a rule parked with enabled = false still declares
// which files the check skips, and the load validates disabled rules
// deliberately, so the contradiction is refused now rather than on the day
// someone re-enables it.
func TestRuleSpecsRefusesAParkedContradiction(t *testing.T) {
	_, err := assembleRuleSpecs(
		config.RuleSpec{Name: "parked-skip", Check: "HasOnlyASCII", Exclude: []string{`\.csv$`}},
		config.RuleSpec{Name: "live-only", Check: "HasOnlyASCII", Enabled: true, Include: []string{`\.csv$`}},
	)
	if err == nil {
		t.Fatal("a contradiction a disabled rule takes part in must be refused too")
	}
	if want := `rule "parked-skip" excludes "\\.csv$" while rule "live-only" includes it`; !strings.Contains(err.Error(), want) {
		t.Errorf("error must name %q: %v", want, err)
	}
}

// TestRuleSpecsReportsEveryContradictionAtOnce pins the aggregation the rest of
// this file promises: a check that contradicts itself in two places is told
// about both, rather than one refusal per load.
func TestRuleSpecsReportsEveryContradictionAtOnce(t *testing.T) {
	_, err := assembleRuleSpecs(
		config.RuleSpec{Name: "skip-csv", Check: "HasOnlyASCII", Enabled: true, Exclude: []string{`\.csv$`}},
		config.RuleSpec{Name: "only-csv", Check: "HasOnlyASCII", Enabled: true, Include: []string{`\.csv$`}},
		config.RuleSpec{Name: "skip-raw", Check: "HasOnlyASCII", Enabled: true, Exclude: []string{"^raw/"}},
		config.RuleSpec{Name: "only-raw", Check: "HasOnlyASCII", Enabled: true, Include: []string{"^raw/"}},
	)
	if err == nil {
		t.Fatal("two contradicting pairs must be refused")
	}
	for _, want := range []string{
		`check "HasOnlyASCII": rule "skip-csv" excludes "\\.csv$" while rule "only-csv" includes it`,
		`check "HasOnlyASCII": rule "skip-raw" excludes "^raw/" while rule "only-raw" includes it`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q: %v", want, err)
		}
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
