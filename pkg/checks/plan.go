package checks

import (
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
)

// PlanEntry pairs one check with bound rules: in the Plan, every rule of that
// scope which names the check; in a work item, the ones that matched the file.
// Def and Batch are POINTERS into the Plan, which owns both and outlives every
// work item - a work item is built per file, and copying a CheckDef into each
// one costs more than the selection pass it belongs to.
type PlanEntry struct {
	Def   *CheckDef
	Batch *Batch
	Rules []*BoundRule
}

// Plan is the compiled rule set, per dispatch scope. It is built once at
// startup by each frontend, hard-failing on error, and is immutable afterwards,
// so every worker reads it without locking. It is carried explicitly - it never
// lives in config.Config, which the server copies per request.
type Plan struct {
	scopes [NumScopes][]PlanEntry
}

// NewPlan builds a plan from ready-made entries. It is the only writer outside
// Compile, and a TEST SEAM: it exists so a dispatch test can drive a synthetic
// plan without a config. Nothing may call it on a plan Compile has returned -
// the workers read one locklessly, so a plan is immutable once it is out.
func NewPlan(entries map[Scope][]PlanEntry) *Plan {
	plan := &Plan{}
	for scope, scoped := range entries {
		plan.scopes[scope] = scoped
	}
	return plan
}

// add files one bound rule under its check, keeping one entry per check so the
// invocation unit stays (file, check) with that check's rules batched. The
// entry owns the check definition and the batch its rules share.
func (p *Plan) add(scope Scope, def CheckDef, general *config.GeneralConfig, rule *BoundRule) {
	entries := p.scopes[scope]
	for i := range entries {
		if entries[i].Def.Name == def.Name {
			entries[i].Rules = append(entries[i].Rules, rule)
			return
		}
	}
	owned := def
	p.scopes[scope] = append(entries, PlanEntry{
		Def:   &owned,
		Batch: newBatch(general),
		Rules: []*BoundRule{rule},
	})
}

// Scope returns the bound rules of one dispatch scope, grouped by check.
func (p *Plan) Scope(s Scope) []PlanEntry {
	return p.scopes[s]
}

