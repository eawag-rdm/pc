package utils

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// matchRules picks the rules of one check that admit this file, read from the
// subjects the caller Set for it - so an ignoreCase literal gate folds its
// subject once per file however many rules and checks read it. It returns a
// sub-slice of backing, which the caller preallocates once per file so the
// selection pass allocates per file rather than per (file, check).
//
// marks, when non-nil, is the run's rule report writing down what this gate
// decided: entryIdx is the entry's position in the scope, which together with
// the rule index is the rule's identity. The unreported path is a separate loop
// because this is the selection inner loop - it must not carry a per-rule branch
// for the marks. Both entry points build a report, so that path is not the
// common case: it is what the tests and benchmarks (no report at all) and the
// archive-member scope take - see newRuleReport. The other unreported scope,
// repository, never reaches here at all: it dispatches per entry through
// RunRepository, with no per-file selection.
func matchRules(entry checks.PlanEntry, subjects *checks.Subjects, backing []*checks.BoundRule, marks *ruleMarks, entryIdx int) ([]*checks.BoundRule, []*checks.BoundRule) {
	start := len(backing)
	if marks == nil {
		for _, rule := range entry.Rules {
			if rule.Match(subjects) {
				backing = append(backing, rule)
			}
		}
		return backing[start:len(backing):len(backing)], backing
	}
	hit := marks.hit[entryIdx]
	marks.idx = marks.idx[:0]
	for j, rule := range entry.Rules {
		if rule.Match(subjects) {
			backing = append(backing, rule)
			hit[j] = true
			marks.idx = append(marks.idx, j)
		}
	}
	if len(marks.idx) > 1 {
		marks.recordOverlaps(entryIdx)
	}
	return backing[start:len(backing):len(backing)], backing
}

// matchScratch is the selection scratch of a SEQUENTIAL walk: both slices are
// allocated once for the whole phase and reused per file, where the walk used
// to allocate a pair per file (and, for an archive file list, per member).
type matchScratch struct {
	backing []*checks.BoundRule
	matched []checks.PlanEntry
	marks   *ruleMarks // the walk's own rule-report buffer; nil when reporting is off

	// subjects carries the file the walk is on, set once for every rule of every
	// check: an ignoreCase literal gate reads its subject folded, and the fold
	// happens at most once per file per subject.
	subjects checks.Subjects
}

// newMatchScratch sizes the scratch to the worst case of one file: every rule
// of every check matches. backing therefore never grows during a file, so the
// sub-slices handed out within one file stay valid.
func newMatchScratch(entries []checks.PlanEntry, marks *ruleMarks) matchScratch {
	return matchScratch{
		backing: make([]*checks.BoundRule, 0, ruleCount(entries)),
		matched: make([]checks.PlanEntry, 0, len(entries)),
		marks:   marks,
	}
}

// match is matchChecksForFile over the scratch. The result is valid until the
// next call - the caller must run the checks before matching the next file.
func (s *matchScratch) match(entries []checks.PlanEntry, file structs.File) []checks.PlanEntry {
	s.backing, s.matched = s.backing[:0], s.matched[:0]
	s.subjects.Set(file)
	for i, entry := range entries {
		var rules []*checks.BoundRule
		rules, s.backing = matchRules(entry, &s.subjects, s.backing, s.marks, i)
		if len(rules) > 0 {
			hit := entry
			hit.Rules = rules
			s.matched = append(s.matched, hit)
		}
	}
	return s.matched
}

// ruleCount is the size the per-file backing array is preallocated to.
func ruleCount(entries []checks.PlanEntry) int {
	total := 0
	for _, entry := range entries {
		total += len(entry.Rules)
	}
	return total
}

func applyChecksFilteredByFile(ctx context.Context, sink *diagSink, entries []checks.PlanEntry, files []structs.File) []structs.Message {
	return applyFileChecks(ctx, sink, entries, files, nil)
}

// allUnfiltered reports whether every rule of every entry admits every file -
// the shipped configs' case, decided once per pass, not per file.
func allUnfiltered(entries []checks.PlanEntry) bool {
	for _, entry := range entries {
		for _, rule := range entry.Rules {
			if !rule.Unfiltered() {
				return false
			}
		}
	}
	return true
}

// filterChecksForFiles builds the work list: one entry per file that has at
// least one check with a matching rule.
//
// When no rule filters at all, selection is the identity: every work item
// shares the plan's own (immutable) entries and the pass allocates only the
// work list itself - no arenas, no Match calls.
//
// Otherwise a work item's slices outlive the call - the pool reads them - so,
// unlike the sequential walks, they cannot come from a reused scratch. They
// come from two arenas instead, each sized to the whole pass's worst case
// (every rule of every check matches every file) and therefore never
// reallocated: sub-slices carved out of them stay valid, and the pass
// allocates twice rather than twice per file. Workers only read them.
func filterChecksForFiles(entries []checks.PlanEntry, scope checks.Scope, files []structs.File, marks *ruleMarks) []workItem {
	if len(entries) == 0 {
		return nil
	}
	workItems := make([]workItem, 0, len(files))
	if allUnfiltered(entries) {
		// Every rule admits every file, so one file is proof of life for all of
		// them - and, for two rules of one check, proof that they meet on every
		// file: mark once per pass rather than once per (file, rule), and only
		// if there was a file at all.
		if marks != nil && len(files) > 0 {
			marks.markAllAlive()
		}
		for _, file := range files {
			workItems = append(workItems, workItem{File: file, Scope: scope, Checks: entries})
		}
		return workItems
	}
	total := ruleCount(entries)
	ruleArena := make([]*checks.BoundRule, 0, len(files)*total)
	checkArena := make([]checks.PlanEntry, 0, len(files)*len(entries))
	var subjects checks.Subjects
	for _, file := range files {
		subjects.Set(file)
		start := len(checkArena)
		for i, entry := range entries {
			var rules []*checks.BoundRule
			rules, ruleArena = matchRules(entry, &subjects, ruleArena, marks, i)
			if len(rules) > 0 {
				hit := entry
				hit.Rules = rules
				checkArena = append(checkArena, hit)
			}
		}
		if len(checkArena) > start {
			matched := checkArena[start:len(checkArena):len(checkArena)]
			workItems = append(workItems, workItem{File: file, Scope: scope, Checks: matched})
		}
	}
	return workItems
}

