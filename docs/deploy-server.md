# pc-server Deployment Guide

`pc-server` is the HTTP API in front of the `pc` analysis engine. It is a
**single-instance, beta** service that is **co-located with CKAN** and reads
resource files from a **mounted network share** (CKAN's default FileStore
layout). It only calls the CKAN Action API for metadata; it never downloads
resource bytes over HTTP.

This guide covers the operational concerns for running it in Docker behind
nginx.

---

## 1. Storage precondition (required)

The server reads upload resources directly from the local filesystem, from the
path configured as `[collector.CkanCollector.attrs] ckan_storage_path`.

- **Supported:** the **default CKAN FileStore** on a mounted (local or network)
  share. The server resolves each `url_type == "upload"` resource to its sharded
  path under `ckan_storage_path/resources/<id[:3]>/<id[3:6]>/<id[6:]>` and reads
  it there.
- **Unsupported:** cloud-storage backends (S3 / Azure Blob via
  `ckanext-cloudstorage`). With those backends, uploads still have
  `url_type == "upload"` but **no local file** exists, so the analysis cannot
  read them. Do not deploy `pc-server` against a cloud-storage CKAN.

`GET /ready` verifies the mount is present and readable; mount the share so it
is readable by the user running the container, and make sure the path matches
`ckan_storage_path` exactly. A missing/stale mount makes `/ready` return `503
service_not_ready`.

---

## 2. Health & readiness probes

| Endpoint | Meaning | Use for |
|----------|---------|---------|
| `GET /health` | Cheap static `200`, no I/O | **Container liveness** (Docker `HEALTHCHECK`) |
| `GET /ready` | CKAN reachable (cached ~5s) **and** storage mount readable | **Load-balancer / orchestrator readiness gate** |

Both are exempt from rate limiting and the concurrency semaphore, so they are
safe to poll frequently. `/ready` returns `503` with the `service_not_ready`
envelope when CKAN or the mount is unavailable; the result is cached for ~5
seconds to bound upstream load.

Use the two probes for **different** purposes:

- **`/health` is the container liveness/restart check.** It does no upstream
  I/O, so a container is never restarted just because CKAN or the storage mount
  is transiently down. The committed `docker-compose.yml` HEALTHCHECK uses
  `/health` for exactly this reason.
- **`/ready` is the readiness gate** consulted by a load balancer or
  orchestrator to decide whether to *route traffic* to the instance. It returns
  `503 service_not_ready` while CKAN or the mount is unavailable, so traffic is
  withheld - but the container is left running, so it recovers on its own once
  the dependency returns. Do **not** wire `/ready` to a container restart.

Example Docker `HEALTHCHECK` (liveness - matches the committed compose):

```dockerfile
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8080/health || exit 1
```

The URL port must match the `[server] listenAddress` port in `pc.toml`.

---

## 3. Docker image & compose

A **`Dockerfile`** and a **`docker-compose.yml`** ship in the repository root -
use them rather than copying the inline snippets here. The `Dockerfile` is a
minimal multi-stage build (static CGO-off binary on `alpine`) running
unprivileged; the server is configured **entirely from `pc.toml`** (no flags)
and the entrypoint reads it from `/etc/pc/pc.toml`.

**Non-root user (`PC_UID` / `PC_GID` build args).** The container runs as a
non-root `pc` user whose uid:gid is set by the `PC_UID` / `PC_GID` build args
(default `10001:10001`). Set these to the **owner of the CKAN storage share** so
the read-only storage mount is readable from inside the container. They are
baked into the image at build time, so change them in `docker-compose.yml` and
rebuild (`docker compose build` / `up --build`):

```yaml
services:
  pc-server:
    build:
      context: .
      dockerfile: Dockerfile
      args:
        PC_UID: "10001"   # uid:gid the server runs as - set to the
        PC_GID: "10001"   # CKAN storage share's owner so the mount is readable
```

