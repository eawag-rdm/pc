package utils

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// ruleMarks records, for ONE scope, which rules the selection pass admitted and
// which rules of one check co-occurred on a file. A rule is identified by its
// (entry, rule) position in that scope of the Plan - the pair the selection
// loops already hold - so nothing has to be added to checks.BoundRule.
//
// One of these is worker-local: the archive-file-list walk runs in a bare
// worker goroutine, where a shared slice would be a data race and an atomic per
// (member, rule) would put a contended write in the selection inner loop. The
// totals are reached by OR-folding at the join instead.
type ruleMarks struct {
	hit      [][]bool // [entry][rule]
	pairSeen [][]bool // [entry][triangular index], nil row when the entry has <2 rules
	pairLeft []int    // [entry]: unrecorded pairs; 0 skips the pair loop entirely
	idx      []int    // scratch: rule indices matched for the CURRENT entry
	prev     [][]int  // scratch: idx of the PREVIOUS file, per entry; nil row when the entry has <2 rules
}

// newRuleMarks builds the mark shape of one scope. It returns nil for a scope
// with no entries, which is the same "nothing to report" answer a report that
// is switched off gives - so every caller has one nil check, not two.
func newRuleMarks(entries []checks.PlanEntry) *ruleMarks {
	if len(entries) == 0 {
		return nil
	}
	m := &ruleMarks{
		hit:      make([][]bool, len(entries)),
		pairSeen: make([][]bool, len(entries)),
		pairLeft: make([]int, len(entries)),
		prev:     make([][]int, len(entries)),
	}
	// One backing array for every entry's previous-file set: the rows are carved
	// out of it at their worst-case width, so the cache costs two allocations for
	// the whole shape and none per file.
	prevBuf := make([]int, ruleCount(entries))
	widest, off := 0, 0
	for i, entry := range entries {
		k := len(entry.Rules)
		m.hit[i] = make([]bool, k)
		// A single rule has no pair to record: no row, and pairLeft 0 makes
		// recordOverlaps a load and a branch for that entry forever after.
		if k >= 2 {
			pairs := k * (k - 1) / 2
			m.pairSeen[i] = make([]bool, pairs)
			m.pairLeft[i] = pairs
			m.prev[i] = prevBuf[off : off : off+k]
		}
		off += k
		widest = max(widest, k)
	}
	// Sized to the worst case of one entry, so the per-file append never grows
	// it - the selection pass must not allocate per file.
	m.idx = make([]int, 0, widest)
	return m
}

// pairBase is the triangular row offset of rule a among k rules: the index of
// the pair (a, b) is pairBase(a, k) + b. Both the recorder and the reporter go
// through it, so the two never disagree about the layout.
func pairBase(a, k int) int { return a*k - a*(a+1)/2 - a - 1 }

// recordOverlaps marks every pair among the rules that matched the CURRENT
// file of this entry (m.idx). Callers only enter it with at least two matches.
// Each pair is recorded once for the whole run, and two things stop the
// quadratic scan afterwards. pairLeft reaching zero ends it for good, but only
// an entry whose every pair CAN meet ever gets there - one dead rule among
// twenty leaves the O(rules^2) scan running for the whole walk. So the previous
// file's match set is kept too: an identical set adds no pair, and the compare
// that says so is O(rules) - which is the case a directory of similar files
// spends nearly all of its time in.
func (m *ruleMarks) recordOverlaps(entry int) {
	if m.pairLeft[entry] == 0 {
		return
	}
	prev := m.prev[entry]
	if slices.Equal(prev, m.idx) {
		return
	}
	// The row's capacity is the entry's rule count, so this never grows: the
	// selection pass must not allocate per file.
	m.prev[entry] = append(prev[:0], m.idx...)
	seen, k := m.pairSeen[entry], len(m.hit[entry])
	for i, a := range m.idx[:len(m.idx)-1] {
		base := pairBase(a, k)
		for _, b := range m.idx[i+1:] {
			if !seen[base+b] {
				seen[base+b] = true
				m.pairLeft[entry]--
			}
		}
	}
}