// runChecksPool fans workItems out over a worker pool and collects the results.
// Submission runs in a goroutine because submit blocks when the pool's buffer
// is full; it reports how many items were actually submitted so the collect
// loop never waits for results that were never submitted (e.g. when the
// analysis deadline fires mid-submission and the submit loop stops early).
// Collecting exactly `submitted` results also guarantees every submit has
// returned before the deferred stop runs (the pool's stop/submit invariant).
// Cancellation is the loop's second exit: the results the workers abandoned
// with the pool never arrive, so it returns what it has - a partial set by
// design.
// tick, when non-nil, reports how many items have completed. It runs on the
// collect loop's goroutine - the caller's - so it needs no synchronisation.
// Two gates keep it off the result path: a counter that admits every
// len/100th item (the cheap pre-filter, under 200 admissions per pass however
// large it is) and, behind it, a 50 ms wall clock - so it fires at most ~20
// times a second however short the pass is. On a pass that runs to completion
// the last item ticks with both gates bypassed, so the caller's final report is
// exact; a cancelled pass submits fewer items, so that tick never fires and the
// last report undercounts - partial by design, like the messages.
// tick must not panic:
// a panic here skips the submit/collect handshake and runs the deferred stop
// while a submit may be parked (see workerPool.stop), so callers guard their
// callback - ApplyAllChecksWithProgress does.
func runChecksPool(ctx context.Context, sink *diagSink, workItems []workItem, numWorkers int, tick func(int)) []structs.Message {
	if len(workItems) == 0 {
		return nil
	}
	if len(workItems) < numWorkers {
		numWorkers = len(workItems)
	}

	pool := newWorkerPool(ctx, sink, numWorkers)
	pool.start()
	defer pool.stop()

	submittedCh := make(chan int, 1)
	go func() {
		submitted := 0
		for _, entry := range workItems {
			// Stop submitting new files once the deadline fired / the caller
			// cancelled; files already submitted still finish.
			if ctx.Err() != nil {
				break
			}
			if !pool.submit(entry) {
				break
			}
			submitted++
		}
		submittedCh <- submitted
	}()

	var allMessages []structs.Message
	collected := 0
	submitted := -1
	stride := max(1, len(workItems)/100) // count gate: a running counter, no per-result divide
	nextTick := stride
	var lastTick time.Time
	done := ctx.Done() // hoisted: one interface call, not one per result
	for submitted < 0 || collected < submitted {
		select {
		case result := <-pool.results():
			allMessages = append(allMessages, result.Messages...)
			collected++
			if tick != nil {
				if collected == len(workItems) {
					tick(collected) // the final report must be exact
				} else if collected >= nextTick {
					nextTick += stride
					// Count alone fires 100-199 times regardless of duration,
					// which on a millisecond pass is thousands of redraws a
					// second, each stalling this loop and back-pressuring the
					// workers. Rate-limit by wall clock.
					if now := time.Now(); now.Sub(lastTick) > 50*time.Millisecond {
						lastTick = now
						tick(collected)
					}
				}
			}
		case n := <-submittedCh:
			submitted = n
		case <-done:
			// The pool's context derives from this one, so the workers abandon
			// whatever is still queued: those results never arrive and waiting
			// for them would hang here. Read the submitter's count first - the
			// same cancellation unblocks it - so the deferred stop still cannot
			// race a blocked submit.
			if submitted < 0 {
				<-submittedCh
			}
			// Take what the workers DID deliver before returning: once ctx is
			// done the select above picks this case against a non-empty result
			// channel with even odds, so finished work would otherwise be
			// dropped at random. Only the deferred stop closes the channel, so
			// the drain cannot block.
			for {
				select {
				case result := <-pool.results():
					allMessages = append(allMessages, result.Messages...)
				default:
					return allMessages
				}
			}
		}
	}
	return allMessages
}

