# Task 08: Background Job Worker

**Status:** `[ ]` Not started

**Dependencies:** Task 05, Task 07

## Objective

Implement the background worker that processes analysis jobs asynchronously.

## Deliverables

### 1. `internal/server/worker/worker.go`

```go
package worker

import (
    "context"
    "log"
    "sync"
    "time"

    "github.com/eawag-rdm/pc/internal/server/analyzer"
    "github.com/eawag-rdm/pc/internal/server/cache"
    "github.com/eawag-rdm/pc/internal/server/models"
    "github.com/eawag-rdm/pc/internal/server/store"
)

// Job represents a job to be processed
type Job struct {
    ID           string
    CollectionID string
    FilesHash    string
    Request      *models.AnalysisRequest
}

// Worker processes analysis jobs in the background
type Worker struct {
    jobStore    *store.JobStore
    cache       cache.Cache
    analyzer    *analyzer.Analyzer
    cacheTTL    time.Duration

    jobs        chan *Job
    maxWorkers  int
    wg          sync.WaitGroup
    ctx         context.Context
    cancel      context.CancelFunc
}

// Config holds worker configuration
type Config struct {
    MaxWorkers int           // Max concurrent analysis jobs
    QueueSize  int           // Size of job queue
    CacheTTL   time.Duration // How long to cache results
}

// New creates a new worker
func New(
    jobStore *store.JobStore,
    cache cache.Cache,
    analyzer *analyzer.Analyzer,
    cfg Config,
) *Worker

// Start begins processing jobs
func (w *Worker) Start()

// Stop gracefully shuts down the worker
func (w *Worker) Stop()

// Submit adds a job to the processing queue
// Returns false if queue is full
func (w *Worker) Submit(job *Job) bool

// QueueLength returns the current number of queued jobs
func (w *Worker) QueueLength() int
```

### 2. Implementation Details

```go
func (w *Worker) Start() {
    for i := 0; i < w.maxWorkers; i++ {
        w.wg.Add(1)
        go w.process()
    }
}

func (w *Worker) Stop() {
    w.cancel()
    close(w.jobs)
    w.wg.Wait()
}

func (w *Worker) Submit(job *Job) bool {
    select {
    case w.jobs <- job:
        return true
    default:
        return false // Queue is full
    }
}

func (w *Worker) process() {
    defer w.wg.Done()

    for {
        select {
        case <-w.ctx.Done():
            return
        case job, ok := <-w.jobs:
            if !ok {
                return
            }
            w.processJob(job)
        }
    }
}

func (w *Worker) processJob(job *Job) {
    ctx := context.Background()

    // Update status to running
    w.jobStore.UpdateStatus(ctx, job.ID, models.JobStatusRunning, "Starting analysis...")

    // Run analysis with progress updates
    result, err := w.analyzer.AnalyzeWithProgress(job.Request, func(current, total int, msg string) {
        progress := fmt.Sprintf("%d/%d - %s", current, total, msg)
        w.jobStore.UpdateStatus(ctx, job.ID, models.JobStatusRunning, progress)
    })

    if err != nil {
        w.jobStore.SetFailed(ctx, job.ID, err.Error())
        return
    }

    // Store in cache
    w.cache.Set(ctx, &models.CacheEntry{
        CollectionID: job.CollectionID,
        FilesHash:    job.FilesHash,
        Result:       result,
        CreatedAt:    time.Now(),
        ExpiresAt:    time.Now().Add(w.cacheTTL),
    })

    // Mark job completed
    w.jobStore.SetCompleted(ctx, job.ID, result)
}
```

### 3. `internal/server/worker/worker_test.go`

```go
package worker

import (
    "context"
    "sync/atomic"
    "testing"
    "time"

    "github.com/eawag-rdm/pc/internal/server/models"
)

func TestWorker_StartStop(t *testing.T) {
    // Test worker starts and stops cleanly
}

func TestWorker_Submit(t *testing.T) {
    // Test submitting a job
    // Test queue full scenario
}

func TestWorker_ProcessJob(t *testing.T) {
    // Test job processing updates status
    // Test successful completion
    // Test failure handling
}

func TestWorker_Concurrent(t *testing.T) {
    // Test multiple jobs processed concurrently
    // Test max workers limit is respected
}

func TestWorker_Progress(t *testing.T) {
    // Test progress updates are stored
}

func TestWorker_Caching(t *testing.T) {
    // Test result is cached after completion
}

// Mock implementations for testing
type mockJobStore struct {
    jobs map[string]*models.Job
}

type mockCache struct {
    entries map[string]*models.CacheEntry
}

type mockAnalyzer struct {
    result string
    err    error
    delay  time.Duration
}
```

## Tests Required

- `TestWorker_StartStop` - Lifecycle management
- `TestWorker_Submit` - Job submission and queue handling
- `TestWorker_ProcessJob` - Job processing flow
- `TestWorker_Concurrent` - Concurrency behavior
- `TestWorker_Progress` - Progress reporting
- `TestWorker_Caching` - Cache integration

## Acceptance Criteria

- [ ] Worker processes jobs in background
- [ ] Multiple workers run concurrently (up to max)
- [ ] Queue handles overflow gracefully
- [ ] Progress is updated during analysis
- [ ] Results are cached on completion
- [ ] Failures are handled and recorded
- [ ] Clean shutdown waits for in-progress jobs
- [ ] All tests pass
