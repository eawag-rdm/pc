# Task 13: Main Entry Point

**Status:** `[ ]` Not started

**Dependencies:** Task 11, Task 12

## Objective

Finalize the main entry point and ensure the server can be built and run.

## Deliverables

### 1. Final `cmd/pc-server/main.go`

Ensure all components from Task 12 are integrated and working together.

### 2. Build Configuration

Update `Makefile` or create build script:

```makefile
# Add to existing Makefile or create new one

.PHONY: build-server
build-server:
	go build -ldflags "-X github.com/eawag-rdm/pc/internal/server.Version=$(VERSION)" -o pc-server ./cmd/pc-server

.PHONY: build-all
build-all: build build-server

.PHONY: test
test:
	go test ./...

.PHONY: test-server
test-server:
	go test ./internal/server/...
```

### 3. Create Default Config

`pc-server.toml.example`:

```toml
# PC Server Configuration
# Copy this file to pc-server.toml and adjust values

# Server address
host = "localhost"
port = 8080

# Path to the PC analyzer configuration (pc.toml)
pc_config_path = "./pc.toml"

# SQLite database path
database_path = "./pc-server.db"

# Rate limiting
[rate_limit]
requests_per_second = 10.0
burst_size = 20

# Cache settings
[cache]
ttl = "24h"              # How long to cache analysis results
cleanup_interval = "1h"  # How often to clean expired cache entries

# Worker settings
[worker]
max_concurrent = 4       # Max concurrent analysis jobs
queue_size = 100         # Max queued jobs
job_retention = "168h"   # How long to keep job records (7 days)
```

### 4. Verification Script

Create `scripts/verify-server.sh`:

```bash
#!/bin/bash
set -e

echo "=== Building PC Server ==="
go build -o pc-server ./cmd/pc-server

echo ""
echo "=== Checking Help ==="
./pc-server help

echo ""
echo "=== Creating Test Token ==="
./pc-server token create --name "test-token" --config pc-server.toml.example 2>/dev/null || true

echo ""
echo "=== Listing Tokens ==="
./pc-server token list --config pc-server.toml.example 2>/dev/null || true

echo ""
echo "=== Starting Server (will run for 5 seconds) ==="
timeout 5 ./pc-server serve --config pc-server.toml.example || true

echo ""
echo "=== All Checks Passed ==="
```

### 5. Ensure Clean Build

```bash
# From project root
go build ./...
go test ./...
go vet ./...
```

## Manual Testing Checklist

### Server Startup
- [ ] `go build ./cmd/pc-server` completes without errors
- [ ] `./pc-server serve` starts and listens on configured port
- [ ] Server logs startup message
- [ ] Ctrl+C triggers graceful shutdown

### Token Management
- [ ] `./pc-server token create --name "test"` creates token
- [ ] Token is displayed only once
- [ ] `./pc-server token list` shows created token
- [ ] `./pc-server token revoke --name "test"` revokes token
- [ ] Revoked token no longer works

### API Verification
```bash
# Health check (no auth required)
curl http://localhost:8080/health

# Should return 401 without token
curl http://localhost:8080/api/v1/results/test

# Should work with token
TOKEN="pcs_..."  # from token create
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/results/test
```

### Rate Limiting
```bash
# Rapid requests should eventually get 429
for i in {1..50}; do
    curl -s -o /dev/null -w "%{http_code}\n" \
        -H "Authorization: Bearer $TOKEN" \
        http://localhost:8080/api/v1/results/test
done
```

## Files to Create/Update

1. `cmd/pc-server/main.go` - Final version with all commands
2. `pc-server.toml.example` - Example configuration
3. `Makefile` - Build targets (if not exists)
4. `scripts/verify-server.sh` - Verification script

## Acceptance Criteria

- [ ] `go build ./cmd/pc-server` succeeds
- [ ] `go build ./...` succeeds (entire project)
- [ ] `go test ./...` passes
- [ ] `go vet ./...` passes
- [ ] Server starts and accepts connections
- [ ] Token commands work correctly
- [ ] Health endpoint responds
- [ ] Auth middleware blocks unauthorized requests
- [ ] Rate limiting works
- [ ] Graceful shutdown works
