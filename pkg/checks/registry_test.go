package checks

import (
	"os"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// testsWithAnchors copies cfg's legacy sections and fills the anchored checks
// (which RuleSpecs refuses to leave undeclared) with empty sections, so a
// fixture that configures only the check under test stays a complete config.
func testsWithAnchors(tests map[string]*config.TestConfig, rules []config.RuleSpec) map[string]*config.TestConfig {
	declared := func(name string) bool {
		if tests[name] != nil {
			return true
		}
		for _, rule := range rules {
			if rule.Check == name {
				return true
			}
		}
		return false
	}
	anchored := AnchoredChecks()
	filled := make(map[string]*config.TestConfig, len(tests)+len(anchored))
	for name, section := range tests {
		filled[name] = section
	}
	for _, name := range anchored {
		if !declared(name) {
			filled[name] = &config.TestConfig{}
		}
	}
	return filled
}

// bindTestRule binds EVERY rule cfg declares for the named check, through the
// SAME assembly and selector compilation utils.Compile uses at startup
// (RuleSpecs + CompileRuleSelectors, both in this package precisely so the two
// callers cannot drift - pkg/utils cannot be imported here, it imports this
// package). The batch mirrors Compile's member-admission decision through the
// same MemberAdmission call Compile makes.
func bindTestRule(t testing.TB, name string, cfg config.Config, scope Scope) (CheckDef, []*BoundRule, *Batch) {
	t.Helper()
	registry := NewRegistry()
	def, known := registry.Lookup(name)
	if !known {
		t.Fatalf("check %q is not registered", name)
	}
	cfg.Tests = testsWithAnchors(cfg.Tests, cfg.Rules)
	specs, err := RuleSpecs(&cfg, registry)
	if err != nil {
		t.Fatalf("assemble rule specs: %v", err)
	}

	general := cfg.General
	if general == nil {
		general = &config.GeneralConfig{}
	}
	var rules []*BoundRule
	for _, spec := range specs {
		if spec.Check != name {
			continue
		}
		rule, err := def.Bind(spec, general)
		if err != nil {
			t.Fatalf("bind rule %q: %v", spec.Name, err)
		}
		selectors, err := CompileRuleSelectors(spec, []Scope{scope})
		if err != nil {
			t.Fatalf("compile selectors for %q: %v", spec.Name, err)
		}
		if selectors[0].AdmitNone {
			t.Fatalf("rule %q admits nothing in scope %s", spec.Name, scope)
		}
		rule.SetSelectors(selectors[0])
		rules = append(rules, rule)
	}
	if len(rules) == 0 {
		t.Fatalf("no rule spec for check %q", name)
	}
	batch := &Batch{limits: archiveLimits(general), maxContentScan: general.MaxContentScanFileSize}
	batch.Admit, batch.PerRule = MemberAdmission(rules)
	return def, rules, batch
}

// runRule runs one check over one file through its bound rules, batched as the
// dispatch batches them.
func runRule(t testing.TB, name string, cfg config.Config, scope Scope, file structs.File) []structs.Message {
	t.Helper()
	def, rules, batch := bindTestRule(t, name, cfg, scope)
	return def.RunFile(file, scope, batch, rules)
}

// runRepoRule is runRule for the repository-scoped checks, whose rules narrow
// the file set before the check sees it.
func runRepoRule(t testing.TB, name string, cfg config.Config, repository structs.Repository) []structs.Message {
	t.Helper()
	def, rules, batch := bindTestRule(t, name, cfg, ScopeRepository)
	return def.RunRepository(repository, batch, rules)
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
	}
	for _, def := range registry.Defs() {
		if def.Scopes == 0 {
			t.Errorf("%s: no scopes", def.Name)
		}
		fileScoped := def.Scopes.Has(ScopeFile) || def.Scopes.Has(ScopeArchiveFileList) || def.Scopes.Has(ScopeArchiveMember)
		if fileScoped && (def.RunFile == nil || def.RunRepository != nil) {
			t.Errorf("%s: a file-scoped check must set RunFile and only that", def.Name)
		}
		if def.Scopes.Has(ScopeRepository) && (def.RunRepository == nil || def.RunFile != nil) {
			t.Errorf("%s: a repository check must set RunRepository and only that", def.Name)
		}
		if (def.RunFile == nil) == (def.RunRepository == nil) {
			t.Errorf("%s: exactly one of RunFile / RunRepository must be set", def.Name)
		}
		if def.Bind == nil {
			t.Errorf("%s: no Bind", def.Name)
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
	gated.SetSelectors(RuleSelectors{Member: member})

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

// TestArchiveFileListBaseNames pins the base-name reading of a [[rule]] at
// archive-file-list scope: the synthetic file's Name there is the FULL member
// path, so a "name" subject means its BASE name - a basename pattern hits, a
// path-prefix pattern misses - in that scope exactly as at file scope, where
// Name already is the base name. A declared "path" subject keeps addressing
// the whole member path.
func TestArchiveFileListBaseNames(t *testing.T) {
	member := structs.File{Name: "data/report.csv", RelPath: "data/report.csv"}
	plain := structs.File{Name: "report.csv", RelPath: "data/report.csv"}
	bind := func(t *testing.T, subject string, include []string, scope Scope) *BoundRule {
		t.Helper()
		spec := config.RuleSpec{Name: "list-rule", Check: "HasOnlyASCII", Enabled: true, Subject: subject, Include: include}
		selectors, err := CompileRuleSelectors(spec, []Scope{scope})
		if err != nil {
			t.Fatalf("compile selectors: %v", err)
		}
		rule := &BoundRule{}
		rule.SetSelectors(selectors[0])
		return rule
	}

	if !bind(t, "", []string{`^report`}, ScopeArchiveFileList).Match(member) {
		t.Error("a basename pattern must match the member's BASE name at archive-file-list scope")
	}
	if !bind(t, "", []string{`^report`}, ScopeFile).Match(plain) {
		t.Error("a basename pattern must match the file's name at file scope")
	}
	if bind(t, "", []string{`^data/`}, ScopeArchiveFileList).Match(member) {
		t.Error("a path-prefix pattern must not match a member's base name")
	}
	if bind(t, "", []string{`^data/`}, ScopeFile).Match(plain) {
		t.Error("a path-prefix pattern must not match a file's name")
	}
	if !bind(t, "path", []string{`^data/`}, ScopeArchiveFileList).Match(member) {
		t.Error("a declared \"path\" subject must match the full member path")
	}
}

// TestBindKeywordsMultipleParamSets pins the [[rule.params]] batching: ONE
// rule carries N parameter sets, each matched and reported with its own info -
// exactly as a legacy section's keywordArguments list bound.
func TestBindKeywordsMultipleParamSets(t *testing.T) {
	path := tempFile([]byte("the password sits next to Q:\\share\n"))
	defer os.Remove(path)
	general := &config.GeneralConfig{MaxContentScanFileSize: 1024 * 1024}
	def, _ := NewRegistry().Lookup("IsFreeOfKeywords")
	rule, err := def.Bind(config.RuleSpec{
		Name: "multi", Check: "IsFreeOfKeywords", Enabled: true,
		Params: []map[string]interface{}{
			{"keywords": []string{"password"}, "info": "credential keyword"},
			{"keywords": []string{"Q:"}, "info": "internal keyword"},
		},
	}, general)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	msgs := def.RunFile(structs.File{Path: path, Name: "notes.txt"}, ScopeFile, NewBatch(general), []*BoundRule{rule})
	if len(msgs) != 2 {
		t.Fatalf("expected one finding per parameter set, got %v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "credential keyword") || !strings.Contains(msgs[1].Content, "internal keyword") {
		t.Errorf("each set must report with its own info: %v", msgs)
	}
}
