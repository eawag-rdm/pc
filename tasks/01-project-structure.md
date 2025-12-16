# Task 01: Project Structure

**Status:** `[ ]` Not started

**Dependencies:** None

## Objective

Set up the directory structure for the PC server without any implementation code.

## Deliverables

### 1. Create directory structure

```
internal/
└── server/
    ├── api/
    ├── cache/
    ├── store/
    ├── models/
    ├── analyzer/
    └── worker/

cmd/
└── pc-server/
```

### 2. Create placeholder files

Each directory should have a `.go` file with just the package declaration:

- `internal/server/server.go` - `package server`
- `internal/server/config.go` - `package server`
- `internal/server/api/handlers.go` - `package api`
- `internal/server/api/middleware.go` - `package api`
- `internal/server/api/responses.go` - `package api`
- `internal/server/cache/cache.go` - `package cache`
- `internal/server/cache/sqlite.go` - `package cache`
- `internal/server/store/jobs.go` - `package store`
- `internal/server/store/tokens.go` - `package store`
- `internal/server/models/types.go` - `package models`
- `internal/server/analyzer/analyzer.go` - `package analyzer`
- `internal/server/worker/worker.go` - `package worker`
- `cmd/pc-server/main.go` - `package main`

### 3. Verify build

```bash
go build ./...
```

Should complete without errors.

## Tests Required

None for this task (structure only).

## Acceptance Criteria

- [ ] All directories created
- [ ] All placeholder files created with correct package names
- [ ] `go build ./...` succeeds
- [ ] No changes to existing `pkg/` code or `main.go`
