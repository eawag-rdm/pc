# Plan: check rules, filter unification, work-package rebuild

Status: DRAFT (2026-08-07), not yet reviewed. Written from a full read of the
current check-loading surface; every claim below carries a file:line so the
reviewers can verify rather than trust. Follows the house workflow: lean
design -> two-expert review -> operator approval -> implementation, one fix
per commit, every commit green on its own.

Companion to docs/plan-archive-limits-ooxml.md and
docs/plan-hardening-collector-pdf.md (the QUEUED note at the end of the
latter is this document).

## 1. What the operator asked for

1. Checks with **specific parameters for specific files**, selected by
   whitelist/blacklist. Today impossible - see §2.3.
2. **Whitelist/blacklist collision checks.**
3. An **improvement to how work packages are built**.

## 2. Current state (evidence)

### 2.1 Three filter mechanisms, three different meanings for one TOML list

The same `whitelist`/`blacklist` strings are interpreted three ways:

| | `utils.skipFileCheck` | iterator `fileGoodToUnpack` | `leakNameFilter` |
|---|---|---|---|
| where | check_utils.go:75-95 | archive_iterator.go:257-275 | leakcheck.go:68-99 |
| matching | **regex** (RE2, unanchored) | **literal substring** | **regex** (RE2, unanchored) |
| case | **sensitive** | **insensitive** | **sensitive** |
| subject | `file.Name` | full member path | `file.Name` |
| precedence | **whitelist wins** | **blacklist wins** | whitelist wins |
| bad regex | whitelist: skips EVERYTHING (fail-closed); blacklist: skips nothing | n/a | filter disabled (**fail-open**) |
| compiled | **per file x check, every call** (check_utils.go:63) | cached matcher | once per analysis (leakcheck.go:128) |

The collision is not theoretical: `IsArchiveFreeOfKeywords` passes
`[test.IsFreeOfKeywords]`'s lists to BOTH mechanisms - regex against the
archive's own name, literal substring against its members
(checks_by_file.go:260-263). A pattern like `".*\.log$"` filters the archive
correctly and silently fails to filter members.

### 2.2 Check identity is reflection-derived and fragile

`optimization.FunctionName` (worker_pool.go:92-96) takes the last
dot-segment of `runtime.FuncForPC(...).Name()`. Consequences:

- A closure or any wrapper yields `func1`, so `config.Tests["func1"]` misses
  and `skipFileCheck` returns false - **the check runs unfiltered**
  (check_utils.go:85). Already reachable: check_utils_ctx_test.go:61.
- Renaming a Go function silently orphans its `[test.X]` section. Nothing
  validates that a section name maps to a real check.
- One hard-coded alias exists because the model cannot express scope:
  `IsArchiveFreeOfKeywords` -> `IsFreeOfKeywords` (check_utils.go:78-82).
- `FunctionName` is called once per file x check (check_utils.go:118, :155,
  :183, :316, :410; worker_pool.go:106) - reflection on a hot path.
- A second, fully drifted name table exists in the TUI
  (output/tui/summary.go:361-374): of its keys only `IsFreeOfKeywords`
  matches a real check; the rest are dead.

### 2.3 One parameter set per check, one file set per check

`keywordArguments` is `[]map[string]interface{}` and every consumer loops
over all entries, scanning **all** admitted files with **every** entry
(checks_by_file.go:361, :388, :405, :527, :653). The whitelist/blacklist is
per section, shared by all entries. So "these keywords only for `data/*.csv`,
those only for `docs/`" is unexpressible - the operator's request #1.

Also unexpressible today: numeric or boolean per-check parameters -
`parseKeywordArguments` (config_parser.go:227-233) keeps only strings and
string lists and **silently drops** int64/bool/float64; per-path limits (all
limits are `[general]`); filters on repository checks
(`ApplyChecksFilteredByRepository`, check_utils.go:434-452, never calls
`skipFileCheck`, so lists under `[test.HasReadme]` are inert); and separate
config for a check's top-level vs in-archive scope.

### 2.4 Validation gaps

`ValidateChecksConfig` (validate_checks.go:19-110) checks keyword/name shapes
and, for `IsFreeOfSecrets` only, that patterns compile and `attrs` keys are
known. It does NOT check: that a `[test.X]` name is a real check; that
whitelist/blacklist patterns compile for any other check; `attrs` for any
other check. `assesLists` (config_parser.go:546-553) rejects both-lists-set
but only via `LoadConfig`, not `ParseConfig`.

Unchecked type assertions on config are the main panic surface
(checks_by_file.go:282-284 and five siblings; :690; :260-261). They are
contained by `SafeRunCheck` (worker_pool.go:129-149) - but `skipFileCheck`
and `filterChecksForFiles` run OUTSIDE that guard.

### 2.5 Work packages

A work item is `{file, []check}` (check_utils.go:171-174) - per file, all its
checks sequential in one worker (deliberate, worker_pool.go:98-99). Five
registry slices drive five phases in fixed order (check_utils.go:478-497).