// Compile turns the configured rules into the plan the engine dispatches. It is
// the boot gate for the whole rule surface: ruleSpecs assembles the specs
// ([[rule]] sections and synthesized defaults - refusing a config silent about
// the anchored checks), every rule's parameters are bound and its selectors
// compiled - DISABLED rules included, so a config the checks could not honour
// fails at load, not on the day a rule is re-enabled; only enabled rules enter
// the plan. Every load error is aggregated and named with its rule, so a config
// author is told all of them at once rather than one per run.
//
// A bad include/exclude pattern is reported as a *selector.CompileError, which
// errors.As pulls out of the aggregate - callers that want the faulty patterns
// rather than the message can match on it.
func Compile(cfg *config.Config, reg Registry) (*Plan, error) {
	// A nil config is a caller bug, not a plan: dereferencing it below would
	// panic, and a fabricated empty config would scan nothing.
	if cfg == nil {
		return nil, errors.New("config must not be nil")
	}
	// A zero-value Registry{} is constructible anywhere and would compile to a
	// plan with no entries: every package scans clean and the server caches
	// that as authoritative. An empty registry is a load error, not a plan.
	if reg.Len() == 0 {
		return nil, errors.New("check registry has no definitions (an empty plan would report every package clean)")
	}
	// [general] carries the scan bounds every acquisition reads. A fabricated
	// zero value would silently mean "scan nothing" (MaxContentScan 0), so a
	// missing section is a load error, not a default.
	if cfg.General == nil {
		return nil, errors.New("config section [general] is required (it carries the scan and memory limits)")
	}
	general := cfg.General

	var errs []error
	specs, err := ruleSpecs(cfg, reg)
	if err != nil {
		errs = append(errs, err)
	}

	plan := &Plan{}
	named := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if _, duplicate := named[spec.Name]; duplicate {
			errs = append(errs, fmt.Errorf("rule %q: duplicate rule name", spec.Name))
			continue
		}
		named[spec.Name] = struct{}{}
		def, known := reg.Lookup(spec.Check)
		if !known {
			// ruleSpecs filtered unknown checks already; hand-built spec lists
			// get the same verdict.
			errs = append(errs, fmt.Errorf("rule %q: unknown check %q", spec.Name, spec.Check))
			continue
		}
		scopes, err := ruleScopes(spec, def)
		if err != nil {
			errs = append(errs, fmt.Errorf("rule %q: %w", spec.Name, err))
			continue
		}
		if err := contradictorySelector(spec); err != nil {
			errs = append(errs, fmt.Errorf("rule %q: %w", spec.Name, err))
			continue
		}
		// Bind ONCE per rule: the parameters do not depend on the scope, and a
		// per-scope bind would report every parameter fault once per scope.
		bound, err := def.Bind(spec, general)
		if err != nil {
			errs = append(errs, fmt.Errorf("rule %q: %w", spec.Name, err))
			continue
		}
		if strings.HasPrefix(spec.Name, config.DefaultRulePrefix) {
			// A synthesized default rule names no configuration an operator
			// wrote and its findings are rendered to end users, so they stay
			// untagged. Decided ONCE here rather than per message in tag.
			bound.Rules = nil
		}
		// Selectors likewise compile ONCE per rule; the result carries one
		// placement per scope.
		selectors, err := compileRuleSelectors(spec, scopes)
		if err != nil {
			errs = append(errs, fmt.Errorf("rule %q: %w", spec.Name, err))
			continue
		}
		for i, scope := range scopes {
			// Validated like any other rule, but a disabled rule stays out of
			// the plan.
			if !spec.Enabled {
				continue
			}
			// One copy per scope, sharing the bound closures: each scope carries
			// its own selectors, and nothing in a bound rule reads itself.
			scoped := *bound
			scoped.setSelectors(selectors[i])
			plan.add(scope, def, general, &scoped)
		}
	}
	// Merging is a post-pass over the finished plan, so its faults join the
	// rules' own: a config author is told about a check that outgrew the merge
	// mask beside whatever else the load found.
	errs = append(errs, plan.buildMergeNodes()...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	plan.buildMemberAdmission()
	return plan, nil
}

