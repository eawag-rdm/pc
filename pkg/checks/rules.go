package checks

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/selector"
)

// This file owns the path from declared rules to bindable specs and compiled
// selectors. It lives HERE, next to the registry, so utils.Compile and the
// checks tests translate and compile through the SAME code and cannot drift
// apart (utils imports checks; checks must not import utils). The legacy
// [test.X] translation inside it is sugar for one more release and dies with
// that surface.

// anchoredChecks must each be DECLARED on one surface or the load fails - the
// contract the old [test.X] validation enforced: these checks' parameters are
// load-bearing enough that a config silent about them is a mistake, not a
// default. Every other check synthesizes a default rule.
var anchoredChecks = []string{"IsFreeOfKeywords", "IsValidName", "HasReadme"}

// AnchoredChecks returns, as a copy, the checks that must be explicitly
// configured - declared on one surface - for a config to load. Fixtures derive
// their anchor declarations from it, so adding an anchor cannot desynchronise
// them.
func AnchoredChecks() []string {
	return append([]string(nil), anchoredChecks...)
}

// RuleSpecs assembles the rule specs one config declares, in the registry's
// DECLARED order by check - that order is the order the dispatch runs a file's
// checks in, and therefore the order findings are rendered in. Per check: its
// [[rule]] sections in config order, else its translated legacy [test.X]
// section, else - anchoredChecks excepted - a synthesized default rule with an
// empty selector and the parameters its own Bind produces for the zero spec;
// without that, assembly would silently delete every check no config names.
// Errors are aggregated into one joined error, not short-circuited.
func RuleSpecs(cfg *config.Config, reg Registry) ([]config.RuleSpec, error) {
	var errs []error
	byCheck := make(map[string][]config.RuleSpec, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if _, known := reg.Lookup(rule.Check); !known {
			errs = append(errs, fmt.Errorf("rule %q: unknown check %q", rule.Name, rule.Check))
			continue
		}
		byCheck[rule.Check] = append(byCheck[rule.Check], rule)
	}
	orphans := make([]string, 0, len(cfg.Tests))
	for name := range cfg.Tests {
		if _, known := reg.Lookup(name); !known {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans) // map order is random; error order must not be
	for _, name := range orphans {
		errs = append(errs, fmt.Errorf("config section [test.%s] names no known check", name))
	}

	specs := make([]config.RuleSpec, 0, reg.Len()+len(cfg.Rules))
	tocSynthesized := false
	for _, def := range reg.Defs() {
		rules, present := byCheck[def.Name]
		section := cfg.Tests[def.Name]
		switch {
		case present && section != nil:
			// ParseConfig refuses the mix already; a hand-built config gets the
			// same verdict here rather than a silent surface preference.
			errs = append(errs, fmt.Errorf("check %q is configured by both [[rule]] and [test.%s]: use one surface per check", def.Name, def.Name))
		case present:
			// "What counts as the readme" must have exactly one definition;
			// a second HasReadme rule would declare a second one.
			if def.Name == "HasReadme" && len(rules) > 1 {
				errs = append(errs, fmt.Errorf("check %q allows exactly one rule (it defines what counts as the readme), got %d", def.Name, len(rules)))
				continue
			}
			specs = append(specs, rules...)
		case section != nil:
			specs = append(specs, legacySectionSpec(def.Name, section))
		case slices.Contains(anchoredChecks, def.Name):
			errs = append(errs, fmt.Errorf("check %q is not configured: declare a [[rule]] for it (or a legacy [test.%s] section)", def.Name, def.Name))
		default:
			if def.Name == "ReadMeContainsTOC" {
				tocSynthesized = true
			}
			specs = append(specs, defaultRuleSpec(def.Name))
		}
	}
	errs = append(errs, shareReadmeNames(specs, tocSynthesized)...)
	return specs, errors.Join(errs...)
}

// shareReadmeNames keeps "what counts as the readme" single-sourced: the one
// ENABLED HasReadme rule defines it (RuleSpecs refuses a second, and a
// disabled one contributes nothing - the TOC check then falls back to the
// built-in default names), and every ReadMeContainsTOC spec reads that rule's
// parameters. Declaring parameters on a TOC [[rule]] is refused, so the two
// checks can never disagree; a legacy TOC section's own keywordArguments were
// always ignored in favour of the readme's list and keep being dropped. The
// inherited params carry their surface's Legacy flag with them, so a lenient
// legacy list never gets the [[rule]] surface's strict key check (whose faults
// would be misattributed to the TOC rule). A synthesized TOC - no surface of
// its own - additionally takes the readme rule's file filter, exactly as the
// [test.*] surface always behaved; a declared one keeps its own.
func shareReadmeNames(specs []config.RuleSpec, tocSynthesized bool) []error {
	var errs []error
	var readme *config.RuleSpec
	for i := range specs {
		if specs[i].Check == "HasReadme" && specs[i].Enabled {
			readme = &specs[i]
			break
		}
	}
	for i := range specs {
		spec := &specs[i]
		if spec.Check != "ReadMeContainsTOC" {
			continue
		}
		if len(spec.Params) > 0 {
			if !spec.Legacy {
				errs = append(errs, fmt.Errorf("rule %q: ReadMeContainsTOC takes no parameters (what counts as the readme is the HasReadme rule's readme_names)", spec.Name))
				continue
			}
			spec.Params = nil
		}
		if readme == nil {
			continue
		}
		spec.Params, spec.Legacy = readme.Params, readme.Legacy
		if tocSynthesized {
			spec.Include, spec.Exclude = readme.Include, readme.Exclude
			spec.Subject, spec.IgnoreCase = readme.Subject, readme.IgnoreCase
		}
	}
	return errs
}

// legacySectionSpec translates one legacy [test.X] section into a rule spec.
// One section becomes ONE rule carrying its N parameter sets, never N rules.
func legacySectionSpec(name string, section *config.TestConfig) config.RuleSpec {
	spec := config.RuleSpec{
		Name:    name,
		Check:   name,
		Enabled: true,
		Legacy:  true,
		Include: section.Whitelist,
		Exclude: section.Blacklist,
		Params:  section.KeywordArguments,
		Attrs:   section.Attrs,
	}
	if name == "IsFreeOfSecrets" {
		// The scan's phase gate is the rule's own enabled flag; the legacy
		// surface carries it in the section's attrs table.
		spec.Enabled, _ = section.Attrs["enabled"].(bool)
	}
	return spec
}

// defaultRuleSpec synthesizes the default rule of a check no config names: an
// empty selector, the parameters its Bind produces for the zero spec, and a
// name OUTSIDE the operator's namespace - the prefix is reserved at decode, so
// a [[rule]] can never collide with it. The secret scan stays opt-in: its
// default rule is disabled.
func defaultRuleSpec(name string) config.RuleSpec {
	return config.RuleSpec{Name: config.DefaultRulePrefix + name, Check: name, Enabled: name != "IsFreeOfSecrets"}
}

// RuleSelectors is one rule's compiled filters for ONE scope: the dispatch
// gate, the archive-member gate (nil admits every member), whether the gate's
// "name" subject means the BASE name of a member path, and - legacy only -
// whether the rule's whitelist selected nothing, so the rule has no subject in
// this scope and is not dispatched there at all.
type RuleSelectors struct {
	Gate      selector.Selector
	Member    *selector.Selector
	BaseNames bool
	AdmitNone bool
}

// CompileRuleSelectors compiles one rule's file filters for all the scopes it
// serves; the returned slice is parallel to scopes. On the [[rule]] surface
// the patterns compile ONCE per rule; the legacy branch recompiles them per
// scope (it dies with the sugar) but a fault is reported once either way.
//
// A [[rule]] has ONE selector semantics: regex, case-sensitive unless
// ignoreCase, matched against its declared subject per scope - at file and
// repository scope the collected file's name or RelPath, at the archive scopes
// the member path or its BASE name. An archive-member rule therefore gates
// MEMBERS, never the container: its dispatch gate admits every archive and the
// member selector decides inside - gating the container on a member-addressed
// pattern is exactly the two-readings bug the one semantics removes. At the
// archive-member and repository scopes an UNDECLARED subject defaults to
// "path", the string those scopes address in practice and the one the legacy
// lists always matched there: a "name" default would make a member pattern
// like include = ["data/"] silently match nothing, and would silently stop a
// migrated repository blacklist like ["^raw/"] from matching.
//
// A translated legacy [test.X] list keeps its TWO readings for the sugar's
// remaining release: the dispatch gate reads it as regexes over the file (or
// archive) name - the repository path at repository scope - the member gate as
// case-insensitive literal substrings over the member path.
func CompileRuleSelectors(spec config.RuleSpec, scopes []Scope) ([]RuleSelectors, error) {
	if spec.Legacy {
		return legacyRuleSelectors(spec, scopes)
	}
	sel, err := selector.Compile(ruleSelectorSpec(spec, spec.Subject))
	if err != nil {
		return nil, err
	}
	pathSel := sel
	if spec.Subject == "" && (slices.Contains(scopes, ScopeArchiveMember) || slices.Contains(scopes, ScopeRepository)) {
		pathSel, err = selector.Compile(ruleSelectorSpec(spec, "path"))
		if err != nil {
			return nil, err // unreachable: same patterns as above, valid subject
		}
	}
	compiled := make([]RuleSelectors, len(scopes))
	for i, scope := range scopes {
		switch scope {
		case ScopeArchiveMember:
			if !pathSel.Unfiltered() {
				scoped := pathSel
				compiled[i].Member = &scoped
			}
		case ScopeRepository:
			compiled[i].Gate = pathSel
		default:
			compiled[i].Gate = sel
			compiled[i].BaseNames = scope == ScopeArchiveFileList && sel.Subject() == selector.SubjectName
		}
	}
	return compiled, nil
}

// ruleSelectorSpec is a rule's selector Spec under the given subject.
func ruleSelectorSpec(spec config.RuleSpec, subject string) selector.Spec {
	return selector.Spec{
		Rule:       spec.Name,
		Subject:    subject,
		IgnoreCase: spec.IgnoreCase,
		Include:    spec.Include,
		Exclude:    spec.Exclude,
	}
}

// legacyRuleSelectors preserves each legacy reading in the place that had it.
// Every scope compiles the same lists, so a fault is reported from the first
// scope that hits it and the later, identical verdicts are skipped.
func legacyRuleSelectors(spec config.RuleSpec, scopes []Scope) ([]RuleSelectors, error) {
	compiled := make([]RuleSelectors, len(scopes))
	for i, scope := range scopes {
		subject := "name"
		if scope == ScopeRepository {
			subject = "path"
		}
		gate, err := selector.CompileLegacyRegexLists(spec.Name, subject, spec.Include, spec.Exclude)
		if err != nil {
			return nil, err
		}
		compiled[i].Gate = gate
		if scope != ScopeArchiveMember {
			continue
		}
		// AdmitNone reports a legacy whitelist that selected nothing, which
		// must go on selecting nothing.
		member, admitNone, err := selector.CompileLegacyLists(spec.Name, spec.Include, spec.Exclude)
		if err != nil {
			return nil, err
		}
		if admitNone {
			compiled[i] = RuleSelectors{AdmitNone: true}
			continue
		}
		compiled[i].Member = member
	}
	return compiled, nil
}

// MemberAdmission decides the member pre-filter the archive iterator runs for
// a plan's member-scope rules, and whether the per-rule member gates must
// still be consulted behind it: ONE rule hands the iterator its own member
// selector and the gates are skipped - every shipped config's case - while
// several rules share a union pre-filter that is nobody's own filter, so each
// rule's gate still decides. utils.Compile records the pair on the batch, over
// the WHOLE plan; the checks tests mirror the decision through this same
// function, so the two cannot drift.
func MemberAdmission(rules []*BoundRule) (admit *selector.Selector, perRule bool) {
	if len(rules) == 0 {
		return nil, false
	}
	if len(rules) == 1 {
		return rules[0].Member, false
	}
	return unionMemberAdmission(rules), true
}

// unionMemberAdmission builds the one pre-filter pass the iterator runs in
// front of several member rules' own gates: the union of their include
// literals, matched over the member PATH. A name-subject rule's literal
// appears in the path whenever it appears in the base name, so the union only
// ever over-admits - and the per-rule gates still decide. nil when any rule
// may not take part (see selector.UnionLiterals): then no member may be
// skipped on the union's behalf.
func unionMemberAdmission(rules []*BoundRule) *selector.Selector {
	var include []string
	for _, rule := range rules {
		if rule.Member == nil {
			return nil // admits every member: nothing may be skipped for it
		}
		literals, ok := rule.Member.UnionLiterals()
		if !ok {
			return nil
		}
		for _, literal := range literals {
			include = append(include, regexp.QuoteMeta(literal))
		}
	}
	union, err := selector.Compile(selector.Spec{Rule: "archive members", Subject: "path", Include: include})
	if err != nil {
		return nil
	}
	return &union
}
