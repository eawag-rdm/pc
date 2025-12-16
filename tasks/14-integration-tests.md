# Task 14: Integration Tests

**Status:** `[ ]` Not started

**Dependencies:** Task 13

## Objective

Create end-to-end integration tests that verify the complete server functionality.

## Deliverables

### 1. `internal/server/integration_test.go`

```go
//go:build integration

package server_test

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/eawag-rdm/pc/internal/server"
    "github.com/eawag-rdm/pc/internal/server/models"
    "github.com/eawag-rdm/pc/internal/server/store"
)

// testServer holds a running test server instance
type testServer struct {
    server  *server.Server
    baseURL string
    token   string
    cleanup func()
}

// setupTestServer creates and starts a test server
func setupTestServer(t *testing.T) *testServer {
    t.Helper()

    tmpDir := t.TempDir()
    dbPath := filepath.Join(tmpDir, "test.db")

    // Create minimal pc.toml for testing
    pcConfigPath := filepath.Join(tmpDir, "pc.toml")
    pcConfig := `[operation.main]
collector = "LocalCollector"

[test.HasOnlyASCII]
blacklist = []
whitelist = []

[collector.LocalCollector]
attrs = {includeFolders = false}
`
    os.WriteFile(pcConfigPath, []byte(pcConfig), 0644)

    cfg := &server.Config{
        Host:         "localhost",
        Port:         0, // Let OS assign port
        PCConfigPath: pcConfigPath,
        DatabasePath: dbPath,
        RateLimit: server.RateLimitConfig{
            RequestsPerSecond: 100, // High for tests
            BurstSize:         200,
        },
        Cache: server.CacheConfig{
            TTL:             server.Duration(1 * time.Hour),
            CleanupInterval: server.Duration(1 * time.Hour),
        },
        Worker: server.WorkerConfig{
            MaxConcurrent: 2,
            QueueSize:     10,
            JobRetention:  server.Duration(1 * time.Hour),
        },
    }

    srv, err := server.New(cfg)
    if err != nil {
        t.Fatalf("Failed to create server: %v", err)
    }

    // Start server in background
    go srv.Start(context.Background())

    // Wait for server to be ready
    // TODO: Get actual port from server
    baseURL := "http://localhost:8080" // Will need to be dynamic

    // Create test token
    db, _ := store.New(dbPath)
    tokenStore := store.NewTokenStore(db.Conn())
    token, _ := tokenStore.Create(context.Background(), "test", nil)

    return &testServer{
        server:  srv,
        baseURL: baseURL,
        token:   token,
        cleanup: func() {
            srv.Shutdown(context.Background())
            db.Close()
        },
    }
}

func (ts *testServer) request(t *testing.T, method, path string, body interface{}) (*http.Response, []byte) {
    t.Helper()

    var reqBody io.Reader
    if body != nil {
        data, _ := json.Marshal(body)
        reqBody = bytes.NewReader(data)
    }

    req, err := http.NewRequest(method, ts.baseURL+path, reqBody)
    if err != nil {
        t.Fatalf("Failed to create request: %v", err)
    }

    req.Header.Set("Content-Type", "application/json")
    if ts.token != "" {
        req.Header.Set("Authorization", "Bearer "+ts.token)
    }

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        t.Fatalf("Request failed: %v", err)
    }
    defer resp.Body.Close()

    respBody, _ := io.ReadAll(resp.Body)
    return resp, respBody
}
```

### 2. Integration Test Cases

