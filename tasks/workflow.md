# PC Server Implementation Workflow

## Overview

This document describes the workflow for implementing the PC Web Service - an HTTP API layer on top of the existing PC (Package Checker) CLI tool.

## Task Process

1. **Pick a task** - Work on tasks in order (dependencies are noted in each task)
2. **Implement** - Write the code as specified in the task
3. **Write tests** - All new code must have corresponding tests
4. **Run tests** - Ensure all tests pass: `go test ./...`
5. **Submit for review** - Mark task as "ready for review"
6. **Approval** - Task is only "done" when approved by maintainer
7. **Mark done** - Update task status to `[x]` when approved

## Task Status Legend

- `[ ]` - Not started
- `[~]` - In progress
- `[?]` - Ready for review
- `[x]` - Done (approved + tests pass)

## Task Dependencies

```
01-project-structure
        │
        ▼
02-models ─────────────────────────────────┐
        │                                   │
        ▼                                   │
03-database-layer                          │
        │                                   │
        ├───────────┬───────────┐          │
        ▼           ▼           ▼          │
04-cache    05-job-store   06-token-auth   │
        │           │           │          │
        └───────────┴───────────┘          │
                    │                       │
                    ▼                       │
              07-analyzer ◄─────────────────┘
                    │
                    ▼
            08-job-worker
                    │
                    ▼
            09-http-handlers
                    │
                    ▼
            10-rate-limiting
                    │
                    ▼
            11-server-config
                    │
                    ▼
            12-token-cli
                    │
                    ▼
            13-main-entrypoint
                    │
                    ▼
            14-integration-tests
```

## Directory Structure (Target)

```
pc/
├── main.go                      # CLI (existing, unchanged)
├── cmd/
│   └── pc-server/
│       └── main.go              # HTTP server entry point
├── pkg/                         # Existing packages (shared)
│   └── ...
├── internal/
│   └── server/
│       ├── server.go            # Server setup
│       ├── config.go            # Configuration
│       ├── api/
│       │   ├── handlers.go
│       │   ├── handlers_test.go
│       │   ├── middleware.go
│       │   ├── middleware_test.go
│       │   └── responses.go
│       ├── cache/
│       │   ├── cache.go         # Interface
│       │   ├── sqlite.go        # Implementation
│       │   └── sqlite_test.go
│       ├── store/
│       │   ├── jobs.go
│       │   ├── jobs_test.go
│       │   ├── tokens.go
│       │   └── tokens_test.go
│       ├── models/
│       │   └── types.go
│       ├── analyzer/
│       │   ├── analyzer.go
│       │   └── analyzer_test.go
│       └── worker/
│           ├── worker.go
│           └── worker_test.go
└── tasks/                       # This folder
    ├── workflow.md
    └── NN-task-name.md
```

## Running Tests

```bash
# All tests
go test ./...

# Specific package
go test ./internal/server/cache/...

# With coverage
go test -cover ./internal/server/...

# Verbose
go test -v ./internal/server/...
```

## Commits

Each completed task should be committed separately with a clear message:

```
pc-server: implement cache layer

- Add Cache interface
- Add SQLite implementation
- Add tests for cache operations
```
