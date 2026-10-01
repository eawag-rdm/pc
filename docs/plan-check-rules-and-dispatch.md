# Plan: check rules, filter unification, work-package rebuild

Status: APPROVED 2026-08-11 (operator; §6 decided the same day) — IMPLEMENTATION
IN PROGRESS. Committed: R1 bc3cb36, R2 13b548e, R3 5c2b456, R4 c1687ff,
R5 f9121a0, R6 b53bd7e (Phase A complete), R7 027c592. Each commit went
through an implement -> blind-review (engineer/architect/+performance on
perf-gated) -> fix -> verify cycle; review-driven revisions are recorded
inline as CORRECTION/REVISED blocks in §3.2 and §4. Next: R8. Rev 4 folds in
every residual of the rev-3 approval round (architect + performance, both
NEEDS-REVISION). Rev 2 (2026-08-07) went through a three-expert round - Go
engineer, architect, performance - all three returned NEEDS-REVISION; rev 3
re-baselined every claim on the post-F1-F8 code (entry point is now
`internal/analysis.Run`;
`ReadArchiveFileList(file, maxMembers, maxTotalMemory) (list, truncated, err)`;
saturating walk budgets; wipe-on-boot result cache) and reshaped the design
around the four converged blockers rather than caveating them. Every claim
carries a file:line so reviewers verify rather than trust. House workflow: lean
design -> expert review -> operator approval -> implementation, one fix per
commit, every commit green on its own.

Companion to docs/plan-archive-limits-ooxml.md and
docs/plan-hardening-collector-pdf.md (the QUEUED note at the end of the latter
is this document). This plan now also owns two items parked elsewhere: F4
(docs/to-fix.md:75-90) and ctx-threading (docs/to-fix.md:6,
docs/plan-fix-high-med.md:21-22) - see §3.5.

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
| where | check_utils.go:75-95 | archive_iterator.go:257-275 | leakcheck.go:62-99 |
| matching | **regex** (RE2, unanchored) | **literal substring** | **regex** (RE2, unanchored) |
| case | **sensitive** | **insensitive** | **sensitive** |
| subject | `file.Name` (basename) | full member path | `file.Name` |
| precedence | **whitelist wins** | **blacklist wins** | whitelist wins |
| bad regex | whitelist: skips EVERYTHING (fail-closed); blacklist: skips nothing | n/a | filter disabled (**fail-open**, leakcheck.go:77-78) |
| compiled | **per file x check, every call** (check_utils.go:63-64) | cached matcher (matcher.go:215-241) | once per analysis (leakcheck.go:128) |

The collision is not theoretical, and it hits the same list twice inside one
check: `IsArchiveFreeOfKeywords` is filtered by `skipFileCheck` as a regex over
the archive's own basename, then passes `[test.IsFreeOfKeywords]`'s lists to the
iterator as literal substrings over member paths
(checks_by_file.go:260-263). `IsFreeOfSecrets` has the identical split -
regex over `f.Name` at leakcheck.go:131-132, literal substring over members at
leakcheck.go:221. A pattern like `".*\.log$"` filters the container correctly and
silently fails to filter members.

**Consequence for this plan:** no translation can be lossless for both readings
of one list. One semantics must be chosen and the difference declared
user-visible (§3.2, R5).

### 2.2 Check identity is reflection-derived and fragile

`optimization.FunctionName` (worker_pool.go:90-96) takes the last dot-segment of
`runtime.FuncForPC(...).Name()`. Consequences:

- A closure or any wrapper yields `func1`, so `config.Tests["func1"]` misses and
  `skipFileCheck` returns false - **the check runs unfiltered**
  (check_utils.go:84-86). Already reachable: check_utils_ctx_test.go:61.
- Renaming a Go function silently orphans its `[test.X]` section. Nothing
  validates that a section name maps to a real check.
- One hard-coded alias exists because the model cannot express scope:
  `IsArchiveFreeOfKeywords` -> `IsFreeOfKeywords` (check_utils.go:78-82).
- `FunctionName` runs once per file x check: check_utils.go:76, :118, :155,
  :348, :442, :473 and worker_pool.go:106 - reflection on a hot path.
- A second, fully drifted name table exists in the TUI
  (output/tui/summary.go:359-382, used at :132): of its eleven keys only
  `IsFreeOfKeywords` matches a real check; the rest are dead.

Three message `TestName`s are **not** check names and must stay fixed strings:
`ArchiveFileList` (check_utils.go:316), `FilesPresent` (check_utils.go:500),
`Readability` (server/handlers.go:546).

### 2.3 One parameter set per check, one file set per check

`keywordArguments` is `[]map[string]interface{}` (config_parser.go:11-19) and
every consumer loops over all entries, scanning **all** admitted files with
**every** entry (checks_by_file.go:282, :361, :388, :405, :527, :653). The
whitelist/blacklist is per section, shared by all entries. So "these keywords
only for `data/*.csv`, those only for `docs/`" is unexpressible - request #1.

Checks look their own section up by hardcoded name
(`config.Tests["IsFreeOfKeywords"]`, checks_by_file.go:260-261, :282, :361,
:388, :405, :527, :653; `config.Tests["IsValidName"]`, :690;
`config.Tests["HasReadme"]`, checks_by_respository.go:25). The signature
`func(structs.File, config.Config) []structs.Message` carries no rule identity,
so two rules of one check are unreachable without changing it.

Also unexpressible today: numeric or boolean per-check parameters -
`parseKeywordArguments` (config_parser.go:221-238) keeps only strings and string
lists and **silently drops** int64/bool/float64 at :227-231; per-path limits (all
limits are `[general]`); filters on repository checks
(`ApplyChecksFilteredByRepository`, check_utils.go:466-484, never calls
`skipFileCheck`, so lists under `[test.HasReadme]` are inert); separate config
for a check's top-level vs in-archive scope.

**And path-prefix selection of any kind.** No `structs.File` field carries a
repo-relative path: `Path` is collector-raw - the walk path rooted at the
scanned directory for local (local_collector.go:125, :140), the opaque FileStore
blob `.../resources/f46/e74/...` for CKAN (ckan_collector.go:536) - and `Name` is
a basename or a flat CKAN resource name (file.go:13-21, :40-42). So
`include = ["^docs/"]`, operator request #1's own example, selects nothing
against either field. §3.2's `subject = "path"` therefore needs a new field (R1).