// markAllAlive records a scope whose every rule admits every file: each rule is
// alive, and any two rules of one entry co-occur on every file there is - so one
// file is the whole answer, and it costs one pass over the marks instead of one
// per file.
func (m *ruleMarks) markAllAlive() {
	for i, hit := range m.hit {
		for j := range hit {
			hit[j] = true
		}
		seen := m.pairSeen[i]
		for p := range seen {
			seen[p] = true
		}
		m.pairLeft[i] = 0
	}
}

// ruleReport is the run-scoped fold of every phase's marks: what the rule
// CONFIGURATION did over this scan, as opposed to what the checks found. It
// hangs off the run's diagSink and turns into diagnostics once every phase has
// joined.
type ruleReport struct {
	mu     sync.Mutex
	plan   *checks.Plan
	scopes [checks.NumScopes]*ruleMarks // folded totals; nil when the scope has no entries
	ran    [checks.NumScopes]bool       // the scope's phase actually iterated candidates
}

// newRuleReport derives the per-scope shapes from the plan the run dispatches.
// A nil report - a nil plan here, or a sink built without one, which is what
// the tests and benchmarks use - switches reporting off through the nil
// receiver of every method below.
func newRuleReport(plan *checks.Plan) *ruleReport {
	if plan == nil {
		return nil
	}
	r := &ruleReport{plan: plan}
	// An ALLOW-LIST of the scopes whose selection pass observes the rule's own
	// gate, not a skip-list of the rest: a scope added later must be silent until
	// someone decides it is reportable, because the default direction of a
	// skip-list is the one that accuses a correct configuration.
	//
	// ScopeArchiveMember is out: at that scope a [[rule]]'s patterns compile into
	// the MEMBER selector and the dispatch gate is left empty
	// (checks.CompileRuleSelectors), so the container gate the selection pass
	// observes admits every archive. A mark taken there would call every member
	// rule alive as soon as the package holds one archive, and would pair two
	// rules with disjoint member patterns that never meet on a single member. The
	// report that would be honest here belongs where the member gate is actually
	// consulted - inside the archive acquisition in pkg/checks - which has no
	// diagnostic sink yet.
	//
	// ScopeRepository is out for the opposite reason: nothing there is a dispatch
	// decision at all. A repository rule always runs, and its selector only
	// NARROWS the file set it is handed (checks.BoundRule narrow), so a rule that
	// admits no file still reports - HasReadme over an empty set tells the user
	// the repository has no ReadMe. "Matched no file" would then warn about a rule
	// that did exactly its job.
	r.scopes[checks.ScopeFile] = newRuleMarks(plan.Scope(checks.ScopeFile))
	r.scopes[checks.ScopeArchiveFileList] = newRuleMarks(plan.Scope(checks.ScopeArchiveFileList))
	return r
}

// local hands out a fresh buffer of one scope's shape for a single phase, or a
// single archive on the worker path: everything inside the selection loops
// writes into it. That buffer is what the reporting costs a pass - the struct
// plus six slices, plus up to two allocations per entry - taken once per phase
// at file scope but once per ARCHIVE on the file-list path, and nothing per file
// thereafter.
//
// entries must be the very slice the caller then hands to matchRules: a mark is
// identified by its position in it, so a shortened or reordered subset would
// attribute one rule's marks to another rule's name - which a length test alone
// does not catch, hence the backing-array identity, every real caller passing
// the plan's own slice.
//
// A mismatch is a wiring bug, and the answer to it is to switch reporting off
// for that phase, not to panic: one caller (archiveFileListChecks) runs under
// safeRun, which would turn the panic into a subject-tagged diagnostic and thus
// into a depositor-facing "this archive could not be fully scanned" - a bug in
// here must never become a message to the end user.
func (r *ruleReport) local(scope checks.Scope, entries []checks.PlanEntry) *ruleMarks {
	if r == nil || r.scopes[scope] == nil {
		return nil
	}
	planned := r.plan.Scope(scope)
	if len(entries) != len(planned) || (len(entries) > 0 && &entries[0] != &planned[0]) {
		return nil
	}
	return newRuleMarks(planned)
}

