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
| `[[rule]]` | both | check rules - the check-configuration surface (see below) |
| `[collector.LocalCollector]` | CLI | local file system collector; `attrs`: `maxFolderDepth` (0 = top level only, N = descend N levels; boundary-depth folders are listed but not entered), `maxFileCount` (walk stops after N collected entries, 0 = no cap), `includeFolders` (legacy, true = unlimited recursion; an explicit `maxFolderDepth` wins) |
| `[collector.CkanCollector]` | both | CKAN URL, server-side token, TLS verify, storage path |
| `[operation.main]` | CLI | which collector the CLI uses |
| `[server]`, `[server.smtp]` | server | listen address, rate limits, timeouts, CORS, admin alerts |

**Startup validation:** the `[[rule]]` surface is checked exhaustively, the rest
of the file only in places. Both binaries compile the whole rule set before
anything is scanned, so inside a rule an unknown key, a wrong-typed value, an
unknown parameter key, an uncompilable pattern, an unknown check or an unknown
scope refuses the start - and so do an unknown top-level table, any wrong-typed
`[server]` value, an `attrs` value of an unsupported type (`maxFileCount = 1.5`
fails), a negative `maxArchiveMemberCount`, `maxPDFPages`, `maxPDFFileSize` or
`maxCores`, and a secret-scan `timeoutSeconds` longer than the server's
`requestTimeoutSeconds` - that last one for whoever loads the file, the CLI
included. The retired `[test.<CheckName>]` surface is an unknown top-level
table today, so a config still carrying one stops with the error
`unknown top-level config key(s): test`.

Everywhere else a key nobody reads is **silently ignored**: unknown keys in
`[general]`, `[server]`, `[server.smtp]` and `[collector.X].attrs` do nothing,
as do keys sitting directly under `[collector.X]` rather than in its `attrs`
table and anything in `[operation.X]` but `collector`. The older `[general]`
byte sizes keep their default when wrong-typed (`maxArchiveFileSize = "10MB"`;
`maxArchiveMemberCount`, `maxPDFPages`, `maxPDFFileSize` and `maxCores` do
fail). The worst of it is a misspelled `[server]` key: `listenaddress` (lower
case `a`) never reaches the server, which then listens on the default
`127.0.0.1:8080` and is unreachable from outside its container.

Faults are aggregated per stage: one failed load names every rule that stage
faulted on. It does not cross stages - a decode fault (an unknown or wrong-typed
key) returns before any pattern, scope or parameter is compiled.

