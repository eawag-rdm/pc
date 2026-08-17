// This file dies with the legacy [test.X] surface it compares against.

package utils

import (
	"reflect"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
)

// legacyConfig is planConfig for the surface the fixtures moved off: it keeps
// the [test.X] sections they used to build alive for the comparison below.
func legacyConfig(tests map[string]*config.TestConfig) config.Config {
	return config.Config{
		General: &config.GeneralConfig{MaxContentScanFileSize: config.DefaultMaxContentScanFileSize},
		Tests:   tests,
	}
}

// TestFixtureMigrationKeepsThePlan is the gate on moving this package's
// BENCHMARK fixtures from [test.X] sections onto [[rule]] specs: each pair is
// one benchmark's config as it was and as it is now, and a benchmark whose
// filter workload quietly changed is a baseline nobody can compare against.
// Only these three fixtures are paired - every other migrated fixture belongs
// to a test that fails if it was transcribed wrongly, and TOML-level fixtures
// (main_test.go, pkg/server/server_test.go) declare their rules in text this
// guard never sees.
//
// Identity is asserted at two levels: the assembled specs - every declared
// field but the surface marker - and the compiled plan's shape. It is not
// claimed beyond these fixtures. Two knowingly divergent readings exist in the
// migration at large: at archive-file-list scope a legacy list gates the whole
// member path while a [[rule]] gates its base name, and the leak scan's knobs
// move from a section's attrs table into rule params, which the translation
// never produces.
func TestFixtureMigrationKeepsThePlan(t *testing.T) {
	cases := []struct {
		name     string
		legacy   config.Config
		migrated config.Config
		// scopes narrows the plan comparison; empty compares every scope.
		scopes []checks.Scope
	}{
		{
			// The archive scopes diverge on purpose here: the keyword list moves
			// off the archive's dispatch gate and onto the members, which no
			// legacy section can express. The benchmark reads the file scope, and
			// that is where the workload must not have moved.
			name: "benchFilterConfig",
			legacy: legacyConfig(map[string]*config.TestConfig{
				"HasOnlyASCII":     {Blacklist: []string{`\.png$`, `\.jpg$`}},
				"IsFreeOfKeywords": {Whitelist: []string{`\.txt$`, `\.csv$`}},
				"IsValidName":      {},
			}),
			migrated: benchFilterConfig(),
			scopes:   []checks.Scope{checks.ScopeFile},
		},
		{
			name: "benchUnfilteredConfig",
			legacy: legacyConfig(map[string]*config.TestConfig{
				"HasOnlyASCII":     {},
				"IsFreeOfKeywords": {},
				"IsValidName":      {},
			}),
			migrated: benchUnfilteredConfig(),
		},
		{
			name: "benchPipelineConfig",
			legacy: legacyConfig(map[string]*config.TestConfig{
				"HasOnlyASCII": {},
				"IsFreeOfKeywords": {KeywordArguments: []map[string]interface{}{
					{"keywords": []string{"password"}, "info": "Possible credentials in file"},
				}},
				"IsValidName": {KeywordArguments: []map[string]interface{}{
					{"disallowed_names": []string{".Rhistory", "__pycache__"}},
				}},
			}),
			migrated: benchPipelineConfig(),
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			assertSameSpecs(t, specsOf(t, test.legacy), specsOf(t, test.migrated))
			assertSamePlan(t, compilePlan(t, test.legacy), compilePlan(t, test.migrated), test.scopes)
		})
	}
}

// specsOf assembles what one config declares, through the production assembly
// both frontends run: the translated [test.X] sections on one side, the rules
// the fixture now spells out on the other.
func specsOf(t *testing.T, cfg config.Config) []config.RuleSpec {
	t.Helper()
	cfg = withRequiredAnchors(cfg)
	specs, err := checks.RuleSpecs(&cfg, checks.NewRegistry())
	if err != nil {
		t.Fatalf("assemble rules: %v", err)
	}
	return specs
}

// assertSameSpecs compares the assembled specs field by field - the level the
// translation (checks.legacySectionSpec) is mirrored at, and the only level
// that sees a wrong keyword list or a list moved from include to exclude.
//
// Legacy and Attrs are left out: Legacy IS the surface marker the migration
// removes, and Attrs is that surface's own sugar, which no [[rule]] sets. Both
// die with the sections.
func assertSameSpecs(t *testing.T, legacy, migrated []config.RuleSpec) {
	t.Helper()
	if len(legacy) != len(migrated) {
		t.Fatalf("%d assembled rules, want %d", len(migrated), len(legacy))
	}
	for i, want := range legacy {
		got := migrated[i]
		for _, field := range []struct {
			name      string
			want, got interface{}
		}{
			{"name", want.Name, got.Name},
			{"check", want.Check, got.Check},
			{"scope", want.Scope, got.Scope},
			{"enabled", want.Enabled, got.Enabled},
			{"subject", want.Subject, got.Subject},
			{"ignoreCase", want.IgnoreCase, got.IgnoreCase},
			{"include", want.Include, got.Include},
			{"exclude", want.Exclude, got.Exclude},
			{"params", want.Params, got.Params},
		} {
			if !reflect.DeepEqual(field.want, field.got) {
				t.Errorf("rule %d (%s): %s = %#v, want %#v", i, want.Name, field.name, field.got, field.want)
			}
		}
	}
}

// assertSamePlan reports every way the migrated plan's shape differs from the
// legacy one's: the entries of each scope and their order (the order findings
// are rendered in), the check each entry runs, and per rule its name (which
// tags its findings), whether its dispatch gate admits every file, and whether
// it carries an archive-member gate at all.
func assertSamePlan(t *testing.T, legacy, migrated *Plan, scopes []checks.Scope) {
	t.Helper()
	if len(scopes) == 0 {
		for scope := checks.Scope(0); scope < checks.NumScopes; scope++ {
			scopes = append(scopes, scope)
		}
	}
	for _, scope := range scopes {
		want, got := legacy.scope(scope), migrated.scope(scope)
		if len(want) != len(got) {
			t.Errorf("%s: %d plan entries, want %d", scope, len(got), len(want))
			continue
		}
		for i := range want {
			if got[i].def.Name != want[i].def.Name {
				t.Errorf("%s: entry %d runs check %q, want %q", scope, i, got[i].def.Name, want[i].def.Name)
				continue
			}
			check := want[i].def.Name
			if len(got[i].rules) != len(want[i].rules) {
				t.Errorf("%s %s: %d rules, want %d", scope, check, len(got[i].rules), len(want[i].rules))
				continue
			}
			for j, rule := range want[i].rules {
				moved := got[i].rules[j]
				if moved.Rule != rule.Rule {
					t.Errorf("%s %s: rule %d is named %q, want %q", scope, check, j, moved.Rule, rule.Rule)
					continue
				}
				if moved.Unfiltered() != rule.Unfiltered() {
					t.Errorf("%s %s rule %q: unfiltered gate = %v, want %v", scope, check, rule.Rule, moved.Unfiltered(), rule.Unfiltered())
				}
				if (moved.Member != nil) != (rule.Member != nil) {
					t.Errorf("%s %s rule %q: member gate set = %v, want %v", scope, check, rule.Rule, moved.Member != nil, rule.Member != nil)
				}
			}
		}
	}
}
