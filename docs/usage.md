# PC Usage Guide - CLI and Server

PC can be run in two ways:

1. **CLI (`pc`)** - scan a local folder or a CKAN package from the command line
   (interactive TUI, JSON, plain text or HTML output).
2. **HTTP server (`pc-server`)** - a REST API that analyzes CKAN packages on
   request, meant to be called from a web frontend.

Both are built from this repository and share the same configuration file and
the same check engine. This guide covers running and configuring each; for
production deployment of the server (Docker, nginx/HTTPS, log retention) see
[deploy-server.md](deploy-server.md).

---

## 1. Shared configuration (`pc.toml`)

Both the CLI and the server read a single TOML file. Start from the template:

```bash
cp pc.toml.example pc.toml
```

If no `-config` flag is given, `./pc.toml` is used.

| Section | Used by | Purpose |
|---|---|---|
| `[general]` | both | memory/scan limits, summary text |
| `[test.<CheckName>]` | both | per-check configuration (see below) |
| `[collector.LocalCollector]` | CLI | local file system collector; `attrs`: `maxFolderDepth` (0 = top level only, N = descend N levels; boundary-depth folders are listed but not entered), `maxFileCount` (walk stops after N collected entries, 0 = no cap), `includeFolders` (legacy, true = unlimited recursion; an explicit `maxFolderDepth` wins) |
| `[collector.CkanCollector]` | both | CKAN URL, server-side token, TLS verify, storage path |
| `[operation.main]` | CLI | which collector the CLI uses |
| `[server]`, `[server.smtp]` | server | listen address, rate limits, timeouts, CORS, admin alerts |

**Startup validation:** both binaries fail fast with a clear error when the
configuration is unusable - the CLI checks the `[test.*]` sections it needs;
the server additionally validates every `[server]` value and the
`[collector.CkanCollector]` attrs. The sections `[test.IsFreeOfKeywords]`,
`[test.IsValidName]` and `[test.HasReadme]` are **required** (the checks
dereference them). `[test.IsFreeOfSecrets]` is optional; when present its
`attrs` are validated (types, unknown keys, timeout vs. server timeout).

### Per-check configuration

Each `[test.<CheckName>]` section accepts:

- `blacklist` - files matching these patterns are excluded from the check
- `whitelist` - only files matching these patterns are checked
- `keywordArguments` - check-specific arguments

Only one of `blacklist`/`whitelist` may be non-empty per check.

**Regex is only supported in `blacklist` and `whitelist`** (file-path
filtering):

```toml
[test.IsFreeOfKeywords]
blacklist = [".*\\.log$", "temp.*", "test[0-9]+\\.txt"]
```

**`keywords` and `disallowed_names` are literal strings** (keywords are matched
case-insensitively):

```toml
[test.IsFreeOfKeywords]
keywordArguments = [
    { keywords = ["password", "api_key", "secret"], info = "Sensitive data found:" },
    { keywords = ["/Users/", "C:\\"], info = "Hardcoded paths found:" }
]

[test.IsValidName]
keywordArguments = [
    { disallowed_names = [".DS_Store", "__pycache__", ".vscode"] }
]

[test.HasReadme]
# filenames recognized as the repository readme (case-insensitive);
# shared by HasReadme and ReadMeContainsTOC
keywordArguments = [
    { readme_names = ["readme.md", "readme.txt", "readme", "read me", "read me.txt", "read me.md", "read-me", "read-me.txt", "read-me.md", "read_me", "read_me.txt", "read_me.md"] }
]
```

Do **not** use regex in keywords - `"pass.*"` looks for the literal text
`pass.*`, not a pattern.

### Secret scan: `[test.IsFreeOfSecrets]`

> **Status (2026-08-04): dormant.** The scan is shipped disabled
> (`enabled = false` in the example configs) because the current scanner runs
> are too slow for our latency target. The code stays in place and working —
> set `enabled = true` to reactivate. Until then, the keyword check is the
> only content-based credential net.

