# Plan: archive member limit, OOXML secret coverage, scanner hardening

Status: REVIEWED (2026-07-29, two-expert pass) - commit series revised per
findings. Coarse by design; each commit gets detailed at implementation time.
One commit per fix; every commit builds and tests green on its own.

REFRAME (2026-08-04): the betterleaks secret scan is DORMANT (shipped
`enabled = false`; too slow for the latency target - code kept, reactivation =
one attr flip). Consequences for this series while dormant:
- C5 (argv chunking) and C6 (extraction hygiene) act on the secret scan's
  extraction/invocation path only -> DEFERRED until reactivation.
- C7/C8 (OOXML) remain valuable for the keyword check (D2: both checks
  consume); their secret-scan-specific parts (temp-dir text writes, plain-scan
  exclusion) are inert until reactivation.
- C4 (member-count enforcement) unaffected - the keyword check unpacks too.

COMPANION PLAN (2026-08-04): docs/plan-hardening-collector-pdf.md - audit-
driven hardening series H1-H6 (runs BEFORE C4), recursive LocalCollector, and
PDF content checks (P1-P3, after C7). C4 below is revised by it.

## Background

- Content-unpacking checks: `IsArchiveFreeOfKeywords` and `IsFreeOfSecrets`;
  both consume `readers.InitArchiveIterator`. Only these two decompress archive
  content (name-list checks walk headers only).
- Existing bounds: per-member size (`maxArchiveFileSize`), per-archive byte
  budget (`maxTotalArchiveMemory`), whole-file gate (`maxContentScanFileSize`),
  one-level unpacking.
- Review corrections to the original draft (verified against Go 1.23 stdlib and
  vendored libs):
  - Lying-size zip/7z headers CANNOT balloon RAM - both readers hard-stop at
    the declared size. The real holes are: non-text members fully decompressed
    just for type-sniffing and never charged to the memory budget (honest
    deflate bombs = unbounded inflate CPU); 7z solid-folder transit
    decompression; and a pre-existing double-read bug (zip/7z members
    decompressed twice, memory budget charged twice).
  - A tar.gz pre-count walk is NOT bounded by the member limit (tar.Next
    decompresses content between headers) - count must be enforced inline
    during the single real pass.
  - Per-archive-dir argv passing does not retire ARG_MAX (plain top-level
    files scale argv too) - chunked scanner invocations do.
  - OOXML (xlsx/docx) is itself zip; the extraction libs read unbounded into
    memory. Inner declared-size gating is required and is sound precisely
    because the stdlib enforces declared sizes.

## Commit series (revised order)

**C1 - fix(readers): consume the look-ahead buffer in zip/7z paths.** DONE
(3da4069). Pre-existing bug: members decompressed twice,
`maxTotalArchiveMemory` charged twice (effective budget halved), solid 7z
quadratic. Foundation for everything after.

**C2 - fix(readers): bound decompression work.** DONE, with reviewed
deviations (two-expert pass 2026-07-30): sample-sniff (512 bytes, per-iterator
scratch) before full read of zip/7z members; text members read into one exact
declared-size allocation + 1-byte probe (overrun ack, keeps zip CRC check
alive). Declared-size gate (4x budget) applied to 7z ONLY - zip is already
CPU-bounded post-sniff and a zip gate would cost coverage with no bound gain.
ADDED: tar.gz walk cap (counting reader enforcing 4x budget in Read itself,
header pre-check) closing the same bomb class for tar.gz; explicit tar
skip-drains removed (tar.Next auto-discards, Seeks on plain tar). Closes the
honest-deflate CPU bomb and bounds 7z solid-transit cost.

**C3 - feat(config): `maxArchiveMemberCount` (default 1000).** DONE
(two-expert pass 2026-07-30). Fail-fast typed getter (generalized tomlInt
core; [server] wrappers untouched); wrong type or negative = load error.
Single defaulting site `GeneralConfig.ArchiveLimits()` (covers hand-built
structs; 0 never means unlimited); checks' duplicated derivation gone.
`readers.ArchiveLimits` struct param (keyed literals; MaxSize renamed
MaxMemberSize int64; fail-closed on non-positive). `maxMemberCount` stored,
enforced in C4. Docs + example configs. Skip wording for C4: "more than N
members" (exact count unknowable for tar.gz). SPLIT OUT: [general] fail-fast
migration of the six existing keys = own follow-up commit (deployed-config
behavior change; needs tomlInt64/tomlString).

**C4 - feat(readers): enforce the member-count limit.** DONE (b91ddae). REVISED 2026-08-04
(audit + two-reviewer verification; details in
docs/plan-hardening-collector-pdf.md Part 2). EOCD pre-gate DROPPED:
zip.OpenReader repeats the identical EOCD scan internally, the EOCD count is
total entries (false-rejects folder-heavy zips), and honest zip64 handling
buys nothing over the CD walk. "Member" = unpack candidate only (regular
file, size > 0, name-filter pass) - directory/cruft entries do not count.
- zip/7z: exact candidate pre-count over the materialized central directory /
  file list (O(entries) field reads) BEFORE the first bufferNext call. Zero
  decompression.
- tar/tar.gz: inline during the single real pass via a dedicated
  candidateCount field (dead tar fileIndex increment dropped) - at candidate
  limit+1, stop iteration + skip acknowledgement.
- Skip acknowledgement Source = the archive File (not a fabricated member);
  emitted once per check that attempts unpacking (same duplication semantics
  as existing member-size skips). Runs AFTER the H1-H6 hardening series.