// applyFileChecks is the whole file phase, shared by both entry points: the PDF
// pre-pass, selection, then execution of the work list. begin, when non-nil, is
// handed the work-item count once that list exists and before any check runs -
// the progress path needs it for its total up front - and returns the tick (or
// nil, for no progress).
//
// Two files are enough to be worth a pool. The pass is NOT gated on the CPU
// budget: most of its time is spent blocked on reads (and on PDF extraction),
// and a blocked goroutine releases its P - so even a one-CPU budget overlaps
// that waiting, which a sequential walk turns into a sum. At least two workers
// for the same reason.
func applyFileChecks(ctx context.Context, sink *diagSink, entries []checks.PlanEntry, files []structs.File, begin func(int) func(int)) []structs.Message {
	for _, file := range files {
		helpers.PDFTracker.AddFileIfPDF("", file)
	}
	marks := sink.rules.local(checks.ScopeFile, entries)
	workItems := filterChecksForFiles(entries, checks.ScopeFile, files, marks)
	// Selection is over before the pool starts, so folding here - rather than at
	// every return below - keeps the buffer off the worker path entirely. The
	// scope counts as exercised only if there was anything to select over.
	sink.rules.fold(checks.ScopeFile, marks, len(files) > 0)
	var tick func(int)
	if begin != nil {
		tick = begin(len(workItems))
	}
	if len(files) >= 2 {
		return runChecksPool(ctx, sink, workItems, max(2, runtime.GOMAXPROCS(0)), tick)
	}

	// Sequential processing for small workloads: no pool, no channels.
	var messages = []structs.Message{}
	for i, item := range workItems {
		// Stop between files once the analysis deadline fired / the caller
		// cancelled (coarse-grained: a file in progress finishes).
		if ctx.Err() != nil {
			return messages
		}
		for _, entry := range item.Checks {
			ret := safeRunCheck(ctx, sink, entry, item.File, item.Scope)
			if ret != nil {
				// Add test name to each message
				for j := range ret {
					ret[j].TestName = entry.Def.Name
				}
				messages = append(messages, ret...)
			}
		}
		if tick != nil {
			tick(i + 1)
		}
	}
	return messages
}

// refuseStreamListArchives takes the archives the two archive passes may not
// open away from them: a stream-list archive over the content-scan cap, whose
// member list cannot be had without decompressing its stream. It returns the
// acknowledgements dispatch owes for them and the file set both passes run on -
// files itself when nothing is refused, so the common case copies nothing. It
// runs once, before either pass, because the two have to refuse the same
// archives and only one message stands for both.
//
// The size comes from os.Stat, like the content gates in pkg/checks: a CKAN
// File.Size is metadata. A stat error leaves the archive to the passes, whose
// own reads report it. A cancelled run refuses nothing: the passes stop at their
// own cancellation points.
//
// general is nil only for a config built in code - a loaded one without
// [general] is a load error (checks.Compile) - and its cap is then unknown, so
// nothing is refused.
func refuseStreamListArchives(ctx context.Context, general *config.GeneralConfig, listEntries, memberEntries []checks.PlanEntry, files []structs.File) ([]structs.Message, []structs.File) {
	if ctx.Err() != nil || general == nil {
		return nil, files
	}
	limit := general.MaxContentScanFileSize
	var refusals []structs.Message
	var remaining []structs.File // nil until the first refusal: no refusal, no copy
	var scratch matchScratch
	for i, file := range files {
		var size int64
		refused := false
		if file.IsArchive && readers.IsStreamListArchive(file.Name) {
			if info, err := os.Stat(file.Path); err == nil && info.Size() > limit {
				size, refused = info.Size(), true
			}
		}
		if !refused {
			if remaining != nil {
				remaining = append(remaining, file)
			}
			continue
		}
		if remaining == nil {
			remaining = append(make([]structs.File, 0, len(files)), files[:i]...)
			// The container gate of the member pass: the refusal matches the
			// container against the member-scope entries with the same rule
			// matching that pass applies to it, so the two agree on the verdict
			// and the message cannot claim a content scan the pass would never
			// have scheduled.
			scratch = newMatchScratch(memberEntries, nil)
		}
		if skipped := skippedArchiveWork(len(listEntries) > 0, len(scratch.match(memberEntries, file)) > 0); skipped != "" {
			refusals = append(refusals, archiveSizeSkipMessage(file, size, limit, skipped))
		}
	}
	if remaining == nil {
		remaining = files
	}
	return refusals, remaining
}

// skippedArchiveWork names the work a refused archive loses: the member-name
// checks when the plan schedules any, the content scan when a member-scope entry
// admits the container. Neither means nothing was scheduled for this archive, so
// nothing is owed an acknowledgement.
func skippedArchiveWork(names, content bool) string {
	switch {
	case names && content:
		return "member-name checks and content scan"
	case names:
		return "member-name checks"
	case content:
		return "content scan"
	}
	return ""
}

func applyChecksFilteredByFileOnArchiveFileList(ctx context.Context, sink *diagSink, config config.Config, entries []checks.PlanEntry, files []structs.File) []structs.Message {
	// No file-list entries: nothing consumes a listing, so skip it. This drops the
	// pass's side effects for these archives - pdf_files member rows, the walk-cap
	// skip message, the reader-error warning - accepted: nothing was scheduled.
	if len(entries) == 0 {
		return []structs.Message{}
	}

	// Filter to only archive files, minus the stream-list ones: listing those
	// costs a decompression, and the member phase owns their listing instead
	// (streamListArchiveChecks).
	var archiveFiles []structs.File
	for _, file := range files {
		if file.IsArchive && !readers.IsStreamListArchive(file.Name) {
			archiveFiles = append(archiveFiles, file)
		}
	}

	if len(archiveFiles) == 0 {
		return []structs.Message{}
	}

	// Use parallel processing for multiple archives
	if len(archiveFiles) >= 2 && runtime.GOMAXPROCS(0) > 1 {
		return applyArchiveFileListChecksParallel(ctx, sink, config, entries, archiveFiles)
	}

	// Sequential processing for single archive or single CPU
	var messages = []structs.Message{}
	for _, file := range archiveFiles {
		if ctx.Err() != nil {
			return messages
		}
		msgs := processArchiveFileList(ctx, sink, config, entries, file)
		messages = append(messages, msgs...)
	}
	return messages
}

