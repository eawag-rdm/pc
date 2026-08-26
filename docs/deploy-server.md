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
the binary stays bundled and the check reactivates by flipping the rule's own
`enabled` key.* The image bundles the
[betterleaks](https://github.com/betterleaks/betterleaks) binary for the
`IsFreeOfSecrets` `[[rule]]` - version and SHA-256 are pinned via the
`BETTERLEAKS_VERSION` / `BETTERLEAKS_SHA256` build args in the `Dockerfile`
(checksum-verified at build). The scanner runs offline, capped at its
`[rule.params]` `maxProcs` cores and `timeoutSeconds` per analysis, and fed only
files (and archive containers) within its `maxFileSize`. `maxProcs`
is itself capped by the **configured** `[general] maxCores` (default 4): the
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

**PDF worker processes.** Extraction runs outside the server process, in up to
`min(4, maxCores)` subprocesses - the same binary re-executed with an argv
sentinel, so `ps` inside the container shows them beside PID 1 (`pc-server
__pc-pdf-worker`). Size the container for one PDF engine **per worker**, not one
per server: each holds pdfium's ~18 MiB of wasm linear memory plus its own
wazero runtime, and the resident figure tracks the largest document that worker
has handled. They also run alongside the server's own pools, so a host can
briefly see up to twice `maxCores` while extraction is in flight. A graceful
shutdown ends them once the analyses drain, and any orderly exit of the server -
including a drain that times out - closes their pipes, whose EOF is what reaps
them. A `kill -9` or an OOM kill can leave a worker mid-document and not reading
that pipe, which is what the parent-death signal the pool sets on every worker
backstops; it is Linux-only, and the deploy image is Linux, so it applies here.
The full model - lazy start, retirement, and what happens if the binary is
replaced under a running server - is in
[usage.md](usage.md#pdf-extraction-runs-in-worker-processes).

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
- `client_ip` (the connection's address) is logged only when
  `logClientIP = true` (it is personal data under the Swiss revFADP; documented
  purpose is abuse/security, and retention is bounded by the Docker log driver
  settings above). Under the same gate, a request carrying an `X-Real-IP`
  header also gets a `real_ip` field with the header value whitespace-trimmed
  and truncated to 45 bytes - it is *not* checked against `trustedProxies`, so
  an untrusted client can put anything there. The limiter key is unaffected
  (see section 5).

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
- Entries are parsed at boot under the same grammar as `allowedClients`
  (section 9): CIDRs only, no host bits (`192.0.2.7/24` is refused - write
  `192.0.2.0/24` for the network or `192.0.2.7/32` for the single host) and no
  IPv4-mapped IPv6 prefixes (write the plain IPv4 form). A bad entry **stops the
  server**, naming it - it is never silently dropped, which would leave the
  proxy untrusted and key every client behind it on the proxy's own address.
  **This is a change:** entries with host bits used to be accepted and silently
  masked (`10.1.2.3/8` became `10.0.0.0/8`), and an IPv4-mapped entry such as
  `::ffff:127.0.0.1/128` used to be normalized to `127.0.0.1/32` and work. Both
  now stop the start, so rewrite them (`10.0.0.0/8`, `127.0.0.1/32`) before
  upgrading.
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
`SIGKILL` backstop. A second `SIGTERM` / `SIGINT` during the drain ends the
process immediately, without waiting for the in-flight analyses.

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

---

## 9. Client allow-list (`allowedClients`)

`POST /api/v1/analyze` can be restricted to a list of source networks. The
feature is **off by default**: with the key absent or empty, every client may
call the endpoint as before.

```toml
[server]
allowedClients = ["192.0.2.0/24", "2001:db8:1::/48"]   # CIDRs only
```

- **CIDR-only grammar, checked at boot.** Every entry must be a CIDR
  (`192.0.2.7/32` for a single host). A bare IP, a hostname or a typo **stops
  the server**, naming the entry - no entry is ever silently dropped. An entry
  with host bits set (`192.0.2.7/24`) is refused as well, because only you know
  whether that meant the one host or the whole /24. `trustedProxies`
  (section 5) uses the same grammar, so a line moves between the two keys
  unchanged.
- **`/analyze` only.** `/health` and `/ready` are never gated, so probes keep
  working from the orchestrator's own addresses.
- **A `POST` from a client outside the list gets `403 client_not_allowed`** in
  the standard error envelope. Denials appear in the access log as ordinary 403
  requests; there is no separate event. Only `POST` is gated, so
  `GET /api/v1/analyze` draws the usual `405 method_not_allowed` with
  `Allow: POST`, and an `OPTIONS` preflight is answered `204` by CORS upstream of
  both the route guard and the gate.
- **It runs ahead of the rate limiter**, so denied requests never enter the
  limiter's key map and cannot evict honest clients' counters.

**It does not replace the rate limits.** Allow-listed clients still have their
per-IP and global hourly budgets: the allow-list decides *who may ask*, the
limiter *how often*.

**It matches the connection's peer address, never a header.** The gate reads
`RemoteAddr` - the address the kernel sees - and never `X-Real-IP`, so it is
independent of `trustProxyHeaders`/`trustedProxies` (section 5) and no request
header can influence it. An address that cannot be parsed is denied (fail
closed) - a zoned IPv6 peer such as `fe80::1%eth0` is one of those, so an
`fe80::/10` entry cannot admit zoned link-local clients. The two families are
matched separately: `0.0.0.0/0` does not cover IPv6 clients and `::/0` does not
cover IPv4 ones, so a list meant to admit everything needs both.

**Behind a reverse proxy the list therefore controls which HOSTS may reach the
endpoint.** The peer is the proxy, so listing it admits every client that
connects through it - the allow-list cannot tell those clients apart. The rate
limiter can, but only when `trustProxyHeaders = true`, `trustedProxies` names the
proxy and the proxy itself overwrites `X-Real-IP` (section 5); without all three,
every client behind the proxy shares one rate-limit bucket.

**This is a change:** the gate used to match the client IP the rate limiter
derives, so behind a proxy an `allowedClients` holding the END CLIENTS' CIDRs
worked. It now matches the connection peer, so such a list denies everything -
list the **proxy's** address instead. Denials also moved from
`404 not_found` to `403 client_not_allowed`, which matters for any client keying
on the old response.

After deploying the key, **confirm the `client allow-list enabled` line in the
boot log** (it carries the entry count): a binary older than the feature ignores
unknown `[server]` keys silently and would keep admitting everyone.

Debugging a lockout: the access log's `client_ip` field (section 4, needs
`logClientIP = true`) shows the connection peer as logged - IPv6 bracketed. The
gate parses that same peer itself and denies outright when it cannot (a
zone-suffixed IPv6 address, say), so a logged value is not proof that anything
was matched. `real_ip` plays no part in the decision. Note also that a `403` on
`/api/v1/analyze` can come from this gate **or** from a token `access_denied` -
the access record carries no error code, so correlate by `request_id`.

Operational cost: the list needs maintenance. IPv6 clients usually need their
`/64` (a `/128` admits one address only), NAT pools and VPN ranges change, and
if a browser frontend calls the API directly the client IP is the **user's**
browser - an allow-list then ends public browser use (which is why CORS remains
a separate, browser-only control).