// ruleScopes resolves the scopes a rule serves: the ones it names, or - through
// defaultScopes - the check's own when it names none. It is the ONE reading of
// a declared scope list: Compile plans by it and the duplicate refusal takes a
// rule's identity under it, so a name no scope answers to cannot be a load
// error on one path and a silent omission on the other. A scope the check does
// not support is a load error, and so is a scope named twice - the rule would
// be added to that scope's plan twice, doubling its findings.
func ruleScopes(spec config.RuleSpec, def CheckDef) ([]Scope, error) {
	if len(spec.Scope) == 0 {
		return defaultScopes(def), nil
	}
	scopes := make([]Scope, 0, len(spec.Scope))
	for _, name := range spec.Scope {
		scope, err := ParseScope(name)
		if err != nil {
			return nil, err
		}
		if !def.Scopes.Has(scope) {
			return nil, fmt.Errorf("check %q does not support scope %q", spec.Check, name)
		}
		if slices.Contains(scopes, scope) {
			return nil, fmt.Errorf("scope %q is declared twice", name)
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

// contradictorySelector implements the honest subset of include/exclude
// contradiction detection: an identical pattern in both lists can never match,
// because exclude wins. Full regex intersection is undecidable, and a
// recognizer for "exclude matches everything" could only ever spell out a
// handful of its infinitely many forms, so the rest is the run-scoped
// dead-rule report's job.
func contradictorySelector(spec config.RuleSpec) error {
	for _, excluded := range spec.Exclude {
		for _, included := range spec.Include {
			if included == excluded {
				return fmt.Errorf("%q is in include AND exclude; exclude wins, so the rule can never match it", included)
			}
		}
	}
	return nil
}

// buildMemberAdmission records, on every archive-member entry, the member
// pre-filter the iterator runs and whether the per-rule member gates still
// have to run behind it. The decision is memberAdmission's, made over the WHOLE
// plan and recorded on the entry: it describes what the iterator was given,
// which the rules matching one archive cannot tell.
func (p *Plan) buildMemberAdmission() {
	entries := p.scopes[ScopeArchiveMember]
	var rules []*BoundRule
	for _, entry := range entries {
		rules = append(rules, entry.Rules...)
	}
	if len(rules) == 0 {
		return
	}
	admit, perRule := memberAdmission(rules)
	for i := range entries {
		entries[i].Batch.admit = admit
		entries[i].Batch.perRule = perRule
	}
}

// mergedScopes are the dispatch scopes whose entries are merged: the ones that
// acquire ONCE per (file, check) and hand that acquisition to every rule that
// matched the file, which is exactly what a shared unit set can serve.
//
// The archive-member scope is out because its member gate decides per MEMBER,
// behind the dispatch gate the merge would fold. The repository scope is out
// because each of its rules is handed a file set narrowed by its own selector -
// the one thing a merged pass would have to share.
var mergedScopes = [...]Scope{ScopeFile, ScopeArchiveFileList}

// buildMergeNodes records, on every entry of the merged scopes, the entry's
// rules folded into one pass over deduplicated units.
//
// Rule i of the entry becomes bit 1<<i - the same i pkg/utils' rule report
// indexes its per-rule marks by, so the ENTRY'S RULE LIST STAYS AS COMPILED:
// nothing here reorders it, filters it or renumbers it, and the node is a
// sibling structure that only references it.
func (p *Plan) buildMergeNodes() []error {
	var errs []error
	for _, scope := range mergedScopes {
		entries := p.scopes[scope]
		for i := range entries {
			node, err := mergeUnits(entries[i].Rules)
			if err != nil {
				errs = append(errs, fmt.Errorf("check %q (scope %s): %w", entries[i].Def.Name, scope, err))
				continue
			}
			entries[i].Batch.merged = node
		}
	}
	return errs
}

// maxMergedRules is what one entry's contributor mask holds. Past it the load
// fails rather than falling back to a per-rule pass: an untested second loop on
// the acquisition's hot path is worse than a refusal a config author can read,
// and no configuration comes near 64 rules of ONE check in ONE scope.
const maxMergedRules = 64

// mergeUnits folds one entry's rules into its merge node: every rule's units in
// declared order, each either a new unit or a contributor bit on the unit whose
// parameters it repeats. It also stamps each rule with its bit.
func mergeUnits(rules []*BoundRule) (*mergeNode, error) {
	if len(rules) > maxMergedRules {
		return nil, fmt.Errorf("%d rules exceed the %d one check may hold in one scope", len(rules), maxMergedRules)
	}
	node := &mergeNode{trivial: len(rules) == 1}
	for i, rule := range rules {
		rule.bit = 1 << i
		for _, u := range rule.units {
			if j := node.find(u.key); j >= 0 {
				node.units[j].mask |= rule.bit
				continue
			}
			u.mask = rule.bit
			node.units = append(node.units, u)
		}
	}
	// Attribution is interned once every contributor of a unit is known, so a
	// finding is tagged with a slice built here rather than one per message.
	for i := range node.units {
		u := &node.units[i]
		u.single = make([][]string, len(rules))
		for j := range rules {
			if u.mask&(1<<j) != 0 {
				u.single[j] = rules[j].Rules
			}
		}
		if bits.OnesCount64(u.mask) == 1 {
			// One contributor: its own interned names, nil for a synthesized
			// default rule - which is how a default's findings stay untagged.
			u.names = u.single[bits.TrailingZeros64(u.mask)]
			continue
		}
		names := make([]string, 0, bits.OnesCount64(u.mask))
		for rest := u.mask; rest != 0; rest &= rest - 1 {
			names = append(names, u.single[bits.TrailingZeros64(rest)]...)
		}
		u.names = names
	}
	return node, nil
}

// find returns the index of the unit these parameters already bound, or -1. A
// unit that binds no key merges with nothing: every bind that emits one sets a
// key, and folding two unkeyed units into one would silently drop a check's
// findings.
func (m *mergeNode) find(key any) int {
	if key == nil {
		return -1
	}
	for i := range m.units {
		if m.units[i].key == key {
			return i
		}
	}
	return -1
}
