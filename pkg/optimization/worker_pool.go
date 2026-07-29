package optimization

import (
	"context"
	"log"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// WorkerPool manages concurrent processing of files
type WorkerPool struct {
	numWorkers int
	workChan   chan WorkItem
	resultChan chan WorkResult
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
}

// WorkItem represents a unit of work to be processed
type WorkItem struct {
	File   structs.File
	Checks []func(structs.File, config.Config) []structs.Message
	Config config.Config
}

// WorkResult represents the result of processing a work item
type WorkResult struct {
	Messages []structs.Message
}

// NewWorkerPool creates a new worker pool with the specified number of workers
func NewWorkerPool(numWorkers int) *WorkerPool {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &WorkerPool{
		numWorkers: numWorkers,
		workChan:   make(chan WorkItem, numWorkers*2), // Buffer to prevent blocking
		resultChan: make(chan WorkResult, numWorkers*2),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start initializes and starts all workers
func (wp *WorkerPool) Start() {
	for i := 0; i < wp.numWorkers; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}
}

// worker processes work items from the work channel
func (wp *WorkerPool) worker(id int) {
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
			case wp.resultChan <- WorkResult{
				Messages: messages,
			}:
			case <-wp.ctx.Done():
				return
			}
		}
	}
}

// FunctionName returns the bare name of a function value. It is the single
// shared implementation for deriving check/test names (pkg/utils uses it too).
func FunctionName(i interface{}) string {
	fullName := runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	parts := strings.Split(fullName, ".")
	return parts[len(parts)-1]
}

// processWorkItem applies all checks to a single file
// This ensures all checks for a single file run in the same worker to avoid IO conflicts
func (wp *WorkerPool) processWorkItem(work WorkItem) []structs.Message {
	var allMessages []structs.Message

	// Run all checks for this file sequentially in the same worker
	// This avoids IO conflicts from multiple goroutines reading the same file
	for _, check := range work.Checks {
		testName := FunctionName(check)
		messages := SafeRunCheck(check, work.File, work.Config, testName)
		if len(messages) > 0 {
			// Add test name to each message
			for i := range messages {
				messages[i].TestName = testName
			}
			allMessages = append(allMessages, messages...)
		}
	}

	return allMessages
}

// SafeRun is the canonical panic guard around check execution: it runs fn and
// converts a panic into a logged failure instead of letting it propagate. A
// panic in a pool goroutine is not covered by any request-level recover, so
// without this a single buggy check (or unreadable/crafted archive) kills the
// whole process. The panic value and stack go to stderr for the operator; the
// buffered GlobalLogger gets only a short, path-free notice - tagged with
// subject (a display name) when the failure concerns one file, so the server
// can acknowledge that file as unscanned. It lives in this package (not
// pkg/utils) because the worker pool needs it and utils imports optimization.
func SafeRun(what, subject string, fn func() []structs.Message) (messages []structs.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("%s panicked: %v\n%s", what, r, debug.Stack())
			if subject != "" {
				output.GlobalLogger.FileError(subject, "%s failed: internal error", what)
			} else {
				output.GlobalLogger.Error("%s failed: internal error", what)
			}
			messages = nil
		}
	}()
	return fn()
}

// SafeRunCheck is SafeRun specialized for a single file check.
func SafeRunCheck(check func(structs.File, config.Config) []structs.Message, file structs.File, cfg config.Config, testName string) []structs.Message {
	return SafeRun("Check "+testName+" on file '"+file.Name+"'", file.GetDisplayName(), func() []structs.Message {
		return check(file, cfg)
	})
}

// Submit adds a work item to the processing queue (blocks until space is available)
func (wp *WorkerPool) Submit(work WorkItem) bool {
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

// Results returns the result channel for consuming processed results
func (wp *WorkerPool) Results() <-chan WorkResult {
	return wp.resultChan
}

// Stop gracefully shuts down the worker pool.
//
// INVARIANT: Stop must not run concurrently with a blocked Submit. Submit's
// select waits on both the work channel and the pool context; cancel()+close()
// here can make BOTH cases ready, and if the runtime commits the send case the
// process panics with "send on closed channel". Callers must ensure all Submit
// calls have returned before Stop runs - the submit/collect handshake in
// pkg/utils/check_utils.go (collect exactly `submitted` results, which
// happens-after the submitter goroutine's last Submit) provides this.
func (wp *WorkerPool) Stop() {
	wp.cancel() // Cancel context first to stop accepting new work
	close(wp.workChan)
	wp.wg.Wait()
	close(wp.resultChan)
}