// processArchiveFileList processes all file list checks for a single archive
// This keeps files within each archive sequential while allowing parallelism across archives.
// The whole body runs under safeRun (not just the check invocations):
// ReadArchiveFileList parses untrusted archive bytes, and on the parallel path
// this function runs in a bare worker goroutine where an unrecovered panic
// would kill the process.
func processArchiveFileList(ctx context.Context, sink *diagSink, cfg config.Config, entries []checks.PlanEntry, archiveFile structs.File) []structs.Message {
	return safeRun(sink, "Processing archive '"+archiveFile.Name+"'", archiveFile.GetDisplayName(), func() []structs.Message {
		return archiveFileListChecks(ctx, sink, cfg, entries, archiveFile)
	})
}

// archiveWalkLimits derives the file-list walk bounds from the single defaulting
// site, so a hand-built config (General nil or zero fields) gets the documented
// defaults instead of "unlimited".
func archiveWalkLimits(cfg config.Config) (maxMembers int, maxTotalMemory int64) {
	general := cfg.General
	if general == nil {
		general = &config.GeneralConfig{}
	}
	_, totalMemory, memberCount := general.ArchiveLimits()
	return memberCount, totalMemory
}

// archiveWalkSkipMessage acknowledges an archive whose member list busts the
// walk bounds, mirroring the content path's member-count skip. It must be a
// Message, not a logger warning: warnings collapse into the generic unscanned
// acknowledgement in server responses, hiding the reason.
func archiveWalkSkipMessage(archiveFile structs.File, maxMembers int, maxTotalMemory int64) structs.Message {
	reason := fmt.Sprintf("Skipped name checks of archive members: member count exceeds maximum (%d).", maxMembers)
	// A stream-list walk stops on the member cap OR on its decompressed-byte
	// budget and reports both as one truncation flag, so both are named.
	if readers.IsStreamListArchive(archiveFile.Name) {
		reason = fmt.Sprintf("Skipped name checks of archive members: member count exceeds maximum (%d) or decompressed size exceeds the walk budget set by the total archive memory limit (%d bytes).", maxMembers, maxTotalMemory)
	}
	return structs.Message{
		Content:  reason,
		Source:   archiveFile,
		TestName: "ArchiveFileList",
		Skipped:  true,
		Reason:   reason,
	}
}

// archiveReadSkipMessage acknowledges an archive whose file could not be read,
// so its member names never read as checked and clean. The caller acks transient
// read failures only; a format error stays the diagnostic it has always been.
func archiveReadSkipMessage(archiveFile structs.File) structs.Message {
	reason := "Skipped name checks of archive members: archive could not be read."
	return structs.Message{
		Content:   reason,
		Source:    archiveFile,
		TestName:  "ArchiveFileList",
		Skipped:   true,
		Transient: true,
		Reason:    reason,
	}
}

// archiveSizeSkipMessage acknowledges a stream-list archive the file-list and
// member passes never see. It carries both of them: skipped names the work each
// loses, and the content half is worded by the caller exactly when a member-scope
// entry had admitted the container - the check that would otherwise acknowledge
// that half is never invoked. It shares archiveWalkSkipMessage's TestName: both
// are dispatch's word about one archive's member list, and renderers group by it.
func archiveSizeSkipMessage(archiveFile structs.File, size, limit int64, skipped string) structs.Message {
	reason := fmt.Sprintf("Skipped archive checks (%s): file size (%d bytes) exceeds maximum (%d bytes).", skipped, size, limit)
	return structs.Message{
		Content:  reason,
		Source:   archiveFile,
		TestName: "ArchiveFileList",
		Skipped:  true,
		Reason:   reason,
	}
}

func archiveFileListChecks(ctx context.Context, sink *diagSink, cfg config.Config, entries []checks.PlanEntry, archiveFile structs.File) []structs.Message {
	var messages []structs.Message

	maxMembers, maxTotalMemory := archiveWalkLimits(cfg)
	fileList, truncated, err := readers.ReadArchiveFileList(archiveFile, maxMembers, maxTotalMemory)
	if err != nil {
		sink.add(structs.DiagWarning, archiveFile.GetDisplayName(), "Error (archive filelist checks) reading archive file list of '%s' -> %v", archiveFile.Name, err)
		if readers.IsTransientReadError(err) {
			messages = append(messages, archiveReadSkipMessage(archiveFile))
		}
		return messages
	}
	if truncated {
		// The list is partial by construction: run no checks on it.
		return append(messages, archiveWalkSkipMessage(archiveFile, maxMembers, maxTotalMemory))
	}
	return archiveFileListMemberChecks(ctx, sink, entries, archiveFile, fileList)
}

