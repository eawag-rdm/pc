package utils

import (
	"errors"
	"fmt"
	"slices"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
)

// checkRules pairs one check with bound rules: in the Plan, every rule of that
// scope which names the check; in a work item, the ones that matched the file.
// def and batch are POINTERS into the Plan, which owns both and outlives every
// work item - a work item is built per file, and copying a CheckDef into each
// one costs more than the selection pass it belongs to.
type checkRules struct {
	def   *checks.CheckDef
	batch *checks.Batch
	rules []*checks.BoundRule
}

// Plan is the compiled rule set, per dispatch scope. It is built once at
// startup by each frontend, hard-failing on error, and is immutable afterwards,
// so every worker reads it without locking. It is carried explicitly - it never
// lives in config.Config, which the server copies per request.
type Plan struct {
	scopes [checks.NumScopes][]checkRules
}

// add files one bound rule under its check, keeping one entry per check so the
// invocation unit stays (file, check) with that check's rules batched. The
// entry owns the check definition and the batch its rules share.
func (p *Plan) add(scope checks.Scope, def checks.CheckDef, general *config.GeneralConfig, rule *checks.BoundRule) {
	entries := p.scopes[scope]
	for i := range entries {
		if entries[i].def.Name == def.Name {
			entries[i].rules = append(entries[i].rules, rule)
			return
		}
	}
	owned := def
	p.scopes[scope] = append(entries, checkRules{
		def:   &owned,
		batch: checks.NewBatch(general),
		rules: []*checks.BoundRule{rule},
	})
}

// scope returns the bound rules of one dispatch scope, grouped by check.
func (p *Plan) scope(s checks.Scope) []checkRules {
	return p.scopes[s]
}

// Compile turns the configured rules into the plan the engine dispatches. It is
// the boot gate for the whole rule surface: checks.RuleSpecs assembles the
// specs ([[rule]] sections, translated [test.X] sections, synthesized
// defaults - refusing a config silent about the anchored checks), every rule's
// parameters are bound and its selectors compiled - DISABLED rules included,
// so a config the checks could not honour fails at load, not on the day a rule
// is re-enabled; only enabled rules enter the plan. Every load error is
// aggregated and named with its rule, so a config author is told all of them
// at once rather than one per run.
//
// A bad include/exclude (or legacy whitelist/blacklist) pattern is reported as
// a *selector.CompileError, which errors.As pulls out of the aggregate -
// callers that want the faulty patterns rather than the message can match on
// it.
func Compile(cfg *config.Config, reg checks.Registry) (*Plan, error) {
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
	specs, err := checks.RuleSpecs(cfg, reg)
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
			// RuleSpecs filtered unknown checks already; hand-built spec lists
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
		// Selectors likewise compile ONCE per rule; the result carries one
		// placement per scope.
		selectors, err := checks.CompileRuleSelectors(spec, scopes)
		if err != nil {
			errs = append(errs, fmt.Errorf("rule %q: %w", spec.Name, err))
			continue
		}
		for i, scope := range scopes {
			// A whitelist that selects nothing selects nothing: the rule has no
			// subject in this scope, so it is not dispatched there at all.
			if selectors[i].AdmitNone {
				continue
			}
			// Validated like any other rule, but a disabled rule stays out of
			// the plan.
			if !spec.Enabled {
				continue
			}
			// One copy per scope, sharing the bound closures: each scope carries
			// its own selectors, and nothing in a bound rule reads itself.
			scoped := *bound
			scoped.SetSelectors(selectors[i])
			plan.add(scope, def, general, &scoped)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	plan.buildMemberAdmission()
	return plan, nil
}

// ruleScopes resolves the scopes a rule serves: the ones it names, or the
// check's own when it names none. A scope the check does not support is a load
// error, and so is a scope named twice - the rule would be added to that
// scope's plan twice, doubling its findings.
func ruleScopes(spec config.RuleSpec, def checks.CheckDef) ([]checks.Scope, error) {
	if len(spec.Scope) == 0 {
		var scopes []checks.Scope
		for scope := checks.Scope(0); scope < checks.NumScopes; scope++ {
			if def.Scopes.Has(scope) {
				scopes = append(scopes, scope)
			}
		}
		return scopes, nil
	}
	scopes := make([]checks.Scope, 0, len(spec.Scope))
	for _, name := range spec.Scope {
		scope, err := checks.ParseScope(name)
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
// dead-rule report's job. Legacy sections cannot set both lists, so only the
// [[rule]] surface is checked.
func contradictorySelector(spec config.RuleSpec) error {
	if spec.Legacy {
		return nil
	}
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
// have to run behind it. The decision is checks.MemberAdmission's, made over
// the WHOLE plan and recorded on the entry: it describes what the iterator was
// given, which the rules matching one archive cannot tell.
func (p *Plan) buildMemberAdmission() {
	entries := p.scopes[checks.ScopeArchiveMember]
	var rules []*checks.BoundRule
	for _, entry := range entries {
		rules = append(rules, entry.rules...)
	}
	if len(rules) == 0 {
		return
	}
	admit, perRule := checks.MemberAdmission(rules)
	for i := range entries {
		entries[i].batch.Admit = admit
		entries[i].batch.PerRule = perRule
	}
}
