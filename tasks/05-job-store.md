# Task 05: Job Store

**Status:** `[ ]` Not started

**Dependencies:** Task 03

## Objective

Implement the job store for tracking analysis job lifecycle.

## Deliverables

### 1. `internal/server/store/jobs.go`

```go
package store

import (
    "context"
    "database/sql"
    "errors"
    "time"

    "github.com/eawag-rdm/pc/internal/server/models"
)

var (
    ErrJobNotFound = errors.New("job not found")
)

// JobStore manages job records in the database
type JobStore struct {
    db *sql.DB
}

// NewJobStore creates a new job store
func NewJobStore(db *sql.DB) *JobStore

// Create inserts a new job record
func (s *JobStore) Create(ctx context.Context, job *models.Job) error

// Get retrieves a job by ID
func (s *JobStore) Get(ctx context.Context, jobID string) (*models.Job, error)

// FindPending finds a pending/running job for the given collection and hash
// Used to avoid duplicate analysis of the same files
func (s *JobStore) FindPending(ctx context.Context, collectionID, filesHash string) (*models.Job, error)

// UpdateStatus updates job status and progress
func (s *JobStore) UpdateStatus(ctx context.Context, jobID string, status models.JobStatus, progress string) error

// SetCompleted marks job as completed with result
func (s *JobStore) SetCompleted(ctx context.Context, jobID string, result string) error

// SetFailed marks job as failed with error message
func (s *JobStore) SetFailed(ctx context.Context, jobID string, errMsg string) error

// ListByCollection returns all jobs for a collection (most recent first)
func (s *JobStore) ListByCollection(ctx context.Context, collectionID string, limit int) ([]*models.Job, error)

// Cleanup removes old completed/failed jobs older than the given duration
func (s *JobStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error)
```

### 2. `internal/server/store/jobs_test.go`

```go
package store

import (
    "context"
    "testing"
    "time"

    "github.com/eawag-rdm/pc/internal/server/models"
)

func TestJobStore_Create(t *testing.T) {
    // Test creating a new job
    // Test creating with same ID (should fail)
}

func TestJobStore_Get(t *testing.T) {
    // Test get existing job
    // Test get non-existent job
}

func TestJobStore_FindPending(t *testing.T) {
    // Test finding pending job
    // Test finding running job
    // Test no pending job exists
    // Test completed job is not returned
}

func TestJobStore_UpdateStatus(t *testing.T) {
    // Test status update
    // Test progress update
    // Test update non-existent job
}

func TestJobStore_SetCompleted(t *testing.T) {
    // Test marking job completed
    // Test result is stored
    // Test completed_at is set
}

func TestJobStore_SetFailed(t *testing.T) {
    // Test marking job failed
    // Test error message is stored
}

func TestJobStore_ListByCollection(t *testing.T) {
    // Test listing jobs
    // Test order (most recent first)
    // Test limit
    // Test empty collection
}

func TestJobStore_Cleanup(t *testing.T) {
    // Test cleanup removes old jobs
    // Test cleanup keeps recent jobs
    // Test cleanup keeps pending/running jobs
}
```

## Tests Required

- `TestJobStore_Create` - Job creation
- `TestJobStore_Get` - Job retrieval
- `TestJobStore_FindPending` - Finding duplicate jobs
- `TestJobStore_UpdateStatus` - Status/progress updates
- `TestJobStore_SetCompleted` - Completion handling
- `TestJobStore_SetFailed` - Failure handling
- `TestJobStore_ListByCollection` - Listing jobs
- `TestJobStore_Cleanup` - Old job removal

## Acceptance Criteria

- [ ] Jobs can be created and retrieved
- [ ] Pending jobs can be found to avoid duplicates
- [ ] Status transitions work correctly
- [ ] Completed/failed jobs have correct timestamps
- [ ] Cleanup removes only old completed/failed jobs
- [ ] All tests pass
