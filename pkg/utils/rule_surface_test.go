package utils

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// baseTOML is the [general] section every [[rule]] fixture below shares.
const baseTOML = "[general]\nmaxContentScanFileSize = 1048576\n\n"

// anchors declares the checks Compile requires a rule for (derived from
// checks.AnchoredChecks, so fixtures cannot drift from the code), minus the
// ones the fixture declares itself, so a success-path fixture stays a complete
// config. The keyword anchor is scoped to "file" so a fixture probing the
// archive-member scope keeps that scope to itself.
func anchors(except ...string) string {
	var doc strings.Builder
	for _, check := range checks.AnchoredChecks() {
		if slices.Contains(except, check) {
			continue
		}
		doc.WriteString("[[rule]]\nname  = \"anchor-" + check + "\"\ncheck = \"" + check + "\"\n")
		if check == "IsFreeOfKeywords" {
			doc.WriteString("scope = [\"file\"]\n")
		}
		doc.WriteString("\n")
	}
	return doc.String()
}

// loadAndCompile drives doc through the REAL pipeline both frontends use:
// LoadConfig, then Compile against the real registry.
func loadAndCompile(t *testing.T, doc string) (*Plan, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pc.toml")
	if err := os.WriteFile(path, []byte(baseTOML+doc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return Compile(cfg, checks.NewRegistry())
}

// mustFail asserts the pipeline refuses doc with an error naming every want.
func mustFail(t *testing.T, doc string, want ...string) {
	t.Helper()
	_, err := loadAndCompile(t, doc)
	if err == nil {
		t.Fatal("expected a load error")
	}
	for _, sub := range want {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error must name %q: %v", sub, err)
		}
	}
}

func TestRuleDuplicateName(t *testing.T) {
	mustFail(t, `
[[rule]]
name  = "twice"
check = "HasOnlyASCII"

[[rule]]
name  = "twice"
check = "HasNoWhiteSpace"
`, "twice", "duplicate rule name")
}

func TestRuleUnknownCheck(t *testing.T) {
	mustFail(t, `
[[rule]]
name  = "typo"
check = "HasOnlyAscii"
`, "typo", "unknown check")
}

func TestRuleInvalidRegex(t *testing.T) {
	// The error must carry the selector fault's own wording - the faulty list
	// position and the regexp engine's diagnosis - not just any substring.
	mustFail(t, `
[[rule]]
name    = "bad-pattern"
check   = "HasOnlyASCII"
include = ["("]
`, "bad-pattern", "include[0]", "missing closing")
}

func TestRuleEmptyPatternRejected(t *testing.T) {
	mustFail(t, `
[[rule]]
name    = "empty-pattern"
check   = "HasOnlyASCII"
exclude = [""]
`, "empty-pattern", "empty pattern")
}

func TestRuleUnsupportedScope(t *testing.T) {
	mustFail(t, `
[[rule]]
name  = "wrong-scope"
check = "IsFreeOfKeywords"
scope = ["repository"]
`, "wrong-scope", "does not support scope")

	mustFail(t, `
[[rule]]
name  = "no-such-scope"
check = "IsFreeOfKeywords"
scope = ["nonsense"]
`, "no-such-scope", "unknown scope")
}

func TestRuleUnknownParamKey(t *testing.T) {
	mustFail(t, `
[[rule]]
name  = "typo-param"
check = "IsFreeOfKeywords"
  [rule.params]
  keywords = ["password"]
  info     = "found"
  keyword  = ["typo"]
`, "typo-param", `unknown key "keyword"`)

	// A check that takes no parameters refuses any [rule.params] key.
	mustFail(t, `
[[rule]]
name  = "params-on-paramless"
check = "HasOnlyASCII"
  [rule.params]
  keywords = ["password"]
`, "params-on-paramless", "takes no parameters")
}

func TestRuleContradictoryIncludeExclude(t *testing.T) {
	mustFail(t, `
[[rule]]
name    = "self-cancelling"
check   = "HasOnlyASCII"
include = ["\\.csv$"]
exclude = ["\\.csv$"]
`, "self-cancelling", "include AND exclude")

	// The gate's honest scope ends there: an exclude that HAPPENS to match
	// everything is legal - recognizing ".*" while missing its infinitely many
	// spellings would be a dishonest promise, so the load accepts it.
	if _, err := loadAndCompile(t, anchors()+`
[[rule]]
name    = "excludes-everything"
check   = "HasOnlyASCII"
exclude = [".*"]
`); err != nil {
		t.Fatalf("an exclude-all pattern is the operator's business: %v", err)
	}
}

// TestRuleExcludeWinsPrecedence pins the one defined precedence of the new
// surface: exclude beats include, against the declared subject.
func TestRuleExcludeWinsPrecedence(t *testing.T) {
	plan, err := loadAndCompile(t, anchors()+`
[[rule]]
name    = "csv-outside-raw"
check   = "HasOnlyASCII"
subject = "path"
include = ["\\.csv$"]
exclude = ["^raw/"]
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rule := planRule(t, plan, "HasOnlyASCII", checks.ScopeFile)
	if rule.Match(structs.File{Name: "a.csv", RelPath: "raw/a.csv"}) {
		t.Error("exclude must win over include")
	}
	if !rule.Match(structs.File{Name: "b.csv", RelPath: "docs/b.csv"}) {
		t.Error("an included, non-excluded path must be admitted")
	}
	if rule.Match(structs.File{Name: "c.txt", RelPath: "docs/c.txt"}) {
		t.Error("a path no include pattern matches must be refused")
	}
}

// TestRuleDisabledLeavesPlan pins the new surface's off switch: enabled = false
// validates like a live rule but is dispatched nowhere, and no default rule is
// synthesized in its place - the operator said off.
func TestRuleDisabledLeavesPlan(t *testing.T) {
	plan, err := loadAndCompile(t, anchors("HasReadme")+`
[[rule]]
name    = "no-readme-check"
check   = "HasReadme"
enabled = false
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, entry := range plan.scope(checks.ScopeRepository) {
		if entry.def.Name == "HasReadme" {
			t.Fatal("a disabled rule's check must not be dispatched")
		}
	}
}

// TestRuleMemberScopeGatesMembers pins §3.2's one semantics at archive-member
// scope: the selector addresses MEMBERS, so the dispatch gate admits every
// archive and the member gate carries the pattern - matched per the rule's
// subject, which defaults to "path" in this scope.
func TestRuleMemberScopeGatesMembers(t *testing.T) {
	plan, err := loadAndCompile(t, anchors("IsFreeOfKeywords")+`
[[rule]]
name    = "csv-members"
check   = "IsFreeOfKeywords"
scope   = ["archive-member"]
include = ["\\.csv$"]
  [rule.params]
  keywords = ["password"]
  info     = "found"
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rule := planRule(t, plan, "IsFreeOfKeywords", checks.ScopeArchiveMember)
	if !rule.Unfiltered() {
		t.Error("a member rule's dispatch gate must admit every archive")
	}
	if rule.Member == nil {
		t.Fatal("the member gate must carry the rule's selector")
	}
	if !rule.Member.Match("one.csv") || rule.Member.Match("two.txt") {
		t.Error("the member gate must match the pattern against members")
	}
	// One member rule: the iterator filters through that rule's own selector.
	entry := planEntry(t, plan, "IsFreeOfKeywords", checks.ScopeArchiveMember)
	if entry.batch.Admit != rule.Member || entry.batch.PerRule {
		t.Error("with one member rule the iterator must use its selector directly")
	}
}

// TestCompileShippedConfigs is R9's gate: every shipped config through
// LoadConfig + Compile. It must live HERE, not in pkg/config -
// TestParseShippedConfigsLoad cannot reach Compile, and LoadConfig alone
// rejects neither an unknown check name, nor a bad param type, nor an
// unsupported scope.
func TestCompileShippedConfigs(t *testing.T) {
	for _, path := range []string{
		"../../pc.toml",
		"../../pc.toml.example",
		"../../testdata/test_config.toml",
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("%s is not in the tree (pc.toml is a local, gitignored config)", path)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			plan, err := Compile(cfg, checks.NewRegistry())
			if err != nil {
				t.Fatalf("compile config: %v", err)
			}
			// Every shipped config carries ONE keyword rule (its groups ride as
			// [[rule.params]] sets), so the archive path takes the
			// single-member-rule admission: the iterator gets that rule's own
			// selector and the per-rule gates stay off.
			entry := planEntry(t, plan, "IsFreeOfKeywords", checks.ScopeArchiveMember)
			if len(entry.rules) != 1 || entry.batch.PerRule {
				t.Errorf("shipped configs must keep the single-member-rule fast path: %d rules, PerRule %v", len(entry.rules), entry.batch.PerRule)
			}
		})
	}
}

// TestRuleAnchorsRequired pins the restored pre-rules contract: the keyword,
// name and readme checks are load-bearing enough that a config silent about
// them refuses the load instead of running defaults.
func TestRuleAnchorsRequired(t *testing.T) {
	mustFail(t, "",
		`check "IsFreeOfKeywords" is not configured`,
		`check "IsValidName" is not configured`,
		`check "HasReadme" is not configured`)
}

// TestRuleDuplicateScope pins that a scope named twice is a load error: the
// rule would enter that scope's plan twice and double its findings.
func TestRuleDuplicateScope(t *testing.T) {
	mustFail(t, `
[[rule]]
name  = "twice-scoped"
check = "IsFreeOfKeywords"
scope = ["file", "file"]
  [rule.params]
  keywords = ["password"]
  info     = "found"
`, "twice-scoped", "declared twice")
}

// TestRuleReadmeDefinitionCluster pins the single-source contract around the
// readme: parameters on ReadMeContainsTOC are refused (it inherits), and a
// second HasReadme rule is refused (it would define the readme twice).
func TestRuleReadmeDefinitionCluster(t *testing.T) {
	mustFail(t, anchors()+`
[[rule]]
name  = "toc-names"
check = "ReadMeContainsTOC"
  [rule.params]
  readme_names = ["manual.rst"]
`, "toc-names", "takes no parameters")

	mustFail(t, anchors("HasReadme")+`
[[rule]]
name  = "readme-one"
check = "HasReadme"

[[rule]]
name  = "readme-two"
check = "HasReadme"
`, "HasReadme", "exactly one rule")

	// Every offending TOC rule is named at once, not one per load.
	mustFail(t, anchors()+`
[[rule]]
name  = "toc-one"
check = "ReadMeContainsTOC"
  [rule.params]
  readme_names = ["a.md"]

[[rule]]
name  = "toc-two"
check = "ReadMeContainsTOC"
  [rule.params]
  readme_names = ["b.md"]
`, "toc-one", "toc-two", "takes no parameters")
}

// TestRuleAmbiguousReadmeBlamesOnlyDeclaredRules pins the other ordering of the
// refused pair - the parameter fault on the FIRST readme rule - and what the
// refusal must not make of it: two DECLARED definitions of the readme elect
// neither, so the synthesized TOC rule binds its own defaults and an ambiguous
// readme blames no rule but the two the operator wrote. Both rules carry a bad
// key, so electing either of them would show up, and both faults must be named.
// Switching the second one off changes nothing: the config still declares the
// readme twice, and the refusal and the sharing read that same count.
func TestRuleAmbiguousReadmeBlamesOnlyDeclaredRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		second string
	}{
		{"both enabled", ""},
		{"second disabled", "enabled = false\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadAndCompile(t, anchors("HasReadme")+`
[[rule]]
name  = "readme-one"
check = "HasReadme"
  [rule.params]
  readme_nmaes = ["manual.rst"]

[[rule]]
name  = "readme-two"
check = "HasReadme"
`+tc.second+`
  [rule.params]
  raedme_names = ["handbook.rst"]
`)
			if err == nil {
				t.Fatal("expected a load error")
			}
			for _, want := range []string{
				`check "HasReadme" allows exactly one rule`,
				`rule "readme-one": params: unknown key "readme_nmaes"`,
				`rule "readme-two": params: unknown key "raedme_names"`,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must name %q: %v", want, err)
				}
			}
			if phantom := config.DefaultRulePrefix + "ReadMeContainsTOC"; strings.Contains(err.Error(), phantom) {
				t.Errorf("no fault may be reported against %q, which no config declares: %v", phantom, err)
			}
		})
	}
}

// TestRuleDisabledReadmeLeavesTOCDefaults pins the disabled half of the same
// contract: a DISABLED HasReadme rule contributes nothing, so the synthesized
// ReadMeContainsTOC falls back to the check's default names - which recognize
// readme.md, proving the disabled rule's list was not inherited.
func TestRuleDisabledReadmeLeavesTOCDefaults(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "readme.md")
	if err := os.WriteFile(readme, []byte("lists nothing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := loadAndCompile(t, anchors("HasReadme")+`
[[rule]]
name    = "readme-off"
check   = "HasReadme"
enabled = false
  [rule.params]
  readme_names = ["manual.rst"]
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	files := []structs.File{
		{Path: readme, Name: "readme.md", RelPath: "readme.md"},
		{Path: filepath.Join(dir, "data.csv"), Name: "data.csv", RelPath: "data.csv"},
	}
	toc := 0
	for _, m := range applyChecksFilteredByRepository(context.Background(), &diagSink{}, plan.scope(checks.ScopeRepository), files) {
		if m.TestName == "ReadMeContainsTOC" && strings.Contains(m.Content, "data.csv") {
			toc++
		}
	}
	if toc != 1 {
		t.Fatalf("the default names must recognize readme.md and report the missing file, got %d TOC findings", toc)
	}
}

// TestRuleNameMayShadowCheckName pins the synthesized rules' namespace: a
// [[rule]] reusing a check's name for ANOTHER check must not collide with the
// default rule synthesized for that check.
func TestRuleNameMayShadowCheckName(t *testing.T) {
	plan, err := loadAndCompile(t, anchors()+`
[[rule]]
name  = "HasNoWhiteSpace"
check = "HasOnlyASCII"
`)
	if err != nil {
		t.Fatalf("a rule named after another check must not collide with its default rule: %v", err)
	}
	planRule(t, plan, "HasOnlyASCII", checks.ScopeFile)
	// The shadowed check's default rule is still synthesized and dispatched.
	planRule(t, plan, "HasNoWhiteSpace", checks.ScopeFile)
}

// TestRuleMemberSubjectDefaultsToPath pins the member scope's subject default:
// the selector addresses member PATHS unless the rule declares otherwise, so a
// pattern like ^data/ works - under a "name" default it would silently match
// nothing. A declared subject stays respected.
func TestRuleMemberSubjectDefaultsToPath(t *testing.T) {
	plan, err := loadAndCompile(t, anchors("IsFreeOfKeywords")+`
[[rule]]
name    = "data-members"
check   = "IsFreeOfKeywords"
scope   = ["archive-member"]
include = ["^data/"]
  [rule.params]
  keywords = ["password"]
  info     = "found"
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rule := planRule(t, plan, "IsFreeOfKeywords", checks.ScopeArchiveMember)
	if rule.Member == nil || rule.Member.Subject() != selector.SubjectPath {
		t.Fatal("an undeclared subject must default to \"path\" at archive-member scope")
	}
	if !rule.Member.Match("data/one.csv") || rule.Member.Match("docs/one.csv") {
		t.Error("the member selector must match the member path")
	}

	declared, err := loadAndCompile(t, anchors("IsFreeOfKeywords")+`
[[rule]]
name    = "named-members"
check   = "IsFreeOfKeywords"
scope   = ["archive-member"]
subject = "name"
include = ["\\.csv$"]
  [rule.params]
  keywords = ["password"]
  info     = "found"
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	named := planRule(t, declared, "IsFreeOfKeywords", checks.ScopeArchiveMember)
	if named.Member == nil || named.Member.Subject() != selector.SubjectName {
		t.Fatal("a declared subject must stay respected at archive-member scope")
	}
}

// TestRuleRepositorySubjectDefaultsToPath pins the repository scope's subject
// default: the selector addresses the collection-relative PATH unless the rule
// declares otherwise - the string the legacy lists always matched there, so a
// migrated blacklist keeps matching. A declared subject stays respected.
func TestRuleRepositorySubjectDefaultsToPath(t *testing.T) {
	plan, err := loadAndCompile(t, anchors("HasReadme")+`
[[rule]]
name    = "data-tree"
check   = "HasReadme"
include = ["^data"]
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rule := planRule(t, plan, "HasReadme", checks.ScopeRepository)
	if !rule.Match(structs.File{Name: "notes.txt", RelPath: "data/notes.txt"}) {
		t.Error("an undeclared subject must match the collection-relative path at repository scope")
	}
	if rule.Match(structs.File{Name: "data.csv", RelPath: "docs/data.csv"}) {
		t.Error("the path default must not fall back to matching the file name")
	}

	declared, err := loadAndCompile(t, anchors("HasReadme")+`
[[rule]]
name    = "named-readme"
check   = "HasReadme"
subject = "name"
include = ["^data"]
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	named := planRule(t, declared, "HasReadme", checks.ScopeRepository)
	if !named.Match(structs.File{Name: "data.csv", RelPath: "docs/data.csv"}) ||
		named.Match(structs.File{Name: "notes.txt", RelPath: "data/notes.txt"}) {
		t.Error("a declared \"name\" subject must stay respected at repository scope")
	}
}

// TestLegacyTOCSectionParamsIgnored pins the transitional leniency: a legacy
// [test.ReadMeContainsTOC] carrying keywordArguments keeps loading - its own
// params were always ignored in favour of the HasReadme list - while the
// [[rule]] surface's hard refusal stays (TestRuleReadmeDefinitionCluster).
func TestLegacyTOCSectionParamsIgnored(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "myreadme.md")
	if err := os.WriteFile(readme, []byte("lists nothing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := loadAndCompile(t, anchors("HasReadme")+`
[test.HasReadme]
keywordArguments = [{readme_names = ["myreadme.md"]}]

[test.ReadMeContainsTOC]
keywordArguments = [{readme_names = ["other.rst"]}]
`)
	if err != nil {
		t.Fatalf("a legacy TOC section with params must keep loading: %v", err)
	}
	files := []structs.File{
		{Path: readme, Name: "myreadme.md", RelPath: "myreadme.md"},
		{Path: filepath.Join(dir, "data.csv"), Name: "data.csv", RelPath: "data.csv"},
	}
	toc := 0
	for _, m := range applyChecksFilteredByRepository(context.Background(), &diagSink{}, plan.scope(checks.ScopeRepository), files) {
		if m.TestName == "ReadMeContainsTOC" && strings.Contains(m.Content, "data.csv") {
			toc++
		}
	}
	if toc != 1 {
		t.Fatalf("HasReadme's list must win over the TOC section's own params, got %d TOC findings", toc)
	}
}

// TestRuleTOCInheritsLegacyLeniency pins the provenance of inherited params:
// a declared [[rule]] TOC reading a legacy [test.HasReadme] section's list
// keeps that surface's lenient key reading - a stray legacy key must not
// boot-fail with an error misattributed to the TOC rule.
func TestRuleTOCInheritsLegacyLeniency(t *testing.T) {
	plan, err := loadAndCompile(t, anchors("HasReadme")+`
[test.HasReadme]
keywordArguments = [{readme_names = ["readme.md"], comment = "legacy keys read leniently"}]

[[rule]]
name  = "toc-check"
check = "ReadMeContainsTOC"
`)
	if err != nil {
		t.Fatalf("inherited legacy params must keep their lenient reading: %v", err)
	}
	planRule(t, plan, "ReadMeContainsTOC", checks.ScopeRepository)
}