```go
func TestIntegration_HealthEndpoint(t *testing.T) {
    ts := setupTestServer(t)
    defer ts.cleanup()

    resp, body := ts.request(t, "GET", "/health", nil)

    if resp.StatusCode != http.StatusOK {
        t.Errorf("Expected 200, got %d", resp.StatusCode)
    }

    var health models.HealthResponse
    json.Unmarshal(body, &health)

    if health.Status != "ok" {
        t.Errorf("Expected status 'ok', got '%s'", health.Status)
    }
}

func TestIntegration_Unauthorized(t *testing.T) {
    ts := setupTestServer(t)
    defer ts.cleanup()

    // Remove token for this request
    ts.token = ""

    resp, _ := ts.request(t, "GET", "/api/v1/results/test", nil)

    if resp.StatusCode != http.StatusUnauthorized {
        t.Errorf("Expected 401, got %d", resp.StatusCode)
    }
}

func TestIntegration_AnalysisWorkflow(t *testing.T) {
    ts := setupTestServer(t)
    defer ts.cleanup()

    // Create test files
    testDir := t.TempDir()
    testFile := filepath.Join(testDir, "test.txt")
    os.WriteFile(testFile, []byte("test content"), 0644)

    // 1. Submit analysis
    req := models.AnalysisRequest{
        CollectionID: "test-collection",
        Files: []models.FileInfo{
            {
                FileID:       "file1",
                Path:         testFile,
                LastModified: time.Now(),
            },
        },
    }

    resp, body := ts.request(t, "POST", "/api/v1/analyze", req)

    if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
        t.Fatalf("Expected 202 or 200, got %d: %s", resp.StatusCode, body)
    }

    var jobResp models.JobResponse
    json.Unmarshal(body, &jobResp)

    if jobResp.JobID == "" {
        t.Fatal("Expected job_id in response")
    }

    // 2. Poll for completion
    maxWait := 30 * time.Second
    start := time.Now()

    for time.Since(start) < maxWait {
        resp, body = ts.request(t, "GET", "/api/v1/jobs/"+jobResp.JobID, nil)

        if resp.StatusCode != http.StatusOK {
            t.Fatalf("Expected 200, got %d", resp.StatusCode)
        }

        json.Unmarshal(body, &jobResp)

        if jobResp.Status == models.JobStatusCompleted {
            break
        }
        if jobResp.Status == models.JobStatusFailed {
            t.Fatalf("Job failed: %s", jobResp.Error)
        }

        time.Sleep(100 * time.Millisecond)
    }

    if jobResp.Status != models.JobStatusCompleted {
        t.Fatalf("Job did not complete in time, status: %s", jobResp.Status)
    }

    // 3. Verify result is cached
    resp, body = ts.request(t, "GET", "/api/v1/results/test-collection", nil)

    if resp.StatusCode != http.StatusOK {
        t.Fatalf("Expected 200, got %d", resp.StatusCode)
    }
}

func TestIntegration_CacheHit(t *testing.T) {
    ts := setupTestServer(t)
    defer ts.cleanup()

    testDir := t.TempDir()
    testFile := filepath.Join(testDir, "test.txt")
    os.WriteFile(testFile, []byte("test content"), 0644)

    req := models.AnalysisRequest{
        CollectionID: "cache-test",
        Files: []models.FileInfo{
            {
                FileID:       "file1",
                Path:         testFile,
                LastModified: time.Now(),
            },
        },
    }

    // First request - should create job
    resp1, body1 := ts.request(t, "POST", "/api/v1/analyze", req)

    var job1 models.JobResponse
    json.Unmarshal(body1, &job1)

    // Wait for completion
    // ... (same polling as above)

    // Second request with same files - should hit cache
    resp2, body2 := ts.request(t, "POST", "/api/v1/analyze", req)

    var job2 models.JobResponse
    json.Unmarshal(body2, &job2)

    if !job2.Cached {
        t.Error("Expected second request to be cached")
    }

    if job2.Status != models.JobStatusCompleted {
        t.Error("Cached response should be immediately completed")
    }
}

func TestIntegration_DeleteCache(t *testing.T) {
    ts := setupTestServer(t)
    defer ts.cleanup()

    // ... setup and run analysis ...

    // Delete cache
    resp, _ := ts.request(t, "DELETE", "/api/v1/results/test-collection", nil)

    if resp.StatusCode != http.StatusNoContent {
        t.Errorf("Expected 204, got %d", resp.StatusCode)
    }

    // Verify cache is gone
    resp, _ = ts.request(t, "GET", "/api/v1/results/test-collection", nil)

    if resp.StatusCode != http.StatusNotFound {
        t.Errorf("Expected 404 after delete, got %d", resp.StatusCode)
    }
}

func TestIntegration_RateLimiting(t *testing.T) {
    // Create server with low rate limit
    // ...

    // Send many rapid requests
    // Verify 429 is returned after limit exceeded
}
```

### 3. Running Integration Tests

```bash
# Run integration tests only
go test -tags=integration ./internal/server/...

# Run all tests including integration
go test -tags=integration ./...

# Run with verbose output
go test -v -tags=integration ./internal/server/...
```

### 4. CI Configuration Update

Add to `.github/workflows/go.yml`:

```yaml
- name: Run integration tests
  run: go test -tags=integration -timeout 5m ./internal/server/...
```

## Tests Required

- `TestIntegration_HealthEndpoint` - Health check works
- `TestIntegration_Unauthorized` - Auth blocks without token
- `TestIntegration_AnalysisWorkflow` - Full analysis flow works
- `TestIntegration_CacheHit` - Caching works correctly
- `TestIntegration_DeleteCache` - Cache deletion works
- `TestIntegration_RateLimiting` - Rate limiting works

## Acceptance Criteria

- [ ] Integration tests are in separate file with build tag
- [ ] Tests can start a real server instance
- [ ] Full analysis workflow is tested end-to-end
- [ ] Cache behavior is verified
- [ ] Auth is verified
- [ ] Tests clean up after themselves
- [ ] All integration tests pass
- [ ] Tests are included in CI