**Listen address must bind `0.0.0.0`.** Inside the container the server is only
reachable if `[server] listenAddress` binds all interfaces, e.g.
`listenAddress = "0.0.0.0:8080"` (the default `127.0.0.1:8080` is reachable only
from inside the container). The **published port must match that port**: the
container side of `ports:` must equal the `listenAddress` port. Bind on host
localhost when nginx terminates in front of it (`"127.0.0.1:8080:8080"`, as the
committed compose does), or use `"8080:8080"` to expose it directly.

**Mounts** (both read-only): `pc.toml` at `/etc/pc/pc.toml`, and the CKAN
storage share at the path that **equals** `[collector.CkanCollector.attrs]
ckan_storage_path` in `pc.toml`. The committed compose also sets
`restart: unless-stopped`, the `/health` healthcheck (§2), the log rotation
(§4) and `stop_grace_period: 340s` (§7).

**Result cache volume.** The `pc-cache` named volume (`/var/lib/pc`) holds the
optional result cache when `[server] resultCacheDir` points inside it. Only the
wasm cache below persists across recreations: the server deletes the cache's
`entries/` subdirectory at every start and refuses to start if it cannot. Run
**one server per `resultCacheDir`** - there is no lock, so a second instance
(second replica, blue/green overlap) wipes the first one's entries at its boot;
give each instance its own directory.

