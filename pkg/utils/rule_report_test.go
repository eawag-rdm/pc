package utils

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// asciiRule is one [[rule]] for the parameterless name check, as the config
// surface produces it. The check serves the file AND the archive-file-list
// scope, so every case here names the scope it means: an undeclared scope would
// put the same rule in both and double the diagnostics it can produce.
func asciiRule(name string, scope []string, include ...string) config.RuleSpec {
	return config.RuleSpec{
		Name:    name,
		Check:   "HasOnlyASCII",
		Scope:   scope,
		Enabled: true,
		Include: include,
	}
}

// ruleReportFiles writes one file per name into a fresh temp dir. The files are
// real because the file phase acquires content: a missing file logs into the
// process-global logger, which these tests must leave untouched.
func ruleReportFiles(t *testing.T, names ...string) []structs.File {
	t.Helper()
	dir := t.TempDir()
	files := make([]structs.File, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("harmless\n"), 0o600); err != nil {
			t.Fatalf("write fixture %q: %v", name, err)
		}
		files = append(files, structs.ToFile(path, name, -1, ""))
	}
	return files
}

// ruleReportArchives builds two archives whose member sets are DISJOINT: a.zip
// holds only alpha.txt, b.zip only beta.txt. Disjoint is the whole point - two
// archives carrying the same members leave every buffer with the same marks, so
// an OR-fold and a clobbering one are indistinguishable. Two archives are also
// what makes applyArchiveFileListChecksParallel fan its workers out.
//
// buildNamedZip fixes the file name on disk, so each archive's own Name is set
// here: the reader switches on Name's suffix and opens Path, and a failure
// message has to be able to tell the two apart.
func ruleReportArchives(t *testing.T) []structs.File {
	t.Helper()
	archives := []structs.File{
		structs.ToFile(buildNamedZip(t, []string{"alpha.txt"}), "a.zip", -1, ""),
		structs.ToFile(buildNamedZip(t, []string{"beta.txt"}), "b.zip", -1, ""),
	}
	// The disjointness is the fixture's contract, so it is checked rather than
	// assumed: a shared member would silently defuse every fold assertion below.
	seen := map[string]string{}
	for _, archive := range archives {
		if !archive.IsArchive {
			t.Fatalf("fixture %q is not recognised as an archive", archive.Name)
		}
		members := ruleReportMembers(t, archive)
		if len(members) != 1 {
			t.Fatalf("archive %q must hold exactly one member, got %v", archive.Name, members)
		}
		if other, shared := seen[members[0]]; shared {
			t.Fatalf("member %q is in both %q and %q - the archives must be disjoint", members[0], other, archive.Name)
		}
		seen[members[0]] = archive.Name
	}
	return archives
}

// ruleReportMembers asks the reader the same question the archive file list
// phase asks, with the same bounds, so a fixture's member set is verified rather
// than assumed.
func ruleReportMembers(t *testing.T, archive structs.File) []string {
	t.Helper()
	maxMembers, maxMemory := archiveWalkLimits(planConfig(nil))
	list, truncated, err := readers.ReadArchiveFileList(archive, maxMembers, maxMemory)
	if err != nil || truncated {
		t.Fatalf("archive %q must be readable and untruncated: truncated %v, err %v", archive.Name, truncated, err)
	}
	names := make([]string, 0, len(list))
	for _, member := range list {
		names = append(names, member.Name)
	}
	return names
}

// ruleDiagsNaming returns the diagnostics of one level whose text names every
// rule given. Names are matched in their QUOTED form - the way the report spells
// them - so "alpha" never matches a note about "alpha-two", and the assertion
// stays off the exact wording.
func ruleDiagsNaming(diags []structs.Diagnostic, level structs.DiagLevel, names ...string) []structs.Diagnostic {
	var found []structs.Diagnostic
	for _, diag := range diags {
		if diag.Level != level {
			continue
		}
		named := true
		for _, name := range names {
			if !strings.Contains(diag.Message, strconv.Quote(name)) {
				named = false
				break
			}
		}
		if named {
			found = append(found, diag)
		}
	}
	return found
}

// assertRuleDiags asserts how many rule diagnostics of one level name the given
// rules, and that each one carries an EMPTY subject - the operator channel that
// keeps a configuration note out of the depositor-facing response.
func assertRuleDiags(t *testing.T, diags []structs.Diagnostic, level structs.DiagLevel, want int, names ...string) {
	t.Helper()
	found := ruleDiagsNaming(diags, level, names...)
	if len(found) != want {
		t.Fatalf("%s diagnostics naming %v: got %d, want %d (all: %v)", level, names, len(found), want, diags)
	}
	for _, diag := range found {
		if diag.Subject != "" {
			t.Errorf("a rule diagnostic must carry an empty subject, got %q: %s", diag.Subject, diag.Message)
		}
	}
}