The server additionally validates every `[server]` value and the
`[collector.CkanCollector]` attrs. The checks `IsFreeOfKeywords`, `IsValidName`
and `HasReadme` must be declared (see below); `IsFreeOfSecrets` is optional, and
when it is configured its knobs are validated too (types, unknown keys, scan
timeout vs. the server's request timeout).

### Check rules (`[[rule]]`)

A `[[rule]]` is one named instance of a check with its own file selector and its
own parameters. A check may carry several rules, and each reports under its own
name - one rule per parameter set, so a second rule of a check must bind
parameters the first does not, or gate different strings. `pc.toml.example`
carries the full key list and every check name; the surface itself is:

```toml
[[rule]]
# Sensitive keywords in file contents, everywhere
name  = "sensitive-content"
check = "IsFreeOfKeywords"
  [[rule.params]]
  keywords = ["password", "api_key", "secret"]
  info     = "Sensitive data found:"
  [[rule.params]]
  keywords = ["/Users/", "C:\\"]
  info     = "Hardcoded paths found:"

[[rule]]
# A second rule of the same check, over one subtree of the collection only
name    = "notebook-todos"
check   = "IsFreeOfKeywords"
scope   = ["file"]
subject = "path"
include = ["^notebooks/"]
  [[rule.params]]
  keywords = ["TODO", "FIXME"]
  info     = "Unfinished notes:"
```

- `name` - operator-chosen, and unique across the whole run. It names the rule
  in the diagnostics and in the rule-focused results. The `default:` prefix is
  reserved for the rules pc synthesizes itself and is refused on a declared rule.
- `check` - the check the rule instantiates.
- `scope` - the dispatch phases the rule runs in: `file`, `archive-file-list`,
  `archive-member`, `repository`. Omitted means the check's own scopes, which
  `pc.toml.example` lists per check; naming a scope the check does not serve, or
  naming one twice, fails the load.
- `subject` - `name` or `path`, the string the patterns are matched against.
  **The default depends on the scope** (see below).
- `include` / `exclude` - RE2 regexes, matched **unanchored** and
  case-sensitively unless `ignoreCase = true`. `exclude` wins: a subject any
  exclude pattern matches is out, whatever `include` says. An absent or empty
  `include` admits everything. The same pattern in both lists is a load error -
  exclude wins, so the rule could never match it. Between two rules of one check
  the patterns are no part of any verdict: what a config may not say twice is a
  parameter set (see `[[rule.params]]`), and each rule carries the file set it
  was written for.
- `enabled = false` - the rule does not run (a check with another enabled rule
  still does). It is validated at load all the same, so a config the checks
  could not honour fails now rather than on the day someone re-enables the rule.
- `[[rule.params]]` - the check's own parameters, type-checked at load by the
  check itself; an unknown parameter key fails the load. Repeating the table
  declares several parameter sets on one rule - each keyword group above reports
  with its own `info`. The single-bracket `[rule.params]` is the one-set
  spelling. **One rule per parameter set**: two rules of one check that bind the
  same parameters where they gate the same strings - a scope both serve, read
  there through the same subject, under the same `ignoreCase` - are refused,
  because the pair is one scan whose findings then carry both names. "The same
  parameters" means a `[[rule.params]]` group both rules declare, or none on
  either side for a check that takes none; a rule declaring one group twice is
  refused for the same reason, and a rule parked with `enabled = false` counts.

What `subject` selects, per scope:

| Scope | `subject = "name"` | `subject = "path"` | default |
|---|---|---|---|
| `file` | the file's name | its collection-relative path | `name` |
| `archive-file-list` | the member's base name | the member path in the archive | `name` |
| `archive-member` | the member's base name | the member path in the archive | `path` |
| `repository` | the file's name | its collection-relative path | `path` |

A declared `subject` holds for every scope the rule serves; only the default
varies by scope. The two path defaults are the strings those phases address in
practice: they are why `include = ["data/"]` on an archive-member rule matches
member paths as written instead of silently matching nothing, and why a
repository pattern like `^raw/` is read against the relative path. An
archive-member rule selects **members, not archives** - every archive is opened,
and the patterns decide which members inside it are read.

Parameters are never patterns: `keywords`, `disallowed_names` and `readme_names`
are literal strings (keywords are matched case-insensitively). `"pass.*"` looks
for the literal text `pass.*`.

### Checks that must be declared

`IsFreeOfKeywords`, `IsValidName` and `HasReadme` must be declared by a
`[[rule]]` or the load fails. It is the declaration that is required, not the
parameters: a rule with no `[[rule.params]]` loads, and `HasReadme` then falls
back to its built-in readme filename list (`pc.toml.example` ships exactly that
rule). A keyword or name rule without parameters has nothing to look for and
reports nothing.

Every other check runs whether or not a config mentions it: pc synthesizes a
default rule named `default:<CheckName>` with an empty selector and the check's
built-in defaults, so deleting a rule never silently deletes a check. The
exception is `IsFreeOfSecrets`, whose synthesized rule is disabled - the secret
scan is opt-in (see below).

`HasReadme` takes exactly **one** rule: it defines what counts as the repository
readme, and a second definition is refused. `ReadMeContainsTOC` takes no
parameters of its own - it reads that rule's `readme_names`.

`IsFileNameTooLong` takes one optional parameter, `maxLength`: the most bytes a
file name's last path component may have (default 64); anything but a positive
integer fails the load.

### Converting a pre-`[[rule]]` config

`whitelist` → `include`, `blacklist` → `exclude`, and one `keywordArguments`
entry → one `[[rule.params]]` table. **Redo archive-member rules by hand:** the
old list gated the archive by its own name *and* matched members as
case-insensitive literal substrings, while a `[[rule]]` gates members only and
reads its patterns as case-sensitive regexes - so a mechanical translation
silently **widens** the scan, and nothing errors.

### Two identical rules are refused

Two rules of one check that differ only in their `name` fail the load: the config
says one thing twice, and every finding the pair produces would be reported
twice.

The comparison resolves the spellings that do no work, so none of them saves a
duplicate: an omitted key against a declared empty one (`include = []`), the
order scopes are written in, an omitted `scope` against the check's own scopes
spelled out, and the scope-dependent `subject` default against that same subject
written out.

It is a **syntactic** comparison, deliberately: nothing static can decide whether
two different patterns select the same files. So a pair made distinct by a field
that does no work - an `ignoreCase` with no patterns to fold, a `subject` no
pattern reads - or by an extra pattern that matches nothing does load. It still
carries the same parameters, so it is one scan reporting one finding that names
both rules: the cost is the redundant name, not a doubled finding, and the
overlap warning below does not fire on it either.

### Rule diagnostics

Two warnings about the rule configuration itself, produced once per run that
finished. A cancelled run - a server analysis stopped by `requestTimeoutSeconds`,
say - emits neither: a scan that stopped early says nothing about the
configuration.

- **Dead rule** - a rule that matched no file this run, named with its check and
  its scope. Usually a typo in a pattern, or a rule left behind by a config
  edit. A phase that never ran reports nothing: a package with no archives says
  nothing about archive rules.
- **Overlap** - two rules of one check applied to the same file and scan for
  different things, so that file can be reported twice. Rules carrying the same
  parameters are one scan and one finding naming both, so they never earn the
  notice. What is left is frequently deliberate - the two keyword rules above
  overlap on every notebook by design - so the warning informs rather than
  accuses. Ignore it when you want both verdicts on that file; act on it when one
  of the two rules was meant for other files.

Both are reported at `file` and `archive-file-list` scope only. At
`archive-member` scope the dispatch gate admits every archive and the patterns
decide inside it, so a mark taken there would call every member rule alive as
soon as the package holds one archive. At `repository` scope a rule always runs
whatever its selector admits, so "matched no file" would accuse a rule of doing
its job - `HasReadme` over an empty set is exactly how "there is no readme" gets
said.

Both are operator-facing. On the CLI they appear in `warnings` (`-json`, HTML,
TUI) and in the `=== Diagnostics ===` block of `-plain`. Server responses never
carry them: their `warnings`/`errors` arrays are always empty (see the server
section below), so on the server every diagnostic goes to the log as a
`scan_diagnostic` record keyed by `request_id` instead.

### Secret scan: `IsFreeOfSecrets`

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
[[rule]]
name    = "secret-scan"
check   = "IsFreeOfSecrets"
# enabled: toggle the scan - the rule's own key, not a parameter
enabled = false
  [rule.params]
  # binary: scanner executable (name in PATH or absolute path);
  # timeoutSeconds: whole-scan cap (must not exceed [server] requestTimeoutSeconds);
  # maxProcs: CPU cores the scanner may use, capped by the configured [general] maxCores (the lower wins);
  # maxFileSize: max bytes a top-level file or archive container may have to be scanned by this rule (0 = no limit)
  binary         = "betterleaks"
  timeoutSeconds = 120
  maxProcs       = 4
  maxFileSize    = 0
```

`maxFileSize` is the rule's own size cap in bytes; `0` or an absent key means it
caps nothing. It is measured on top-level files and on archive containers - an
archive over the cap is dropped whole, before any member is extracted from it -
and never on the members themselves, which follow `[general]`
`maxArchiveFileSize` as always. Against `maxContentScanFileSize` it only ever
tightens: the lower of the two applies, and the skip message names which one.

The CLI needs the `betterleaks` binary on PATH (or `binary` set to an absolute
path); the server's Docker image ships it. The scanner runs fully offline - no
finding validation calls, nothing leaves the machine. Note that low-entropy
plain passwords (e.g. `password = hunter2`) are below the scanner's generic-rule
entropy threshold; add a `password` keyword group to an `IsFreeOfKeywords` rule
if you want a literal-match safety net for those.

### `[general]` limits

```toml
[general]
maxArchiveFileSize     = 10485760   # max size per file inside an archive (bytes)
maxTotalArchiveMemory  = 536870912  # total memory budget for archive processing
maxArchiveMemberCount  = 1000       # max members per archive (both archive walks)
maxPDFPages            = 10         # page ceiling per PDF (longer = skipped whole)
maxPDFFileSize         = 1048576    # max PDF size in bytes (larger = not read)
maxContentScanFileSize = 20971520   # max size for content-scanned files
maxCores               = 4          # CPU cores this process may use (0 = the same default)
```

`maxCores` caps this process's CPU: every worker pool, every
parallel/sequential threshold and the PDF engine's instance pool size
themselves from it. The external secret scanner is a child process and inherits
nothing automatically, so it follows the **configured** `maxCores` - the lower
of its own `maxProcs` and `maxCores` - whatever budget pc settled on for itself.
On a machine smaller than `maxCores`, or under a lower `GOMAXPROCS`, the scanner
can therefore be granted more cores than pc runs with (`GOMAXPROCS=1 pc` runs pc
at 1 while the scanner still gets 4; a 2-core host with `maxCores = 4` likewise
gives it 4). It also runs alongside pc's own pools, so a host can briefly see up
to twice `maxCores` while a scan is in flight.

It applies **whether or not you set it**: an absent key means the default of 4,
not "use every core", so a 16-core host runs pc at 4 cores unless told
otherwise. For pc itself it is a ceiling and never a request: the budget is the
lowest of `maxCores`, a `GOMAXPROCS` environment variable and what the machine
or cgroup allows, rather than oversubscribing the machine (`GOMAXPROCS=16 pc`
still runs at 4).

pc always pins the budget at startup, so it never follows a container CPU quota
that changes while it runs - in either direction. A quota raised after start is
not taken up; a quota lowered after start leaves pc throttled rather than
resized.

These limits gate the keyword checks and the secret scan alike: files above
`maxContentScanFileSize` are never content-scanned (skip acknowledgement
instead), and only archive members within `maxArchiveFileSize` /
`maxTotalArchiveMemory` are unpacked. Excel/Word files (`.xlsx`/`.docx`) are
text-extracted and keyword-scanned both at top level and inside archives;
their zip index is checked against the same limits before parsing, and
over-long extractions are truncated with an acknowledgement (the truncated
part is still scanned). PDFs are text-extracted (sandboxed PDFium) and
keyword-scanned page by page - findings cite the page.

`maxPDFFileSize` (default 1 MB) and `maxPDFPages` (default 10) are
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
then skipped with one acknowledgement.

`maxArchiveMemberCount` bounds both archive walks, each counting what it
processes: the content scan counts unpack candidates (members past the name
filter with content to read), the member-name checks count every archive entry,
directories included. Over the limit the content scan stops and acknowledges the
archive (members already scanned stay reported), while the name-check walk
checks nothing and emits one skip acknowledgement. For `.tar.gz` both walks are
additionally bounded by the decompression budget (4x `maxTotalArchiveMemory`),
because listing members decompresses through their bodies and a member count
alone never stops a gzip bomb. 0 is not unlimited — it means the default of
1000.

Files over `maxContentScanFileSize` are reported as *skipped* rather than
content-scanned. Archive members over the per-file or total-memory budget are
skipped and acknowledged in the output.

### PDF extraction runs in worker processes

PDF text extraction does not happen inside `pc`/`pc-server`: the process starts
**up to `min(4, maxCores)` worker subprocesses**, each running the sandboxed
PDFium engine, and hands them one document at a time. They are the same binary
re-executed with the undocumented argv sentinel `__pc-pdf-worker`, so `ps` shows
entries like `pc-server __pc-pdf-worker` beside the main process. Running that
by hand does nothing useful: it compiles the wasm module on first use, writes a
binary greeting to your terminal and then waits for jobs on stdin (Ctrl-D to
exit).

Workers start lazily - a scan without PDFs starts none - and the first one to
start is on its own until it reports ready, so a cold compile (seconds; a warm
start is milliseconds) is paid once rather than four times in parallel. If it
fails, every caller queued behind it is told at once instead of repeating the
wait. A worker is retired 60-120 s after its last job (a 60 s threshold checked
every 60 s), whenever it fails, and after an outsized document - over 4 MiB read
or 16 MiB of extracted text, which the shipped `maxPDFFileSize` (1 MB) and
per-file text budget keep out of reach, so this only fires where those gates
have been raised. Each worker carries its own wasm runtime, so expect its
resident memory to track the largest document it has handled. The workers run
alongside pc's own pools, so a host can briefly see up to twice `maxCores` while
extraction is in flight - the same envelope as the secret scanner.

Workers do not outlive their parent: its exit closes their pipes and they end on
the EOF, on every platform and every orderly path. On Linux the kernel is
additionally asked to `SIGKILL` a worker whose parent is killed outright - a
`kill -9` or an OOM kill - so one wedged mid-document is not left behind; other
platforms have no such backstop.

Replacing the binary under a **running** process is the one case that needs
care: workers started after the replacement are the new binary, and if its wire
protocol differs they are refused rather than spoken to. PDF scanning then
reports "PDF engine unavailable" (a skip acknowledgement per file) until the
process is restarted; nothing is mis-scanned.

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

**Exit codes:** any error exits **1** - a config that does not load, a rule set
that does not compile, an unknown or missing collector, a location that cannot
be collected, and equally a result that cannot be rendered afterwards (TUI,
formatting or HTML); the JSON error envelope on stdout says which. Only a run
that completed exits **0**, findings or not.

### Result sections

`-json` writes the result document, `-html` renders it and the TUI browses it
(`-plain` prints a summary of the same findings). Beside `scanned`, `skipped`,
`pdf_files`, `warnings` and `errors`, the document carries four detail sections -
the same findings, indexed by four different questions:

| Section | Answers |
|---|---|
| `details_subject_focused` | what is wrong with this file? |
| `details_check_focused` | which files failed this check? |
| `details_rule_focused` | which configured rule reported this? |
| `details_metadata` | what is wrong with the package metadata? (CKAN only) |

`details_rule_focused` holds one entry per rule that found something - its total
and one line per affected subject, the message text staying in the two sections
above. A rule that found nothing does not appear, and neither does a finding that
no configured rule owns: a skip acknowledgement, a CKAN metadata finding, or a
finding of a synthesized `default:` rule. It counts **attributions, not
findings**: a finding that names several rules is counted under every one of
them, so for a check with declared rules its rule totals sum to at least that
check's finding count - and a check running on its synthesized `default:` rule
contributes no entry at all, because those findings carry no rule name.
`details_check_focused` stays the authoritative count. The section earns its
place when a check carries several rules.

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
| 403 | `client_not_allowed` | The connection's address is outside `allowedClients` (`POST /api/v1/analyze` only) |
| 404 | `package_not_found` | No such package (or private + unauthorized) |
| 404 | `not_found` | No such endpoint path |
| 405 | `method_not_allowed` | Endpoint exists but not for this HTTP method (with `Allow` header) |
| 422 | `malformed_resource` | A resource is malformed - missing both `url_type` and `url`, or an upload missing `name`/`url`/`size`. The message names the exact resource + package. |
| 429 | `rate_limited` | Hourly request budget exceeded (with `Retry-After`); responses served from the result cache are refunded and count against a budget `cachedRequestLimitFactor`× larger |
| 502 | `ckan_unavailable` | CKAN unreachable or unusable: transport failure, transport-level 5xx, CKAN throttling (429), oversized/truncated/malformed response (bad JSON or no `result` object), or no answer within `ckanRequestTimeoutSeconds` |
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
| `trustedProxies` | - | CIDRs allowed to set `X-Real-IP` (set this behind nginx!); empty trusts no peer. Any non-CIDR, host-bits or IPv4-mapped entry stops the boot |
| `allowedClients` | - | connection peer addresses (CIDRs) allowed to reach `POST /api/v1/analyze` - behind a proxy that is the proxy's own address; empty admits every client. Any non-CIDR entry stops the boot |
| `allowedOrigins` | - | CORS allow-list of exact origin URLs (your frontend) |
| `perIPRequestsPerHour` | 4 | per-client-IP hourly budget (0 = unlimited) |
| `globalRequestsPerHour` | 20 | all-clients hourly budget (0 = unlimited) |
| `cachedRequestLimitFactor` | 100 | cache hits are refunded and counted against budgets × this factor; 0 disables the refund |
| `burstFactor` | 0.5 | extra headroom applied to the budgets |
| `analysisBusyWaitSeconds` | 2 | wait for the single analysis slot before `service_busy` |
| `maxTrackedRateKeys` | 10000 | rate-limiter memory bound |
| `contactMessage` | … | contact suffix shown in error envelopes |
| `logClientIP` | `true` | record the client IP (`client_ip`, plus `real_ip` from `X-Real-IP`) in access logs |
| `requestTimeoutSeconds` | 300 | hard bound for a WHOLE analysis (CKAN call + checks) → `analysis_timeout` (504) |
| `ckanRequestTimeoutSeconds` | 10 | bound for the CKAN `package_show` call → `ckan_unavailable` (502); must be ≤ `requestTimeoutSeconds`; the cache's freshness probe uses this or 5s, whichever is smaller |
| `resultCacheDir` | - (disabled) | absolute path of the per-package result cache directory; its reserved `entries/` subdir is wiped at every start (failure aborts the boot); empty disables caching |
| `resultCacheMaxEntries` | 500 | max cached packages before oldest-entry eviction |
| `resultCacheMaxAgeHours` | 0 (no limit) | max age of a cache entry; 0 disables the age limit |

Only **one analysis runs at a time** (by design); a second request waits up to
`analysisBusyWaitSeconds` for the slot and then receives `service_busy` (503)
with a `Retry-After` header.

### Result cache

**Server-only** (the CLI never caches). When `resultCacheDir` is set, each
successful analysis is stored as one file per package, named after the package
id and the CKAN `metadata_modified` timestamp `package_show` carries. The file
holds the response body verbatim, so the filename alone decides freshness and a
hit costs a single read.
A repeat request for an unchanged package is served from the cache after a
single `package_search` probe for that timestamp - the full `package_show` and
the analysis are both skipped (response header `X-PC-Cache: hit`/`miss`).

The probe carries the request's token and searches with `include_private`, so
private packages are probed too, each under its own token's visibility: a caller
who may not read the package matches nothing and falls through to
`package_show` and its usual 404/403. Two consequences are accepted
deliberately, both bounded. An *invalid* token on a **public** cached package is
served from the cache - CKAN treats an unusable key as anonymous, and public
data is anonymous-readable anyway. And a package that has just been made private
keeps being served from the cache until the search index catches up: until it
does, a caller with no right to read the package can receive the analysis that
was stored while it was still readable. How long that lasts is the deployment's
Solr commit interval, not something the server can bound. Search-index lag is
the trade throughout: in that same window a just-edited package can be served
from the cache, or an unchanged one re-analysed in full.

Entries live in an `entries/` subdirectory that the server owns wholesale. The
server reserves that one name inside `resultCacheDir` (a file or symlink
sitting there aborts the boot instead of being deleted); everything else you
keep in the directory is never touched. **The subdirectory is deleted and
recreated on every server start**, so a changed `pc.toml` or a new binary can
never be served stale results: both require a restart, and the restart empties
the cache. If the clearing fails (read-only mount, wrong ownership after a
rebuild) **the server refuses to start** and prints an error naming the
directory - it never boots on entries it cannot prove it wrote. Run **one
server per `resultCacheDir`**: a second instance would wipe the first one's
entries at its boot.

*Upgrading from a version before the `entries/` layout:* entries were written
directly into `resultCacheDir` back then. Delete leftover `*.json` in that
directory once after upgrading - the server neither reads nor removes them.

Nothing in the directory is meant to survive a restart, so it needs no
persistent storage - only ownership by the server user and private permissions
(cached files can contain private-package findings). Every restart therefore
starts cold: the first request per package pays a full scan again. Within a run
an entry is reused until the package's `metadata_modified` changes in CKAN;
`resultCacheMaxAgeHours` can additionally age entries out, but is off by
default - CKAN's `metadata_modified` alone decides freshness.

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