// archiveFileListMemberChecks runs the file-list scope over one archive's member
// list, whichever walk produced it: the reader's own listing pass, or the fused
// content walk that noted the same names on its way through.
func archiveFileListMemberChecks(ctx context.Context, sink *diagSink, entries []checks.PlanEntry, archiveFile structs.File, fileList []structs.File) []structs.Message {
	var messages []structs.Message

	// One rule-report buffer per ARCHIVE, taken and folded here: on the parallel
	// path this function is the worker goroutine, so a buffer shared with the
	// other archives would be a data race. The scope counts as exercised only
	// past the caller's own returns, and only for a member list some walk really
	// produced - one that was never walked proves nothing about its rules.
	marks := sink.rules.local(checks.ScopeArchiveFileList, entries)
	walked := len(fileList) > 0
	defer func() { sink.rules.fold(checks.ScopeArchiveFileList, marks, walked) }()

	scratch := newMatchScratch(entries, marks)
	for _, archivedFile := range fileList {
		// Stop between archived files once the deadline fired / the caller
		// cancelled.
		if ctx.Err() != nil {
			return messages
		}
		helpers.PDFTracker.AddFileIfPDF(archiveFile.Name+" -> ", archivedFile)

		for _, entry := range scratch.match(entries, archivedFile) {
			ret := safeRunCheck(ctx, sink, entry, archivedFile, checks.ScopeArchiveFileList)
			if ret != nil {
				for j := range ret {
					ret[j].TestName = entry.Def.Name
				}
				messages = append(messages, ret...)
			}
		}
	}
	return messages
}

// applyArchiveFileListChecksParallel processes archive file list checks in parallel across archives
// Each archive is processed by a single worker, keeping files within each archive sequential
func applyArchiveFileListChecksParallel(ctx context.Context, sink *diagSink, cfg config.Config, entries []checks.PlanEntry, archiveFiles []structs.File) []structs.Message {
	numWorkers := runtime.GOMAXPROCS(0)
	if len(archiveFiles) < numWorkers {
		numWorkers = len(archiveFiles)
	}

	// Channel for archive files to process
	archiveChan := make(chan structs.File, len(archiveFiles))
	// Channel for results
	resultChan := make(chan []structs.Message, len(archiveFiles))

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for archiveFile := range archiveChan {
				// After the deadline fires, drain the remaining archives without
				// processing them so the result count stays consistent.
				var messages []structs.Message
				if ctx.Err() == nil {
					messages = processArchiveFileList(ctx, sink, cfg, entries, archiveFile)
				}
				resultChan <- messages
			}
		}()
	}

	// Send work
	for _, file := range archiveFiles {
		archiveChan <- file
	}
	close(archiveChan)

	// Wait for workers to finish and close result channel
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	var allMessages []structs.Message
	for messages := range resultChan {
		allMessages = append(allMessages, messages...)
	}

	return allMessages
}

// applyChecksFilteredByFileOnArchive is the archive-member phase. Its own
// selection takes no rule marks: what it decides is the CONTAINER's gate, not
// the member rule's own selector, so nothing observable here is reportable
// (newRuleReport). The stream-list archives it hands over to the fused walk DO
// fold marks - of the file-list scope, whose checks that walk runs too.
func applyChecksFilteredByFileOnArchive(ctx context.Context, sink *diagSink, cfg config.Config, listEntries, memberEntries []checks.PlanEntry, files []structs.File) []structs.Message {
	// Filter to only archive files. The stream-list ones skip this phase's
	// selection - one routine owns both halves of them and matches the container
	// itself - but they ride the same pool: what they do is extraction like any
	// other archive's, and the pool's worker count is what bounds it.
	var archiveFiles, streamList []structs.File
	for _, file := range files {
		if !file.IsArchive {
			continue
		}
		if readers.IsStreamListArchive(file.Name) {
			streamList = append(streamList, file)
			continue
		}
		archiveFiles = append(archiveFiles, file)
	}

	// One work list for the whole phase: a stream-list archive's item is the
	// fused walk of both its archive scopes.
	workItems := filterChecksForFiles(memberEntries, checks.ScopeArchiveMember, archiveFiles, nil)
	for _, file := range streamList {
		workItems = append(workItems, streamListWorkItem(sink, cfg, listEntries, memberEntries, file))
	}

	// Use parallel processing for archives as they are CPU-intensive
	if len(workItems) >= 2 && runtime.GOMAXPROCS(0) > 1 {
		return applyArchiveChecksParallel(ctx, sink, workItems)
	}

	// Sequential processing for single archives
	var messages = []structs.Message{}
	for _, item := range workItems {
		if ctx.Err() != nil {
			return messages
		}
		if item.Run != nil {
			messages = append(messages, item.Run(ctx)...)
			continue
		}
		for _, entry := range item.Checks {
			ret := safeRunCheck(ctx, sink, entry, item.File, item.Scope)
			if ret != nil {
				// Add test name to each message
				for j := range ret {
					ret[j].TestName = entry.Def.Name
				}
				messages = append(messages, ret...)
			}
		}
	}
	return messages
}

// streamListWorkItem is one stream-list archive's place in the archive-member
// pool: an item whose processing is the fused walk of both its archive scopes.
// The member-name checks of these archives therefore run from the member phase
// - a narrow exception to the phase split, because the walk that scans their
// content is also the only affordable way to list them.
func streamListWorkItem(sink *diagSink, cfg config.Config, listEntries, memberEntries []checks.PlanEntry, archiveFile structs.File) workItem {
	return workItem{
		File:  archiveFile,
		Scope: checks.ScopeArchiveMember,
		Run: func(ctx context.Context) []structs.Message {
			// The whole body runs under ONE guard (not just the check
			// invocations): it walks untrusted archive bytes on a bare worker
			// goroutine, where an unrecovered panic would kill the process, and
			// one guard for both scopes is the intent - a panic in either half
			// discards this archive's findings as a unit rather than leaving
			// half of them to stand for the archive.
			return safeRun(sink, "Processing archive '"+archiveFile.Name+"'", archiveFile.GetDisplayName(), func() []structs.Message {
				return streamListArchiveChecks(ctx, sink, cfg, listEntries, memberEntries, archiveFile)
			})
		},
	}
}