// assertNoDeadRule asserts that nothing accuses the named rule of having
// matched no file, at ANY level: the claim is about the accusation, not about
// how loudly it is made, so a level to filter on is one more thing the report
// could move without this ever failing again. It cannot go through
// assertRuleDiags either, because an overlap notice names BOTH rules of its pair
// - a count by name alone would read a notice ABOUT the rule as an accusation
// AGAINST it. deadRuleReason tells the two apart, and it is the producer's own
// const so the two spellings cannot drift.
func assertNoDeadRule(t *testing.T, diags []structs.Diagnostic, name string) {
	t.Helper()
	for _, diag := range diags {
		// Quoted, the way the report spells a rule name, so "alpha" never matches
		// an accusation against "alpha-two".
		if strings.Contains(diag.Message, strconv.Quote(name)) && strings.Contains(diag.Message, deadRuleReason) {
			t.Fatalf("rule %q must not be reported dead: %s", name, diag.Message)
		}
	}
}

// TestRuleReportDeadRuleWarnsOnce pins the dead-rule warning: a file-scope rule
// whose include pattern selects none of the collected files is reported exactly
// once, its sibling on the same check is not reported at all, and a rule that
// merely shares a check with another produces no overlap notice unless the two
// actually met on a file.
func TestRuleReportDeadRuleWarnsOnce(t *testing.T) {
	files := ruleReportFiles(t, "alpha.csv", "beta.csv", "gamma.txt")
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("csv-only", []string{"file"}, `\.csv$`),
		asciiRule("tar-only", []string{"file"}, `\.tar$`),
	}
	plan := compilePlan(t, cfg)

	_, diags := ApplyAllChecks(context.Background(), cfg, plan, files, false)

	assertRuleDiags(t, diags, structs.DiagWarning, 1, "tar-only")
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "csv-only")
	// The two rules never admitted the same file, so nothing may claim they did.
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "csv-only", "tar-only")
}

// TestRuleReportOverlapOnFilteredPath pins the overlap notice on the selection
// pass that actually matches: two rules of one check admitting the same file are
// reported ONCE for the whole run - per (scope, check, rule pair) - however many
// files the two share.
func TestRuleReportOverlapOnFilteredPath(t *testing.T) {
	files := ruleReportFiles(t, "alpha.csv", "alpha_two.csv", "beta.csv", "gamma.txt")
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("csv-rule", []string{"file"}, `\.csv$`),
		asciiRule("alpha-rule", []string{"file"}, `^alpha`),
	}
	plan := compilePlan(t, cfg)

	// Anti-vacuity: two files carry BOTH rules, so a per-file notice would show
	// up as two.
	if allUnfiltered(plan.scope(checks.ScopeFile)) {
		t.Fatal("the fixture must take the filtered selection path")
	}

	_, diags := ApplyAllChecks(context.Background(), cfg, plan, files, false)

	assertRuleDiags(t, diags, structs.DiagWarning, 1, "csv-rule", "alpha-rule")
	assertNoDeadRule(t, diags, "csv-rule")
	assertNoDeadRule(t, diags, "alpha-rule")
}