### 2.4 Validation gaps

`ValidateChecksConfig` (validate_checks.go:19-110) checks keyword/name shapes
and, for `IsFreeOfSecrets` only, that patterns compile (:50-55) and `attrs` keys
are known. It does NOT check: that a `[test.X]` name is a real check; that
whitelist/blacklist patterns compile for any other check; `attrs` for any other
check. `assesLists` (config_parser.go:544-553) rejects both-lists-set but only
via `LoadConfig` (:555-570), not `ParseConfig` - and
`TestParseShippedConfigsLoad` (config_parser_test.go:911-924) calls
`ParseConfig`, so the shipped configs are never checked against it.

Unchecked type assertions on config are the main panic surface
(checks_by_file.go:283-284 and five siblings; :691; :260-261). They are
contained by `SafeRunCheck` (worker_pool.go:144-149) - but `skipFileCheck` and
`filterChecksForFiles` run OUTSIDE that guard.

### 2.5 Work packages

A work item is `{file, []check}` (check_utils.go:171-174, worker_pool.go:28-32) -
per file, all its checks sequential in one worker (deliberate,
worker_pool.go:98-99). Five registry slices (check_utils.go:20-27, :28-31,
:33-35, :40-42, :56-60) drive five phases in fixed order
(`ApplyAllChecks`, check_utils.go:513-532).

Costs measured/observed:
- One `.zip` is opened up to four times per run: as a file by `IsFreeOfKeywords`
  (wastefully - sniffed binary, then keyword-looped over empty content,
  checks_by_file.go:398-414), by `ReadArchiveFileList` (archives.go:199-214), by
  `IsArchiveFreeOfKeywords`'s iterator (checks_by_file.go:263), and by
  leakcheck's independent iterator (leakcheck.go:221; the two-budget cost is
  acknowledged at leakcheck.go:211-215). With the secret scan disabled - the
  shipped default - production reality is **two** opens, not four.
- The joined whitelist/blacklist regex is **recompiled per file x check**
  (check_utils.go:63-64); no cache, unlike `optimization.GetMatcher`.
  Measured: 8456 ns and 33 allocs per file x check.
- `optimization.GetMatcher` (matcher.go:215-241) costs ~106 ns plus an
  allocation and a global `RLock` on **every** keyword scan of every file and
  every archive member.
- The TUI path silently runs phase 1 **single-threaded**
  (check_utils.go:534-539 documents it; :582 -> :133-167) while JSON/plain/server
  run it parallel - same workload, different concurrency.
- `WorkerPool` ignores the caller ctx and builds its own (worker_pool.go:45);
  cancellation is only observed between items.
- Worker sizing uses `runtime.NumCPU()` (check_utils.go:250, :365, :459), which
  oversubscribes CPU-quota'd containers; Go 1.25's `GOMAXPROCS` is cgroup-aware.

### 2.6 What the post-F1-F8 baseline already fixed (do not re-litigate)

- `internal/analysis.Run` (analysis.go:36-42) is the single entry point for both
  frontends; its doc (:6-7, :31-35) names THIS rework as the owner of
  value-returned results, the `ProgressCallback` text leak, and
  `checksAcrossFiles`.
- `ReadArchiveFileList(file, maxMembers, maxTotalMemory)` returns
  `(list, truncated, err)` (archives.go:199-214); truncation is acknowledged by
  `archiveWalkSkipMessage` (check_utils.go:311-320) and the partial list is
  discarded (check_utils.go:331-334).
- The name walk and the content path count **different universes** on purpose:
  the walk counts all entries and discards on truncation (archives.go:49-52),
  the content path counts unpack candidates and keeps already-scanned members
  (archive_iterator.go:343-350). Any single-pass fusion must respect this (§3.6).
- Walk budgets saturate instead of wrapping (`walkByteBudget`,
  archives.go:19-31).
- The server result cache is wiped on boot (server.go:93-99, cache.go:29-35), so
  a compiled plan needs no cache-fingerprint contribution.
- Server analysis concurrency is pinned to 1 **because** results flow through
  process globals (handlers.go:124-126) - the thing §3.5 removes.

## 3. Design

### 3.1 Rules, registry, bound runners (addresses request #1)

A **rule** is a named instance of a check with its own parameters and its own
file selector. The check stays a pure function of (file, bound rules); the rule
is the configured unit.

```toml
[[rule]]
name    = "credentials-everywhere"      # unique, operator-chosen
check   = "IsFreeOfKeywords"            # explicit, no reflection
scope   = ["file", "archive-member"]    # default from the CheckDef
subject = "name"                        # "name" (basename) | "path"
include = ["\\.csv$", "\\.txt$"]        # selector (see 3.2)
  [rule.params]
  keywords = ["password", "api_key"]
  info     = "Possible credentials in file"

[[rule]]
name    = "internal-paths-in-docs"
check   = "IsFreeOfKeywords"
subject = "path"                        # ^docs/ is a PATH prefix, not a basename
include = ["^docs/"]
  [rule.params]
  keywords = ["Q:", "/Users/"]
  info     = "Possible internal information"
```

Two rules, same check, different keywords, different files - the thing that
cannot be expressed today.

**Four layers, three packages, no cycle.** `pkg/checks` imports `pkg/config`
today, so a config type referencing `CheckDef` would be a cycle:

| layer | package | holds |
|---|---|---|
| declarative | `pkg/config` | `RuleSpec{Name, Check, Scope, Subject, Enabled, IgnoreCase, Include, Exclude, Params map[string]any}` - strings and raw TOML values only, no check knowledge |
| registry | `pkg/checks` | `Registry` = `map[string]CheckDef`, returned by a **constructor** `checks.NewRegistry()`, never an `init()`-populated global |
| compile | `pkg/utils` | `Compile(cfg *config.Config, reg checks.Registry) (*Plan, error)` - the only package that already imports both |
| engine | `pkg/utils` | the work item `{file, []*BoundRule}`, `WorkerPool`, `SafeRun`/`SafeRunCheck` - **moved here from `pkg/optimization`** |

`Selector` lives in a new **leaf** package `pkg/selector` (no internal imports),
so config, readers and checks may all import it. This preserves the value-type
convention `pkg/readers` established with `ArchiveLimits`
(checks_by_file.go:418-429): readers keeps importing no config.

