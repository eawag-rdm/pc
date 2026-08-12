package utils

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/selector"
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
// the only place that sees both the config and the check registry. Every load
// error is aggregated and named with its rule, so a config author is told all
// of them at once rather than one per run.
//
// A bad whitelist/blacklist pattern is reported as a *selector.CompileError,
// which errors.As pulls out of the aggregate - callers that want the faulty
// patterns rather than the message can match on it.
func Compile(cfg *config.Config, reg checks.Registry) (*Plan, error) {
	// [general] carries the scan bounds every acquisition reads. A fabricated
	// zero value would silently mean "scan nothing" (MaxContentScan 0), so a
	// missing section is a load error, not a default.
	if cfg.General == nil {
		return nil, errors.New("config section [general] is required (it carries the scan and memory limits)")
	}
	general := cfg.General

	specs, errs := legacyRuleSpecs(cfg, reg)

	plan := &Plan{}
	named := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		if _, duplicate := named[spec.Name]; duplicate {
			errs = append(errs, fmt.Errorf("rule %q: duplicate rule name", spec.Name))
			continue
		}
		named[spec.Name] = struct{}{}
		def, known := reg.Lookup(spec.Check)
		if !known {
			errs = append(errs, fmt.Errorf("rule %q: unknown check %q", spec.Name, spec.Check))
			continue
		}
		scopes, err := ruleScopes(spec, def)
		if err != nil {
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
		for _, scope := range scopes {
			gate, member, admitNone, err := scopeSelectors(spec, scope)
			if err != nil {
				errs = append(errs, fmt.Errorf("rule %q: %w", spec.Name, err))
				continue
			}
			// A whitelist that selects nothing selects nothing: the rule has no
			// subject in this scope, so it is not dispatched there at all.
			if admitNone {
				continue
			}
			// One copy per scope, sharing the bound closures: each scope carries
			// its own selectors, and nothing in a bound rule reads itself.
			scoped := *bound
			scoped.SetSelectors(gate, member)
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
// error.
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
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

// scopeSelectors compiles a rule's file filters for one scope. A legacy
// [test.X] list has TWO readings, and an archive-member rule is subject to
// BOTH: the dispatch gate that decides whether the ARCHIVE is scanned reads it
// as regexes over the archive's own name, while the gate that decides which
// MEMBERS are scanned reads it as case-insensitive literal substrings over the
// member path. Each is preserved here in the place that had it; admitNone
// reports the whitelist that selected nothing.
//
// Collapsing the two readings into one is R9's job - do not do it here, it is a
// user-visible config-surface change and this commit is behaviour-preserving.
func scopeSelectors(spec config.RuleSpec, scope checks.Scope) (gate selector.Selector, member *selector.Selector, admitNone bool, err error) {
	subject := "name"
	if scope == checks.ScopeRepository {
		subject = "path"
	}
	gate, err = selector.CompileLegacyRegexLists(spec.Name, subject, spec.Include, spec.Exclude)
	if err != nil || scope != checks.ScopeArchiveMember {
		return gate, nil, false, err
	}
	member, admitNone, err = selector.CompileLegacyLists(spec.Name, spec.Include, spec.Exclude)
	if err != nil || admitNone {
		return selector.Selector{}, nil, admitNone, err
	}
	return gate, member, false, nil
}

// buildMemberAdmission gives every archive-member entry the filter the iterator
// runs in front of the per-rule member gates. With ONE such rule - every shipped
// config's case - it is that rule's own member selector and the per-rule gates
// are skipped entirely; with more it is a single pass over the union of their
// literals, and each rule is still consulted for the members that pass. A rule
// that may not take part in a union (see selector.UnionLiterals) disables the
// pass, so no member is ever skipped on its behalf.
//
// The decision is made HERE, over the whole plan, and recorded on the entry: it
// describes what the iterator was given, which the rules matching one archive
// cannot tell.
func (p *Plan) buildMemberAdmission() {
	entries := p.scopes[checks.ScopeArchiveMember]
	var rules []*checks.BoundRule
	for _, entry := range entries {
		rules = append(rules, entry.rules...)
	}
	if len(rules) == 0 {
		return
	}
	if len(rules) == 1 {
		entries[0].batch.Admit = rules[0].Member
		return
	}
	for i := range entries {
		entries[i].batch.PerRule = true
	}
	var include []string
	for _, rule := range rules {
		if rule.Member == nil {
			return // admits every member: nothing may be skipped for it
		}
		literals, ok := rule.Member.UnionLiterals()
		if !ok {
			return
		}
		for _, literal := range literals {
			include = append(include, regexp.QuoteMeta(literal))
		}
	}
	// Subject "path": the union carries member-path literals, so it must match
	// the same subject the per-rule member gates do.
	union, err := selector.Compile(selector.Spec{Rule: "archive members", Subject: "path", Include: include})
	if err != nil {
		return
	}
	for i := range entries {
		entries[i].batch.Admit = &union
	}
}

// legacyRuleSpecs translates the [test.X] sections into rule specs: one section
// becomes ONE rule carrying its N parameter sets, never N rules. Every
// registered check no section names gets a default rule with an empty selector
// and the parameters its own Bind produces for the zero spec - without that,
// translation would silently delete every section-less check.
//
// The specs come out in the registry's DECLARED order, not sorted: that order is
// the order the dispatch runs a file's checks in, and therefore the order
// findings are rendered in. Sorting it would reorder every report.
func legacyRuleSpecs(cfg *config.Config, reg checks.Registry) ([]config.RuleSpec, []error) {
	var errs []error
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

	specs := make([]config.RuleSpec, 0, reg.Len())
	for _, def := range reg.Defs() {
		name := def.Name
		spec := config.RuleSpec{Name: name, Check: name, Enabled: true}
		section := cfg.Tests[name]
		if section != nil {
			spec.Include, spec.Exclude = section.Whitelist, section.Blacklist
			spec.Params = legacyParams(section)
		}
		switch name {
		case "ReadMeContainsTOC":
			// "What counts as the readme" has exactly one definition: the TOC
			// check binds [test.HasReadme]'s list, never a second copy. With no
			// section of its own it takes that section's file filter too, so
			// both readme checks see the same narrowed file set - one section,
			// one rule, read by two checks.
			if readme := cfg.Tests["HasReadme"]; readme != nil {
				spec.Params = legacyParams(readme)
				if section == nil {
					spec.Include, spec.Exclude = readme.Whitelist, readme.Blacklist
				}
			}
		case "IsFreeOfSecrets":
			// The scan's phase gate used to be an attrs peek; it is the rule's
			// own enabled flag now. A disabled rule leaves the plan here, so its
			// lists are never compiled: the gate that refuses a config the scan
			// could not honour is config.ValidateChecksConfig, at boot.
			spec.Enabled = false
			if section != nil {
				spec.Enabled, _ = section.Attrs["enabled"].(bool)
			}
		}
		specs = append(specs, spec)
	}
	return specs, errs
}

func legacyParams(section *config.TestConfig) map[string]interface{} {
	return map[string]interface{}{
		checks.ParamSets:  section.KeywordArguments,
		checks.ParamAttrs: section.Attrs,
	}
}