// TestRuleReportUnfilteredFastPathMarksEveryRule pins the OTHER branch of
// filterChecksForFiles: when no rule filters at all, selection is the identity
// and the pass answers for the whole scope once rather than once per (file,
// rule). Two unfiltered rules of one check leave no dead-rule warning behind AND
// meet on every file there is, so the overlap notice is due exactly once.
func TestRuleReportUnfilteredFastPathMarksEveryRule(t *testing.T) {
	files := ruleReportFiles(t, "alpha.csv", "beta.csv", "gamma.txt")
	cfg := planConfig(nil)
	// The two rules must differ in something the loader can see - two rules of
	// one check identical but for their name are a load error - and they may not
	// differ by include/exclude, which would take them off the fast path this
	// test exists for. ignoreCase is that difference, and it stays inside the one
	// scope this case is about: with no pattern to fold it admits exactly what
	// wide-one admits.
	wideTwo := asciiRule("wide-two", []string{"file"})
	wideTwo.IgnoreCase = true
	cfg.Rules = []config.RuleSpec{
		asciiRule("wide-one", []string{"file"}),
		wideTwo,
	}
	plan := compilePlan(t, cfg)
	entries := plan.scope(checks.ScopeFile)
	if !allUnfiltered(entries) {
		t.Fatal("the fixture must take the unfiltered fast path")
	}

	// The fast path itself: the work items share the plan's own entries (no
	// arenas, no Match calls) and every rule is still marked alive.
	marks := newRuleMarks(entries)
	items := filterChecksForFiles(entries, checks.ScopeFile, files, marks)
	if len(items) != len(files) || &items[0].Checks[0] != &entries[0] {
		t.Fatal("the unfiltered pass must share the plan's entries")
	}
	for i, row := range marks.hit {
		for j, hit := range row {
			if !hit {
				t.Errorf("rule %q of check %s was not marked alive on the fast path", entries[i].rules[j].Rule, entries[i].def.Name)
			}
		}
	}

	_, diags := ApplyAllChecks(context.Background(), cfg, plan, files, false)
	assertNoDeadRule(t, diags, "wide-one")
	assertNoDeadRule(t, diags, "wide-two")
	// Three files, one notice: the fast path records the pair for the pass, not
	// for each file it skipped matching.
	assertRuleDiags(t, diags, structs.DiagWarning, 1, "wide-one", "wide-two")
}

// TestRuleReportEmptyPackageStaysSilent pins both halves of the fast path's
// empty-input guard, which the same fixture reaches from two directions. With no
// file to select over, the pass proves nothing: marking the scope alive would
// claim two rules met on a file that does not exist, and counting the phase as
// exercised would call every rule of the plan dead.
func TestRuleReportEmptyPackageStaysSilent(t *testing.T) {
	cfg := planConfig(nil)
	// Same difference as above, and for the same reason: the loader rejects two
	// rules of one check that differ only in their name, and an include pattern
	// would take the pair off the fast path this case has to reach.
	wideTwo := asciiRule("wide-two", []string{"file"})
	wideTwo.IgnoreCase = true
	cfg.Rules = []config.RuleSpec{
		asciiRule("wide-one", []string{"file"}),
		wideTwo,
	}
	plan := compilePlan(t, cfg)
	// The pair would be recorded wholesale if the guard let the empty pass mark:
	// the entry has to be on the fast path for this case to reach it.
	if !allUnfiltered(plan.scope(checks.ScopeFile)) {
		t.Fatal("the fixture must take the unfiltered fast path")
	}

	_, diags := ApplyAllChecks(context.Background(), cfg, plan, nil, true)

	assertRuleDiags(t, diags, structs.DiagWarning, 0, "wide-one", "wide-two")
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "wide-one")
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "wide-two")
}

// TestRuleReportProgressEntryPointEmits pins the report on the OTHER entry
// point: ApplyAllChecksWithProgress is the default CLI/TUI path and appends the
// run's rule diagnostics through its own tail, which can drift from its twin's.
// A nil callback is the no-progress shape both twins share below the tick.
func TestRuleReportProgressEntryPointEmits(t *testing.T) {
	files := ruleReportFiles(t, "alpha.csv", "beta.csv")
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("csv-only", []string{"file"}, `\.csv$`),
		asciiRule("tar-only", []string{"file"}, `\.tar$`),
	}
	plan := compilePlan(t, cfg)

	_, diags := ApplyAllChecksWithProgress(context.Background(), cfg, plan, files, false, nil)

	assertRuleDiags(t, diags, structs.DiagWarning, 1, "tar-only")
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "csv-only")
}

// TestRuleReportCancelledRunEmitsNothing pins the cancellation gate on both entry
// points: a run the deadline cut short stopped where it stopped, so "this rule
// matched nothing" would describe the walk rather than the configuration - and on
// a server timeout it would tell the operator a correct config is broken. The
// same plan run to completion DOES accuse the rule, which is what keeps this from
// passing for the wrong reason.
func TestRuleReportCancelledRunEmitsNothing(t *testing.T) {
	files := ruleReportFiles(t, "alpha.csv", "beta.csv")
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("csv-only", []string{"file"}, `\.csv$`),
		asciiRule("tar-only", []string{"file"}, `\.tar$`),
	}
	plan := compilePlan(t, cfg)

	_, complete := ApplyAllChecks(context.Background(), cfg, plan, files, false)
	assertRuleDiags(t, complete, structs.DiagWarning, 1, "tar-only")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, plain := ApplyAllChecks(ctx, cfg, plan, files, false)
	assertRuleDiags(t, plain, structs.DiagWarning, 0, "tar-only")

	_, progress := ApplyAllChecksWithProgress(ctx, cfg, plan, files, false, nil)
	assertRuleDiags(t, progress, structs.DiagWarning, 0, "tar-only")
}

