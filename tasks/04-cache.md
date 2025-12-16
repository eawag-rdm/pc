# Task 04: Cache Implementation

**Status:** `[ ]` Not started

**Dependencies:** Task 03

## Objective

Implement the cache layer for storing and retrieving analysis results.

## Deliverables

### 1. `internal/server/cache/cache.go`

```go
package cache

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "sort"
    "strconv"

    "github.com/eawag-rdm/pc/internal/server/models"
)

// Cache defines the interface for analysis result caching
type Cache interface {
    // Get retrieves a cached result if valid (matching hash and not expired)
    Get(ctx context.Context, collectionID, filesHash string) (*models.CacheEntry, error)

    // Set stores an analysis result
    Set(ctx context.Context, entry *models.CacheEntry) error

    // Delete removes a cached result
    Delete(ctx context.Context, collectionID string) error

    // Cleanup removes expired entries, returns count of deleted entries
    Cleanup(ctx context.Context) (int64, error)
}

// ComputeFilesHash generates a deterministic hash from file info
// Used to detect when files have changed
func ComputeFilesHash(files []models.FileInfo) string {
    // Sort files by FileID for deterministic order
    sorted := make([]models.FileInfo, len(files))
    copy(sorted, files)
    sort.Slice(sorted, func(i, j int) bool {
        return sorted[i].FileID < sorted[j].FileID
    })

    // Build hash input: FileID + LastModified timestamp for each file
    h := sha256.New()
    for _, f := range sorted {
        h.Write([]byte(f.FileID))
        h.Write([]byte(strconv.FormatInt(f.LastModified.Unix(), 10)))
    }

    return hex.EncodeToString(h.Sum(nil))
}
```

### 2. `internal/server/cache/sqlite.go`

```go
package cache

import (
    "context"
    "database/sql"
    "errors"
    "time"

    "github.com/eawag-rdm/pc/internal/server/models"
)

var (
    ErrNotFound = errors.New("cache entry not found")
    ErrExpired  = errors.New("cache entry expired")
    ErrMismatch = errors.New("files hash mismatch")
)

// SQLiteCache implements Cache using SQLite
type SQLiteCache struct {
    db *sql.DB
}

// NewSQLiteCache creates a new SQLite-backed cache
func NewSQLiteCache(db *sql.DB) *SQLiteCache

// Get retrieves a cached result
// Returns ErrNotFound if no entry exists
// Returns ErrExpired if entry exists but is expired
// Returns ErrMismatch if entry exists but hash doesn't match
func (c *SQLiteCache) Get(ctx context.Context, collectionID, filesHash string) (*models.CacheEntry, error)

// Set stores or updates a cache entry
func (c *SQLiteCache) Set(ctx context.Context, entry *models.CacheEntry) error

// Delete removes a cache entry
func (c *SQLiteCache) Delete(ctx context.Context, collectionID string) error

// Cleanup removes all expired entries
func (c *SQLiteCache) Cleanup(ctx context.Context) (int64, error)
```

### 3. `internal/server/cache/sqlite_test.go`

```go
package cache

import (
    "context"
    "testing"
    "time"

    "github.com/eawag-rdm/pc/internal/server/models"
    "github.com/eawag-rdm/pc/internal/server/store"
)

func TestComputeFilesHash(t *testing.T) {
    // Test empty files
    // Test single file
    // Test multiple files (order independence)
    // Test same files, different order = same hash
    // Test different LastModified = different hash
}

func TestSQLiteCache_Get(t *testing.T) {
    // Test get non-existent entry
    // Test get valid entry
    // Test get expired entry
    // Test get with wrong hash
}

func TestSQLiteCache_Set(t *testing.T) {
    // Test insert new entry
    // Test update existing entry (upsert)
}

func TestSQLiteCache_Delete(t *testing.T) {
    // Test delete existing entry
    // Test delete non-existent entry (should not error)
}

func TestSQLiteCache_Cleanup(t *testing.T) {
    // Test cleanup removes expired entries
    // Test cleanup keeps valid entries
    // Test cleanup returns correct count
}
```

## Tests Required

- `TestComputeFilesHash` - Hash generation and determinism
- `TestSQLiteCache_Get` - All get scenarios
- `TestSQLiteCache_Set` - Insert and update
- `TestSQLiteCache_Delete` - Deletion behavior
- `TestSQLiteCache_Cleanup` - Expired entry removal

## Acceptance Criteria

- [ ] `ComputeFilesHash` produces deterministic hashes
- [ ] Cache correctly stores and retrieves entries
- [ ] Expired entries are handled correctly
- [ ] Hash mismatch is detected
- [ ] Cleanup removes only expired entries
- [ ] All tests pass