**Secret scanner (betterleaks).** *Dormant since 2026-08-04: the scan ships
disabled (`enabled = false`) because it is too slow for our latency target;
the binary stays bundled and the check reactivates by flipping the attr.* The
image bundles the
[betterleaks](https://github.com/betterleaks/betterleaks) binary for the
`[test.IsFreeOfSecrets]` check - version and SHA-256 are pinned via the
`BETTERLEAKS_VERSION` / `BETTERLEAKS_SHA256` build args in the `Dockerfile`
(checksum-verified at build). The scanner runs offline, capped at
`attrs.maxProcs` cores and `attrs.timeoutSeconds` per analysis. `maxProcs` is
itself capped by the **configured** `[general] maxCores` (default 4): the
scanner is a child process and inherits none of the server's CPU budget, so the
smaller of those two values wins - not the budget the server settled on, which
may be lower. Note that the scan runs alongside the server's own pools, so a
host can briefly see up to twice `maxCores` while one is in flight.

**PDF engine cache.** The committed compose sets `XDG_CACHE_HOME=/var/lib/pc`
(the persistent named volume) so the sandboxed PDF engine's one-time wasm
compilation (~3 s) is cached across container recreations; without it every
fresh container pays the compile on its first PDF. Cache directories are
keyed by wazero version (`wazero-v<X>-<arch>-<os>`, ~19 MB each) and old
versions are never pruned - clear stale siblings after dependency bumps.
Dev-machine gotcha: `go test` binaries resolve the wazero version to "dev",
so that cache key survives wazero upgrades and can serve native code
compiled by the OLD wazero - run `rm -rf ~/.cache/pc/wazero` after bumping
wazero (release builds key correctly and are unaffected).

**RAM-backed `/tmp` (tmpfs).** The committed compose mounts `/tmp` as tmpfs
(2 GB): the secret scan extracts archive members there before scanning, so
plaintext copies live only in RAM and vanish on container restart. Size it to
at least the worst-case extraction (archives per analysis x
`maxTotalArchiveMemory`); when tmpfs runs full, affected members are skipped
with a logged warning and the analysis continues.

---

## 4. Logging & retention (Docker log driver)

`pc-server` writes **structured JSON logs to stdout** (`log/slog`) and does
**not** write or rotate log files itself. Rotation and retention are the
responsibility of the **Docker log driver** - configure them there.

`docker run`:

```bash
docker run \
  --log-driver json-file \
  --log-opt max-size=10m \
  --log-opt max-file=5 \
  ... pc-server
```

`docker-compose.yml` (the committed compose already sets this `logging:`
block - rotate at 10 MiB, keep 5 files):

```yaml
services:
  pc-server:
    image: pc-server
    logging:
      driver: json-file
      options:
        max-size: "10m"   # rotate at 10 MiB
        max-file: "5"      # keep 5 rotated files
```

Notes:
- The access log emits **one record per request** with `request_id`, method,
  path, `package_id`, status and `latency_ms`.
- The token / `Authorization` header is **never** logged.
- The `ckan_upstream_outcome` record carries either `ckan_status` (CKAN
  answered with a usable body) or `transport_error_class`: `transport` (no
  response at all), `unusable_body` (a response arrived but cannot be used -
  malformed JSON, no `result` object, or past the size cap),
  `resource_unreadable`, or `collector`. For `unusable_body` an extra
  `ckan_error_detail` field names which of the three it was (a fixed string; it
  never carries body content, a URL or the token).
- **Dashboard change:** the oversized-response case used to be logged as class
  `transport` and is now `unusable_body` - update any query keyed on the old
  value.
- `client_ip` is logged only when `logClientIP = true` (it is personal data
  under the Swiss revFADP; documented purpose is abuse/security, and retention
  is bounded by the Docker log driver settings above).

---

## 5. Reverse proxy (nginx) and `trustedProxies`

The rate limiter keys on the **client IP**. Behind nginx the real client IP
arrives in a header, so the server reads `X-Real-IP` - but **only** when the
connection's `RemoteAddr` is within a configured trusted-proxy CIDR. This
prevents a client from spoofing `X-Real-IP` to evade or poison rate limiting.

Configure it consistently on both sides:

`pc.toml`:
```toml
[server]
trustProxyHeaders = true
trustedProxies    = ["127.0.0.1/32"]   # the address nginx connects FROM
```

`nginx`:
```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;            # the limiter key source
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 330s;                            # ≥ requestTimeoutSeconds + 30s
}
```

Guidance:
- `trustedProxies` must list the address(es) nginx **connects from** (often
  `127.0.0.1/32` for a co-located proxy, or the proxy's Docker-network IP/CIDR).
- If nginx connects over a Docker bridge, set `trustedProxies` to that bridge
  subnet (e.g. `172.17.0.0/16`), not `127.0.0.1/32`.
- If you do **not** run behind a trusted proxy, set `trustProxyHeaders = false`
  so the connection `RemoteAddr` is always used.
- IPv6 clients are keyed on their `/64` prefix.

CORS: set `allowedOrigins` to the exact origin(s) of your frontend (e.g.
`["https://frontend.example.org"]`). The server answers `OPTIONS` preflight,
allows `GET/POST/OPTIONS` and the `Authorization` header, and echoes the
allowed origin with credentials enabled.

---

## 6. Admin alerts (`[server.smtp]`)

When the server returns a **server-fault** response it can email an admin list.
This is configured in an optional `[server.smtp]` sub-section:

```toml
[server.smtp]
host = "smtp.example.org"        # SMTP relay host; empty/unset disables alerts
port = 25                        # default 25
from = "pc-server@example.org"   # From / envelope-sender address
to   = ["rdm@example.org"]       # admin recipients (>= 1 required when enabled)
```

It is a **plain SMTP relay with NO authentication** - point it at a relay that
accepts mail from the container's network.

- **Disabled** unless `host` is set **and** `to` has at least one recipient.
  With either missing, no alerter is created and faults are only logged.
- **Validated at boot when enabled** (fail fast): `port` must be `1–65535`, and
  `from` and every `to` entry must be valid email addresses. A bad value stops
  the server from starting rather than failing silently at the first fault.
- **Which responses trigger mail:** only `internal_error` (including a recovered
  panic) and `resource_unreadable`. 4xx responses and the other 5xx codes
  (`analysis_timeout`, `ckan_unavailable`, `service_busy`, `service_not_ready`,
  `server_restarting`) are **not** server faults and never alert. Every
  `internal_error` is reported - no rate cap, no dedup. `resource_unreadable`
  is reported **at most once per package per hour**: a broken file in storage
  faults on every retry of the same package, so repeats inside the window are
  swallowed and tallied; the next alert for that package carries a
  `suppressed: N further occurrences since last alert` line.
- **Limits of the cooldown:** each swallowed occurrence still leaves an
  `admin_alert_suppressed` log line, so a storm is visible in the logs; a tally
  that never gets a follow-up alert (the fault stops, or the server shuts down)
  is lost; and the window lives in memory only, so after a restart the first
  fault for a package alerts again.
- **The mail carries no secrets.** It contains only the `request_id`, the error
  `code`, the request `method`, `path` and `package_id`, and (for
  `resource_unreadable`) the suppressed-occurrence count - never the token,
  CKAN URL, raw upstream body, internal file paths or a stack trace. The full
  cause and stack stay in the server logs, keyed by the same `request_id`.

Delivery is asynchronous (a single background worker, so at most one SMTP
connection is open at a time) and bounded by a 10s timeout, so a slow or broken
relay never blocks request handling or shutdown; delivery failures are logged
(`admin_alert_failed`) but never affect the client response.

---

## 7. Graceful shutdown

On `SIGTERM` / `SIGINT` the server:
1. flips into **draining** mode - new `POST /api/v1/analyze` requests are
   rejected with `503 server_restarting`;
2. stops accepting new connections and **waits for in-flight analyses to
   finish**, bounded by a drain timeout of `requestTimeoutSeconds + 30s`
   (**330s** at the default 300s), so a running analysis can hit its own
   deadline and still flush its `504` envelope;
3. exits.

`/health` and `/ready` continue to answer during the drain. Make sure your
orchestrator's stop grace period is longer than that drain timeout, e.g.
`docker stop --time 340` (the committed compose sets
`stop_grace_period: 340s`), so analyses are not killed mid-flight. Raise both
together whenever you raise `requestTimeoutSeconds`. The drain is best-effort:
an analysis that ignores its deadline is still cut off by the orchestrator's
`SIGKILL` backstop.

If the listen address cannot be bound at startup (port in use, bad address),
the process **exits non-zero immediately** rather than hanging.

---

## 8. HTTP hardening (built in)

- `ReadHeaderTimeout` (10s, slowloris guard) and `MaxHeaderBytes` (1 MiB) are
  set.
- `ReadTimeout` is 30s.
- `WriteTimeout` is **not** a fixed value: it is derived from the configured
  request timeout as `requestTimeoutSeconds + 30s` (a fixed 30s margin). With
  the default `requestTimeoutSeconds = 300` this is **330s**. It scales with
  `requestTimeoutSeconds` on purpose, so the socket always outlives the
  analysis deadline: when the analysis hits its hard timeout the handler writes
  a clean `analysis_timeout` (504) envelope, and the longer `WriteTimeout`
  guarantees that 504 can be fully flushed before the socket's write deadline
  tears the connection down. Raising `requestTimeoutSeconds` automatically
  raises `WriteTimeout` with it.
- Request bodies are capped (the analyze body is a tiny JSON object).
- Two nested timeouts bound an analysis: `ckanRequestTimeoutSeconds`
  (default **10s**) caps the single CKAN `package_show` call - a CKAN that
  cannot answer a metadata GET within it is reported as `ckan_unavailable`
  (502) - and `requestTimeoutSeconds` (default **300s**) caps the whole
  request including the checks phase; when it fires the checks stop between
  files and the client receives `analysis_timeout` (504).
- A panic in any handler is recovered and returned as `internal_error` (500)
  without crashing the process or leaking a stack trace to the client (this
  also fires an admin alert - see §6). A panic inside a checks worker
  goroutine is likewise converted into a logged failure instead of killing
  the process.
