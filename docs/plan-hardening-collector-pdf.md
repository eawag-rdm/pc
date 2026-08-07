# Plan: iterator hardening, recursive collector, PDF content checks

Status: REVIEWED (2026-08-04). Produced from a post-implementation architecture
audit of the C1-C3 archive work, then independently verified by two reviewers
(engineering + performance; both PROCEED-WITH-CHANGES, all changes folded in
below). Coarse by design; each commit gets detailed at implementation time. One
fix per commit; every commit builds and tests green on its own.

Companion to docs/plan-archive-limits-ooxml.md (C4/C7/C8 live there; its C4 is
REVISED by this plan - see H7 note and the C4 section below). Betterleaks
remains dormant; nothing here depends on it.

## Part 1 - hardening series (H1-H6, before C4)

**H1 - fix(readers): export Close; release fds on early exit.** DONE (942961e).
close() is idempotent already - export it. Both consumers defer per-archive
iteration (NOT per function - fd pile-up across the leakcheck loop). The
leakcheck Mkdir-failure path currently abandons the open archive AND drops
already-recorded skip acknowledgements - drain SkipMessages on every early
exit. Test: failure-path releases fd and keeps acks.

**H2 - fix(readers): skip-ack precedence.** DONE (807b73f).
Reorder member checks in the (post-H3 shared) loop: name filter -> size ack ->
memory ack. Today memory runs first: oversized members get mislabeled
memory-skips depending on prior consumption; name-filtered members get acks
although excluded from unpacking. The tar.gz walk-cap pre-check stays FIRST
(cap enforcement must not slide into the mid-drain countingReader path).
Deliberate behavior change: name-filtered oversized members lose their size
acks. Tests: precedence per label, filtered-oversized emits nothing.

**H3 - refactor(readers): one candidate pipeline.** DONE (e5fcca7, before H2 - reorder landed once, in the unified loop).
Fold isTarTextFileWithContent into sniffThenRead (verified: 1-byte probe at a
tar member boundary is a true no-op - stdlib regFileReader returns (0, EOF)
without touching the underlying reader; no walkCounter movement). Extract the
shared post-filter tail (classify -> read -> ack-or-buffer -> charge) taking
(name, declared, io.Reader); zip/7z/tar loops call it. DECIDED behavior change:
a truncated tar member is now scanned-truncated instead of silently skipped
(iteration still ends right after - tar errors are sticky); truncated-tar
fixture required. Constraints: sniffBuf stays on the iterator, no per-member
heap allocation in the tail (dispatch cost measured: noise). This is the
commit that makes C4's count ack and C8's OOXML branch one-edit changes.

**H4 - perf(checks): lowercase once per member.** DONE (6b2737a; measured -14% time, -50% allocs on the shipped 2-set shape).
Measured: GetMatcher cache hits are noise (145 ns; ~0.7 ms per 1000x5-set
archive), but FindMatchesWithOriginalCase lowercases the full member content
PER keyword set (~62% of a small-set call; ~30% of keyword-scan CPU and ~2x
content in GC pressure with the shipped 2-set config). Restructure: lower each
member's content once, run every keyword set against the shared lowered text
(API takes pre-lowered bytes). Matcher-resolution hoisting rides along as a
by-product; preserve the empty-name -> include short-circuit (a naive
HasAnyMatch("") returns false and would flip inclusion). Same commit or
sibling: fix the GetMatcher cache key collision (strings.Join with "|"
collides ["a|b"] with ["a","b"]) and bound/leave-documented the cache.
Benchmark before/after committed in the message.

**H5 - fix(readers): copy-and-release short-delivering members.** DONE (7674b09).
Early-EOF path returns content[:n+m] over a declared-size backing array.
Copy-and-release when cap-len >= 64 KB (memcpy of the SMALL actual content,
rare path): frees the oversized backing immediately and makes len == cap, so
memory accounting is honest without charging declared (charge-cap was
REJECTED: it would drain the budget by declared size and skip later members
that fit - coverage regression). Truncated-member fixture (7z shorter than
declared) asserting the release.

**H6 - fix(readers+checks): small-findings batch.** DONE (fb17a97 open-failure acks, 0ef760e limits helper, 3d97d2c dead error plumbing, 91cb0de dormancy hygiene).
- zip/7z f.Open() failure: today a bare continue - no log, no ack; a member
  whose open fails vanishes silently (scan-evasion hole in an otherwise
  strict ack discipline). Log + skip ack.