// streamListArchiveChecks runs both archive scopes over ONE stream-list archive
// off a single decompression of its stream.
//
// When a single member-scope entry admits the container, the content walk notes
// the member names it passes and the file-list entries then run on that list.
// Everywhere else - nothing content-scans this archive, several entries do, the
// check bailed before opening it, or its walk stopped short of the archive's end
// - the plain file-list walk runs here instead, exactly as it would have in its
// own pass, and owns whatever findings, acknowledgement or diagnostic that
// archive has coming.
func streamListArchiveChecks(ctx context.Context, sink *diagSink, cfg config.Config, listEntries, memberEntries []checks.PlanEntry, archiveFile structs.File) []structs.Message {
	scratch := newMatchScratch(memberEntries, nil)
	matched := scratch.match(memberEntries, archiveFile)
	if len(matched) == 0 {
		if ctx.Err() != nil || len(listEntries) == 0 {
			return nil
		}
		return archiveFileListChecks(ctx, sink, cfg, listEntries, archiveFile)
	}

	// The collector rides ONE walk: a second content check would open the archive
	// again and its fill would be refused, so with more than one matched entry
	// the names come from the listing walk instead - two decompressions, correct
	// output. With no file-list check to run there is nothing to collect for.
	// Its cap comes from the same config resolution as the walk's own member cap
	// (config.GeneralConfig.ArchiveLimits, via archiveWalkLimits here and via
	// checks.archiveLimits there), so the two bounds cannot drift apart - they
	// count different things, headers against unpack candidates, which is why
	// either can bust while the other holds.
	maxMembers, _ := archiveWalkLimits(cfg)
	var collector *structs.ArchiveNameCollector
	handed := archiveFile
	if len(matched) == 1 && len(listEntries) > 0 {
		collector = structs.NewArchiveNameCollector(maxMembers)
		handed.MemberNames = collector
	}

	var messages []structs.Message
	for _, entry := range matched {
		ret := safeRunCheck(ctx, sink, entry, handed, checks.ScopeArchiveMember)
		if ret != nil {
			for j := range ret {
				ret[j].TestName = entry.Def.Name
			}
			messages = append(messages, ret...)
		}
	}
	if collector != nil {
		// The collector is dispatch's private hand-off to the calls above, and a
		// check that acknowledges the archive sources its message at the File it
		// was handed: strip it before any of these travel on, or one finding pins
		// a whole member list in the result set.
		for i := range messages {
			if src, ok := messages[i].Source.(structs.File); ok && src.MemberNames != nil {
				src.MemberNames = nil
				messages[i].Source = src
			}
		}

		if members, ok := collector.Result(); ok {
			// The walk labels its members with the archive's Name, the file-list
			// reader with its display name - which for a CKAN resource is a
			// different string, and what every finding of this archive has always
			// carried.
			display := archiveFile.GetDisplayName()
			for i := range members {
				members[i].ArchiveName = display
			}
			return append(messages, archiveFileListMemberChecks(ctx, sink, listEntries, archiveFile, members)...)
		}
	}
	// No member list came out of the content walk, whatever stopped it: the
	// listing the file-list pass would have done is then still owed, and it
	// reaches its own verdict on the same bytes - the acknowledgement for a list
	// that busts the walk bounds or an archive it cannot read, the diagnostic for
	// one it cannot parse.
	if ctx.Err() != nil || len(listEntries) == 0 {
		return messages
	}
	return append(messages, archiveFileListChecks(ctx, sink, cfg, listEntries, archiveFile)...)
}

// archiveWorkers sizes the archive-member pass: half the CPU budget as a proxy
// for "fewer concurrent extractions, lower peak memory" - the constraint is
// extraction MEMORY, which no CPU quota bounds. Halving alone would collapse a
// 2- or 3-core budget to one extraction at a time (harmless under NumCPU, where
// that floor was unreachable above 3 cores; live once the budget can be capped),
// so the floor is two - never more than the budget itself.
func archiveWorkers(procs int) int {
	return min(procs, max(2, procs/2))
}

// applyArchiveChecksParallel processes the archive-member phase's work list in
// parallel. Archive extraction is memory-intensive, so it uses a fraction of the
// CPU budget - and every archive of the phase rides this one pool, so that
// fraction is the whole phase's concurrent-extraction bound.
func applyArchiveChecksParallel(ctx context.Context, sink *diagSink, workItems []workItem) []structs.Message {
	numWorkers := archiveWorkers(runtime.GOMAXPROCS(0))
	return runChecksPool(ctx, sink, workItems, numWorkers, nil)
}

func applyChecksFilteredByRepository(ctx context.Context, sink *diagSink, entries []checks.PlanEntry, files []structs.File) []structs.Message {
	var messages = []structs.Message{}
	repo := structs.Repository{Files: files}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return messages
		}
		testName := entry.Def.Name
		ret := safeRun(sink, "Check "+testName, "", func() []structs.Message {
			return entry.Def.RunRepository(ctx, repo, entry.Batch, entry.Rules)
		})
		if ret != nil {
			// Add test name to each message
			for i := range ret {
				ret[i].TestName = testName
			}
			messages = append(messages, ret...)
		}
	}
	return messages
}