// fold ORs one buffer into the scope's totals and records whether the phase that
// filled it reached its candidates at all - which is what makes a dead-rule
// warning honest: a package with no archives leaves the archive scopes
// unexercised, and their rules unreported. The two travel together because they
// are one fact about one phase: setting exercised without delivering the marks
// would report EVERY rule of that scope dead, the unsafe direction of a partial
// wiring. It also spares the archive path a second turn of the lock.
//
// Both buffers were built from the same plan scope, so the shapes match by
// construction. The totals' pairLeft, idx and prev stay untouched: the total
// never records, it is only read by diagnostics.
//
// The missing total is guarded with the buffer: local never hands one out for an
// unreported scope, but fold takes any ruleMarks a caller built by hand - the
// benchmarks do - and a nil-deref is the wrong way to say "not reported".
func (r *ruleReport) fold(scope checks.Scope, m *ruleMarks, exercised bool) {
	if r == nil || m == nil || r.scopes[scope] == nil {
		return
	}
	total := r.scopes[scope]
	r.mu.Lock()
	r.ran[scope] = r.ran[scope] || exercised
	for i, row := range m.hit {
		for j, hit := range row {
			if hit {
				total.hit[i][j] = true
			}
		}
	}
	for i, row := range m.pairSeen {
		for p, seen := range row {
			if seen {
				total.pairSeen[i][p] = true
			}
		}
	}
	r.mu.Unlock()
}

// deadRuleReason is the verdict half of the dead-rule warning. It is a const
// because the tests assert the ABSENCE of that verdict: a second spelling over
// there would stop matching the day this one is reworded, and the assertion
// would pass for the wrong reason ever after.
const deadRuleReason = "matched no file this run"

// diagnostics returns the run's rule diagnostics, built through the same
// constructor diagSink.add uses. They are RETURNED rather than pushed into the
// sink: the report hangs off the sink, and a child writing back into its parent
// hides where the run's diagnostics come from - the entry points append these to
// what drain() handed them. A nil report has none.
//
// Both kinds carry an EMPTY subject: they describe the configuration, not a
// file, so they belong on the operator channel (see structs.Diagnostic).
//
// Order is deterministic - scopes, then plan order of entries and rules, then
// pairs by (a, b) - so a rendered run is comparable to the next one. It reads
// the marks without the lock: every phase has returned by the time the entry
// points call this, so every worker that folded is joined.
func (r *ruleReport) diagnostics() []structs.Diagnostic {
	if r == nil {
		return nil
	}
	var items []structs.Diagnostic
	// One stamp for the whole batch: these are verdicts about the run, all
	// reached at the same instant - the moment its last phase joined.
	stamp := time.Now().Format(time.RFC3339)
	for scope := checks.Scope(0); scope < checks.NumScopes; scope++ {
		marks := r.scopes[scope]
		if marks == nil {
			continue
		}
		for i, entry := range r.plan.Scope(scope) {
			if r.ran[scope] {
				for j, hit := range marks.hit[i] {
					if !hit {
						// Only the fact, no diagnosis of the surface: the rule may
						// be a synthesized one inheriting another rule's
						// selectors, so naming its own patterns would be wrong.
						items = append(items, newDiagnosticAt(structs.DiagWarning, "",
							fmt.Sprintf("rule %q of check %s (scope %s) %s", entry.Rules[j].Rule, entry.Def.Name, scope, deadRuleReason), stamp))
					}
				}
			}
			seen := marks.pairSeen[i]
			if len(seen) == 0 {
				continue
			}
			k := len(entry.Rules)
			for a := 0; a < k-1; a++ {
				base := pairBase(a, k)
				for b := a + 1; b < k; b++ {
					if seen[base+b] {
						// A legitimate configuration (two keyword rules with
						// different keyword lists overlap by design), so the
						// wording informs rather than accuses. It is the rule
						// APPLICATION that doubles, not the acquisition:
						// checks.CheckDef.RunFile reads the file once per (file,
						// check) and hands that content to every matched rule, so
						// what the overlap can duplicate is findings.
						//
						// A WARNING because it is a verdict on the
						// configuration and the operator is the one who can act
						// on it: DiagWarning is the level the CLI renders - its
						// JSON formatter keeps error and warning and drops the
						// rest (pkg/output/json/formatter.go), and every other
						// CLI renderer is built from that JSON.
						items = append(items, newDiagnosticAt(structs.DiagWarning, "",
							fmt.Sprintf("rules %q and %q of check %s (scope %s) both apply to the same file - it is read once, but both rules report on it", entry.Rules[a].Rule, entry.Rules[b].Rule, entry.Def.Name, scope), stamp))
					}
				}
			}
		}
	}
	return items
}
