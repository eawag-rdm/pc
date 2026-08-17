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
// Two rules of one check that differ only in their name are refused as well -
// see duplicateRuleErrors. Errors are aggregated into one joined error, not
// short-circuited.
//
// The assembled specs are returned beside a non-nil error - a provisional
// signature: the caller binds and compiles them to collect the faults assembly
// cannot see, so one load reports every fault of the specs returned here. A
// non-nil error still means the load must fail.
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
	for i, rule := range rules {
		identities[i] = ruleIdentityOf(rule, def)
	}
	var errs []error
	for j := 1; j < len(identities); j++ {
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
	Legacy     bool
	Include    []string
	Exclude    []string
	Params     []map[string]interface{}
	Attrs      map[string]interface{}

	// NameSubject and PathSubject are the rule's subject reading per scope
	// CLASS, the two readings CompileRuleSelectors compiles, and each is set
	// only where the rule's scopes contain a scope that reads it. A check
	// serving both classes - IsFreeOfKeywords, over files AND archive members -
	// therefore carries two readings, and two of its rules are the same rule
	// only where they agree on both: an undeclared subject and a spelled-out
	// subject = "path" are one rule at the archive-member scope and two
	// different ones at the file scope, where the first gates on the base name
	// and the second on the path. Neither reading describes a LEGACY spec: that
	// one compiles through legacyRuleSelectors, which reads no declared subject
	// at all.
	NameSubject string
	PathSubject string
}

// ruleIdentityOf reads one declared spec into its identity key, so a comparison
// sees what a rule DOES rather than how it was spelled. Three no-op config
// edits that would evade a raw compare are resolved:
//
//  1. Empty against nil: an omitted key decodes to nil, a declared empty list
//     (include = [], params = []) to a non-nil empty value, so the two are one
//     filter in two shapes. Only a non-empty value enters the key. An empty
//     [rule.params] TABLE is not one of these shapes - it decodes to ONE empty
//     parameter set, which every check but the secret scan refuses at bind -
//     and is therefore kept as declared.
//  2. Scope: an omitted (or empty) scope resolves through DefaultScopes, the
//     resolution utils.Compile plans by, so scope = [] and a spelled-out list
//     of every supported scope are one set. The names are sorted either way -
//     declaration order is no part of the meaning.
//  3. Subject: one reading per scope class, over the same scope switch
//     CompileRuleSelectors compiles by - see ruleIdentity.
func ruleIdentityOf(spec config.RuleSpec, def CheckDef) ruleIdentity {
	id := ruleIdentity{
		Check:      spec.Check,
		Enabled:    spec.Enabled,
		IgnoreCase: spec.IgnoreCase,
		Legacy:     spec.Legacy,
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
	// Only the legacy surface fills Attrs, and it becomes one spec per section:
	// this arm guards hand-built specs, which the exported RuleSpecs admits.
	if len(spec.Attrs) > 0 {
		id.Attrs = spec.Attrs
	}
	scopes := DefaultScopes(def)
	names := make([]string, 0, NumScopes)
	if len(spec.Scope) == 0 {
		for _, scope := range scopes {
			names = append(names, scope.String())
		}
	} else {
		names = append(names, spec.Scope...) // never sort the caller's slice
		scopes = make([]Scope, 0, len(spec.Scope))
		for _, name := range spec.Scope {
			// A name no scope answers to is Compile's error to report; here it
			// is simply a scope no subject can be read from.
			if scope, err := ParseScope(name); err == nil {
				scopes = append(scopes, scope)
			}
		}
	}
	sort.Strings(names)
	id.Scope = names
	// The scopes are walked over the same switch CompileRuleSelectors compiles
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

// DefaultScopes returns the scopes a rule serves when it declares none: the
// check's own, in dispatch order. It is the ONE statement of that default -
// utils.Compile resolves a rule's scopes through it and the duplicate refusal
// normalizes through it - so the plan and the refusal cannot come to disagree
// about what an undeclared scope means.
func DefaultScopes(def CheckDef) []Scope {
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
// CompileRuleSelectors for why those two default to the path). It is the ONE
// statement of that default: CompileRuleSelectors compiles the path scopes'
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

// shareReadmeNames keeps "what counts as the readme" single-sourced: the one
// ENABLED HasReadme rule defines it (RuleSpecs refuses a second, and a
// disabled one contributes nothing - the TOC check then falls back to the
// built-in default names), and every ReadMeContainsTOC spec reads that rule's
// parameters. Two DECLARED HasReadme rules elect neither, enabled or not - see
// electReadme. Declaring parameters on a TOC [[rule]] is refused, so the two
// checks can never disagree; a legacy TOC section's own keywordArguments were
// always ignored in favour of the readme's list and keep being dropped. The
// inherited params carry their surface's Legacy flag with them, so a lenient
// legacy list never gets the [[rule]] surface's strict key check (whose faults
// would be misattributed to the TOC rule). A synthesized TOC - no surface of
// its own - additionally takes the readme rule's file filter, exactly as the
// [test.*] surface always behaved; a declared one keeps its own.
func shareReadmeNames(specs []config.RuleSpec, tocSynthesized bool) []error {
	var errs []error
	readme := electReadme(specs)
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

// electReadme returns the rule that defines what counts as the readme: the one
// enabled HasReadme spec, or nothing where none is enabled. More than one
// DECLARED HasReadme spec - the config RuleSpecs refuses - elects nothing
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
	if subject := resolvedSubject(spec, scopes); subject != sel.Subject().String() {
		// The scope set resolves an undeclared subject to one sel was not
		// compiled for, so the scopes that address the path get their own.
		pathSel, err = selector.Compile(ruleSelectorSpec(spec, subject))
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
