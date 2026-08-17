package utils

import (
	"context"
	"log"
	"runtime"
	"runtime/debug"
	"sync"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// workerPool manages concurrent processing of files
type workerPool struct {
	numWorkers int
	workChan   chan workItem
	resultChan chan workResult
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
	// sink receives the run diagnostics a worker produces (a recovered check
	// panic). It is a constructor argument rather than a field set afterwards
	// so a caller cannot forget it, and newWorkerPool rejects nil outright: a
	// nil sink surfaces as a nil dereference inside a deferred recover, which
	// is a second panic while unwinding that no recover catches and the bare
	// worker goroutine does not survive.
	sink *diagSink
}

// workItem represents a unit of work to be processed: one file with the checks
// that matched it, each carrying its own matched rules. It lives here rather
// than in pkg/optimization because it holds bound rules, and pkg/checks already
// imports that package.
type workItem struct {
	File   structs.File
	Scope  checks.Scope
	Checks []checks.PlanEntry
}

// workResult represents the result of processing a work item
type workResult struct {
	Messages []structs.Message
}

// newWorkerPool creates a new worker pool with the specified number of workers.
// Cancelling ctx tears the pool down; ctx must not be nil. stop must still be
// called either way - it is what releases the derived context. sink collects
// the diagnostics the workers produce and must not be nil.
func newWorkerPool(ctx context.Context, sink *diagSink, numWorkers int) *workerPool {
	if sink == nil {
		panic("utils: newWorkerPool requires a diagnostics sink")
	}
	if numWorkers <= 0 {
		numWorkers = runtime.GOMAXPROCS(0)
	}

	poolCtx, cancel := context.WithCancel(ctx)

	return &workerPool{
		numWorkers: numWorkers,
		sink:       sink,
		workChan:   make(chan workItem, numWorkers*2), // Buffer to prevent blocking
		resultChan: make(chan workResult, numWorkers*2),
		ctx:        poolCtx,
		cancel:     cancel,
	}
}

// start initializes and starts all workers
func (wp *workerPool) start() {
	for i := 0; i < wp.numWorkers; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}
}

// worker processes work items from the work channel
func (wp *workerPool) worker(id int) {
	defer wp.wg.Done()

	for {
		select {
		case <-wp.ctx.Done():
			return
		case work, ok := <-wp.workChan:
			if !ok {
				return
			}

			messages := wp.processWorkItem(work)

			select {
			case wp.resultChan <- workResult{
				Messages: messages,
			}:
			case <-wp.ctx.Done():
				return
			}
		}
	}
}

// processWorkItem applies all checks to a single file
// This ensures all checks for a single file run in the same worker to avoid IO conflicts
func (wp *workerPool) processWorkItem(work workItem) []structs.Message {
	var allMessages []structs.Message

	// Run all checks for this file sequentially in the same worker
	// This avoids IO conflicts from multiple goroutines reading the same file
	for _, entry := range work.Checks {
		messages := safeRunCheck(wp.ctx, wp.sink, entry, work.File, work.Scope)
		if len(messages) > 0 {
			// Add test name to each message
			for i := range messages {
				messages[i].TestName = entry.Def.Name
			}
			allMessages = append(allMessages, messages...)
		}
	}

	return allMessages
}

// logPanic reports a recovered check panic. The panic value and stack go to
// stderr for the operator; the run's sink gets only a short, path-free notice -
// tagged with subject (a display name) when the failure concerns one file, so
// the server can acknowledge that file as unscanned.
func logPanic(sink *diagSink, what, subject string, recovered interface{}) {
	log.Printf("%s panicked: %v\n%s", what, recovered, debug.Stack())
	sink.add(structs.DiagError, subject, "%s failed: internal error", what)
}

// safeRun is the canonical panic guard around check execution: it runs fn and
// converts a panic into a logged failure instead of letting it propagate. A
// panic in a pool goroutine is not covered by any request-level recover, so
// without this a single buggy check (or unreadable/crafted archive) kills the
// whole process.
func safeRun(sink *diagSink, what, subject string, fn func() []structs.Message) (messages []structs.Message) {
	defer func() {
		if r := recover(); r != nil {
			logPanic(sink, what, subject, r)
			messages = nil
		}
	}()
	return fn()
}

// safeRunCheck is safeRun specialized for one check over one file. Its panic
// label is built inside the recover branch, not handed in: concatenating it up
// front cost a string and an allocation for every check that did NOT panic -
// which is every check, on every file.
func safeRunCheck(ctx context.Context, sink *diagSink, entry checks.PlanEntry, file structs.File, scope checks.Scope) (messages []structs.Message) {
	defer func() {
		if r := recover(); r != nil {
			logPanic(sink, "Check "+entry.Def.Name+" on file '"+file.Name+"'", file.GetDisplayName(), r)
			messages = nil
		}
	}()
	if !entry.Def.Scopes.Has(scope) {
		// Wrong-phase dispatch would silently swap the acquisition (a file's own
		// content for an archive's members). The guard is one bit test; safeRun's
		// recover turns it into a logged internal error rather than bad results.
		panic("check " + entry.Def.Name + " does not serve scope " + scope.String())
	}
	return entry.Def.RunFile(ctx, file, scope, entry.Batch, entry.Rules)
}

// submit adds a work item to the processing queue (blocks until space is available)
func (wp *workerPool) submit(work workItem) bool {
	// Check if context is cancelled first to avoid sending on closed channel
	select {
	case <-wp.ctx.Done():
		return false
	default:
	}
	// Now try to send
	select {
	case wp.workChan <- work:
		return true
	case <-wp.ctx.Done():
		return false
	}
}

// results returns the result channel for consuming processed results
func (wp *workerPool) results() <-chan workResult {
	return wp.resultChan
}

// stop gracefully shuts down the worker pool.
//
// INVARIANT: stop must not run concurrently with a blocked submit. submit's
// select waits on both the work channel and the pool context; cancel()+close()
// here can make BOTH cases ready, and if the runtime commits the send case the
// process panics with "send on closed channel". Callers must ensure all submit
// calls have returned before stop runs. runChecksPool provides this twice over:
// on the normal path by the submit/collect handshake (collect exactly
// `submitted` results, which happens-after the submitter goroutine's last
// submit), and on the cancellation path by reading the submitter's count before
// it returns. The second argument holds only because this pool's context is a
// DESCENDANT of the one the collector watches - that is what unblocks a submit
// parked on a full work channel. Giving the pool an independent context would
// turn that read into a permanent block.
func (wp *workerPool) stop() {
	wp.cancel() // Cancel context first to stop accepting new work
	close(wp.workChan)
	wp.wg.Wait()
	close(wp.resultChan)
}