- checks-side archiveLimits(cfg) helper (struct literal currently duplicated;
  C7 adds a third site).
- Drop the dead error returns of unpack* / Next's dead error branch (or keep
  WITH a comment if C8 will produce real errors there - decide at C8 design).
- leakcheck memberSize shadowing; dormancy nits (shared timeout const in
  config, comment on the while-disabled timeout cross-check, suppress the
  "Running secret scan..." progress line while dormant).

## Part 2 - C4 revision (member-count enforcement, lives in the other plan) - DONE (b91ddae)

EOCD pre-gate DROPPED (both reviewers): zip.OpenReader already performs the
identical EOCD scan internally (pre-gate = duplicate cost for every legit
archive), the EOCD count is total entries - not unpack candidates - so it
false-rejects folder-heavy zips, and honest zip64 handling (count field
0xFFFF, 65557-byte window, comment false-positives) buys nothing the CD walk
does not already give us. Replacement, keeping the plan's "zero member reads
for over-limit zips" promise:
- zip/7z: exact candidate pre-count (regular, size > 0, name-filter pass) over
  the already-materialized central directory / file list - O(entries) field
  reads, zero decompression, BEFORE the first bufferNext call.
- tar/tar.gz: inline during the single pass - dedicated candidateCount field
  (drop the dead tar fileIndex increment; do not overload fileIndex again),
  stop at limit+1 with the archive-level ack ("more than N members").
- maxArchiveMemberCount = 0 semantics: already documented as 0-means-default
  (usage.md, struct comment) - CLOSED, no parse change.
- Optional LATER server-hardening: a coarse pre-OpenReader guard against CD
  materialization bombs needs its own (much larger) threshold and zip64
  support - explicitly NOT on the C4 critical path.

## Part 3 - feature A: recursive LocalCollector

Today: includeFolders bool - false = top-level only, true = UNBOUNDED WalkDir
recursion, no depth or count bound; dirs are emitted as File entries (size -1).

**A1 - feat(config): collector attr parsing for integers.** DONE (e06f503).
MANDATORY prerequisite the coarse plan missed: the collector-attr parser keeps
only string/bool/[]string - a TOML integer attr is silently dropped before the
collector sees it. Extend with the fail-fast convention (generalize the
existing typed-getter core). includeFolders keeps accepting its legacy string
form ("true") - backcompat, do not break deployed configs.

**A2 - feat(collectors): maxFolderDepth + maxFileCount.** DONE (fa01d4e; boundary-depth dirs are listed, not entered).
- maxFolderDepth int: 0 = top-level only (current default behavior), N =
  descend N levels. Depth = separator count on currentPath[len(cleanPath)+1:]
  (no filepath.Rel per entry - it allocates). SkipDir at the boundary prevents
  the ReadDir - no wasted I/O. Decide + document whether boundary-depth dirs
  still emit File entries.
- maxFileCount int: cap on collected files; stop the walk with fs.SkipAll (the
  only way the cap bounds worst-case I/O - a continue-walk no-op does not) +
  warning and truncation notice.
- Backcompat: includeFolders = true with no maxFolderDepth set keeps unlimited
  recursion; explicit maxFolderDepth wins. CKAN collector untouched.
- Per-entry cost: separator count tens of ns, Info() lstat already exists -
  noise (measured stance, not hope).

## Part 4 - PDF content checks (P1-P3, after C7)

Library decision (researched + perf-verified): klippa-app/go-pdfium in
WebAssembly mode - MIT wrapper, permissive PDFium + Apache-2.0 wazero, NO cgo,
~5 MB wasm embedded via go:embed, PDFium engine (rank 1/7 text quality, 97%,
fastest tier in the py-pdf benchmark; ~0.2 s/doc in wasm mode), wazero sandbox
contains malformed-PDF faults with memory-limit pages + per-instance timeouts.
Fallback if it disappoints in practice: poppler pdftotext subprocess (GPL is
clean under the exec model; Docker-bundled like betterleaks). Rejected:
pdfcpu (no decoded-text extraction as of v0.14.0), ledongthuc/dslipak forks
(open infinite-loop/OOM/panic issues on malformed input), go-fitz + unipdf
(AGPL).

HARD requirements (perf review; without ALL of these, PDF support is
perf-negative and gets scoped server-only instead):
1. Lazy init: wasm runtime compiled/instantiated only when the run actually
   contains a PDF (helpers.PDFTracker already inventories PDFs before checks
   run). A zero-PDF run pays 0 ms.
