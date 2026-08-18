# syntax=docker/dockerfile:1
#
# Minimal multi-stage build for pc-server.
# The server is configured ENTIRELY from a TOML file (no tunable flags); mount
# it read-only at /etc/pc/pc.toml. See docker-compose.yml for the volume/port
# wiring and the alignment notes with pc.toml.

# ---- build stage ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# Module download layer (cached unless go.mod/go.sum change).
COPY go.mod go.sum ./
RUN go mod download

# Build a static, stripped binary (CGO off -> runs on a minimal base, no libc).
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/pc-server ./cmd/pc-server

# ---- betterleaks stage ----
# Secret-scanner binary for the IsFreeOfSecrets check: pinned release, checksum-verified.
FROM alpine:3.20 AS betterleaks
ARG BETTERLEAKS_VERSION=1.7.2
ARG BETTERLEAKS_SHA256=ea9ed6a4aa2845ac2e00c0eafbc841057631321d53c061d5a435cf33e6e9ddaf
RUN apk add --no-cache ca-certificates && \
    wget -qO /tmp/betterleaks.tar.gz "https://github.com/betterleaks/betterleaks/releases/download/v${BETTERLEAKS_VERSION}/betterleaks_${BETTERLEAKS_VERSION}_linux_x64.tar.gz" && \
    echo "${BETTERLEAKS_SHA256}  /tmp/betterleaks.tar.gz" | sha256sum -c - && \
    tar -xzf /tmp/betterleaks.tar.gz -C /usr/local/bin betterleaks

# ---- runtime stage ----
FROM alpine:3.20

# The non-root user is created with a configurable uid:gid (build args) so it can
# match the OWNER of the CKAN storage share - set these in docker-compose so the
# read-only mount is readable. They must not collide with an existing id in the
# base image (share-owner ids are typically > 1000, which is fine).
ARG PC_UID=10001
ARG PC_GID=10001

# ca-certificates: the server calls the CKAN API over HTTPS.
# (busybox wget, already in alpine, is used by the compose healthcheck.)
# /var/lib/pc: writable state dir for the optional result cache
# ([server] resultCacheDir) and the wasm compilation cache. Mounted as a named
# volume in docker-compose to give the wasm cache a persistent home and the
# right ownership; the result cache itself is cleared at every server start.
# Owned by the pc user because the container runs unprivileged.
RUN apk add --no-cache ca-certificates && \
    addgroup -g "${PC_GID}" pc && \
    adduser -D -u "${PC_UID}" -G pc pc && \
    mkdir -p /var/lib/pc && \
    chown pc:pc /var/lib/pc

COPY --from=build /out/pc-server /usr/local/bin/pc-server
# Secret scanner used by the IsFreeOfSecrets check (its [[rule]] in pc.toml).
COPY --from=betterleaks /usr/local/bin/betterleaks /usr/local/bin/betterleaks

# Runs unprivileged as the pc user built above. The listen address comes from
# [server] listenAddress in the TOML and must bind 0.0.0.0 (not 127.0.0.1) to be
# reachable from outside the container.
USER pc
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/pc-server", "-config", "/etc/pc/pc.toml"]