// TestRuleReportUnexercisedScopeStaysSilent pins what makes a dead-rule warning
// honest: a scope whose phase never iterated candidates proves nothing about its
// rules, so a package with no archive leaves the archive file list unreported.
// The second half is the anti-vacuity half - the SAME rule, over a package that
// does exercise the scope, is reported dead.
func TestRuleReportUnexercisedScopeStaysSilent(t *testing.T) {
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("list-dead", []string{"archive-file-list"}, "zzz-no-such-member"),
	}
	plan := compilePlan(t, cfg)

	// No archive file: the phase returns before it reaches a member list, so its
	// rules may not be called dead.
	plainFiles := ruleReportFiles(t, "alpha.csv", "beta.csv")
	_, diags := ApplyAllChecks(context.Background(), cfg, plan, plainFiles, false)
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "list-dead")

	// Same rule, same plan, a package whose member lists are walked: now the
	// silence above is the scope's, not a report that never fires.
	_, diags = ApplyAllChecks(context.Background(), cfg, plan, ruleReportArchives(t), false)
	assertRuleDiags(t, diags, structs.DiagWarning, 1, "list-dead")
}

// TestRuleReportEmptyArchiveIsNotEvidence is the other half of that flag, and the
// narrower one. Here the phase DOES reach the archive, opens it and reads its
// member list successfully - the walk is entered - but the list is empty, so not
// one rule was ever asked about a member. Marking the scope exercised on that
// evidence would report every archive-file-list rule dead: a false accusation
// against a correct config, which is the one failure mode this feature must never
// produce.
func TestRuleReportEmptyArchiveIsNotEvidence(t *testing.T) {
	// A zip writer closed without adding an entry: a valid archive holding
	// nothing. buildNamedZip writes it into its own temp dir.
	path := buildNamedZip(t, nil)
	archive := structs.ToFile(path, "members.zip", -1, "")
	if !archive.IsArchive {
		t.Fatal("the fixture must be collected as an archive, or the phase never looks at it")
	}

	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("list-rule", []string{"archive-file-list"}, "file1"),
	}
	plan := compilePlan(t, cfg)

	// The case rests on the walk being ENTERED and finding nothing, not on the
	// phase bailing out before it (which the test above covers): ruleReportMembers
	// asks the reader the same question the phase asks, and fails the test unless
	// the archive is readable and untruncated.
	if members := ruleReportMembers(t, archive); len(members) != 0 {
		t.Fatalf("the fixture must hold no member at all, got %v", members)
	}

	_, diags := ApplyAllChecks(context.Background(), cfg, plan, []structs.File{archive}, false)
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "list-rule")
}

// TestRuleReportArchiveMemberScopeStaysSilent pins the other scope the report
// deliberately does NOT cover. At archive-member scope a [[rule]]'s patterns
// compile into the MEMBER selector and the dispatch gate is left empty, so the
// container gate the selection pass observes admits every archive: a mark taken
// there would call every member rule alive the moment the package holds one
// archive, and would pair two rules whose member patterns are disjoint and never
// meet on a single member. Both rules here are exactly that pair.
func TestRuleReportArchiveMemberScopeStaysSilent(t *testing.T) {
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		memberRule("member-nowhere", []string{"^no-such-dir/"}, false),
		memberRule("member-elsewhere", []string{"^also-not/"}, false),
	}
	plan := compilePlan(t, cfg)

	// The mutation pin: the scope is out of the report's allow-list, and the plan
	// really does carry rules here, or the nil shape would prove nothing.
	if len(plan.scope(checks.ScopeArchiveMember)) == 0 {
		t.Fatal("the fixture must carry archive-member rules, or the missing mark shape proves nothing")
	}
	if newRuleReport(plan).scopes[checks.ScopeArchiveMember] != nil {
		t.Error("the archive-member scope must get no mark shape at all")
	}

	// Anti-vacuity for the contract below: the container gate really is empty, so
	// a mark taken here would call both rules alive on any archive at all.
	for _, rule := range planEntry(t, plan, "IsFreeOfKeywords", checks.ScopeArchiveMember).rules {
		if !rule.Unfiltered() {
			t.Fatalf("rule %q: a member rule's dispatch gate must admit every archive", rule.Rule)
		}
	}

	// The contract: no member of either archive matches either rule, and the two
	// never meet - so neither a dead-rule warning nor an overlap notice may appear.
	_, diags := ApplyAllChecks(context.Background(), cfg, plan, ruleReportArchives(t), false)
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "member-nowhere")
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "member-elsewhere")
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "member-nowhere", "member-elsewhere")
}