Secrets (credentials, API tokens, private keys) are found by the external
[betterleaks](https://github.com/betterleaks/betterleaks) scanner rather than
keyword matching - real secret formats with far fewer false positives. One
scanner run covers the whole package: plain files are scanned in place, archive
members are extracted (size-gated, see `[general]` limits) to a private temp
directory first. Findings are condensed to one message per file (rule ids +
line numbers); secret values themselves never appear in any output.

```toml
[test.IsFreeOfSecrets]
blacklist = []
whitelist = []
# enabled: toggle the scan; binary: scanner executable (name in PATH or absolute path);
# timeoutSeconds: whole-scan cap (must not exceed [server] requestTimeoutSeconds);
# maxProcs: CPU cores the scanner may use
attrs = {enabled = false, binary = "betterleaks", timeoutSeconds = 120, maxProcs = 3}
```

The CLI needs the `betterleaks` binary on PATH (or `binary` set to an absolute
path); the server's Docker image ships it. The scanner runs fully offline - no
finding validation calls, nothing leaves the machine. Note that low-entropy
plain passwords (e.g. `password = hunter2`) are below the scanner's generic-rule
entropy threshold; add a `password` keyword group to `[test.IsFreeOfKeywords]`
if you want a literal-match safety net for those.

### `[general]` limits

```toml
[general]
maxArchiveFileSize     = 10485760   # max size per file inside an archive (bytes)
maxTotalArchiveMemory  = 536870912  # total memory budget for archive processing
maxArchiveMemberCount  = 1000       # max unpack-candidate members per archive
maxPDFPages            = 25         # page ceiling per PDF (longer = skipped whole)
maxPDFFileSize         = 5242880    # max PDF size in bytes (larger = not read)
maxContentScanFileSize = 20971520   # max size for content-scanned files
```

These limits gate the keyword checks and the secret scan alike: files above
`maxContentScanFileSize` are never content-scanned (skip acknowledgement
instead), and only archive members within `maxArchiveFileSize` /
`maxTotalArchiveMemory` are unpacked. Excel/Word files (`.xlsx`/`.docx`) are
text-extracted and keyword-scanned both at top level and inside archives;
their zip index is checked against the same limits before parsing, and
over-long extractions are truncated with an acknowledgement (the truncated
part is still scanned). PDFs are text-extracted (sandboxed PDFium) and
keyword-scanned page by page - findings cite the page.

`maxPDFFileSize` (default 5 MB) and `maxPDFPages` (default 25) are
**admission gates, not truncation points**: a PDF over either limit is
**not scanned at all** and gets a skip acknowledgement naming the limit it
hit. Nothing partial is reported, so a scanned PDF is always a
fully-scanned PDF. Both gates are cheap - size is checked from the file
header before the body is read, and the page count costs about 1% of an
extraction, so long documents are rejected almost for free. **Tune these to
your data**: raising them scans more documents at proportionally more CPU;
leaving them low means long reports go unchecked (their acknowledgements
are the record of that).

Within an admitted PDF, extracted text shares the `maxArchiveFileSize` cap
(over-long extractions are truncated with an acknowledgement, and the
truncated part is still scanned) and a 30 s per-file backstop skips
pathological files. Password-protected, image-only/scanned, and unparsable
PDFs each get their own skip acknowledgement. PDFs inside archives obey the
same two gates and are extracted the same way (page attribution is lost -
the member's text is scanned as one block, like xlsx/docx members);
extracted text counts against the archive memory budget, and cumulative PDF
extraction time per archive is capped at 120 s - further PDF members are
then skipped with one acknowledgement. Archives with more than
`maxArchiveMemberCount` unpack candidates get a skip acknowledgement instead of
a content scan (0 is not unlimited — it means the default of 1000).

Files over `maxContentScanFileSize` are reported as *skipped* rather than
content-scanned. Archive members over the per-file or total-memory budget are
skipped and acknowledged in the output.

---

## 2. The CLI (`pc`)

### Build

```bash
go build -ldflags="-s -w" -o pc .
```

### Run

```bash
pc                                   # scan ./ with ./pc.toml, interactive TUI
pc -config pc.toml -location DIR     # scan a local folder
pc -location my-ckan-package         # scan a CKAN package (CkanCollector configured)
```

**Flags:**

| Flag | Meaning |
|---|---|
| `-config PATH` | config file (default: search `./pc.toml`) |
| `-location PATH_OR_NAME` | local folder or CKAN package name, depending on the collector |
| `-no-tui` | disable the interactive TUI |
| `-json` | JSON to stdout |
| `-plain` | plain-text summary to stdout |
| `-html FILE` | write an HTML report |
| `-cpuprofile` / `-memprofile` | write profiling data |

The TUI is the **default**; `-json` and `-plain` are mutually exclusive; the
other output flags are stackable (e.g. `-json -html report.html`).

Which collector the CLI uses comes from the config:

```toml
[operation.main]
collector = "LocalCollector"   # or "CkanCollector"
```

### Using the CLI against CKAN

The `CkanCollector` resolves a package via the CKAN API, then reads the
resource files **locally** from the CKAN FileStore. The binary must therefore
run on the CKAN server (or a machine with the FileStore mounted) with read
access to the storage:

```bash
# copy to the ckan server
scp pc production-ckan:/home/rdm
# change owner to the owner of the resources
sudo chown ckan:ckan pc
# set the setuid bit so anyone can run the binary as the ckan user
sudo chmod u+s pc
```

To run it from another machine:

```bash
#!/usr/bin/bash
echo -e "\e[31m=>This script is running a binary on prod2!\e[0m"
ssh -i ~/.ssh/id_ed25519_ckool rdm@production-ckan /home/rdm/pc "$@"
```

### TUI over SSH

Allocate a pseudo-terminal (`-t`) and pick a suitable `TERM`:

```bash
ssh -t user@remote-server "export TERM=xterm-256color && cd /path/to/pc && ./pc"
```

- **Garbled display:** try `TERM=screen`, `TERM=xterm`, or `TERM=vt100`.
- **Colors:** `TERM=xterm-256color` for full color.
- **Persistent sessions:** run inside `tmux`.
- **Clipboard (press `c` to copy the summary):** uses OSC 52 escape sequences.
  Works by default in kitty, alacritty and Windows Terminal; iTerm2 needs
  "Applications in terminal may access clipboard" enabled; GNOME Terminal does
  not support it. For tmux add to `~/.tmux.conf`:

  ```
  set -g set-clipboard on
  set -g allow-passthrough on
  ```

---

## 3. The server (`pc-server`)

### Build and run

```bash
go build -o pc-server ./cmd/pc-server
pc-server -config ./pc.toml
```

The server takes **no tunable flags** (`-config` and `-help` only) - every
setting lives in the `[server]` section and is validated at startup.

### Endpoints

#### `GET /health` - liveness

Cheap static `200`, no upstream I/O.

```json
{ "status": "ok", "version": "1.0.0", "timestamp": "2024-01-14T10:30:00Z" }
```

#### `GET /ready` - readiness

Verifies the CKAN Action API is reachable (cached ~5s) **and** the storage
mount is readable. Healthy → `200`; not ready → `503 service_not_ready`. Both
probes are exempt from rate limiting and the analysis gate.

#### `POST /api/v1/analyze`

**Headers:**
- `Authorization: Bearer <your-ckan-api-token>` - **optional**; omit for public packages
- `Content-Type: application/json`

**Body:** `{ "package_id": "my-ckan-package" }`

There is no `ckan_url` field - the CKAN base URL is server-side only (it was
an SSRF / token-exfiltration vector). The response is the same JSON structure
as `pc -json`, plus a `request_id` field (also in the `X-Request-Id` header).
It includes a `details_metadata` section with the Eawag publication-metadata
findings (title/author format, status, review fields, embargo, resource
restriction), derived from the same single `package_show` call as the file
analysis.

```bash
# public package
curl -X POST http://localhost:8080/api/v1/analyze \
  -H "Content-Type: application/json" \
  -d '{"package_id": "my-package"}'

# private package
curl -X POST http://localhost:8080/api/v1/analyze \
  -H "Authorization: Bearer <your-ckan-api-token>" \
  -H "Content-Type: application/json" \
  -d '{"package_id": "my-package"}'
```

### Authentication

The server has no user accounts of its own - it delegates authorization to
CKAN. If a Bearer token is present it is forwarded (raw) to CKAN's
`package_show`, so private packages the token can read are analyzable; without
a token only public packages are. The `token` in `[collector.CkanCollector]`
is used by the **CLI only** - the server blanks it at startup and never
authenticates upstream calls with it. A present-but-malformed `Authorization`
header is rejected with `invalid_request` (400). The token is never logged,
never used as a rate-limit key, and each request's token is isolated from
concurrent requests.

### Error responses

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
| 401 | `token_required` | Package needs a token and none was provided |
| 403 | `access_denied` | Token lacks permission for the package |
| 404 | `package_not_found` | No such package (or private + unauthorized) |
| 404 | `not_found` | No such endpoint path |
| 405 | `method_not_allowed` | Endpoint exists but not for this HTTP method (with `Allow` header) |
| 422 | `malformed_resource` | A resource is malformed - missing both `url_type` and `url`, or an upload missing `name`/`url`/`size`. The message names the exact resource + package. |
| 429 | `rate_limited` | Hourly request budget exceeded (with `Retry-After`); responses served from the result cache are refunded and count against a budget `cachedRequestLimitFactor`× larger |
| 502 | `ckan_unavailable` | CKAN unreachable or unusable: transport failure, transport-level 5xx, CKAN throttling (429), oversized/truncated response, or no answer within `ckanRequestTimeoutSeconds` |
| 503 | `service_busy` | Concurrency limit reached (no queueing) |
| 503 | `service_not_ready` | CKAN or storage mount unavailable (`/ready`) |
| 503 | `server_restarting` | Server is draining during a graceful shutdown |
| 504 | `analysis_timeout` | The analysis exceeded `requestTimeoutSeconds` and was stopped |
| 500 | `resource_unreadable` | An upload file couldn't be read from storage |
| 500 | `internal_error` | Unexpected server-side error (cites `request_id`) |

Notes:

- **`malformed_resource` is the only code with a dynamic message** - every other
  code uses a fixed, non-technical message; raw errors, CKAN bodies, URLs and
  file paths are never exposed in error envelopes (they are logged instead,
  keyed by `request_id`).
- **A package that exists but has zero upload resources** (e.g. all external
  links) is **not** an error: the analysis runs and returns `200` with a normal
  result and a "no files to analyse" notice.
- **Server responses carry no internal detail:** the `warnings`/`errors` arrays
  in the `200` body are always empty, and all `path` fields are blank (files are
  identified by name). A file that could not be read/scanned appears as a
  `skipped[]` entry with the soft reason *"The file could not be fully
  scanned."* - the technical cause (full path, OS error) goes to the server log
  as a `scan_diagnostic` record keyed by `request_id`. CLI output is unaffected:
  its JSON/HTML/TUI keep full paths and the raw warnings.

### `[server]` configuration reference

See `pc.toml.example` for the full commented list.

| Key | Default | Meaning |
|---|---|---|
| `listenAddress` | `127.0.0.1:8080` | bind address (host:port) |
| `trustProxyHeaders` | `true` | honor `X-Real-IP` from trusted proxies |
| `trustedProxies` | - | CIDRs allowed to set `X-Real-IP` (set this behind nginx!) |
| `allowedOrigins` | - | CORS allow-list of exact origin URLs (your frontend) |
| `perIPRequestsPerHour` | 4 | per-client-IP hourly budget (0 = unlimited) |
| `globalRequestsPerHour` | 20 | all-clients hourly budget (0 = unlimited) |
| `cachedRequestLimitFactor` | 100 | cache hits are refunded and counted against budgets × this factor; 0 disables the refund |
| `burstFactor` | 0.5 | extra headroom applied to the budgets |
| `analysisBusyWaitSeconds` | 2 | wait for the single analysis slot before `service_busy` |
| `maxTrackedRateKeys` | 10000 | rate-limiter memory bound |
| `contactMessage` | … | contact suffix shown in error envelopes |
| `logClientIP` | `true` | record the client IP in access logs |
| `requestTimeoutSeconds` | 300 | hard bound for a WHOLE analysis (CKAN call + checks) → `analysis_timeout` (504) |
| `ckanRequestTimeoutSeconds` | 10 | bound for the single CKAN `package_show` call → `ckan_unavailable` (502); must be ≤ `requestTimeoutSeconds` |
| `resultCacheDir` | - (disabled) | directory for the per-package result cache; empty disables caching |
| `resultCacheMaxEntries` | 500 | max cached packages before oldest-entry eviction |
| `resultCacheMaxAgeHours` | 0 (no limit) | max age of a cache entry; 0 disables the age limit |

Only **one analysis runs at a time** (by design); a second request waits up to
`analysisBusyWaitSeconds` for the slot and then receives `service_busy` (503)
with a `Retry-After` header.

### Result cache

**Server-only** (the CLI never caches). When `resultCacheDir` is set, each
successful analysis is stored as one JSON file per package, keyed on the
package's CKAN `metadata_modified` timestamp - which the mandatory
`package_show` call already carries, so freshness costs no extra CKAN request.
A repeat request for an unchanged package is served from the cache in a single
CKAN round-trip (response header `X-PC-Cache: hit`/`miss`; the `package_show`
still runs per request, so authorization is enforced exactly as without the
cache). Entries self-invalidate when `pc.toml` or the server version changes
or when the package changes in CKAN; `resultCacheMaxAgeHours` can additionally
age entries out, but is off by default - CKAN's `metadata_modified` alone
decides freshness. Cached files can contain private-package findings - keep
the directory readable by the server user only.

### Admin email alerts (`[server.smtp]`)

Optional. When configured, the server emails the admin list on every
**server-fault response**:

- `internal_error` (500) - any unexpected server-side error, **including a
  recovered panic** in a handler;
- `resource_unreadable` (500) - an upload file missing/unreadable on the
  storage mount (often a stale NFS mount).

Client-side errors (4xx) and operational conditions (`ckan_unavailable`,
`service_busy`, `analysis_timeout`, `server_restarting`, `service_not_ready`)
do **not** alert. The mail carries no secrets - only `request_id`, code,
method, path and `package_id`; the full cause/stack stays in the logs, keyed
by `request_id`.

```toml
[server.smtp]
host = "smtp.internal.example.org"   # empty disables alerts
port = 25
from = "pc-server@eawag.ch"
to   = ["rdm@eawag.ch"]
```

It is a plain SMTP relay without authentication. When `host` is set, the
settings are validated at startup (valid `from`/`to`, port 1–65535).

### HTTPS

`pc-server` speaks **plain HTTP only** - it has no TLS listener. Run it behind
a TLS-terminating reverse proxy (nginx) and bind it to localhost
(`listenAddress = "127.0.0.1:8080"`). Because the CKAN API token travels in the
`Authorization` header, HTTPS in front is **mandatory** for any non-local
deployment. The nginx config, Docker setup and `trustedProxies` guidance are in
[deploy-server.md](deploy-server.md).

---

## 4. Testing

```bash
go test ./...
```