2. Disk compilation cache: wazero's default cache is in-memory per-process - a
   cold CLI run would recompile (seconds) every time. Use
   NewCompilationCacheWithDir under os.UserCacheDir()/pc; cache is
   wazero-version-keyed (first run per upgrade pays the compile once).
3. Instance pool decoupled from the check worker pool: MaxTotal 2-4 (NOT
   NumCPU - instance linear memory grows to the largest document and never
   shrinks until instance close); workers block on the pool as backpressure;
   recycle instances after large documents.
4. Extraction caps beyond the input gate: page cap + extracted-text cap
   (charge extracted text like an archive member; compressed streams and
   page-count bombs expand past any input-size gate).

**P1 - feat(readers): PDF text extraction.** DONE (P1 in the "sandboxed
PDFium" commit; two-expert pass 2026-08-05). Deviations from coarse text:
NEW [general] maxPDFPages key (default 500 - the sole deterministic CPU
bound; byte caps are blind to scanned/empty pages) + shared text cap +
30 s internal backstop via between-page deadline checks + instance Kill
with CloseOnContextDone (the pool timeout only bounds acquisition);
per-page output so findings cite pages; raw text API (the convenience
call is one wasm call PER CHARACTER); empty FSConfig (library default
mounts host root); 1 GiB wasm memory limit; Go toolchain bumped to 1.25
(go-pdfium requirement, operator-approved). Original coarse text:
readers.ReadPDFFile([]byte) (text, error) via the instance pool, per-file
timeout, the caps above. DEPENDS ON C7's []byte reader refactor (today's
readers take paths). Fixture set: normal LaTeX/Word PDFs, encrypted, malformed
(must skip-ack, never hang/crash), page-bomb (must cap).

**P2 - feat(checks): top-level PDFs into the keyword check.** DONE
(2a1c6e9). Magic searched in first 1029 bytes (prefix-only = one-byte-prepend
evasion); no-magic fallback to generic flow; password/parse/timeout acks;
XDG_CACHE_HOME pinned in compose. Original coarse text:
Route by EXTENSION BEFORE the isTextFile heuristic - a mostly-text PDF can
pass the >=95%-printable sniff and would get raw-scanned, bypassing
extraction. Skip ack when extraction fails or caps trip. C7 shape (same
routing point as OOXML).

**P3 - feat(readers): PDFs inside archives.** DONE (two-expert design pass
2026-08-07, both PROCEED-WITH-CHANGES; all required changes folded in).
Rides C8's extension-routing branch as planned. Deviations from coarse text:
magic sniffed from a min(1028, declared) prefix BEFORE the full member read
(a binary member named .pdf costs ~1 KiB decompressed, like the generic
sniff - a crafted zip of 10 MiB fakes would otherwise cost GBs); per-archive
cumulative PDF wall-clock budget 4x DefaultPDFTimeout = 120 s (constant, not
config) with one archive-level ack - bounds crafted many-PDF archives that
the per-member timeout would amplify to hours inside one uninterruptible
check item; empty placeholder pages filtered before the join (n-page
image-only PDFs yielded n-1 newlines otherwise); MaxPDFPages rides
ArchiveLimits, zero fails closed silently; leakcheck's iterator inherits PDF
extraction intentionally (dormant while betterleaks is off). Known gaps:
no encrypted-PDF fixture repo-wide (password ack path untested top-level
too); 7z detection fixture still needs the 7z CLI.

## Order

H1 -> H2 -> H3 -> H4 -> H5 -> H6 -> C4 -> A1 -> A2 -> C7 -> C8 -> P1 -> P2 -> P3

Still parked: [general] fail-fast migration commit (from C3); C5/C6 (dormant
betterleaks path); fast/slow check split (own plan).

QUEUED (operator request 2026-08-05, after PDF lands): rework how checks load
and apply whitelist/blacklist filters. Today three separate mechanisms exist -
utils.skipFileCheck (regex per check), the archive iterator's literal matcher
(fileGoodToUnpack), and leakcheck's own compiled regex filter - with different
matching semantics. Needs its own design + review pass.

## Open item for the operator

Local pc.toml: maxContentScanFileSize = 1097152000 (~1.02 GB) with a "20MB"
comment; tracked pc.toml.example has 20971520 (real 20 MB). Intended value
unknown - needs a human decision; 1 GB also widens the zip-CD window the
optional C4 server guard would care about.
