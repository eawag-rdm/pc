# Task 03: Database Layer

**Status:** `[ ]` Not started

**Dependencies:** Task 01, Task 02

## Objective

Implement the SQLite database connection, schema, and migration system.

## Deliverables

### 1. `internal/server/store/db.go`

```go
package store

import (
    "context"
    "database/sql"
    _ "github.com/mattn/go-sqlite3"
)

// DB wraps the SQLite database connection
type DB struct {
    conn *sql.DB
}

// New creates a new database connection and runs migrations
func New(dbPath string) (*DB, error)

// Close closes the database connection
func (db *DB) Close() error

// Conn returns the underlying connection (for stores)
func (db *DB) Conn() *sql.DB

// Migrate runs database migrations
func (db *DB) Migrate(ctx context.Context) error
```

### 2. `internal/server/store/schema.go`

```go
package store

// Schema version for migrations
const schemaVersion = 1

// SQL statements for schema creation
const schema = `
-- Jobs table
CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    collection_id TEXT NOT NULL,
    files_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    progress TEXT DEFAULT '',
    result TEXT,
    error TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_jobs_collection ON jobs(collection_id);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at);

-- Analysis cache
CREATE TABLE IF NOT EXISTS analysis_cache (
    collection_id TEXT PRIMARY KEY,
    files_hash TEXT NOT NULL,
    result TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_cache_expires ON analysis_cache(expires_at);

-- API tokens
CREATE TABLE IF NOT EXISTS api_tokens (
    token_hash TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP,
    active INTEGER DEFAULT 1
);

-- Schema version tracking
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY
);
`
```

### 3. `internal/server/store/db_test.go`

```go
package store

import (
    "context"
    "os"
    "path/filepath"
    "testing"
)

func TestNew(t *testing.T) {
    // Test creating new database
    // Test with invalid path
}

func TestMigrate(t *testing.T) {
    // Test schema creation
    // Test idempotency (running twice)
}

func TestClose(t *testing.T) {
    // Test closing connection
}

// Helper to create temp database for tests
func testDB(t *testing.T) (*DB, func()) {
    t.Helper()
    tmpDir := t.TempDir()
    dbPath := filepath.Join(tmpDir, "test.db")

    db, err := New(dbPath)
    if err != nil {
        t.Fatalf("Failed to create test DB: %v", err)
    }

    if err := db.Migrate(context.Background()); err != nil {
        t.Fatalf("Failed to migrate: %v", err)
    }

    return db, func() { db.Close() }
}
```

### 4. Update `go.mod`

Add SQLite driver dependency:

```
github.com/mattn/go-sqlite3 v1.14.x
```

## Tests Required

- `TestNew` - Database creation and connection
- `TestMigrate` - Schema creation and idempotency
- `TestClose` - Clean shutdown

## Acceptance Criteria

- [ ] Database connection works with file path
- [ ] Schema is created on first run
- [ ] Migrations are idempotent (can run multiple times)
- [ ] All tests pass
- [ ] `go build ./...` succeeds