// TestRuleReportRepositoryScopeStaysSilent pins the scope the report
// deliberately does NOT cover. A repository rule's selector is not a dispatch
// gate: the check always runs, and BoundRule.narrow only NARROWS the file set it
// is handed - so a rule that admits no file still runs and still reports, which
// is exactly how HasReadme over an empty set tells the user there is no ReadMe.
// "Matched no file" would accuse a rule that did its job, so the scope carries no
// marks at all: no dead-rule warning, and no overlap notice either.
func TestRuleReportRepositoryScopeStaysSilent(t *testing.T) {
	dir := t.TempDir()
	readme := filepath.Join(dir, "readme.md")
	if err := os.WriteFile(readme, []byte("lists nothing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []structs.File{
		{Path: readme, Name: "readme.md", RelPath: "docs/readme.md"},
		{Path: filepath.Join(dir, "data.csv"), Name: "data.csv", RelPath: "raw/data.csv"},
	}
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		// Admits nothing, yet must stay unaccused - and, below, is shown to
		// still report.
		{Name: "readme-nowhere", Check: "HasReadme", Enabled: true, Include: []string{"^archive/"},
			Params: []map[string]interface{}{{"readme_names": []string{"readme.md"}}}},
		// Two rules of one check that admit every file: at any other scope this
		// is the overlap notice's textbook case.
		{Name: "toc-one", Check: "ReadMeContainsTOC", Enabled: true},
		// ignoreCase only makes the twin distinguishable to the loader, which
		// refuses two rules differing in nothing but the name; with no include
		// or exclude patterns it folds nothing and both still admit every file.
		{Name: "toc-two", Check: "ReadMeContainsTOC", Enabled: true, IgnoreCase: true},
	}
	plan := compilePlan(t, cfg)

	// The mutation pin, kept where its reason lives: the scope is left OUT of the
	// report's allow-list because a repository selector narrows rather than gates,
	// so a rule admitting nothing still runs and still reports. The plan really
	// does carry rules here, or the nil shape would mean nothing.
	if len(plan.scope(checks.ScopeRepository)) == 0 {
		t.Fatal("the fixture must carry repository rules, or the missing mark shape proves nothing")
	}
	if newRuleReport(plan).scopes[checks.ScopeRepository] != nil {
		t.Error("the repository scope must get no mark shape at all")
	}

	// Anti-vacuity: readme-nowhere really admits no file, so the silence below is
	// the scope's rather than the fixture's.
	nowhere := planRule(t, plan, "HasReadme", checks.ScopeRepository)
	for _, file := range files {
		if nowhere.Match(file) {
			t.Fatalf("readme-nowhere must admit no file, it admits %q", file.RelPath)
		}
	}

	sink := &diagSink{rules: newRuleReport(plan)}
	messages := applyChecksFilteredByRepository(context.Background(), sink, plan.scope(checks.ScopeRepository), files)
	diags := append(sink.drain(), sink.rules.diagnostics()...)

	assertRuleDiags(t, diags, structs.DiagWarning, 0, "readme-nowhere")
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "toc-one", "toc-two")

	// The other half of the reason: narrowed to nothing, the rule still RAN and
	// still reported - the finding a dead-rule warning would have contradicted.
	reported := false
	for _, m := range messages {
		reported = reported || m.TestName == "HasReadme"
	}
	if !reported {
		t.Fatal("a repository rule that admits no file must still run and report - that is why the scope is unreported")
	}
}

