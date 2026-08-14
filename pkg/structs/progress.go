package structs

import "fmt"

// ProgressPhase names the part of a scan a progress report comes from. The scan
// engine reports the phase, the frontend words it - no user-facing text is
// authored here.
//
// The zero value is deliberately no phase, so a Progress that was never filled
// in cannot pass for a report from the first one.
type ProgressPhase uint8

const (
	PhaseFileChecks      ProgressPhase = iota + 1 // the per-file phase
	PhaseArchiveFileList                          // the member-name walk of every archive
	PhaseArchiveContent                           // the member-content scan of every archive
	PhaseRepository                               // the repository-wide checks
	PhaseFinalizing                               // every phase has run

	// phaseEnd is one past the last phase and never a phase the engine
	// reports: it counts the block above so the pin below can. It has no name
	// of its own, so String() falls back for it and it cannot pass for a phase
	// wherever the set is walked.
	phaseEnd
)

var progressPhaseNames = [...]string{"file", "archive-file-list", "archive-content", "repository", "finalizing"}

// The names must cover the declared phases exactly, one apiece. phaseEnd counts
// the phases, so a phase added or dropped above without the same move here - or
// the reverse, in either direction - makes the index below negative or past the
// end, and a constant index out of an array's range does not compile.
var _ = [1]struct{}{}[len(progressPhaseNames)-(int(phaseEnd)-1)]

// String returns the phase's debug name - what a log line or a test failure
// prints. The text a user reads is worded by the frontend.
func (p ProgressPhase) String() string {
	if i := int(p) - 1; i >= 0 && i < len(progressPhaseNames) {
		return progressPhaseNames[i]
	}
	return fmt.Sprintf("phase(%d)", uint8(p))
}

// Progress is one report of a scan's progress: the phase it comes from and the
// run's counters, Current of Total tests, which is what a progress bar is drawn
// from.
//
// Every phase reports once as it opens, with Start set and none of its work
// done - for the file phase that is the moment its work list is known, which is
// what completes Total. The file phase then reports zero or more times as its
// items complete: those are rate limited, so far less frequent than one per
// item, and on a run that is not cancelled the last of them is exact.
type Progress struct {
	Phase   ProgressPhase
	Current int
	Total   int
	// Start marks the report that opens a phase, as opposed to a count from
	// inside one. Every phase sets it on its opening report; the frontend
	// currently reads it only for the file phase, the one phase that also
	// reports from inside itself.
	Start bool
}