`pkg/optimization` cannot host the new work item. `WorkItem` holds
`[]func(structs.File, config.Config)` plus a `Config` (worker_pool.go:28-32);
`{file, []*BoundRule}` would force `optimization` -> `checks`, while
`checks` -> `optimization` already exists (checks_by_file.go:15) - a cycle. So
the pool, `SafeRun` and `SafeRunCheck` move to `pkg/utils` in the same commit
that rewrites the work item (R7); their only callers are already there
(check_utils.go:119, :156, :209, :222, :290, :349, :443, :474).
`pkg/optimization` keeps `FastMatcher`/`GetMatcher`, which `checks` and
`readers` both import.

```go
// pkg/checks
type CheckDef struct {
    Name   string
    Scopes ScopeSet // which of the four dispatch phases this check may serve
    // Bind decodes and type-checks params once, at load. Called on the ZERO
    // RuleSpec it yields the check's default params - the defaults live in
    // Bind itself, no separate field; default-rule synthesis (migration
    // decision 1) relies on exactly this.
    Bind   func(config.RuleSpec, readers.ArchiveLimits) (*BoundRule, error)
    // Acquisition + batching. Exactly one is set, per Scopes:
    RunFile       func(f structs.File, rules []*BoundRule) []structs.Message
    RunRepository func(r structs.Repository, rules []*BoundRule) []structs.Message
}

// BoundRule is produced once, at load. Its runner is a CLOSURE built by Bind
// over this rule's concrete typed params - not a sibling field over `any` - so
// nothing is type-asserted after load.
type BoundRule struct {
    Rule string            // rule name: diagnostics, and §6.1's Rule field
    Sel  selector.Selector // compiled once, subject-aware
    // Exactly one is set, matching the def:
    Apply     func(f structs.File, body [][]byte) []structs.Message
    ApplyRepo func(r structs.Repository) []structs.Message
}

// pkg/utils
type Plan struct { /* per scope: []*BoundRule, immutable after Compile */ }
```

**Why the contract is split.** `HasReadme`, `ReadMeContainsTOC` and
`IsFreeOfSecrets` are `func(structs.Repository, config.Config)`
(checks_by_respository.go:46, :57; leakcheck.go:114) - a single `Run(file, ...)`
cannot express them. `RunFile`/`RunRepository` mirror the two dispatch families
that already exist (`ApplyChecksFilteredByFile`, check_utils.go:97;
`ApplyChecksFilteredByRepository`, :466). No check spans both, so exactly one is
set per def and exactly one `Apply*` per rule; the Plan's per-scope slices never
mix the two and the dispatcher never inspects which is set.

**Why closures.** The check acquires content once per (file, check) and hands it
to each of its bound rules; the rule's closure carries its own keywords, its own
`*optimization.FastMatcher`, its own `info` string, its own disallowed-name set.
Archive members reuse `Apply` with the synthetic member `structs.File` and the
member body the iterator already yields. `[]*BoundRule` travels straight through
- Plan -> work item -> `RunFile` - so no per-(file, check) value slice is ever
materialised, and no call site type-asserts anything.

Consequences:

- **Params are decoded and type-checked once, at load** (`Bind`). Every
  `cfg.Tests["X"]` lookup inside `pkg/checks` becomes a bound field: the six
  `argumentSet["keywords"].([]string)` sites (checks_by_file.go:283, :362, :389,
  :406, :528, :654), `disallowed_names` (:691) and `readmeNames`
  (checks_by_respository.go:17-31) all disappear. That closes the §2.4 panic
  surface at its source rather than containing it, and makes numeric/boolean
  parameters possible (§2.3).
- **`Bind` also binds the hot-path objects**: a keyword rule's closure holds the
  `*optimization.FastMatcher` built at load, removing the per-file and
  per-member `GetMatcher` lookup (106 ns + alloc + global RLock,
  matcher.go:215-241). One matcher **per parameter set**, not per rule: keyword
  lists are never merged into one automaton (each match must stay attributable
  to its own `info` string), and since one section becomes one rule carrying N
  parameter sets (migration decision 2 below), the automaton count is identical
  to today's. `Bind`'s limits parameter is the readers-owned value type
  `readers.ArchiveLimits`, built as today from
  `config.GeneralConfig.ArchiveLimits()` plus the `EffectiveMaxPDF*` accessors
  (checks_by_file.go:420-429) - no third limits type enters the design.
- **`check` is an explicit string**, so identity never comes from reflection.
  `FunctionName` (worker_pool.go:90-96) is deleted; there is no string -> check
  resolution after load. The `IsArchiveFreeOfKeywords` alias
  (check_utils.go:78-82) dies with it, as does the drifted TUI table
  (summary.go:359-382).
- **Scope is a set, not a scalar.** Four dispatch phases exist and three of them
  are not interchangeable: `HasOnlyASCII`/`HasNoWhiteSpace`/`IsValidName` run
  BOTH as file checks (check_utils.go:20-27) and over archive file lists
  (:56-60) off one `[test.X]` section, while archive *content* is a third phase
  (:33-35) and repository a fourth (:28-31, :40-42). `Scopes` is
  `{file, archive-file-list, archive-member, repository}`; a rule may name a
  subset, defaulting to the CheckDef's. A rule naming a scope its check does not
  support is a load error.
- **Repository-scope selectors are defined**, closing the inert-list gap
  (check_utils.go:466-484): a repository rule's selector **filters the file set
  handed to the check**, i.e. `Repository.Files` is narrowed before
  `RunRepository`. An **empty selector skips the copy** and passes the caller's
  slice unchanged - which is every shipped config's case today, since default
  rules carry no selector. Narrowing does not gate the rule; a rule whose
  selector admits nothing still runs and can report "no readme" over an empty
  set. `ReadMeContainsTOC` keeps reading
  `HasReadme`'s `readme_names` - "what counts as the readme" stays
  single-sourced (checks_by_respository.go:17-31), expressed as one shared bound
  param, not two config keys.
- **`enabled = true|false` per rule.** The secret scan's phase gate
  (`secretScanEnabled`, check_utils.go:44-54) becomes a plain rule flag instead
  of an attrs peek.
