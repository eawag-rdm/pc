package checks

import (
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// bindTestRule binds the named check's rule from the legacy [test.<name>]
// section of cfg, exactly as utils.Compile does at startup - pkg/utils cannot
// be imported here (it imports this package), so the translation the checks
// tests need is spelled out once, in one place.
func bindTestRule(t testing.TB, name string, cfg config.Config, scope Scope) (CheckDef, *BoundRule, *Batch) {
	t.Helper()
	def, known := NewRegistry().Lookup(name)
	if !known {
		t.Fatalf("check %q is not registered", name)
	}
	section := cfg.Tests[name]
	if name == "ReadMeContainsTOC" {
		section = cfg.Tests["HasReadme"] // the readme list has exactly one definition
	}
	spec := config.RuleSpec{Name: name, Check: name, Enabled: true}
	if section != nil {
		spec.Include, spec.Exclude = section.Whitelist, section.Blacklist
		spec.Params = map[string]interface{}{ParamSets: section.KeywordArguments, ParamAttrs: section.Attrs}
	}

	general := cfg.General
	if general == nil {
		general = &config.GeneralConfig{}
	}
	rule, err := def.Bind(spec, general)
	if err != nil {
		t.Fatalf("bind rule %q: %v", name, err)
	}

	// The two legacy readings, split exactly as utils.Compile splits them: the
	// dispatch gate is the regex reading over the file name, the member gate the
	// case-insensitive literal reading over the member path.
	subject := "name"
	if scope == ScopeRepository {
		subject = "path"
	}
	gate, err := selector.CompileLegacyRegexLists(name, subject, spec.Include, spec.Exclude)
	if err != nil {
		t.Fatalf("compile selector for %q: %v", name, err)
	}
	var member *selector.Selector
	if scope == ScopeArchiveMember {
		var admitNone bool
		if member, admitNone, err = selector.CompileLegacyLists(name, spec.Include, spec.Exclude); err != nil || admitNone {
			t.Fatalf("compile member selector for %q: (%v, %v)", name, admitNone, err)
		}
	}
	rule.SetSelectors(gate, member)
	return def, rule, &Batch{limits: archiveLimits(general), maxContentScan: general.MaxContentScanFileSize, Admit: member}
}

// runRule runs one check over one file through its bound rule.
func runRule(t testing.TB, name string, cfg config.Config, scope Scope, file structs.File) []structs.Message {
	t.Helper()
	def, rule, batch := bindTestRule(t, name, cfg, scope)
	return def.RunFile(file, scope, batch, []*BoundRule{rule})
}

// runRepoRule is runRule for the repository-scoped checks, whose rule narrows
// the file set before the check sees it.
func runRepoRule(t testing.TB, name string, cfg config.Config, repository structs.Repository) []structs.Message {
	t.Helper()
	def, rule, batch := bindTestRule(t, name, cfg, ScopeRepository)
	return def.RunRepository(repository, batch, []*BoundRule{rule})
}

// TestRegistryScopesCoverDispatch pins the registry against the dispatch phases
// it must serve: every check names at least one scope, sets exactly the runner
// that scope needs, and the keyword check owns BOTH the file and the
// archive-member scope - the alias that used to be a second check.
func TestRegistryScopesCoverDispatch(t *testing.T) {
	want := map[string][]Scope{
		"HasOnlyASCII":            {ScopeFile, ScopeArchiveFileList},
		"HasNoWhiteSpace":         {ScopeFile, ScopeArchiveFileList},
		"IsValidName":             {ScopeFile, ScopeArchiveFileList},
		"HasFileNameSpecialChars": {ScopeFile},
		"IsFileNameTooLong":       {ScopeFile},
		"IsFreeOfKeywords":        {ScopeFile, ScopeArchiveMember},
		"HasReadme":               {ScopeRepository},
		"ReadMeContainsTOC":       {ScopeRepository},
		"IsFreeOfSecrets":         {ScopeRepository},
	}
	registry := NewRegistry()
	if registry.Len() != len(want) {
		t.Fatalf("registry holds %d checks, want %d", registry.Len(), len(want))
	}
	for name, scopes := range want {
		def, known := registry.Lookup(name)
		if !known {
			t.Fatalf("check %q is not registered", name)
		}
		for scope := Scope(0); scope < NumScopes; scope++ {
			expected := false
			for _, s := range scopes {
				expected = expected || s == scope
			}
			if def.Scopes.Has(scope) != expected {
				t.Errorf("%s: scope %s = %v, want %v", name, scope, def.Scopes.Has(scope), expected)
			}
		}
		repository := def.Scopes.Has(ScopeRepository)
		if repository && (def.RunRepository == nil || def.RunFile != nil) {
			t.Errorf("%s: a repository check must set RunRepository and only that", name)
		}
		if !repository && (def.RunFile == nil || def.RunRepository != nil) {
			t.Errorf("%s: a file check must set RunFile and only that", name)
		}
		if def.Bind == nil {
			t.Errorf("%s: no Bind", name)
		}
	}
}

// TestRegistryDeclaredOrder pins the order utils.Compile adds the rules to the
// plan in, which is the order a file's checks run in and therefore the order
// findings are rendered in. It is the order of the five dispatch tables this
// registry replaced (file scope, then the archive file list and archive member
// subsets of it, then the repository phase with the leak scan first), so a
// report of one file with several findings reads exactly as before.
func TestRegistryDeclaredOrder(t *testing.T) {
	want := []string{
		"HasOnlyASCII", "HasNoWhiteSpace", "IsFreeOfKeywords", "IsValidName",
		"HasFileNameSpecialChars", "IsFileNameTooLong",
		"IsFreeOfSecrets", "HasReadme", "ReadMeContainsTOC",
	}
	defs := NewRegistry().Defs()
	if len(defs) != len(want) {
		t.Fatalf("registry holds %d checks, want %d", len(defs), len(want))
	}
	for i, name := range want {
		if defs[i].Name != name {
			t.Errorf("check %d is %q, want %q - rendered message order follows this order", i, defs[i].Name, name)
		}
	}
}

// TestMatchMemberGate pins the member gate the archive loop consults once per
// member: nil admits everything, and a compiled legacy member selector keeps
// the case-insensitive LITERAL reading over the full member path.
func TestMatchMemberGate(t *testing.T) {
	member, admitNone, err := selector.CompileLegacyLists("IsFreeOfKeywords", []string{".log"}, nil)
	if err != nil || admitNone || member == nil {
		t.Fatalf("compile member selector: (%v, %v, %v)", member, admitNone, err)
	}
	gated := &BoundRule{}
	gated.SetSelectors(selector.Selector{}, member)

	cases := []struct {
		name string
		rule *BoundRule
		path string
		want bool
	}{
		{"nil member gate admits everything", &BoundRule{}, "deep/run.LOG", true},
		{"case-insensitive literal over the member path", gated, "deep/run.LOG", true},
		{"non-matching path refused", gated, "deep/notes.txt", false},
	}
	for _, tc := range cases {
		if got := tc.rule.matchMember(tc.path); got != tc.want {
			t.Errorf("%s: matchMember(%q) = %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}

// TestRegistryDefsReturnsCopy pins Defs' copy semantics: reordering the
// returned slice must not disturb the registry's own declared order, which is
// load-bearing (see TestRegistryDeclaredOrder).
func TestRegistryDefsReturnsCopy(t *testing.T) {
	registry := NewRegistry()
	leaked := registry.Defs()
	declared := make([]string, len(leaked))
	for i, def := range leaked {
		declared[i] = def.Name
	}
	for i, j := 0, len(leaked)-1; i < j; i, j = i+1, j-1 {
		leaked[i], leaked[j] = leaked[j], leaked[i]
	}
	for i, def := range registry.Defs() {
		if def.Name != declared[i] {
			t.Fatalf("check %d is %q after reordering Defs' result, want %q - Defs must return a copy", i, def.Name, declared[i])
		}
	}
}

// TestParseScopeRoundTrip pins the declared scope names, which the [[rule]]
// surface will spell out.
func TestParseScopeRoundTrip(t *testing.T) {
	for scope := Scope(0); scope < NumScopes; scope++ {
		parsed, err := ParseScope(scope.String())
		if err != nil || parsed != scope {
			t.Errorf("ParseScope(%q) = (%v, %v), want %v", scope.String(), parsed, err, scope)
		}
	}
	if _, err := ParseScope("nonsense"); err == nil {
		t.Error("an unknown scope name must be an error")
	}
}
