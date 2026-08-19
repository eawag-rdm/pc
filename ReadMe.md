# PC - Package Checker

This program aims to improve the quality of data publications by running a few
simple tests to ensure best practices for publishing data are being followed.

Checks are run by file / repository (data package):

**By file:**
- HasOnlyASCII (for filenames)
- HasNoWhiteSpace (for filenames inside archives only; CKAN replaces spaces in
  resource names on download)
- IsFreeOfKeywords (file contents); non-binary files, `.xlsx` and `.docx` are supported
- IsFreeOfSecrets (file contents); credentials, tokens and private keys, found by
  the bundled [betterleaks](https://github.com/betterleaks/betterleaks) scanner
- IsValidName (nonsense files, e.g. `.Rhistory`)
- HasFileNameSpecialChars (``~!?@#$%^&*`;,'"()<>[]{}``)
- IsFileNameTooLong (>64 characters)

**By repository:**
- HasReadme (a readme file exists in the repository)
- ReadMeContainsTOC (readme mentions each file contained in the repository)

Archives (`.zip`, `.tar`, `.tar.gz`, `.7z`) are also supported: file-name checks
run on the contained file list, and file contents are scanned (IsFreeOfKeywords,
IsFreeOfSecrets) within configurable size/memory limits.

Files are discovered via **collectors**:
- `LocalCollector` - reads files from the local file system.
- `CkanCollector` - resolves a CKAN package by name via the CKAN API, then reads
  the resource files locally from the CKAN FileStore. The binary must run where
  the FileStore is readable (the CKAN server or a machine with the mount).

## Two ways to run

**CLI** - interactive scanning with TUI, JSON, plain or HTML output:

```bash
go build -ldflags="-s -w" -o pc .
cp pc.toml.example pc.toml       # then edit
./pc -location ./my-data         # or a CKAN package name, depending on the collector
```

**HTTP server** - a REST API for a web frontend (`POST /api/v1/analyze`,
plus `/health` and `/ready` probes):

```bash
go build -o pc-server ./cmd/pc-server
./pc-server -config ./pc.toml
```

## Configuration

Both binaries share one `pc.toml` (template: `pc.toml.example`):

- `[general]` - scan/memory limits (both)
- `[[rule]]` - one named instance of a check: `name`, `check`, and
  `include`/`exclude` **regex** patterns selecting the files it runs on (both).
  A rule for `IsFreeOfKeywords`, `IsValidName` and `HasReadme` is required; one
  for `IsFreeOfSecrets` is optional and turns on the betterleaks secret scan
  (the CLI needs the scanner on PATH or a path given in `binary`; the Docker
  image ships it).
- `[collector.*]` - collector settings; the CKAN URL, server-side token and
  FileStore path live in `[collector.CkanCollector]` (both)
- `[operation.main]` - which collector the CLI uses (CLI only)
- `[server]` + `[server.smtp]` - listen address, rate limits, timeouts, CORS
  allow-list and optional admin email alerts (server only)

Both binaries validate their configuration at startup and refuse to start with
a clear error when a required section or key is missing or wrong-typed.

**The detailed guide for running and configuring both - CLI flags, TUI over
SSH, all server endpoints, error codes and the full `[server]` reference - is
in [docs/usage.md](docs/usage.md).**

## Production deployment (server)

The server speaks plain HTTP and is designed to run behind nginx with HTTPS
termination, packaged via the provided `Dockerfile` / `docker-compose.yml`
(read-only FileStore mount, configurable UID/GID). See
[docs/deploy-server.md](docs/deploy-server.md).

## Testing

```bash
go test ./...
```