// TestRuleReportArchiveFileListFold pins the worker path end to end: every
// archive walks its member list in its own goroutine with its own marks buffer,
// and the run's totals are the OR-fold of them at the join. Each rule here goes
// alive in ONE archive only, so a fold that overwrote the totals instead of
// OR-ing them would report whichever rule the last worker did not see as dead -
// a false accusation. Both are asserted silent, so the mutation is caught
// whichever archive folds last.
func TestRuleReportArchiveFileListFold(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		// The fan-out is gated on the CPU budget, so on one CPU this would pass
		// down the sequential path without ever running two workers.
		// TestRuleReportFoldAccumulatesAcrossArchives pins the fold itself, in a
		// fixed order, on any budget.
		t.Skip("the archive file list fans out only above one CPU")
	}
	archives := ruleReportArchives(t)
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("list-alpha", []string{"archive-file-list"}, "alpha"),
		asciiRule("list-beta", []string{"archive-file-list"}, "beta"),
		asciiRule("list-dead", []string{"archive-file-list"}, "zzz-no-such-member"),
	}
	plan := compilePlan(t, cfg)

	_, diags := ApplyAllChecks(context.Background(), cfg, plan, archives, false)

	// Each rule was marked in one worker's buffer only; both must survive the join.
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "list-alpha")
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "list-beta")
	// The rule no member of either archive matches is still reported, once.
	assertRuleDiags(t, diags, structs.DiagWarning, 1, "list-dead")
	// Disjoint members: the two live rules never met on one file.
	assertRuleDiags(t, diags, structs.DiagWarning, 0, "list-alpha", "list-beta")
}

// TestRuleReportFoldAccumulatesAcrossArchives pins what the fold ACCUMULATES,
// in an order fixed by construction rather than by the scheduler: the two walks
// are driven here, in this goroutine, populated archive first and empty archive
// second. Every fact the first walk established has to survive the second fold,
// which establishes none - hits, recorded pairs, and the exercised flag alike.
// Each is a separate false-accusation mode: a lost hit reports a live rule dead,
// a lost exercised flag drops every warning the scope earned, and a lost pair
// drops the overlap notice.
func TestRuleReportFoldAccumulatesAcrossArchives(t *testing.T) {
	populated := structs.ToFile(buildNamedZip(t, []string{"alpha.txt"}), "one.zip", -1, "")
	empty := structs.ToFile(buildNamedZip(t, nil), "empty.zip", -1, "")
	if members := ruleReportMembers(t, populated); len(members) != 1 {
		t.Fatalf("the first archive must hold exactly one member, got %v", members)
	}
	if members := ruleReportMembers(t, empty); len(members) != 0 {
		t.Fatalf("the second archive must hold no member at all, got %v", members)
	}

	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("list-alpha", []string{"archive-file-list"}, "alpha"),
		asciiRule("list-txt", []string{"archive-file-list"}, `\.txt$`),
		asciiRule("list-dead", []string{"archive-file-list"}, "zzz-no-such-member"),
	}
	plan := compilePlan(t, cfg)
	entries := plan.scope(checks.ScopeArchiveFileList)

	sink := &diagSink{rules: newRuleReport(plan)}
	// The order is the test: the archive that proves nothing folds LAST.
	archiveFileListChecks(context.Background(), sink, cfg, entries, populated)
	archiveFileListChecks(context.Background(), sink, cfg, entries, empty)
	diags := append(sink.drain(), sink.rules.diagnostics()...)

	// The exercised flag: the first walk reached its members, so the scope stays
	// exercised even though the second one proved nothing.
	assertRuleDiags(t, diags, structs.DiagWarning, 1, "list-dead")
	// The hits: alpha.txt admitted both live rules during the first walk.
	assertNoDeadRule(t, diags, "list-alpha")
	assertNoDeadRule(t, diags, "list-txt")
	// The pair: recorded on alpha.txt, in the first walk's buffer only.
	assertRuleDiags(t, diags, structs.DiagWarning, 1, "list-alpha", "list-txt")
}