// ProgressCallback is called during scanning to report progress. Every report
// runs on the goroutine that called the engine - the file phase ticks from the
// pool's collect loop, which is that same goroutine - so the callback is never
// invoked concurrently, and whatever it spends is spent on the scan.
//
// The panic guard is ASYMMETRIC: a panic in a tick is recovered and logged
// (letting it escape the collect loop would break the pool's handshake), a
// panic in a phase's opening report is not and takes the process down.
type ProgressCallback func(structs.Progress)

// noFilesNotice returns a skip-style acknowledgement that no files were found to
// analyse. ApplyAllChecks(WithProgress) appends it whenever the collected file
// set is empty, so every output (CLI plain/TUI/JSON and the server) surfaces the
// same clear, non-issue message - rather than the CLI erroring or the server
// returning a silent empty result. It is modelled on a per-file skip (Skipped =
// true, with a Reason) but is repository-scoped.
func noFilesNotice() structs.Message {
	const reason = "No files were found to analyse."
	return structs.Message{
		Content:  reason,
		Source:   structs.Repository{},
		TestName: "FilesPresent",
		Skipped:  true,
		Reason:   reason,
	}
}

// ApplyAllChecks runs every scope of the plan over the collected files. ctx
// bounds the work coarsely: once its deadline fires (the server's whole-analysis
// timeout) or it is cancelled, the scan stops at the next cancellation point -
// between files, but also mid-archive, so the file in progress does NOT
// necessarily finish. CLI callers pass context.Background().
//
// CONTRACT ON CANCELLATION: the returned set is partial in an UNSPECIFIED way -
// no error, no marker, and no way to tell it from a clean run that found the
// same messages. Findings the workers had already delivered are kept; whatever
// they abandoned is lost. A caller that cares must consult ctx.Err() itself.
//
// plan must come from Compile; both frontends compile it once at startup and
// carry it. Intended entry point is internal/analysis.Run (progress == nil
// branch), which pairs this with the metadata checks; call it rather than this
// directly.
// Panics on a nil plan: dispatching nothing would report every package clean.
//
// The second return value carries the diagnostics THIS PACKAGE emits, neither
// of them a finding: what happened to the SCAN (an archive whose file list could
// not be read, a check that panicked), and - on a run that was not cancelled -
// verdicts on the rule CONFIGURATION, namely a configured rule that matched no
// file and two rules of one check that both applied to one file. Only two of
// the four scopes are reported that way; newRuleReport says why the other two
// cannot be.
// pkg/utils no longer writes any of them to a process global. It is not yet the
// run's whole set: pkg/readers, pkg/collectors and pkg/checks still emit through
// output.GlobalLogger, so a caller wanting everything must drain that too -
// internal/analysis.Run does exactly that and returns the union. See
// structs.Diagnostic for who may see which.
func ApplyAllChecks(ctx context.Context, config config.Config, plan *checks.Plan, files []structs.File) ([]structs.Message, []structs.Diagnostic) {
	if plan == nil {
		panic("utils: ApplyAllChecks requires a compiled *checks.Plan")
	}
	var messages []structs.Message
	sink := &diagSink{rules: newRuleReport(plan)}

	listChecks := plan.Scope(checks.ScopeArchiveFileList)
	memberChecks := plan.Scope(checks.ScopeArchiveMember)
	// The archives the two archive passes may not open, taken away from them
	// once: both have to refuse the same ones, and one message stands for both.
	refusals, remaining := refuseStreamListArchives(ctx, config.General, listChecks, memberChecks, files)

	messages = append(messages, applyChecksFilteredByFile(ctx, sink, plan.Scope(checks.ScopeFile), files)...)
	messages = append(messages, refusals...)
	messages = append(messages, applyChecksFilteredByFileOnArchiveFileList(ctx, sink, config, listChecks, remaining)...)
	messages = append(messages, applyChecksFilteredByFileOnArchive(ctx, sink, config, listChecks, memberChecks, remaining)...)
	messages = append(messages, applyChecksFilteredByRepository(ctx, sink, plan.Scope(checks.ScopeRepository), files)...)

	// Surface a clear, non-issue notice when there was nothing to analyse.
	if len(files) == 0 {
		messages = append(messages, noFilesNotice())
	}

	// Drained HERE, once, and never inside runChecksPool: that function's
	// cancellation exit returns before its deferred stop() joins the workers, so
	// a drain there would race goroutines still running checks. By the time each
	// phase has returned, every worker it started is joined - which is also what
	// makes the rule report's folded marks complete.
	diagnostics := sink.drain()
	// A cancelled run must not accuse the configuration: the scan stopped where
	// the deadline caught it, so "this rule matched nothing" would be a statement
	// about the walk rather than about the config - and on a server timeout it
	// tells the operator a correct config is broken. The overlap half goes with
	// it: one call site is simpler than two, and the pairs a truncated run
	// happened to see are not worth the second one.
	if ctx.Err() == nil {
		diagnostics = append(diagnostics, sink.rules.diagnostics()...)
	}
	return messages, diagnostics
}