**C5 - fix(checks): chunked scanner invocations (ARG_MAX).**
`runBetterleaks` splits paths into invocations capped at ~1 MB argv and merges
findings (aggregation is order-independent). Covers plain files, temp members,
any archive count. Normalize paths (`filepath.Clean`, `EvalSymlinks` on the
temp root once) before `sources` lookup so findings always map back.

**C6 - fix(checks): extraction hygiene.**
Global (per-analysis) extraction byte cap across archives (temp dir is
tmpfs-backed RAM; per-archive budget alone allows N x 500 MB). Truncate
sanitized member basenames (~100 chars; index prefix keeps uniqueness) and
emit a skip acknowledgement on ANY temp-write failure (today: silent log only,
scan-evasion via long names).

**C7 - feat(readers+checks): OOXML extraction, top level.** DONE (2b6dcbd,
two-expert pass 2026-08-04; keyword-check scope while betterleaks dormant).
Deviations from the coarse text: cores take (io.ReaderAt, size, limits) not
[]byte (streaming preserved; members/PDF wrap bytes); gate = per-entry
MaxArchiveFileSize + summed MaxTotalArchiveMemory (1x member size would
false-reject ordinary files - XML compresses 5-10x); text cap MaxArchiveFileSize
with the xlsx drain-not-break pattern (lib's row producer goroutine has no
other exit); routing hoisted before the text sniff with a not-zip fallback so
misnamed text files keep being scanned; gate/parse/truncation acks added.
Secret-scan parts (temp writes, plain-batch exclusion) remain inert. Original
coarse text follows:
`.xlsx`/`.docx` routed through `ReadXLSXFile`/`ReadDOCXFile` refactored to
`[]byte` input (xlsxreader: `NewReader`; go-docx: `bytes.NewReader` ReaderAt).
BEFORE parsing: open the container with `zip.NewReader`, reject if summed (or
any single) declared inner size exceeds the per-member budget; cap accumulated
extracted text. Secret scan writes extracted text to the temp dir
(`0007_report.xlsx.txt`), original OOXML paths are EXCLUDED from the plain scan
batch (no double scan); temp dir is created whenever OOXML files or archives
exist. Restores pre-slimming keyword-era secret coverage for Excel/Word.

**C8 - feat(readers): OOXML members inside archives.** DONE (5aa6aea).
Extraction inside tryBufferMember (extension routing before the sniff), one
concatenated block per member, extraction cap = remaining archive budget
(amplification-safe), extracted length charged; inner-gate/parse acks;
misnamed-text member fallback. Keyword check needed zero changes, as planned.
Original coarse text follows:
Extraction happens INSIDE the iterator (contract stays "yielded content is
scannable text"; keyword check needs zero changes; findings for members lose
per-sheet indexing - one concatenated text block, documented). Same inner
declared-size gate as C7; extracted text drawn against the archive memory
budget. Both content checks gain coverage. New capability, not a regression
fix.

## Efficiency invariants (from review)

- Counting is O(index) for zip/7z, inline for tar-family - never a dedicated
  decompression pass.
- No member is decompressed more than once per check (C1), non-text members at
  most 512 bytes + skip (C2).
- Over-limit archives: zero member reads (zip), bounded header walk
  (tar-family).

## Test plan (per commit, sketch)

Crafted fixtures per finding: zip/7z double-read regression test (C1);
honest-deflate bomb with corrupt tails proving no decompression attempt (C2,
structural - no timing assertions); EOCD pre-gate on a many-entry zip vs.
folder-heavy legit zip staying under the candidate limit (C4); argv-chunking
with synthetic 30x1500-member corpus, assert multiple invocations + merged
findings + no unexpected-path warnings (C5); ENAMETOOLONG member gets a skip
ack (C6); planted assignment-form secrets in xlsx/docx, top-level and
in-archive, finding maps to original file, secret value absent from output,
bomb-xlsx rejected by inner gate (C7/C8). E2E real-binary rerun.

## Later: fast/slow check split with streamed results (outline only)

Not part of the commit series above; to be detailed in its own plan.

- Split checks into fast (name/metadata) and slow (content: keywords, archive
  contents, secret scan) phases; run fast first, report each phase as it
  completes.
- Phase A (no API change): split BY_FILE into fast/content groups, second
  worker-pool pass, TUI renders metadata results immediately; plain output in
  two blocks; JSON output stays blocking.
- Phase B (server): keep /api/v1/analyze blocking; add an SSE endpoint
  alongside that streams phase results (nginx: response buffering off for that
  route). Messages carry a phase tag so all outputs group consistently.
- Known consequences to design for: partial-failure semantics (fast ok, slow
  failed) in envelope/alerts/exit codes; cache only complete two-phase runs;
  per-phase timeout budgets; rate-limit accounting stays per analysis.

## Out of scope

PDF content scanning (needs a PDF text extractor - separate decision), nested
archive recursion (deliberately unsupported - it is the nesting protection),
CLI-wide timeout, bare non-tar `.gz` content (pre-existing blind spot: fails
the tar parse fast and cheap; blacklistable per check if ever desired).

## Decisions (resolved 2026-07-29)

- D1: member-count cap applies to content-unpacking checks only
- D2: `[]byte`-based readers; both keyword check and secret scan consume OOXML
- D3: decompression-bound fix included (now C2, ordered early per review)