// TestRuleReportLocalBuffersArePrivate pins the property the fold above RESTS on,
// without relying on the scheduler to expose it: the archive file list hands one
// buffer to each archive's worker goroutine, so two calls must share neither the
// struct nor a backing array with each other or with the folded total. Handing
// back the total instead would be a data race the fold's assertions can only
// catch by luck. The entry-count panic is pinned here too: a mark is identified
// by its POSITION in the entry slice, so a caller matching against a different
// slice than it declared would file one rule's marks under another rule's name.
func TestRuleReportLocalBuffersArePrivate(t *testing.T) {
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("list-one", []string{"archive-file-list"}, "file1"),
		asciiRule("list-two", []string{"archive-file-list"}, `\.txt$`),
	}
	plan := compilePlan(t, cfg)
	entries := plan.scope(checks.ScopeArchiveFileList)
	report := newRuleReport(plan)

	first := report.local(checks.ScopeArchiveFileList, entries)
	second := report.local(checks.ScopeArchiveFileList, entries)
	if first == nil || second == nil {
		t.Fatal("the archive file list scope must be reported")
	}
	if first == second {
		t.Fatal("each archive's worker needs its own buffer, not a shared one")
	}
	if first == report.scopes[checks.ScopeArchiveFileList] {
		t.Fatal("local must not hand back the folded total - the workers would write into it")
	}
	pairRows := 0
	for i := range entries {
		// Every entry has at least one rule, so hit[i][0] always exists; a pair
		// row exists only from two rules up.
		if &first.hit[i][0] == &second.hit[i][0] {
			t.Fatalf("entry %d: two buffers must not share a hit array - concurrent workers would race on it", i)
		}
		if len(first.pairSeen[i]) == 0 {
			continue
		}
		pairRows++
		if &first.pairSeen[i][0] == &second.pairSeen[i][0] {
			t.Fatalf("entry %d: two buffers must not share a pair array - concurrent workers would race on it", i)
		}
	}
	if pairRows == 0 {
		t.Fatal("the fixture must carry a multi-rule entry, or the pair arrays go unchecked")
	}

	// A mismatched entry slice switches reporting off for that phase; it must NOT
	// panic. One caller runs under safeRun, which would turn the panic into a
	// subject-tagged diagnostic and so into a depositor-facing "this archive could
	// not be fully scanned" - a bug in here must never reach the end user.
	if got := report.local(checks.ScopeArchiveFileList, entries[:len(entries)-1]); got != nil {
		t.Error("a shortened entry slice must switch reporting off, not hand out a mis-shaped buffer")
	}
	// Same LENGTH, different backing array. A length test alone cannot tell this
	// from the plan's own slice, and a reordered copy would file one rule's marks
	// under another rule's name.
	copied := append([]checkRules(nil), entries...)
	if got := report.local(checks.ScopeArchiveFileList, copied); got != nil {
		t.Error("an entry slice that is not the plan's own must switch reporting off")
	}
}

// TestRuleReportOffStaysInert pins the switched-off path: a nil marks buffer and
// a sink built without a report behave exactly as before - the same work list, no
// panic through the nil receivers the entry points call unconditionally, and no
// diagnostics.
func TestRuleReportOffStaysInert(t *testing.T) {
	files := ruleReportFiles(t, "alpha.csv", "beta.csv", "gamma.txt")
	cfg := planConfig(nil)
	cfg.Rules = []config.RuleSpec{
		asciiRule("csv-rule", []string{"file"}, `\.csv$`),
		asciiRule("alpha-rule", []string{"file"}, `^alpha`),
	}
	entries := compilePlan(t, cfg).scope(checks.ScopeFile)

	// matchRules keeps TWO selection loops - one that records marks and one that
	// does not - and they must stay semantically identical. Every other
	// phase-level test in this package takes the unreported one, which is not what
	// production runs, so the comparison is over the selected *checks.BoundRule
	// POINTERS: equal counts would pass even if the two loops picked different
	// rules of the same check.
	off := filterChecksForFiles(entries, checks.ScopeFile, files, nil)
	on := filterChecksForFiles(entries, checks.ScopeFile, files, newRuleMarks(entries))
	if len(off) != len(on) || len(off) == 0 {
		t.Fatalf("selection must not depend on the report: %d work items off, %d on", len(off), len(on))
	}
	multiRule := 0
	for i := range off {
		if off[i].File.Name != on[i].File.Name || len(off[i].Checks) != len(on[i].Checks) {
			t.Fatalf("work item %d drifted with the report off: %+v vs %+v", i, off[i], on[i])
		}
		for c := range off[i].Checks {
			offCheck, onCheck := off[i].Checks[c], on[i].Checks[c]
			if offCheck.def != onCheck.def {
				t.Fatalf("work item %d check %d: %q with the report off, %q with it on", i, c, offCheck.def.Name, onCheck.def.Name)
			}
			if len(offCheck.rules) != len(onCheck.rules) {
				t.Fatalf("work item %d check %s: %d rules off, %d on", i, offCheck.def.Name, len(offCheck.rules), len(onCheck.rules))
			}
			if len(offCheck.rules) > 1 {
				multiRule++
			}
			for r := range offCheck.rules {
				if offCheck.rules[r] != onCheck.rules[r] {
					t.Fatalf("work item %d check %s rule %d: %q with the report off, %q with it on",
						i, offCheck.def.Name, r, offCheck.rules[r].Rule, onCheck.rules[r].Rule)
				}
			}
		}
	}
	// Anti-vacuity: a check whose rules are all single would make the pointer
	// comparison unable to see a wrong PICK, only a wrong count.
	if multiRule == 0 {
		t.Fatal("the fixture must select more than one rule of one check on some file")
	}

	sink := &diagSink{}
	applyChecksFilteredByFile(context.Background(), sink, entries, files)
	if diags := append(sink.drain(), sink.rules.diagnostics()...); len(diags) != 0 {
		t.Fatalf("a sink without a report must stay silent, got %v", diags)
	}
}

