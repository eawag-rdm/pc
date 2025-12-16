# Task 02: Data Models

**Status:** `[ ]` Not started

**Dependencies:** Task 01

## Objective

Define all data types used by the server for requests, responses, and internal state.

## Deliverables

### 1. `internal/server/models/types.go`

```go
package models

import (
    "encoding/json"
    "time"
)

// ============================================================================
// Request Types
// ============================================================================

// AnalysisRequest represents a request to analyze a file collection
type AnalysisRequest struct {
    CollectionID string     `json:"collection_id"` // e.g., CKAN package ID
    Files        []FileInfo `json:"files"`
}

// FileInfo represents a single file in the collection
type FileInfo struct {
    FileID       string    `json:"file_id"`       // e.g., CKAN resource ID
    Path         string    `json:"path"`          // filesystem path
    LastModified time.Time `json:"last_modified"` // for cache invalidation
}

// ============================================================================
// Response Types
// ============================================================================

// JobResponse is returned when querying job status
type JobResponse struct {
    JobID        string          `json:"job_id"`
    CollectionID string          `json:"collection_id"`
    Status       JobStatus       `json:"status"`
    Progress     string          `json:"progress,omitempty"`
    CreatedAt    time.Time       `json:"created_at"`
    CompletedAt  *time.Time      `json:"completed_at,omitempty"`
    Result       json.RawMessage `json:"result,omitempty"`
    Error        string          `json:"error,omitempty"`
    Cached       bool            `json:"cached"`
}

// ResultResponse is returned when querying cached results directly
type ResultResponse struct {
    CollectionID string          `json:"collection_id"`
    CachedAt     time.Time       `json:"cached_at"`
    ExpiresAt    time.Time       `json:"expires_at"`
    Result       json.RawMessage `json:"result"`
}

// ErrorResponse is returned on errors
type ErrorResponse struct {
    Error   string `json:"error"`
    Code    string `json:"code,omitempty"`
    Details string `json:"details,omitempty"`
}

// HealthResponse is returned by the health endpoint
type HealthResponse struct {
    Status    string `json:"status"` // "ok" or "degraded"
    Version   string `json:"version"`
    Timestamp string `json:"timestamp"`
}

// ============================================================================
// Job Status
// ============================================================================

type JobStatus string

const (
    JobStatusPending   JobStatus = "pending"
    JobStatusRunning   JobStatus = "running"
    JobStatusCompleted JobStatus = "completed"
    JobStatusFailed    JobStatus = "failed"
)

// ============================================================================
// Internal/Database Types
// ============================================================================

// Job represents a job record in the database
type Job struct {
    ID           string     `db:"id"`
    CollectionID string     `db:"collection_id"`
    FilesHash    string     `db:"files_hash"`
    Status       JobStatus  `db:"status"`
    Progress     string     `db:"progress"`
    Result       *string    `db:"result"`
    Error        *string    `db:"error"`
    CreatedAt    time.Time  `db:"created_at"`
    CompletedAt  *time.Time `db:"completed_at"`
}

// ToResponse converts a Job to a JobResponse
func (j *Job) ToResponse() *JobResponse {
    resp := &JobResponse{
        JobID:        j.ID,
        CollectionID: j.CollectionID,
        Status:       j.Status,
        Progress:     j.Progress,
        CreatedAt:    j.CreatedAt,
        CompletedAt:  j.CompletedAt,
        Cached:       false,
    }
    if j.Result != nil {
        resp.Result = json.RawMessage(*j.Result)
    }
    if j.Error != nil {
        resp.Error = *j.Error
    }
    return resp
}

// CacheEntry represents a cached analysis result
type CacheEntry struct {
    CollectionID string    `db:"collection_id"`
    FilesHash    string    `db:"files_hash"`
    Result       string    `db:"result"`
    CreatedAt    time.Time `db:"created_at"`
    ExpiresAt    time.Time `db:"expires_at"`
}

// APIToken represents an API token record
type APIToken struct {
    TokenHash string     `db:"token_hash"`
    Name      string     `db:"name"`
    CreatedAt time.Time  `db:"created_at"`
    ExpiresAt *time.Time `db:"expires_at"`
    Active    bool       `db:"active"`
}
```

### 2. `internal/server/models/types_test.go`

```go
package models

import (
    "encoding/json"
    "testing"
    "time"
)

func TestJobToResponse(t *testing.T) {
    // Test basic conversion
    // Test with nil Result
    // Test with nil Error
    // Test with CompletedAt set
}

func TestAnalysisRequestJSON(t *testing.T) {
    // Test JSON marshaling/unmarshaling
    // Test with various time formats
}

func TestJobStatus(t *testing.T) {
    // Test status constants are correct strings
}
```

## Tests Required

- `TestJobToResponse` - Verify Job to JobResponse conversion
- `TestAnalysisRequestJSON` - Verify JSON serialization/deserialization
- `TestJobStatus` - Verify status constants

## Acceptance Criteria

- [ ] All types defined as specified
- [ ] `Job.ToResponse()` method implemented
- [ ] All tests pass
- [ ] `go build ./...` succeeds
