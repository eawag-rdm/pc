package optimization

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// WorkerPool manages concurrent processing of files
type WorkerPool struct {
	numWorkers    int
	workChan      chan WorkItem
	resultChan    chan WorkResult
	wg            sync.WaitGroup
	ctx           context.Context
	cancel        context.CancelFunc
	maxQueueSize  int
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
	Error    error
	Duration time.Duration
}

// NewWorkerPool creates a new worker pool with the specified number of workers
func NewWorkerPool(numWorkers int) *WorkerPool {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}
	
	ctx, cancel := context.WithCancel(context.Background())
	
	return &WorkerPool{
		numWorkers:   numWorkers,
		workChan:     make(chan WorkItem, numWorkers*2), // Buffer to prevent blocking
		resultChan:   make(chan WorkResult, numWorkers*2),
		ctx:          ctx,
		cancel:       cancel,
		maxQueueSize: numWorkers * 4,
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
			
			start := time.Now()
			messages := wp.processWorkItem(work)
			duration := time.Since(start)
			
			select {
			case wp.resultChan <- WorkResult{
				Messages: messages,
				Duration: duration,
			}:
			case <-wp.ctx.Done():
				return
			}
		}
	}
}

// getFunctionName returns the name of a function
func getFunctionName(i interface{}) string {
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
		testName := getFunctionName(check)
		messages := check(work.File, work.Config)
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

// Submit adds a work item to the processing queue
func (wp *WorkerPool) Submit(work WorkItem) bool {
	select {
	case wp.workChan <- work:
		return true
	case <-wp.ctx.Done():
		return false
	default:
		// Channel is full, could implement backpressure here
		return false
	}
}

// Results returns the result channel for consuming processed results
func (wp *WorkerPool) Results() <-chan WorkResult {
	return wp.resultChan
}

// Stop gracefully shuts down the worker pool
func (wp *WorkerPool) Stop() {
	close(wp.workChan)
	wp.wg.Wait()
	wp.cancel()
	close(wp.resultChan)
}


// ArchiveWorkerPool specifically handles archive processing with better memory management
type ArchiveWorkerPool struct {
	*WorkerPool
	memoryLimit int64
	currentMem  int64
	memMutex    sync.RWMutex
}

// NewArchiveWorkerPool creates a worker pool optimized for archive processing
func NewArchiveWorkerPool(numWorkers int, memoryLimitMB int64) *ArchiveWorkerPool {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU() / 2 // Use fewer workers for memory-intensive archive work
		if numWorkers < 1 {
			numWorkers = 1
		}
	}
	
	basePool := NewWorkerPool(numWorkers)
	
	return &ArchiveWorkerPool{
		WorkerPool:  basePool,
		memoryLimit: memoryLimitMB * 1024 * 1024,
		currentMem:  0,
	}
}

// CanAllocate checks if we can allocate the requested memory
func (awp *ArchiveWorkerPool) CanAllocate(bytes int64) bool {
	awp.memMutex.RLock()
	defer awp.memMutex.RUnlock()
	
	return awp.currentMem+bytes <= awp.memoryLimit
}

// AllocateMemory reserves memory for processing
func (awp *ArchiveWorkerPool) AllocateMemory(bytes int64) bool {
	awp.memMutex.Lock()
	defer awp.memMutex.Unlock()
	
	if awp.currentMem+bytes <= awp.memoryLimit {
		awp.currentMem += bytes
		return true
	}
	return false
}

// ReleaseMemory frees previously allocated memory
func (awp *ArchiveWorkerPool) ReleaseMemory(bytes int64) {
	awp.memMutex.Lock()
	defer awp.memMutex.Unlock()
	
	awp.currentMem -= bytes
	if awp.currentMem < 0 {
		awp.currentMem = 0
	}
}