- **The Plan is immutable and shared, and its carrier is named.** `Compile` runs
  once at startup - in `main.go` after `LoadConfig`, and in `server.New` next to
  `pcConfig` (server.go:33-53) - hard-failing on error. It is then carried
  explicitly, not globally: `Server`/`Handler` gain a `plan *utils.Plan` field,
  and `analysis.Run` gains a `plan *utils.Plan` parameter (analysis.go:36-42)
  which it passes into the dispatch functions. It is NEVER copied per request:
  it does not live in `config.Config`, which the server shallow-copies per
  request (`deepCopyConfigForRequest`, handlers.go:700-704). R7 is the commit
  that does this.
- Per-rule limit overrides become possible later without new plumbing (e.g. a
  rule for `raw/` with a bigger `maxPDFPages`). NOT in this plan - but `Bind`
  resolves *effective* limits into the bound rule (§6.3), so the follow-up is
  config-only.

**Invocation unit is (file, check), with that check's matching rules batched.**
This is not an optimisation, it is a correctness constraint: per-rule invocation
re-acquires content. Measured on the current code, per-rule re-acquisition costs
2.0x on text files and 2.56x on archives, a PDF is re-extracted per rule
(11-97 ms each), and an archive's member-count and memory budgets silently
become R x per archive. So:

```go
RunFile(file, rules []*BoundRule)  // acquisition once; RunFile loops the
                                   // rules, each rule's closure loops its OWN
                                   // parameter sets (one matcher per set) -
                                   // the loops nest, per migration decision 2
```

The existing loops at checks_by_file.go:282, :361, :388, :405, :527, :653
already have exactly this shape - they iterate parameter sets over
once-acquired, once-lowered content. They become rule loops. Acceptance
criterion: **invocation count per (file, check) is independent of rule count**;
archive member and memory budgets are charged once per archive pass regardless
of how many member-scope rules exist.

**Migration - three explicit decisions** (see §5 for the risk):

1. **Default unfiltered rule - permanent registry semantics, not translation
   sugar.** `Compile` synthesizes, for every registered check named by no rule, a
   rule with an empty selector (matches everything) and the params produced by
   `Bind` on the zero `RuleSpec` - the check's defaults live in its own `Bind`,
   so the synthesized rule has a real params carrier.
   `TestCompileSynthesizesDefaultRules` (R7) asserts the bound params, not just
   the rule's existence. This is a property of the registry: it holds for
   `[[rule]]` configs just as much, and it survives the deletion of the
   `[test.*]` translation (§6.2). Without it, one-rule-per-`[test.X]`-section
   translation would silently delete every check with no section: today that is
   `ReadMeContainsTOC` in pc.toml:121-172 and pc.toml.example:125-177, plus
   `HasNoWhiteSpace`, `HasFileNameSpecialChars` and `IsFileNameTooLong` in
   testdata/test_config.toml:79-110. (`IsArchiveFreeOfKeywords` is NOT in this
   list: it is not a registered check in the new model - archive content
   scanning is the `archive-member` scope of `IsFreeOfKeywords`, bound from the
   same rule and params that the alias at check_utils.go:78-82 borrows today.
   The executed-check multiset guard is keyed by (check, scope), so those
   invocations appear - and stay visible - as `IsFreeOfKeywords` in the
   `archive-member` scope.)
2. **One section -> one rule.** A `[test.X]` section with N `keywordArguments`
   entries becomes ONE rule carrying N parameter sets (which the batched `Run`
   then loops over). Never N rules, never N invocations.
3. **One chosen selector semantics** (§3.2) - regex, case-sensitive by default,
   declared as a user-visible change with a release note. The rev-2 claim that
   both translations could be lossless was wrong (§2.1).

### 3.2 One selector, one semantics (addresses request #2, part 1)

`pkg/selector.Selector`, used by every filter site - checks, archive members,
leakcheck:

- **Regex** (RE2), matched with `MatchString` (unanchored, as today for checks).
  Literal-substring users migrate: a literal `foo` becomes
  `regexp.QuoteMeta("foo")` at load, preserving intent.
- **Case-sensitive** by default, with an explicit per-rule `ignoreCase = true`.
  The archive path is case-insensitive today, so its translated legacy rules get
  `ignoreCase`.
- **Literal fast path, mandatory.** Naive `(?i)`-regex matching of member names
  is 5.4x slower per member than today's literal matcher (332 us -> 1790 us per
  1000 members, against a 661 us walk total: ~3.2x on the whole walk). At compile
  time `Selector` calls `re.LiteralPrefix()`; a **complete** literal is matched
  by substring scan over a lowered scratch buffer (measured 117 ns/member,
  0 allocs - faster than today). CORRECTION (R2, verified empirically):
  `LiteralPrefix()` alone is unsafe as the completeness test - `^foo$` reports
  `("foo", true)`, silently dropping anchors. The implementation gates it on a
  `regexp/syntax` parse requiring `Op == OpLiteral && Flags&FoldCase == 0`;
  `LiteralPrefix` only supplies the literal text. R7's union pass must use the
  same guard (via `Selector.LiteralIncludes()`), never raw `LiteralPrefix`.
  The ignoreCase fold fast path is ASCII-scoped: RE2 `(?i)` does Unicode simple
  folding that byte-lowering cannot reproduce, so non-ASCII subjects fall back
  to the `(?i)`-compiled engine. The regex engine runs only for genuinely
  non-literal patterns. **The scratch belongs to the caller** - the iterator or
  the selection pass - which lowers each subject once per member and hands the
  lowered bytes to every selector that wants them. `Selector` itself stays
  stateless: the Plan is shared across workers, so a selector-owned buffer would
  be a data race.
- **Union admission for member scope, specified.** `Compile` builds it only when
  **two or more** member-scope rules exist; with one - today's shipped reality
  for every config - it is skipped entirely and the single selector runs
  directly. When it is built it is never an OR-loop over per-rule selectors: the
  literal patterns of all member-scope rules become **one multi-pattern
  `FastMatcher` pass**, and only the genuinely non-literal remainder is tried
  after it. Per-rule selectors then run for admitted members only.
  CORRECTION (R2 review): a union skip is sound only for selectors whose
  `UnionLiterals()` reports ok - at least one include pattern, none in regex
  mode, and **not** `ignoreCase`. `FastMatcher` folds with `ToLower`, which is
  strictly narrower than RE2 `(?i)` simple folding (U+017F vs `s`), so an
  ignoreCase selector's literals must never gate a skip - it would silently
  under-admit. R7's union pass consumes `UnionLiterals()` only; a later
  widening (e.g. ASCII-only subjects) needs its own proof.