Costs measured/observed:
- One `.zip` is opened **up to four times** per run: as a file by
  `IsFreeOfKeywords` (wastefully - sniffed binary, then keyword-looped over
  empty content, checks_by_file.go:398-414), by `ReadArchiveFileList`, by
  `IsArchiveFreeOfKeywords`'s iterator, and by leakcheck's independent
  iterator (acknowledged at leakcheck.go:211-215).
- The joined whitelist/blacklist regex is **recompiled per file x check**
  (check_utils.go:63-64); no cache, unlike `optimization.GetMatcher`.
- The TUI path silently runs phase 1 **single-threaded**
  (check_utils.go:541) while JSON/plain/server run it parallel - same
  workload, different concurrency, no stated reason.
- `WorkerPool` ignores the caller ctx and builds its own
  (worker_pool.go:45); cancellation is only observed between items.

## 3. Design

### 3.1 Rule instances (addresses request #1)

Introduce a **rule**: a named instance of a check with its own parameters and
its own file selector. The check stays a pure function; the rule is the
configured unit.

```toml
[[rule]]
name    = "credentials-everywhere"      # unique, operator-chosen
check   = "IsFreeOfKeywords"            # explicit, no reflection
include = ["\\.csv$", "\\.txt$"]        # selector (see 3.2)
  [rule.params]
  keywords = ["password", "api_key"]
  info     = "Possible credentials in file"

[[rule]]
name    = "internal-paths-in-docs"
check   = "IsFreeOfKeywords"
include = ["^docs/"]
  [rule.params]
  keywords = ["Q:", "/Users/"]
  info     = "Possible internal information"
```

Two rules, same check, different keywords, different files - the thing that
cannot be expressed today.

Consequences:
- `check` is an explicit string, so identity no longer comes from reflection.
  A **registry** (`map[string]CheckDef`) replaces the five slices; a rule
  naming an unknown check is a load error. `FunctionName` stays only for
  message labelling, or dies entirely.
- `params` is a typed decode per check (each `CheckDef` supplies a
  `ParseParams(toml) (any, error)`), so numeric/bool parameters work and
  wrong types fail at load instead of panicking at scan time (§2.4).
- Scope becomes explicit per rule (`scope = "file" | "archive-member" |
  "repository"`, default from the CheckDef), retiring the
  `IsArchiveFreeOfKeywords` alias hack.
- Per-rule limit overrides become possible later without new plumbing
  (e.g. a rule for `raw/` with a bigger `maxPDFPages`). NOT in this plan -
  listed as a follow-up so the shape stays compatible.

Backcompat: `[test.<CheckName>]` sections keep working, translated at load
into one rule each (`name = check`, params from `keywordArguments`, selector
from whitelist/blacklist). Deployed configs must not break. New `[[rule]]`
and legacy `[test.*]` for the SAME check is a load error, not a merge.

### 3.2 One selector, one semantics (addresses request #2, part 1)

A single `Selector` type used by every filter site - checks, archive members,
leakcheck:

- **Regex** (RE2), matched with `MatchString` (unanchored, as today for
  checks). Literal-substring users migrate: a literal `foo` becomes
  `regexp.QuoteMeta("foo")` at load, preserving intent.
- **Case-sensitive** by default, with an explicit per-rule
  `ignoreCase = true` (the archive path is case-insensitive today; the
  migration sets `ignoreCase` for translated legacy archive filters so
  behaviour is preserved).
- Matched against a **defined subject**: `path` (full path / member path)
  and `name` (basename) are both available; the rule says which. Today's
  split (`file.Name` for checks, member path for the iterator) is exactly the
  bug this removes.
- Patterns compiled **once at config load** into the Selector, never per
  call. Kills the per-file x check recompile (§2.5) - a pure win.
- `include` and `exclude` may both be present. Defined precedence:
  **exclude wins** (deny beats allow). This changes `skipFileCheck`'s current
  whitelist-wins, but today both-set is a load error anyway
  (`assesLists`), so no deployed config can depend on it.
- A pattern that fails to compile is a **load error** - no more fail-closed
  vs fail-open divergence (§2.1).

Each pattern compiles individually (no `strings.Join(list,"|")`), removing
today's failure mode where one bad entry corrupts the whole expression and an
empty-string entry matches everything.

### 3.3 Collision checks (request #2, part 2)

At config load, after selectors compile:

1. **Contradiction**: a rule whose `include` and `exclude` provably overlap
   such that nothing can match. Full regex intersection is undecidable in
   general; implement the cheap, honest subset - identical pattern in both
   lists, or an `exclude` that is `.*`/`^` (matches everything) - and report
   the rest as a warning only when a rule matches zero collected files at
   run time (see 4, C6).
2. **Duplicate rule names** - hard error.
3. **Shadowing**: two rules of the same check whose selectors overlap on at
   least one collected file, with conflicting params. Report as a warning
   with the offending file, since overlapping rules are legitimate (a file
   may need two keyword groups) - the operator needs to see it, not be
   blocked by it.
