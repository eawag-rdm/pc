# syntax=docker/dockerfile:1
#
# Minimal multi-stage build for pc-server.
# The server is configured ENTIRELY from a TOML file (no tunable flags); mount
# it read-only at /etc/pc/pc.toml. See docker-compose.yml for the volume/port
# wiring and the alignment notes with pc.toml.

# ---- build stage ----
FROM golang:1.23-alpine AS build
WORKDIR /src

# Module download layer (cached unless go.mod/go.sum change).
COPY go.mod go.sum ./
RUN go mod download

# Build a static, stripped binary (CGO off -> runs on a minimal base, no libc).
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/pc-server ./cmd/pc-server

# ---- runtime stage ----
FROM alpine:3.20

# The non-root user is created with a configurable uid:gid (build args) so it can
# match the OWNER of the CKAN storage share — set these in docker-compose so the
# read-only mount is readable. They must not collide with an existing id in the
# base image (share-owner ids are typically > 1000, which is fine).
ARG PC_UID=10001
ARG PC_GID=10001

# ca-certificates: the server calls the CKAN API over HTTPS.
# (busybox wget, already in alpine, is used by the compose healthcheck.)
RUN apk add --no-cache ca-certificates && \
    addgroup -g "${PC_GID}" pc && \
    adduser -D -u "${PC_UID}" -G pc pc

COPY --from=build /out/pc-server /usr/local/bin/pc-server

# Runs unprivileged as the pc user built above. The listen address comes from
# [server] listenAddress in the TOML and must bind 0.0.0.0 (not 127.0.0.1) to be
# reachable from outside the container.
USER pc
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/pc-server", "-config", "/etc/pc/pc.toml"]