// benchCollisionPlan compiles ruleCount rules of ONE check into a plan, so the
// rule-count axis is the only thing that moves between the sub-benchmarks. dead
// makes the last rule match nothing: the entry's pairs can then never all be
// recorded, pairLeft never reaches zero, and the previous-file cache is the only
// thing left to decay the O(rules^2) scan - which is the case that cache exists
// for.
//
// The rules are the KEYWORD check's because the loader refuses two rules of one
// check that differ only in their name, and only a parameterised check can carry
// a difference the SELECTION PASS never reads: the per-rule info string. Their
// include lists stay byte-identical, so the identical-selector worst case this
// benchmark exists for is intact, and a third pattern per rule would put a regex
// on the files the first two reject rather than measuring the recorder.
// IsFreeOfKeywords is anchored, so declaring it here is also what keeps
// withRequiredAnchors from adding a section of its own alongside these rules.
func benchCollisionPlan(b *testing.B, ruleCount int, dead bool) *Plan {
	b.Helper()
	cfg := planConfig(nil)
	for i := 0; i < ruleCount; i++ {
		include := []string{`\.txt$`, `\.csv$`}
		if dead && i == ruleCount-1 {
			include = []string{`\.no-such-extension$`}
		}
		name := fmt.Sprintf("collide-%02d", i)
		cfg.Rules = append(cfg.Rules, config.RuleSpec{
			Name:    name,
			Check:   "IsFreeOfKeywords",
			Scope:   []string{"file"},
			Enabled: true,
			Include: include,
			Params:  []map[string]interface{}{{"keywords": []string{"password"}, "info": name}},
		})
	}
	plan := benchPipelinePlan(b, withRequiredAnchors(cfg))
	for _, entry := range plan.scope(checks.ScopeFile) {
		if entry.def.Name == "IsFreeOfKeywords" && len(entry.rules) == ruleCount {
			return plan
		}
	}
	b.Fatalf("expected one IsFreeOfKeywords entry of %d rules", ruleCount)
	return nil
}

// BenchmarkRuleCollisionAnalysis measures what the rule report COSTS the
// selection pass: the same 5000-file pass with the report on and off, at two rule
// counts, in the two shapes the recorder behaves differently in. Files and plans
// are built outside the timed loop; the buffer is not, because production takes
// one per phase - and per ARCHIVE on the file-list path. Reusing one across
// iterations would record every pair on the first file of the first iteration and
// time the decayed path forever after.
//
//   - off / on: the A/B that isolates the reporting.
//   - -dead: one rule of the entry matches nothing, so pairLeft never reaches
//     zero and only the previous-file cache ends the quadratic scan.
//   - Rules5 / Rules20: the cost must stay linear in the rule count.
func BenchmarkRuleCollisionAnalysis(b *testing.B) {
	const fileCount = 5000
	files := benchFilterFiles(fileCount)

	run := func(b *testing.B, plan *Plan, reported bool) {
		entries := plan.scope(checks.ScopeFile)
		// The report is built once per RUN in production, so it is built here;
		// local and fold are what a phase pays, so they are inside the loop.
		var report *ruleReport
		if reported {
			report = newRuleReport(plan)
		}
		b.ReportAllocs()
		for b.Loop() {
			marks := report.local(checks.ScopeFile, entries)
			items := filterChecksForFiles(entries, checks.ScopeFile, files, marks)
			report.fold(checks.ScopeFile, marks, len(files) > 0)
			if len(items) == 0 {
				b.Fatal("no work items built")
			}
		}
	}

	for _, ruleCount := range []int{5, 20} {
		for _, dead := range []bool{false, true} {
			plan := benchCollisionPlan(b, ruleCount, dead)
			suffix := ""
			if dead {
				suffix = "-dead"
			}
			b.Run(fmt.Sprintf("Rules%d/off%s", ruleCount, suffix), func(b *testing.B) { run(b, plan, false) })
			b.Run(fmt.Sprintf("Rules%d/on%s", ruleCount, suffix), func(b *testing.B) { run(b, plan, true) })
		}
	}
}
