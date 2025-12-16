# Task 09: HTTP Handlers

**Status:** `[ ]` Not started

**Dependencies:** Task 04, Task 05, Task 08

## Objective

Implement the HTTP API handlers for the server.

## Deliverables

### 1. `internal/server/api/responses.go`

```go
package api

import (
    "encoding/json"
    "net/http"

    "github.com/eawag-rdm/pc/internal/server/models"
)

// respondJSON sends a JSON response
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(data)
}

// respondError sends an error response
func respondError(w http.ResponseWriter, status int, code, message string) {
    respondJSON(w, status, models.ErrorResponse{
        Error: message,
        Code:  code,
    })
}
```

### 2. `internal/server/api/handlers.go`

```go
package api

import (
    "encoding/json"
    "net/http"
    "time"

    "github.com/eawag-rdm/pc/internal/server/cache"
    "github.com/eawag-rdm/pc/internal/server/models"
    "github.com/eawag-rdm/pc/internal/server/store"
    "github.com/eawag-rdm/pc/internal/server/worker"
)

// Handler holds dependencies for HTTP handlers
type Handler struct {
    jobStore *store.JobStore
    cache    cache.Cache
    worker   *worker.Worker
    version  string
}

// NewHandler creates a new handler
func NewHandler(
    jobStore *store.JobStore,
    cache cache.Cache,
    worker *worker.Worker,
    version string,
) *Handler

// --- Health Check ---

// Health handles GET /health
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
    respondJSON(w, http.StatusOK, models.HealthResponse{
        Status:    "ok",
        Version:   h.version,
        Timestamp: time.Now().UTC().Format(time.RFC3339),
    })
}

// --- Analysis ---

// Analyze handles POST /api/v1/analyze
// Accepts AnalysisRequest, returns JobResponse
func (h *Handler) Analyze(w http.ResponseWriter, r *http.Request) {
    // 1. Parse request body
    // 2. Validate request
    // 3. Compute files hash
    // 4. Check cache - if hit, return immediately with cached=true
    // 5. Check for existing pending job with same hash
    // 6. Create new job
    // 7. Submit to worker
    // 8. Return 202 Accepted with job info
}

// --- Jobs ---

// GetJob handles GET /api/v1/jobs/{job_id}
func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
    // 1. Extract job_id from path
    // 2. Fetch job from store
    // 3. Return job response
}

// --- Results ---

// GetResult handles GET /api/v1/results/{collection_id}
func (h *Handler) GetResult(w http.ResponseWriter, r *http.Request) {
    // 1. Extract collection_id from path
    // 2. Fetch from cache (any hash)
    // 3. Return result or 404
}

// DeleteResult handles DELETE /api/v1/results/{collection_id}
func (h *Handler) DeleteResult(w http.ResponseWriter, r *http.Request) {
    // 1. Extract collection_id from path
    // 2. Delete from cache
    // 3. Return 204 No Content
}
```

### 3. Route Registration

```go
// RegisterRoutes sets up the HTTP routes
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
    // Public
    mux.HandleFunc("GET /health", h.Health)

    // Protected (auth middleware applied separately)
    mux.HandleFunc("POST /api/v1/analyze", h.Analyze)
    mux.HandleFunc("GET /api/v1/jobs/{job_id}", h.GetJob)
    mux.HandleFunc("GET /api/v1/results/{collection_id}", h.GetResult)
    mux.HandleFunc("DELETE /api/v1/results/{collection_id}", h.DeleteResult)
}
```

### 4. `internal/server/api/handlers_test.go`

```go
package api

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/eawag-rdm/pc/internal/server/models"
)

func TestHandler_Health(t *testing.T) {
    // Test health endpoint returns correct structure
}

func TestHandler_Analyze_Success(t *testing.T) {
    // Test successful analysis submission
    // Verify 202 response
    // Verify job_id is returned
}

func TestHandler_Analyze_CacheHit(t *testing.T) {
    // Test cache hit returns immediately
    // Verify cached=true in response
}

func TestHandler_Analyze_DuplicateJob(t *testing.T) {
    // Test submitting same analysis twice
    // Should return existing job
}

func TestHandler_Analyze_InvalidRequest(t *testing.T) {
    // Test invalid JSON
    // Test missing required fields
}

func TestHandler_Analyze_QueueFull(t *testing.T) {
    // Test behavior when worker queue is full
}

func TestHandler_GetJob_Found(t *testing.T) {
    // Test getting existing job
}

func TestHandler_GetJob_NotFound(t *testing.T) {
    // Test getting non-existent job
}

func TestHandler_GetResult_Found(t *testing.T) {
    // Test getting cached result
}

func TestHandler_GetResult_NotFound(t *testing.T) {
    // Test getting non-existent result
}

func TestHandler_DeleteResult(t *testing.T) {
    // Test deleting result
}
```

## API Endpoints Summary

| Method | Path | Description | Response Code |
|--------|------|-------------|---------------|
| GET | /health | Health check | 200 |
| POST | /api/v1/analyze | Submit analysis | 202 / 200 (cached) |
| GET | /api/v1/jobs/{id} | Get job status | 200 / 404 |
| GET | /api/v1/results/{id} | Get cached result | 200 / 404 |
| DELETE | /api/v1/results/{id} | Delete cached result | 204 |

## Tests Required

- Health endpoint test
- Analyze: success, cache hit, duplicate, invalid request, queue full
- GetJob: found, not found
- GetResult: found, not found
- DeleteResult: success

## Acceptance Criteria

- [ ] All endpoints implemented as specified
- [ ] Correct HTTP status codes returned
- [ ] Request validation works
- [ ] Cache is checked before creating jobs
- [ ] Duplicate jobs are detected
- [ ] All tests pass