4. **Dead rule**: selector matches no collected file in this run - warning,
   emitted once per run, naming the rule. Catches the typo class that today
   silently disables filtering.
5. **Unknown check name / unknown params key** - hard error (closes §2.4).

Reporting: collisions surface as normal `structs.Message` warnings so they
appear in JSON/TUI/HTML, plus a startup error for the hard cases.

### 3.4 Work packages (request #3)

Two changes, both perf-positive:

**(a) Open each archive once.** Today up to four opens per archive (§2.5).
Build a per-archive work package that runs the name-list checks, the keyword
scan and (when enabled) the leak extraction off a **single** iterator pass.
This also fixes the two-independent-budgets problem leakcheck.go:211-215
documents, and halves container extraction (pdf/xlsx/docx) when the secret
scan is on. Requires the iterator to feed multiple consumers - the cleanest
form is a callback/visitor per member rather than the current
buffer-then-promote pull API used twice.

**(b) Work item = {file, []rule}, built from the registry.** Same per-file
granularity (keep it - concurrent reads of one file are the thing it
avoids), but selection happens once against compiled selectors instead of
reflection + regex compile per file x check.

Also in scope, small:
- Drop the wasteful `IsFreeOfKeywords`-on-an-archive path
  (checks_by_file.go:398-414).
- Make the TUI path use the same parallel phase 1 as every other path, or
  document why not (check_utils.go:541).
- Pass the caller ctx into `WorkerPool` instead of `context.Background()`
  (worker_pool.go:45).

NOT in scope: making checks context-aware (signature change, own plan), the
fast/slow check split (parked), per-rule limit overrides (§3.1).

## 4. Commit series

Each commit builds and tests green alone. Order chosen so behaviour-preserving
refactors land before anything user-visible.

- **R1 - refactor(config): compile selectors once.** Introduce `Selector`
  (compile at load, per-pattern, defined subject), use it in `skipFileCheck`
  only. Pure win: removes the per-call recompile. Behaviour preserved except
  bad patterns now fail at load. Tests: existing `TestSkipFileCheck` and
  `TestMatchPatterns` must stay green, plus load-error cases.
- **R2 - refactor(readers): iterator takes a Selector.** Replace
  `fileGoodToUnpack`'s literal matcher; translated legacy lists get
  `ignoreCase` + quoted literals so `TestFiltersDuringArchiveIteration`
  (archive_iterator_test.go:182, 7 cases x 3 formats) stays green unchanged.
  This is the commit that ends the three-semantics split.
- **R3 - refactor(checks): leakcheck uses the shared Selector.** Deletes
  `leakNameFilter`; fail-open divergence goes with it.
- **R4 - feat(config): check registry.** `map[string]CheckDef` with explicit
  names, scopes and `ParseParams`. Registry replaces the five slices;
  `FunctionName` no longer drives config lookup. Kills the
  `IsArchiveFreeOfKeywords` alias. Unknown `[test.X]` becomes a load error.
  Fix or delete the drifted TUI name table (summary.go:361-374) here.
- **R5 - feat(config): `[[rule]]` instances + legacy translation.** The
  operator-visible feature. Typed params; two rules of one check with
  different selectors; `[test.*]` translated at load; mixing both for one
  check is an error.
- **R6 - feat(config): collision checks.** §3.3, hard errors + warnings.
- **R7 - perf(utils): one iterator pass per archive.** §3.4(a). Benchmark
  before/after in the commit message (expect ~2x on archive-heavy packages
  with the secret scan on, less without).
- **R8 - refactor(utils): rule-based work items + ctx plumbing.** §3.4(b),
  the TUI parallelism inconsistency, drop the archive-as-file keyword path.

## 5. Risks

- **R2 is the sharp edge.** Archive filtering changes from case-insensitive
  literal to case-sensitive regex under the hood. The translation must be
  exact or operators silently lose (or gain) member coverage. Mitigation: the
  existing 21-case table test is the gate; do not modify it in R2.
- **Config churn.** Legacy translation must be lossless for every shipped
  config; `TestParseShippedConfigsLoad` (config_parser_test.go:913) plus
  pc.toml.example and testdata/test_config.toml are the fixtures.
- **R7 touches the iterator's consumer contract**, which the whole
  archive-limits series was built on. It needs its own design pass and
  probably its own review round.
- Message `TestName` currently comes from reflection; with rules it should
  become the **rule name**. That changes user-visible output and any
  downstream consumer keyed on test names - call it out for the operator
  before R5.

## 6. Open questions for the operator

1. Should `TestName` in output become the rule name (better with multiple
   rules per check) or stay the check name (stable for consumers)?
2. Is a deprecation path wanted for `[test.*]`, or does it stay supported
   indefinitely as sugar for a single rule?
3. Should per-rule limit overrides (`maxPDFPages` for one selector) be in
   scope now, or stay a follow-up as this plan assumes?