- Matched against a **declared subject**, defined per scope. File scope:
  `name` = the basename (today's `file.Name` for collected files), `path` =
  **`RelPath`**, a new repo-relative field on `structs.File` (R1), because
  neither existing field can serve (§2.3). Archive scopes (file-list and
  member): the synthetic member `File` carries the FULL member path in `Name`
  (archives.go:57, :94, :143, :168), so there `path` = that member path -
  member construction sites set `RelPath` to it explicitly - and `name` = its
  basename, taken with `path.Base` at match time (a slice, no alloc, no new
  field). Today's split - `file.Name` for checks, member path for the
  iterator - is exactly the bug this per-scope definition removes. `RelPath`
  is set by the two components that know a root: `LocalCollector`
  (slash-separated, rooted at the scanned path) and `CkanCollector` (the
  resource name - CKAN's namespace is flat, so `^docs/` honestly never matches
  a CKAN resource, which R17 documents rather than papers over).
  `ToFile`/`ToFileWithDisplay` default it to `Name`, so existing construction
  sites and fixtures keep working. Request #1's `include = ["^docs/"]`
  therefore selects something only under `subject = "path"`, and only once R1
  has landed.
- Patterns compiled **once at load** into the Selector, never per call. Kills the
  per-file x check recompile (§2.5) - a pure win.
- `include` and `exclude` may both be present. Defined precedence:
  **exclude wins** (deny beats allow). This inverts `skipFileCheck`'s current
  whitelist-wins, but both-set is a load error today (`assesLists`,
  config_parser.go:544-553), so no deployed config can depend on it.
- **Per-pattern compilation is an invariant.** Never `strings.Join(list, "|")`
  (check_utils.go:63, leakcheck.go:75): the join is 18x slower than per-pattern
  matching, one bad entry corrupts the whole expression, and an empty-string
  entry matches everything.
- **An empty pattern string is a load error.** Today its meaning inverts between
  sites (`matchLiteralPatterns` returns true for an empty subject,
  archive_iterator.go:257-260; an empty regex matches everything).
- A pattern that fails to compile is a **load error** - no more fail-closed vs
  fail-open divergence (§2.1). Errors are **aggregated and rule-named**: all bad
  patterns in one message, not the first.

### 3.3 Collision checks (request #2, part 2)

Two distinct kinds, two different homes and two different audiences.

**Load-time, pure, hard failure** (no file set needed) - in `Compile`:

1. **Duplicate rule names.**
2. **Unknown check name**, unknown/wrong-typed params key, unsupported scope,
   invalid or empty pattern.
3. **Contradiction**: `include` and `exclude` provably cannot both be satisfied.
   Full regex intersection is undecidable; implement the honest subset -
   identical pattern in both lists, or an `exclude` of `.*` / `^` - and leave the
   rest to the run-scoped dead-rule report.

**Run-scoped diagnostics** (need the collected file set):

4. **Dead rule**: selector matched no collected file this run - the typo class
   that today silently disables filtering.
5. **Shadowing**: two rules of one check whose selectors overlap on at least one
   file with conflicting params. Legitimate in general (a file may need two
   keyword groups), so it informs, never blocks.

These add **no extra selector evaluations**: they are fused into the selection
pass that already runs (`filterChecksForFiles`, check_utils.go:176-192) - a
per-rule matched flag yields (4); a per-file matched-rule list yields (5) over
the small k of rules per check. The pair loop tests **membership in the
reported-pair set before computing anything**, so a pair already recorded costs
one lookup; with that set small and capped, the per-file cost decays from
O(F x k^2) to O(F x k).

**Member scope is counted worker-locally.** Member-scope rules are evaluated
inside the archive iterator's parallel workers, so their matched flags are
worker-local `bool`s **OR-folded at the join** - never a shared per-member
atomic, which would put a contended write on the hottest loop in the program.

**Audience:** config-authoring diagnostics go to the **operator channel** (CLI
stderr / server log), NOT into `structs.Message`. The server response is for the
depositor, who cannot act on a config typo and must not see internals - the same
audience split handlers.go:463-471 already enforces for scan diagnostics.

### 3.4 Work packages and dispatch (request #3)

- **Work item = {file, []*BoundRule}**, built once from the Plan against
  compiled selectors instead of reflection + regex compile per file x check.
  Rules are held by pointer into the immutable Plan, slices preallocated; an
  interned per-file rule bitmask is available later if profiling asks for it.
  The item, `WorkerPool` and `SafeRun`/`SafeRunCheck` move to `pkg/utils` in that
  same commit - `pkg/optimization` cannot hold a `[]*BoundRule` without a cycle
  (§3.1).
- **Per-file granularity stays** - all of a file's checks in one worker is what
  avoids concurrent reads of one file (worker_pool.go:98-99).
- **`ctx` becomes part of the check signature** (§3.5) so cancellation is
  observed inside long checks, not only between items; `WorkerPool` takes the
  caller's ctx instead of `context.Background()` (worker_pool.go:45). Granularity
  is fixed: **per archive member and per body entry only**, never inside the
  byte-scan loops, where a `ctx.Err()` per keyword would cost more than the scan
  it guards.
- **Every `runtime.NumCPU()` becomes `runtime.GOMAXPROCS(0)`**: worker sizing
  (check_utils.go:250, :365, :459), the parallel-path guards (:100, :267, :428),
  `NewWorkerPool`'s own fallback (worker_pool.go:41-43) and the pdfium pool's
  `min(4, NumCPU)` (readers/pdf.go:210).
- **The TUI path joins the parallel engine** (check_utils.go:534-539, :582);
  `totalTests` is recomputed from matched rules rather than
  `len(slice) x len(files)` (:544-573), and progress ticks become atomic and
  rate-limited.
- **Drop the archive-as-file keyword path** (checks_by_file.go:398-414): with
  scope sets, `IsFreeOfKeywords` is simply not selected for archives.

### 3.5 What this rework absorbs (previously parked here)

Two work items are parked on this plan by other documents. Rev 2 disowned both;
rev 3 owns them, because their owners' comments point here and nothing else
rebuilds these engines:

- **F4 - results as values** (docs/to-fix.md:75-90, "Fold into the check-rules
  rework plan"): checks and the pipeline return diagnostics and PDF notes as
  values instead of writing `output.GlobalLogger` / `helpers.PDFTracker`.
  `internal/analysis.Run`'s doc already promises this (analysis.go:6-7). It is
  also the stated reason server analysis concurrency is pinned to 1
  (handlers.go:124-126) - lifting that pin is NOT proposed here, only unblocked.
  Also folded: the `ProgressCallback` presentation-text leak (analysis.go:31-32)
  and `checksAcrossFiles` retirement (analysis.go:34-35).
- **ctx-threading** (docs/to-fix.md:6, docs/plan-fix-high-med.md:21-22). This
  plan is the single owner; no other document should re-park it. It lands as its
  own commit (R10), deliberately **not** folded into R7: R7 is already the
  largest commit in the series and the ctx pass is purely mechanical, so the
  second signature rewrite buys review granularity at no design cost.

### 3.6 Out of scope

- **Single-pass archive extraction** (rev 2's R7). It conflicts with the
  deliberate universe split the archive-limits series established: the name walk
  counts all entries and discards on truncation (archives.go:49-52), while the
  content path counts unpack candidates and keeps scanned members
  (archive_iterator.go:343-350). Only the two **content** consumers (keyword scan
  and leak extraction) may fuse; the name-list walk stays separate. With the
  secret scan disabled - the shipped default - the win is 2 opens -> 1, not
  4 -> 1, so rev 2's "~2x" was measured against a configuration nobody runs. It
  needs its own design pass and its own review round: **successor plan
  `docs/plan-archive-single-pass.md`, to be written after this one lands.**
  Notes to carry over: consumer list assembled once per archive, borrowed
  `[]byte` no-retain contract, lower once per member.
- The fast/slow check split (parked).
- Per-rule limit overrides (§3.1, §6.3).
- Raising server analysis concurrency above 1.

## 4. Commit series

Each commit builds and tests green alone; one fix per commit. Order: the
repo-relative path field and the selector unification first (behaviour-preserving
except one declared change), then identity and params, then the config surface,
then the engine. Every benchmark uses `b.Loop()` + `b.ReportAllocs()`,
`-count=10` + benchstat; the listed gate is a **review condition for that
commit**, not a follow-up.

Rev-3 -> rev-4 numbering: R1 is new (`RelPath`); old R1..R8 shift to R2..R9; old
R9 moves to R15 (it needs the Result value - see there); R10..R14 keep their
numbers; old R15->R16, old R16->R17.

**Phase A - subject and selector**

- **R1 - feat(structs): repo-relative `RelPath` on `structs.File`.** Prerequisite
  for `subject = "path"` (§3.2), which is request #1's own example and today
  matches nothing (§2.3). New field, set by the only two components that know a
  root: `LocalCollector` (slash-separated, rooted at the scanned path;
  local_collector.go:125, :140) and `CkanCollector` (the resource name;
  ckan_collector.go:318-326, set before and unaffected by the FileStore path
  rewrite at :536). `ToFile`/`ToFileWithDisplay` default it to `Name`, so every
  existing construction site and fixture is untouched. Derivation is a SLICE of
  the already-built walk path (`strings.TrimPrefix` of the walk root - the
  `ToFile` default is an alias), never `filepath.Rel` - zero allocations per
  file and per archive member (the 96->112 B `structs.File` growth is the whole
  cost). No selector uses it yet.
  Tests: `TestLocalCollectorRelPath` (nested tree, depth-limited walk),
  `TestCkanCollectorRelPath` (survives the FileStore rewrite).
- **R2 - feat(selector): the shared Selector package.** New leaf `pkg/selector`:
  per-pattern compile, `LiteralPrefix` fast path over a **caller-owned** lowered
  scratch buffer (the Selector itself stays stateless), subject `name|path`,
  `ignoreCase`, exclude-wins, empty pattern rejected, aggregated errors,
  union-admission constructor per §3.2. No call sites yet.
  Tests: `TestSelectorCompileErrors`, `TestSelectorSubjects`,
  `TestSelectorExcludeWins`, `TestSelectorEmptyPatternRejected`,
  `TestSelectorLiteralFastPathEquivalence`, `TestSelectorStatelessConcurrent`
  (`-race`, one Selector shared by many goroutines).
  *Gate:* new `BenchmarkSelectorLiteral1000` - 0 allocs/op, <= 150 ns/match.
- **R3 - refactor(utils): `skipFileCheck` matches through a compiled Selector.**
  Selectors compiled once and threaded down the dispatch functions.
  REVISED at the R3 review round (three blind reviewers): compiled at the BOOT
  gates - `main.go` next to `ValidateChecksConfig`, and `server.New` - not per
  analysis. Rationale: `config -> selector` is a legal leaf edge; a server must
  not pass `/ready` on a config it can never serve (each analyze request would
  burn a CKAN round-trip, 500 as `internal_error`, and email the admins with no
  dedup); and `analysis.Run` gains its table parameter once instead of changing
  twice. Further review revisions: the table compiles ONLY sections naming a
  file-dispatch check (repository sections stay on their own filters until
  R6/R7); whitelist+blacklist both set is rejected by `CompileCheckSelectors`
  itself with section attribution (no reliance on the LoadConfig-only
  `assesLists`); the four zero-external-caller dispatch functions are
  unexported; per-dispatch-call check-name resolution replaces per-file
  reflection (measured 2.85x on the pass). Declared behaviour changes: bad or
  empty patterns fail the boot; per-pattern compile scopes an inline flag like
  `(?i)` to its own entry where the old join leaked it across the whole list
  (pinned in `TestMatchPatterns`, documented in R17). **Admission:** `TestSkipFileCheck`
  (check_utils_test.go:73) and `TestMatchPatterns` (:187) feed hand-built
  configs with raw pattern lists and get **rewritten** in this commit to build
  compiled selectors - selectors are never compiled lazily at match time.
  *Gate:* new `BenchmarkFilterChecksForFiles` (pkg/utils/check_utils_bench_test.go),
  5000 files x 6 checks - **0 allocs/op** for the filter (today 8456 ns and
  33 allocs per file x check).
- **R4 - test(readers): discriminating archive member-filter cases.** Today's 21
  cases (`TestFiltersDuringArchiveIteration`, archive_iterator_test.go:182 -
  7 bases x zip/7z/tar) cannot detect the R5 semantics change: every pattern in
  them behaves identically as literal and as regex, and no fixture member
  exercises case. Add, asserting **today's** behaviour: a metachar pattern
  (`".*\.log$"`, `"temp.*"`), an uppercase member name, an empty-string entry.
  Then freeze the table.
- **R5 - refactor(readers): the iterator filters through a Selector.** Replaces
  `fileGoodToUnpack`/`matchLiteralPatterns` (archive_iterator.go:257-275) at its
  five call sites (:340, :729, :792, :851, :928). **This is the declared
  user-visible change**: member filtering goes from case-insensitive literal
  substring to case-sensitive regex unless `ignoreCase` is set. Translated legacy
  lists get `ignoreCase` + `QuoteMeta`, so the R4 table moves only in the rows
  the new expressiveness touches - that diff IS the release note. The iterator
  owns the lowered scratch and lowers each member name once (§3.2).
  Carry-over from the R1 review round: (a) R5 sets `RelPath` explicitly at the
  member construction sites - the R1-updated fixtures already assert
  member `RelPath` == member path, but only via the `ToFile` default, so the
  explicit assignment here is what makes that guard real; (b) decide and pin
  one trailing-slash convention for directory subjects - local directory
  entries carry `d1` while tar directory members carry `test/`, and a regex
  selector must not see two conventions through one field.
  *Gate:* `BenchmarkArchiveNameWalk1000` and `BenchmarkMemberNameFilter1000`
  (pkg/readers/archive_iterator_bench_test.go) - no regression against the
  literal baselines (walk 661 us, filter 332 us per 1000 members).
- **R6 - refactor(checks): leakcheck uses the shared Selector.** Deletes
  `leakNameFilter` (leakcheck.go:62-99, :128); the fail-open divergence
  (:77-78) goes with it, and its two readings of one list (:131-132 vs :221)
  collapse into one.

**Phase B - identity, params, config surface**

- **R7 - feat(checks): check registry, typed params, bound runners.** The core
  commit. `checks.NewRegistry()` (constructed value, no `init()` global);
  `CheckDef{Name, Scopes, Bind, RunFile, RunRepository}` and `*BoundRule` with
  its Bind-produced closure (§3.1); `utils.Compile(cfg, reg) (*Plan, error)`
  called once at startup by `main.go` and `server.New`, hard-failing, the Plan
  carried by the `Handler` field and the new `analysis.Run` parameter into
  dispatch (§3.1 - never per request); **invocation unit (file, check) with
  batched `[]*BoundRule`**; `FastMatcher` bound at load, one per parameter set;
  `WorkerPool`, `SafeRun`, `SafeRunCheck` and the work item move from
  `pkg/optimization` to `pkg/utils` (§3.1 - otherwise a cycle);
  `FunctionName` (worker_pool.go:90-96) deleted along with the alias hack
  (check_utils.go:78-82); legacy `[test.*]` translated per §3.1 (one section ->
  one rule with N parameter sets), default rules synthesized for unnamed checks.
  `ParseConfig` also takes over the `assesLists` check from `LoadConfig`, which
  stays a thin wrapper.
  Tests: `TestCompileRejectsUnknownCheck`, `TestCompileRejectsBadParamType`,
  `TestCompileRejectsUnsupportedScope`, `TestCompileAggregatesLoadErrors`,
  `TestCompileSynthesizesDefaultRules`, `TestExecutedCheckMultisetUnchanged`
  (over pc.toml, pc.toml.example, testdata/test_config.toml - the guard against
  silently deleting the section-less checks); `TestParseShippedConfigsLoad`
  (config_parser_test.go:911-924) switches to `LoadConfig` and gains `pc.toml`.
  *Gates:* **absolute anchors first** - before the commit, record baselines for
  the keyword, archive-keyword and PDF benchmarks on the current tree
  (benchstat, `-count=10`). Then new `BenchmarkIsFreeOfKeywordsRules1/3`,
  `BenchmarkIsArchiveFreeOfKeywordsRules1/3`, `BenchmarkPDFRulesFanout1/3`: the
  **1-rule** case must be **<= 1.0x** the recorded baseline in both ns/op and
  B/op - at today's rule count the rework must cost nothing - and 3 rules
  <= 1.3x the 1-rule time and <= 1.15x B/op; PDF B/op **flat** across rule
  counts; archive open count and member budget independent of rule count.
- **R8 - refactor(tui): drop the drifted check-name table.** summary.go:359-382
  (used at :132). Separate from R7 on purpose: presentation must not ride the
  riskiest refactor.
- **R9 - feat(config): `[[rule]]` sections.** The operator-visible feature:
  declarative `RuleSpec` decode, `scope`/`subject`/`enabled`/`ignoreCase`,
  `[rule.params]` typed per check. `[[rule]]` and `[test.*]` for the SAME check
  is a load error, not a merge. Shipped configs migrate here (pc.toml,
  pc.toml.example:125-177, testdata/test_config.toml:79-110).
  Tests: `TestRuleDuplicateName`, `TestRuleUnknownCheck`, `TestRuleInvalidRegex`,
  `TestRuleEmptyPatternRejected`, `TestRuleMixedWithTestSection`,
  `TestRuleExcludeWinsPrecedence`, `TestRuleUnsupportedScope`,
  `TestRuleUnknownParamKey`, `TestRuleContradictoryIncludeExclude`.
  *Gate:* new `TestCompileShippedConfigs` in **pkg/utils** - every shipped config
  through `LoadConfig` + `Compile`. It must live there, not in `pkg/config`:
  `TestParseShippedConfigsLoad` cannot reach `Compile`, and `LoadConfig` alone
  rejects neither an unknown check name, nor a bad param type, nor an
  unsupported scope.

**Phase C - engine**

- **R10 - refactor(checks): checks take a context.** Signature break (§3.5);
  `WorkerPool` takes the caller's ctx (worker_pool.go:45). Cancellation is
  checked per archive member and per body entry only (§3.4).
- **R11 - perf(checks): drop the archive-as-file keyword path**
  (checks_by_file.go:398-414), now expressible as a scope. This **amends
  `TestExecutedCheckMultisetUnchanged`** (R7): `IsFreeOfKeywords` is no longer
  invoked for archive containers. The delta is intended and is the whole point of
  the commit - the dropped pass sniffed the archive as binary and then
  keyword-looped over empty content, while its members are covered by the
  archive-member scope. The amended expectation is the reviewable artefact.
- **R12 - refactor(utils): size workers by `runtime.GOMAXPROCS(0)`** - all of
  check_utils.go:100, :250, :267, :365, :428, :459; `NewWorkerPool`'s own
  fallback (worker_pool.go:41-43); the pdfium pool's `min(4, NumCPU)`
  (readers/pdf.go:210).
- **R13 - refactor(utils): the TUI file phase joins the parallel engine.**
  check_utils.go:534-539, :582; `totalTests` from matched rules (:544-573);
  progress ticks atomic and rate-limited.
  *Gate:* new `BenchmarkApplyAllChecks` over a 1000-file tree - allocs/file
  <= the pre-R13 baseline.

**Phase D - results as values (absorbs F4)**

- **R14 - refactor(analysis): `Run` returns a Result value.** Messages,
  diagnostics and PDF notes stop travelling through `output.GlobalLogger` /
  `helpers.PDFTracker`; server scrubbing (handlers.go:463-471) becomes an
  explicit presentation step over the returned value.
  Tests: `TestResultValueEquivalence` - the Result's message and diagnostic sets
  equal today's drained-global sets over the shipped fixtures;
  `TestAnalyzeScrubsReturnedResult` (pkg/server) - the scrub runs over the
  returned value and nothing operator-scoped reaches the response.
  *Gate:* re-run `BenchmarkApplyAllChecks` (R13) - allocs/file and B/op <= the
  pre-R14 baseline.
- **R15 - feat(utils): run-scoped rule diagnostics.** §3.3 items 4-5, fused into
  the existing selection pass. It lands **after** R14 by necessity: before R14
  the only route from `pkg/utils` to the server log is `output.GlobalLogger`
  (check_utils.go:66, :328) - the global R14 removes. So the diagnostics ride the
  Result value from the start, and each frontend routes them to its operator
  channel (CLI stderr / server log), never into the response.
  *Gates:* new `BenchmarkRuleCollisionAnalysis` (5 vs 20 rules x 5000 files) -
  cost linear in rule count; and `BenchmarkFilterChecksForFiles` (R3) unchanged
  vs its **post-R7 baseline** (post-R7 the pass legitimately builds one
  preallocated `[]*BoundRule` backing array per file) - the diagnostics
  themselves add **zero** allocations to the pass.
- **R16 - refactor(analysis): narrow `Run`'s provisional surface.**
  `ProgressCallback` loses its engine-authored message text (analysis.go:31-32);
  `checksAcrossFiles` retired (analysis.go:34-35). The `internal` marker
  **stays**: promoting the package is a separate commit needing its own
  justification, and is out of scope here.

**Phase E - docs**

- **R17 - docs: rules configuration.** docs/usage.md:44-60 rewritten: the
  "Only one of `blacklist`/`whitelist`" claim (:52) dies with exclude-wins, the
  regex-only-in-lists note (:54) is restated for `subject`/`ignoreCase`,
  `subject = "path"` is documented against `RelPath` including its flat-namespace
  meaning for CKAN, and a `[test.*]` -> `[[rule]]` migration section lands with
  the R5 member-filter release note. One terse comment line per key
  (pc.toml.example already moved in R9).

## 5. Risks

- **R5 is the sharp edge.** Archive member filtering changes semantics for real
  (§2.1, §3.1). R4 exists precisely because the current 21-case table cannot see
  the change. Mitigation: land R4 first, freeze it, and let the R5 diff serve as
  the release note.
- **`subject = "path"` is honest only for local scans.** CKAN's namespace is
  flat, so a path-prefix rule matches no CKAN resource and does nothing on the
  server. R1 makes that visible rather than silently false, and R17 documents it.
- **R7 is the big one and cannot be split further** without leaving dead code:
  identity, params and dispatch are one knot. Its guard is
  `TestExecutedCheckMultisetUnchanged` over all three shipped configs - the
  section-less checks (`ReadMeContainsTOC`, and three name checks in
  testdata/test_config.toml) are the exact class a naive translation deletes
  silently.
- **Config churn.** Translation must be exact for every shipped config;
  pc.toml, pc.toml.example and testdata/test_config.toml are the fixtures, and
  `TestParseShippedConfigsLoad` must run `LoadConfig` (not `ParseConfig`) to
  exercise the checks it is meant to gate - and `TestCompileShippedConfigs` (R9)
  must exist beside it, because `LoadConfig` alone cannot see a rule's check
  name, param types or scope.
- **Phase D is a wide diff** across both frontends and the server, and it now
  also carries R15's diagnostics, which cannot precede the Result value. It is
  last on purpose: everything before it is testable without touching output
  paths.
- **Message `TestName`** currently comes from reflection. §6.1 proposes it stays
  the check name with a separate `Rule` field; either way the three synthetic
  names (§2.2) stay fixed strings.

## 6. Open questions for the operator

The three reviewers converged on an answer for each; they are recorded as
**PROPOSED** - the decision is the operator's.

**DECIDED 2026-08-11 (operator): all three proposals accepted as written.**
6.3 stays out of scope; a per-rule limit override may be the next change after
this plan lands. The plan document itself stays uncommitted (operator decision).

1. Should `TestName` in output become the rule name (better with multiple rules
   per check) or stay the check name (stable for consumers)?
   **PROPOSED: stay the CHECK name.** It is a wire field - the JSON formatter
   emits it as `checkname` (json/formatter.go:49, :164-182) and the renderers
   group on it (tui/types.go:53, tui/summary.go:114-115). Add a separate `Rule`
   string field carrying the rule name, empty for synthetic messages.
2. Is a deprecation path wanted for `[test.*]`, or does it stay supported
   indefinitely as sugar for a single rule?
   **PROPOSED: sugar for exactly one release.** All shipped configs move to
   `[[rule]]` in R9; the translation is deleted the release after.
3. Should per-rule limit overrides (`maxPDFPages` for one selector) be in scope
   now, or stay a follow-up as this plan assumes?
   **PROPOSED: stay out of scope**, but `Bind` resolves *effective* limits into
   the bound rule (rather than reading `[general]` at scan time), so adding the
   override later is a config-surface change only.