// ApplyAllChecksWithProgress is the progress-reporting twin of ApplyAllChecks
// (same check groups, same messages) for the TUI. Every phase runs the same
// dispatch as its twin - the file phase through the same shared helper, which
// hands out its work-item count before executing, so the announced total is
// what will actually run - and ticks it from the pool's collect loop, rate
// limited there. That shared dispatch pools the file phase, so its messages now
// arrive in completion order, not input order, and the rendered order varies
// between runs (long true of ApplyAllChecks, new here). plan is the same
// startup-built plan ApplyAllChecks takes. Intended caller is
// internal/analysis.Run (progress != nil branch);
// everything else takes ApplyAllChecks. Panics on a nil plan, like its twin.
// Cancellation carries the same contract as its twin: a partial, unmarked
// result set, with ctx.Err() as the caller's only signal.
// The diagnostics return carries the same contract as its twin's, the verdicts
// on the rule configuration and their cancellation gate included.
func ApplyAllChecksWithProgress(ctx context.Context, config config.Config, plan *checks.Plan, files []structs.File, progressCallback ProgressCallback) ([]structs.Message, []structs.Diagnostic) {
	if plan == nil {
		panic("utils: ApplyAllChecksWithProgress requires a compiled *checks.Plan")
	}
	var messages []structs.Message
	sink := &diagSink{rules: newRuleReport(plan)}

	fileChecks := plan.Scope(checks.ScopeFile)
	listChecks := plan.Scope(checks.ScopeArchiveFileList)
	memberChecks := plan.Scope(checks.ScopeArchiveMember)
	repositoryChecks := plan.Scope(checks.ScopeRepository)

	// The archives the two archive passes may not open; see ApplyAllChecks.
	refusals, remaining := refuseStreamListArchives(ctx, config.General, listChecks, memberChecks, files)

	// Calculate total number of tests (including skipped tests). The file phase's
	// term, added by begin below, counts WORK ITEMS (one per file) where phases
	// 2-4 count checks, so the counter is no longer "tests" and the file phase's
	// share of the bar is ~6x smaller than a files x checks count.
	totalTests := 0

	// Count ALL archive tests (including skipped ones)
	for _, file := range files {
		if file.IsArchive {
			totalTests += len(listChecks) + len(memberChecks)
		}
	}

	// Count repository tests
	totalTests += len(repositoryChecks)

	testsRun := 0

	// Step 1: File checks (with per-test progress). begin completes the total
	// from the work list the shared file phase built, then hands back the tick.
	// Without a callback it stays nil: no closures, and a nil tick down a
	// dispatch identical to ApplyAllChecks'.
	var begin func(int) func(int)
	if progressCallback != nil {
		begin = func(items int) func(int) {
			totalTests += items
			progressCallback(structs.Progress{Phase: structs.PhaseFileChecks, Current: testsRun, Total: totalTests, Start: true})
			return func(current int) {
				// The tick runs caller code inside the pool's collect loop: a
				// panic there would skip the submit/collect handshake and run
				// the deferred stop against a parked submit (workerPool.stop),
				// in a goroutine no recover reaches. Progress is cosmetic -
				// drop the tick and keep scanning.
				defer func() {
					if r := recover(); r != nil {
						logPanic(sink, "Progress callback", "", r)
					}
				}()
				testsRun = current
				progressCallback(structs.Progress{Phase: structs.PhaseFileChecks, Current: testsRun, Total: totalTests})
			}
		}
	}
	messages = append(messages, applyFileChecks(ctx, sink, fileChecks, files, begin)...)

	// Step 2: Archive file list checks
	if progressCallback != nil {
		progressCallback(structs.Progress{Phase: structs.PhaseArchiveFileList, Current: testsRun, Total: totalTests, Start: true})
	}
	messages = append(messages, refusals...)
	archiveListTests := applyChecksFilteredByFileOnArchiveFileList(ctx, sink, config, listChecks, remaining)
	messages = append(messages, archiveListTests...)
	// Update count for archive list tests (including skipped ones)
	for _, file := range files {
		if file.IsArchive {
			testsRun += len(listChecks)
		}
	}

	// Step 3: Archive content checks
	if progressCallback != nil {
		progressCallback(structs.Progress{Phase: structs.PhaseArchiveContent, Current: testsRun, Total: totalTests, Start: true})
	}
	archiveContentTests := applyChecksFilteredByFileOnArchive(ctx, sink, config, listChecks, memberChecks, remaining)
	messages = append(messages, archiveContentTests...)
	// Update count for archive content tests (including skipped ones)
	for _, file := range files {
		if file.IsArchive {
			testsRun += len(memberChecks)
		}
	}

	// Step 4: Repository checks
	if progressCallback != nil {
		progressCallback(structs.Progress{Phase: structs.PhaseRepository, Current: testsRun, Total: totalTests, Start: true})
	}
	repoTests := applyChecksFilteredByRepository(ctx, sink, repositoryChecks, files)
	messages = append(messages, repoTests...)
	testsRun += len(repositoryChecks)

	// Final step
	if progressCallback != nil {
		progressCallback(structs.Progress{Phase: structs.PhaseFinalizing, Current: testsRun, Total: totalTests, Start: true})
	}
	// Surface a clear, non-issue notice when there was nothing to analyse.
	if len(files) == 0 {
		messages = append(messages, noFilesNotice())
	}

	// Drained once, after every phase, and the rule verdicts appended only to an
	// uncancelled run; see ApplyAllChecks.
	diagnostics := sink.drain()
	if ctx.Err() == nil {
		diagnostics = append(diagnostics, sink.rules.diagnostics()...)
	}
	return messages, diagnostics
}
