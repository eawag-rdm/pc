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
| `GET /health` | Cheap static `200`, no I/O | Liveness probe |
| `GET /ready` | CKAN reachable (cached ~5s) **and** storage mount readable | Readiness probe / LB health check |

Both are exempt from rate limiting and the concurrency semaphore, so they are
safe to poll frequently. `/ready` returns `503` with the `service_not_ready`
envelope when CKAN or the mount is unavailable; the result is cached for ~5
seconds to bound upstream load.

Example Docker healthcheck:

```dockerfile
HEALTHCHECK --interval=15s --timeout=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/ready || exit 1
```

---

## 3. Logging & retention (Docker log driver)

`pc-server` writes **structured JSON logs to stdout** (`log/slog`) and does
**not** write or rotate log files itself. Rotation and retention are the
responsibility of the **Docker log driver** — configure them there.

`docker run`:

```bash
docker run \
  --log-driver json-file \
  --log-opt max-size=10m \
  --log-opt max-file=5 \
  ... pc-server
```

`docker-compose.yml`:

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
- `client_ip` is logged only when `logClientIP = true` (it is personal data
  under the Swiss revFADP; documented purpose is abuse/security, and retention
  is bounded by the Docker log driver settings above).

---

## 4. Reverse proxy (nginx) and `trustedProxies`

The rate limiter keys on the **client IP**. Behind nginx the real client IP
arrives in a header, so the server reads `X-Real-IP` — but **only** when the
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
    proxy_read_timeout 300s;                            # ≥ requestTimeoutSeconds
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

## 5. Graceful shutdown

On `SIGTERM` / `SIGINT` the server:
1. flips into **draining** mode — new `POST /api/v1/analyze` requests are
   rejected with `503 server_restarting`;
2. stops accepting new connections and **waits for in-flight analyses to
   finish**, bounded by a drain timeout that is larger than
   `requestTimeoutSeconds` (so a running analysis can complete);
3. exits.

`/health` and `/ready` continue to answer during the drain. Make sure your
orchestrator's stop grace period is at least as long as `requestTimeoutSeconds`
(default 300s) plus a small margin, e.g. `docker stop --time 330`, so analyses
are not killed mid-flight.

If the listen address cannot be bound at startup (port in use, bad address),
the process **exits non-zero immediately** rather than hanging.

---

## 6. HTTP hardening (built in)

- `ReadHeaderTimeout` (slowloris guard) and `MaxHeaderBytes` are set.
- `ReadTimeout` 30s, `WriteTimeout` 300s.
- Request bodies are capped (the analyze body is a tiny JSON object).
- A panic in any handler is recovered and returned as `internal_error` (500)
  without crashing the process or leaking a stack trace to the client.
