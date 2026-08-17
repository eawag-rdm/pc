package checks

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/selector"
)

// This file owns the path from declared rules to bindable specs and compiled
// selectors.

// anchoredChecks must each be DECLARED or the load fails: these checks'
// parameters are load-bearing enough that a config silent about them is a
// mistake, not a default. Every other check synthesizes a default rule.
var anchoredChecks = []string{"IsFreeOfKeywords", "IsValidName", "HasReadme"}

// AnchoredChecks returns, as a copy, the checks that must be explicitly
// configured for a config to load. Fixtures derive their anchor declarations
// from it, so adding an anchor cannot desynchronise them.
func AnchoredChecks() []string {
	return append([]string(nil), anchoredChecks...)
}

// ruleSpecs assembles the rule specs one config declares, in the registry's
// DECLARED order by check - that order is the order the dispatch runs a file's
// checks in, and therefore the order findings are rendered in. Per check: its
// [[rule]] sections in config order, else - anchoredChecks excepted - a
// synthesized default rule with an empty selector and the parameters its own
// Bind produces for the zero spec; without that, assembly would silently
// delete every check no config names.
// Two rules of one check that differ only in their name are refused as well -
// see duplicateRuleErrors. Errors are aggregated into one joined error, not
// short-circuited.
//
// The assembled specs are returned beside a non-nil error - a provisional
// signature: the caller binds and compiles them to collect the faults assembly
// cannot see, so one load reports every fault of the specs returned here. A
// non-nil error still means the load must fail.
func ruleSpecs(cfg *config.Config, reg Registry) ([]config.RuleSpec, error) {
	var errs []error
	byCheck := make(map[string][]config.RuleSpec, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if _, known := reg.Lookup(rule.Check); !known {
			errs = append(errs, fmt.Errorf("rule %q: unknown check %q", rule.Name, rule.Check))
			continue
		}
		byCheck[rule.Check] = append(byCheck[rule.Check], rule)
	}

	specs := make([]config.RuleSpec, 0, reg.Len()+len(cfg.Rules))
	tocSynthesized := false
	for _, def := range reg.Defs() {
		rules, present := byCheck[def.Name]
		switch {
		case present:
			// "What counts as the readme" must have exactly one definition; a
			// second HasReadme rule would declare a second one, switched off
			// today or not.
			if def.Name == "HasReadme" && len(rules) > 1 {
				errs = append(errs, fmt.Errorf("check %q allows exactly one rule (it defines what counts as the readme), got %d", def.Name, len(rules)))
			}
			// The rules this arm refuses are assembled anyway: the joined error
			// fails the load either way, and dropping the check's remaining rules
			// here would hide their faults until the refusal is settled and the
			// load rerun.
			errs = append(errs, duplicateRuleErrors(rules, def)...)
			errs = append(errs, contradictorySiblingErrors(rules, def)...)
			specs = append(specs, rules...)
		case slices.Contains(anchoredChecks, def.Name):
			errs = append(errs, fmt.Errorf("check %q is not configured: declare a [[rule]] for it", def.Name))
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

// duplicateRuleErrors refuses a check's rules that differ ONLY in their name:
// the config says one thing twice and every finding the pair produces is
// reported twice. The comparison is SYNTACTIC, over identity keys, so what it
// catches is one rule written twice UP TO SPELLING - ruleIdentityOf resolves
// the no-op spellings (an empty list against an omitted one, scope order, the
// subject default) and nothing further. A pair made distinct by a field that
// does no work here (ignoreCase with no pattern to fold, a subject with no
// pattern to read it from) or by a pattern that matches nothing passes it and
// still reports every finding twice; that is the run-scoped rule-overlap
// notice's subject (pkg/utils/rule_report.go), which judges what the rules
// actually matched.
//
// It runs on the DECLARED specs, before shareReadmeNames rewrites params,
// provenance and (for a synthesized TOC) selectors - a comparison after that
// would judge normalization output rather than what the operator wrote. Each
// twin is reported against the FIRST rule it repeats, so N copies name one
// original.
//
// Disabled rules are compared too: a disabled twin does no work today, but
// this is config hygiene, and Compile already validates disabled rules
// deliberately, so a config the checks cannot honour fails at load rather than
// on the day someone re-enables it.
func duplicateRuleErrors(rules []config.RuleSpec, def CheckDef) []error {
	identities := make([]ruleIdentity, len(rules))
	resolved := make([]bool, len(rules))
	for i, rule := range rules {
		scopes, err := ruleScopes(rule, def)
		if err != nil {
			// A scope list that does not resolve leaves every scope-dependent
			// reading of the rule undecided, so there is no identity to compare -
			// and comparing one anyway made two rules collide over a scope name
			// neither of them serves.
			//
			// This is the one fault the load does NOT report in full: Compile
			// reports the same ruleScopes error, so no bad config gets through,
			// but a genuine twin sharing that scope typo goes unreported until
			// the typo is fixed and the load rerun - two cycles where the rest of
			// this file promises one.
			continue
		}
		identities[i], resolved[i] = ruleIdentityOf(rule, scopes), true
	}
	var errs []error
	for j := 1; j < len(identities); j++ {
		if !resolved[j] {
			continue
		}
		// An unresolved i needs no guard of its own: its identity is the zero
		// value, which no resolved identity equals - Check is the check's name
		// and Scope a non-nil slice.
		for i := 0; i < j; i++ {
			if reflect.DeepEqual(identities[i], identities[j]) {
				errs = append(errs, fmt.Errorf("rule %q: resolves to the same rule as %q; remove one", rules[j].Name, rules[i].Name))
				break
			}
		}
	}
	return errs
}

// ruleIdentity is what makes two rules of one check the SAME rule: every
// declared field that does work, under the readings the rest of the load gives
// it. Name is not among them - it is the one field a twin may differ in, and
// leaving it out is the whole statement of that. The key is EXPLICIT rather
// than the whole config.RuleSpec so that a field added in pkg/config joins the
// identity relation only when someone decides it belongs here.
type ruleIdentity struct {
	Check      string
	Scope      []string // resolved and sorted: a set, not a declaration order
	Enabled    bool
	IgnoreCase bool
	Include    []string
	Exclude    []string
	Params     []map[string]interface{}

	// NameSubject and PathSubject are the rule's subject reading per scope
	// CLASS, the two readings compileRuleSelectors compiles, and each is set
	// only where the rule's scopes contain a scope that reads it. A check
	// serving both classes - IsFreeOfKeywords, over files AND archive members -
	// therefore carries two readings, and two of its rules are the same rule
	// only where they agree on both: an undeclared subject and a spelled-out
	// subject = "path" are one rule at the archive-member scope and two
	// different ones at the file scope, where the first gates on the base name
	// and the second on the path.
	NameSubject string
	PathSubject string
}

// ruleIdentityOf reads one declared spec, under the scopes ruleScopes resolved
// it to, into its identity key, so a comparison sees what a rule DOES rather
// than how it was spelled. Three no-op config edits that would evade a raw
// compare are resolved:
//
//  1. Empty against nil: an omitted key decodes to nil, a declared empty list
//     (include = [], params = []) to a non-nil empty value, so the two are one
//     filter in two shapes. Only a non-empty value enters the key. An empty
//     [rule.params] TABLE is not one of these shapes - it decodes to ONE empty
//     parameter set, which every check but the secret scan refuses at bind -
//     and is therefore kept as declared.
//  2. Scope: the key carries the RESOLVED scopes, the ones Compile plans by, so
//     an omitted scope and a spelled-out list of every supported scope are one
//     set. The names are sorted - declaration order is no part of the meaning.
//  3. Subject: one reading per scope class, over the same scope switch
//     compileRuleSelectors compiles by - see ruleIdentity.
func ruleIdentityOf(spec config.RuleSpec, scopes []Scope) ruleIdentity {
	id := ruleIdentity{
		Check:      spec.Check,
		Enabled:    spec.Enabled,
		IgnoreCase: spec.IgnoreCase,
	}
	if len(spec.Include) > 0 {
		id.Include = spec.Include
	}
	if len(spec.Exclude) > 0 {
		id.Exclude = spec.Exclude
	}
	if len(spec.Params) > 0 {
		id.Params = spec.Params
	}
	names := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		names = append(names, scope.String())
	}
	sort.Strings(names)
	id.Scope = names
	// The scopes are walked over the same switch compileRuleSelectors compiles
	// by, so the key records the reading each of them is actually gated on.
	declared, resolved := declaredSubject(spec), resolvedSubject(spec, scopes)
	for _, scope := range scopes {
		switch scope {
		case ScopeArchiveMember, ScopeRepository:
			id.PathSubject = resolved
		default:
			id.NameSubject = declared
		}
	}
	return id
}

// defaultScopes returns the scopes a rule serves when it declares none: the
// check's own, in dispatch order. It is the ONE statement of that default -
// ruleScopes resolves through it - so the plan and the duplicate refusal cannot
// come to disagree about what an undeclared scope means.
func defaultScopes(def CheckDef) []Scope {
	var scopes []Scope
	for scope := Scope(0); scope < NumScopes; scope++ {
		if def.Scopes.Has(scope) {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// resolvedSubject is the subject a rule addresses over the given scopes: the
// one it declares, or - undeclared - "path" where those scopes include the
// archive-member or repository scope and "name" otherwise (see
// compileRuleSelectors for why those two default to the path). It is the ONE
// statement of that default: compileRuleSelectors compiles the path scopes'
// selector through it and the duplicate refusal normalizes through it.
func resolvedSubject(spec config.RuleSpec, scopes []Scope) string {
	if spec.Subject != "" {
		return spec.Subject
	}
	if slices.Contains(scopes, ScopeArchiveMember) || slices.Contains(scopes, ScopeRepository) {
		return "path"
	}
	return "name"
}

// declaredSubject is the reading every scope but the archive-member and
// repository ones compiles from: the rule's own subject, defaulted through
// selector.ParseSubject rather than restated here.
func declaredSubject(spec config.RuleSpec) string {
	subject, _ := selector.ParseSubject(spec.Subject)
	return subject.String()
}

// contradictorySiblingErrors refuses one check's rules that answer "which files
// does this check run on" twice over: rule A excludes the very pattern rule B
// includes. That question has ONE owner, the check - a [[rule]] block carries a
// parameter set, it does not buy its rule a file set of its own - so a config
// giving two answers is a load error rather than a precedence to settle. What
// it reaches is the half of that written down: a sibling declaring no include
// at all admits everything the other excludes and loads silently, so this
// refuses a config that contradicts itself IN WRITING, not every pair of rules
// that ends up disagreeing about a file.
//
// Nothing is claimed here about what gets scanned: each rule keeps its own
// selector and B really does scan what A skips, which is why the message says
// only that the config says two things.
//
// Two rules are compared only where they gate the SAME STRINGS: a scope both
// serve, read there through the same subject. An exclude at the file scope and
// an include at the archive-member scope address different phases of the scan;
// a path pattern beside a name pattern addresses a different string of the same
// file. Both readings are the ones ruleIdentityOf takes, per scope class. The
// patterns themselves are compared VERBATIM: ignoreCase only widens what a
// pattern matches - it prepends "(?i)" - so one string in both lists still
// means the files one rule skips are files the other targets, folded or not.
// Two patterns that merely overlap as regexes are the run-scoped rule-overlap
// notice's subject, not this gate's.
//
// The pair (i, i) is contradictorySelector's: within ONE rule exclude wins, so
// its message can say the stronger thing that is true there.
//
// Like duplicateRuleErrors it judges the specs as DECLARED - only the arm that
// found [[rule]] sections calls it, so a synthesized rule never reaches it -
// and disabled rules with them, the policy Compile states for the whole
// surface: a parked contradiction is reported now rather than on the day
// someone re-enables the rule. The operator pays for that by keeping a
// predecessor beside the rule that replaced it: parked with enabled = false or
// not, if it excludes what the replacement includes, the config stops loading.
func contradictorySiblingErrors(rules []config.RuleSpec, def CheckDef) []error {
	gates := make([]ruleGate, len(rules))
	for i, rule := range rules {
		// A scope list that does not resolve leaves "do these two meet" with no
		// answer, and an unresolved rule meets nothing: its zero gate serves no
		// scope. Compile reports the same ruleScopes error, so no bad config
		// gets through, but a genuine contradiction behind that scope typo goes
		// unreported until the typo is fixed and the load rerun - two cycles
		// where the rest of this file promises one.
		scopes, err := ruleScopes(rule, def)
		if err != nil {
			continue
		}
		gates[i] = ruleGate{scopes: scopes, name: declaredSubject(rule), path: resolvedSubject(rule, scopes)}
	}
	var errs []error
	for i := range rules {
		for j := range rules {
			if i == j || !gates[i].meets(gates[j]) {
				continue
			}
			for k, excluded := range rules[i].Exclude {
				if !slices.Contains(rules[j].Include, excluded) {
					continue
				}
				// One pattern written twice in one exclude list states the same
				// contradiction twice; it is reported against its first copy.
				if slices.Contains(rules[i].Exclude[:k], excluded) {
					continue
				}
				errs = append(errs, fmt.Errorf("check %q: rule %q excludes %q while rule %q includes it; one check cannot both skip and target the same pattern, so remove one", def.Name, rules[i].Name, excluded, rules[j].Name))
			}
		}
	}
	return errs
}

// ruleGate is what one rule of a compared pair gates on: the scopes it serves
// and its subject reading per scope CLASS - the two readings ruleIdentityOf
// takes, because the question is the same one, which string a pattern is
// matched against. The zero value serves no scope and therefore meets nothing.
type ruleGate struct {
	scopes []Scope
	name   string // read at every scope but the archive-member and repository ones
	path   string // read at the archive-member and repository scopes
}

// meets reports whether two rules gate the same strings anywhere: a scope both
// serve, over the subject each of them reads there. The scopes are walked over
// the same switch compileRuleSelectors compiles by.
func (g ruleGate) meets(other ruleGate) bool {
	for _, scope := range g.scopes {
		if !slices.Contains(other.scopes, scope) {
			continue
		}
		switch scope {
		case ScopeArchiveMember, ScopeRepository:
			if g.path == other.path {
				return true
			}
		default:
			if g.name == other.name {
				return true
			}
		}
	}
	return false
}

// shareReadmeNames keeps "what counts as the readme" single-sourced: the one
// ENABLED HasReadme rule defines it (ruleSpecs refuses a second, and a
// disabled one contributes nothing - the TOC check then falls back to the
// built-in default names), and every ReadMeContainsTOC spec reads that rule's
// parameters. Two DECLARED HasReadme rules elect neither, enabled or not - see
// electReadme. Declaring parameters on a TOC [[rule]] is refused, so the two
// checks can never disagree. A synthesized TOC - no declaration of its own -
// additionally takes the readme rule's file filter; a declared one keeps its
// own.
func shareReadmeNames(specs []config.RuleSpec, tocSynthesized bool) []error {
	var errs []error
	readme := electReadme(specs)
	for i := range specs {
		spec := &specs[i]
		if spec.Check != "ReadMeContainsTOC" {
			continue
		}
		if len(spec.Params) > 0 {
			errs = append(errs, fmt.Errorf("rule %q: ReadMeContainsTOC takes no parameters (what counts as the readme is the HasReadme rule's readme_names)", spec.Name))
			continue
		}
		if readme == nil {
			continue
		}
		spec.Params = readme.Params
		if tocSynthesized {
			spec.Include, spec.Exclude = readme.Include, readme.Exclude
			spec.Subject, spec.IgnoreCase = readme.Subject, readme.IgnoreCase
		}
	}
	return errs
}

// electReadme returns the rule that defines what counts as the readme: the one
// enabled HasReadme spec, or nothing where none is enabled. More than one
// DECLARED HasReadme spec - the config ruleSpecs refuses - elects nothing
// either: the operator wrote two, and honouring either would pick one for them.
// A spec switched off today still declares a readme, so it is counted here
// although it can never be elected.
func electReadme(specs []config.RuleSpec) *config.RuleSpec {
	var readme *config.RuleSpec
	declared := 0
	for i := range specs {
		if specs[i].Check != "HasReadme" {
			continue
		}
		declared++
		if specs[i].Enabled {
			readme = &specs[i]
		}
	}
	if declared > 1 {
		return nil
	}
	return readme
}

// defaultRuleSpec synthesizes the default rule of a check no config names: an
// empty selector, the parameters its Bind produces for the zero spec, and a
// name OUTSIDE the operator's namespace - the prefix is reserved at decode, so
// a [[rule]] can never collide with it. The secret scan stays opt-in: its
// default rule is disabled.
func defaultRuleSpec(name string) config.RuleSpec {
	return config.RuleSpec{Name: config.DefaultRulePrefix + name, Check: name, Enabled: name != "IsFreeOfSecrets"}
}

// ruleSelectors is one rule's compiled filters for ONE scope: the dispatch
// gate, the archive-member gate (nil admits every member), and whether the
// gate's "name" subject means the BASE name of a member path.
type ruleSelectors struct {
	Gate      selector.Selector
	Member    *selector.Selector
	BaseNames bool
}

// compileRuleSelectors compiles one rule's file filters for all the scopes it
// serves; the returned slice is parallel to scopes. The patterns compile ONCE
// per rule, so a fault is reported once.
//
// A [[rule]] has ONE selector semantics: regex, case-sensitive unless
// ignoreCase, matched against its declared subject per scope - at file and
// repository scope the collected file's name or RelPath, at the archive scopes
// the member path or its BASE name. An archive-member rule therefore gates
// MEMBERS, never the container: its dispatch gate admits every archive and the
// member selector decides inside - gating the container on a member-addressed
// pattern is exactly the two-readings bug the one semantics removes. At the
// archive-member and repository scopes an UNDECLARED subject defaults to
// "path", the string those scopes address in practice: a "name" default would
// make a member pattern like include = ["data/"] silently match nothing, and
// would silently stop a repository exclude like ["^raw/"] from matching.
func compileRuleSelectors(spec config.RuleSpec, scopes []Scope) ([]ruleSelectors, error) {
	sel, err := selector.Compile(ruleSelectorSpec(spec, spec.Subject))
	if err != nil {
		return nil, err
	}
	pathSel := sel
	if subject := resolvedSubject(spec, scopes); subject != sel.Subject().String() {
		// The scope set resolves an undeclared subject to one sel was not
		// compiled for, so the scopes that address the path get their own.
		pathSel, err = selector.Compile(ruleSelectorSpec(spec, subject))
		if err != nil {
			return nil, err // unreachable: same patterns as above, valid subject
		}
	}
	compiled := make([]ruleSelectors, len(scopes))
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

// memberAdmission decides the member pre-filter the archive iterator runs for
// a plan's member-scope rules, and whether the per-rule member gates must
// still be consulted behind it: ONE rule hands the iterator its own member
// selector and the gates are skipped - every shipped config's case - while
// several rules share a union pre-filter that is nobody's own filter, so each
// rule's gate still decides. Compile is the only caller: it records the pair on
// the batch over the WHOLE plan, which is what makes the decision independent
// of the rules that happen to match one archive.
func memberAdmission(rules []*BoundRule) (admit *selector.Selector, perRule bool) {
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
