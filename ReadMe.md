# PC - Package Checker

This program aims to improve the quality of data publications via running a few simple tests to ensure best practices for publishing data are being followed.

Checks are run by file / respository (data package).
Currently only a few checks are implemented:

**By file:**
- HasOnlyASCII (for filenames)
- HasNoWhiteSpace (for filenames)
- IsFreeOfKeywords (checking file contents); non binary, .xlsx and .docx are supported
- IsValidName (checking if nonsense files are present eg: .Rhistory)
- HasFileNameSpecialChars (~!?@#$%^&*`;,'"()<>[]{})
- IsFileNameTooLong (>64 is too long)

Archives (.zip, .tar, .7z) are also supported. On these the content (IsFreeOfKeywords) on each file is checked if the file is not too big.
As *.tar.gz* files require complete unpacking of the archive to access the list of contained files it is not supported as it would be too slow for large archives.

**By respository:**
- HasReadme (a readme file exists in the repository)
- ReadMeContainsTOC (readme mentions each file containted in the repository)

How the files are passed to the tool is defined via collectors. Currently the `LocaleCollector` and the `CkanCollector` can be used. 
- the `LocalCollector` reads files from your local file system. 
- the `CkanCollector` parses CKAN packages via their name. It determines resources in that package via a webrequest to the CKAN API. The resources are then also read locally. This means that the package checker needs to be deployed on the production server of CKAN, so that the package resources are readable.

## Configuration

The configuration is specified in TOML format. Each test can be configured with:
- `blacklist`: File paths matching these patterns are excluded from the test
- `whitelist`: Only file paths matching these patterns are included in the test
- `keywordArguments`: Test-specific arguments

### Important: Regex vs Literal String Usage

**Regex patterns are ONLY supported in `blacklist` and `whitelist` fields** for file path filtering:

```toml
[test.IsFreeOfKeywords]
# These support regex patterns for file path matching
blacklist = [".*\\.log$", "temp.*", "test[0-9]+\\.txt"]
whitelist = ["src/.*\\.go", "docs/.*\\.md"]
```

**Keywords and disallowed names use LITERAL string matching only:**

```toml
[test.IsFreeOfKeywords]
keywordArguments = [
    # These are literal strings (case-insensitive)
    { keywords = ["password", "api_key", "secret"], info = "Sensitive data found:" },
    { keywords = ["/Users/", "C:\\"], info = "Hardcoded paths found:" }
]

[test.IsValidName]
keywordArguments = [
    # These are literal filename matches
    { disallowed_names = [".DS_Store", "__pycache__", ".vscode"] }
]
```

**DO NOT use regex patterns in keywords** - they will be treated as literal strings:
- ❌ `"pass.*"` will look for the literal text "pass.*"
- ❌ `"[Pp]assword"` will look for the literal text "[Pp]assword"
- ✅ `"password"` will find "password", "Password", "PASSWORD", etc.

### Performance Optimizations

The tool includes several performance optimizations:
- **Fast string matching** for keyword detection (100x+ faster than regex)
- **Parallel processing** for multiple files using worker pools
- **Streaming I/O** for large files to reduce memory usage
- **Memory limits** for archive processing to prevent excessive resource usage
- **Message truncation** to limit output when many similar issues are found

## Run
Set up the package checker configuration:
```bash
cp pc.toml.example pc.toml
```

Once you edited the necessary config you can run with:
```bash
go run main.go
```

or you compile first and run via:
```bash
pc -location your-ckan-package-name
```

run with Terminal User Interface:
```bash
pc -config pc.toml -location .  --tui
```

run with html output:
```bash
pc -config pc.toml -location .  --html report.html
```

run with plain output:
```bash
pc -config pc.toml -location .  --plain
```

## Building
To build (https://github.com/confluentinc/confluent-kafka-go/issues/1092#issuecomment-2373681430): 
```bash
go build -ldflags="-s -w" . && ./pc
```

## Deployment with CKAN
If you want to use the CKAN collector the binary needs to have access to the resources locally, so it can read them without downloading. Make sure the access rights for the binary are set correctly.

Eg:
```bash
# copy to ckan server
scp pc production-ckan:/home/rdm
# change owner to owner of resources
sudo chown ckan:ckan pc
# set the sticky bit, so anyone can run the binary as the user ckan
sudo chmod u+s pc
```

To run the tool from another computer one could:
```bash
#!/usr/bin/bash
echo -e "\e[31m=>This script is running a binary on prod2!\e[0m"
ssh -i .../.ssh/id_ed25519_ckool rdm@production-ckan /home/rdm/pc "$@"
```

## REST API Server

The package checker includes a REST API server (`pc-server`) for remote package checking. This is useful for integrating package checks into web applications or automated workflows.

### Building the Server

```bash
go build -o pc-server ./cmd/pc-server
```

### Running the Server

```bash
pc-server -config ./pc.toml
```

**Flags:**
- `-config` - Path to PC config file (optional; standard locations are searched if omitted)
- `-help` - Show usage information

The server takes **no tunable flags** — every setting is read from the config
file. The listen address is `[server] listenAddress`, and the CKAN base URL is
`[collector.CkanCollector.attrs] url` (the single source of truth); all other
server tunables live in the `[server]` section of `pc.toml` (see below). All
`[server]` settings are validated at startup, so a bad value fails fast with a
clear error instead of misbehaving at runtime.

### API Endpoints

#### Liveness
```
GET /health
```
A cheap, static `200` with no upstream I/O. Use it for liveness probes.

```json
{ "status": "ok", "version": "1.0.0", "timestamp": "2024-01-14T10:30:00Z" }
```

#### Readiness
```
GET /ready
```
Verifies the CKAN Action API is reachable (result cached ~5s) **and** the
storage mount is readable. Healthy → `200`; not ready → `503` with the
`service_not_ready` error envelope. Use it for readiness probes / load-balancer
health checks. Both `/health` and `/ready` are exempt from rate limiting and the
concurrency semaphore.

#### Analyze Package
```
POST /api/v1/analyze
```

**Headers:**
- `Authorization: Bearer <your-ckan-api-token>` (OPTIONAL — omit for public packages)
- `Content-Type: application/json`

**Request Body:**
```json
{ "package_id": "my-ckan-package" }
```

There is no `ckan_url` field — it was removed as an SSRF / token-exfiltration
vector. **Response:** the same JSON structure as `pc --json`, plus a
`request_id` field (also returned in the `X-Request-Id` response header).

### Authentication

The token is **optional**. If present, the server forwards it (raw) to CKAN's
`package_show` so private packages you can read are analyzed; if absent, only
public packages are accessible. A present-but-malformed `Authorization` header
is rejected with `invalid_request` (400).

### Example Usage

```bash
# Start the server
pc-server -config ./pc.toml

# Liveness / readiness
curl http://localhost:8080/health
curl http://localhost:8080/ready

# Analyze a public package (no token)
curl -X POST http://localhost:8080/api/v1/analyze \
  -H "Content-Type: application/json" \
  -d '{"package_id": "my-package"}'

# Analyze a private package (with your CKAN API token)
curl -X POST http://localhost:8080/api/v1/analyze \
  -H "Authorization: Bearer <your-ckan-api-token>" \
  -H "Content-Type: application/json" \
  -d '{"package_id": "my-package"}'
```

### Error Responses

Every failure uses one envelope:

```json
{
  "error": {
    "code": "package_not_found",
    "message": "<non-technical, English message>",
    "contact": "If you can't resolve this yourself, please contact rdm@eawag.ch.",
    "request_id": "<ULID; also in the X-Request-Id header>"
  }
}
```

| Status | Code | When |
|--------|------|------|
| 400 | `missing_package` | No `package_id` provided |
| 400 | `invalid_package_name` | `package_id` violates CKAN's name grammar |
| 400 | `invalid_request` | Malformed body or malformed `Authorization` header |
| 401 | `invalid_token` | CKAN rejected the token |
| 403 | `access_denied` | Token lacks permission for the package |
| 404 | `package_not_found` | No such package (or private + unauthorized) |
| 429 | `rate_limited` | Hourly request budget exceeded (with `Retry-After`) |
| 503 | `service_busy` | Concurrency limit reached (no queueing) |
| 503 | `service_not_ready` | CKAN or storage mount unavailable (`/ready`) |
| 503 | `server_restarting` | Server is draining during a graceful shutdown |
| 502 | `ckan_unavailable` | CKAN unreachable / 5xx (504 on request timeout) |
| 500 | `resource_unreadable` | An upload file couldn't be read from storage |
| 500 | `internal_error` | Unexpected server-side error (cites `request_id`) |

### Server configuration (`[server]`)

See the `[server]` section in `pc.toml` for the full, commented list. Key knobs:
`listenAddress`, `trustProxyHeaders` / `trustedProxies` (proxy-aware client IP),
`allowedOrigins` (CORS allow-list), `perIPRequestsPerHour` /
`globalRequestsPerHour` / `burstFactor` (fixed-window rate limiting),
`analysisBusyWaitSeconds`, `maxTrackedRateKeys`, `contactMessage`, `logClientIP`,
`requestTimeoutSeconds`.

### Production Deployment

The server speaks plain HTTP and is designed to run behind nginx with HTTPS
termination. See **[docs/deploy.md](docs/deploy.md)** for the full deployment
guide: Docker log retention, the storage-mount precondition, nginx
`trustedProxies` guidance, and graceful-shutdown behaviour.

Minimal nginx reverse proxy:

```nginx
server {
    listen 44433 ssl;
    server_name pc.example.com;

    ssl_certificate /etc/ssl/certs/your-cert.pem;
    ssl_certificate_key /etc/ssl/private/your-key.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;          # rate-limit key source
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 300s;                          # ≥ requestTimeoutSeconds
    }
}
```

Then run the server — it binds to `[server] listenAddress`, so set that to
`127.0.0.1:8080` in `pc.toml` to accept only proxied traffic:
```bash
pc-server -config ./pc.toml
```

### Running TUI over SSH

When running the package checker with TUI interface over SSH, you need to ensure proper terminal allocation:

**Basic SSH execution with TUI:**
```bash
ssh -t user@remote-server "cd /path/to/pc && ./pc"
```

**For better terminal compatibility:**
```bash
ssh -t user@remote-server "export TERM=xterm-256color && cd /path/to/pc && ./pc"
```

**With full environment setup:**
```bash
ssh -t user@remote-server "TERM=xterm-256color LANG=en_US.UTF-8 cd /path/to/pc && ./pc"
```

**Troubleshooting TUI Issues:**

- **Garbled display**: Try different TERM values:
  ```bash
  ssh -t user@remote-server "TERM=screen cd /path/to/pc && ./pc"
  ssh -t user@remote-server "TERM=xterm cd /path/to/pc && ./pc"
  ssh -t user@remote-server "TERM=vt100 cd /path/to/pc && ./pc"
  ```

- **No arrow key navigation**: Ensure your local terminal supports the TERM type being used. Modern terminals like iTerm2, Windows Terminal, or GNOME Terminal work best.

- **Color issues**: Use `TERM=xterm-256color` for full color support, or `TERM=xterm` for basic colors.

- **Using tmux/screen**: For persistent sessions:
  ```bash
  ssh user@remote-server
  tmux new-session "cd /path/to/pc && ./pc"
  ```

**Important**: The `-t` flag is essential as it allocates a pseudo-terminal required for interactive TUI applications.

**Clipboard Issues (Copy Summary)**

When using the TUI summary feature (press `c`) over SSH, the clipboard uses OSC 52 escape sequences to copy to your local clipboard. This requires terminal support:

- **iTerm2 (macOS)**: Enable in Preferences → General → Selection → "Applications in terminal may access clipboard"
- **kitty, alacritty, Windows Terminal**: Works by default
- **GNOME Terminal**: Not supported - use an alternative terminal

**Recommended terminals for Linux:**
- `kitty` - `sudo apt install kitty`
- `alacritty` - `sudo apt install alacritty`
- `tilix` - `sudo apt install tilix`

**For tmux users**, add to `~/.tmux.conf`:
```bash
set -g set-clipboard on
set -g allow-passthrough on
```
Then reload: `tmux source-file ~/.tmux.conf`


## Testing
```
go test ./...
```

