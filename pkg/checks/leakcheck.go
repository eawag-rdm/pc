package checks

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// Defaults for the [test.IsFreeOfSecrets] attrs table.
const (
	defaultLeakBinary   = "betterleaks"
	defaultLeakMaxProcs = 3
	// leakMaxLinesShown caps the line numbers listed per rule in one message.
	leakMaxLinesShown = 5
)

// leakAttrs holds the operational knobs of the leak check, read from the
// attrs table of [test.IsFreeOfSecrets]. Wrong-typed values are rejected at
// config load by ValidateChecksConfig, so plain type assertions suffice here.
type leakAttrs struct {
	enabled        bool
	binary         string
	timeoutSeconds int
	maxProcs       int
}

func leakAttrsFrom(tc *config.TestConfig) leakAttrs {
	a := leakAttrs{
		binary:         defaultLeakBinary,
		timeoutSeconds: config.DefaultSecretsTimeoutSeconds,
		maxProcs:       defaultLeakMaxProcs,
	}
	if tc == nil || tc.Attrs == nil {
		return a
	}
	if v, ok := tc.Attrs["enabled"].(bool); ok {
		a.enabled = v
	}
	if v, ok := tc.Attrs["binary"].(string); ok && v != "" {
		a.binary = v
	}
	if v, ok := tc.Attrs["timeoutSeconds"].(int64); ok && v > 0 {
		a.timeoutSeconds = int(v)
	}
	if v, ok := tc.Attrs["maxProcs"].(int64); ok && v > 0 {
		a.maxProcs = int(v)
	}
	return a
}

// leakNameFilter carries the whitelist/blacklist of [test.IsFreeOfSecrets] as
// regexes compiled once per analysis. Semantics mirror skipFileCheck in
// pkg/utils: a non-empty whitelist admits only matching names, otherwise a
// non-empty blacklist rejects matching names. Patterns are validated at config
// load (ValidateChecksConfig); a compile failure here only warns and disables
// the filter.
type leakNameFilter struct {
	include *regexp.Regexp
	exclude *regexp.Regexp
}

func newLeakNameFilter(tc *config.TestConfig) leakNameFilter {
	compile := func(patterns []string) *regexp.Regexp {
		combined, err := regexp.Compile(strings.Join(patterns, "|"))
		if err != nil {
			output.GlobalLogger.Warning("IsFreeOfSecrets: error compiling pattern '%s': %v", strings.Join(patterns, "|"), err)
			return nil
		}
		return combined
	}
	f := leakNameFilter{}
	if len(tc.Whitelist) > 0 {
		f.include = compile(tc.Whitelist)
	} else if len(tc.Blacklist) > 0 {
		f.exclude = compile(tc.Blacklist)
	}
	return f
}

func (f leakNameFilter) excluded(name string) bool {
	if f.include != nil {
		return !f.include.MatchString(name)
	}
	if f.exclude != nil {
		return f.exclude.MatchString(name)
	}
	return false
}

// leakTempName strips characters that are unsafe in a temp file name.
var leakTempName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// IsFreeOfSecrets scans file contents for secrets with the external betterleaks
// binary. It is repository-scoped so the whole file set goes to one scanner
// invocation (the scanner's startup cost is paid once, not per file).
//
// Size gating follows pc.toml exactly, enforced by pc rather than scanner
// flags: top-level files larger than general.maxContentScanFileSize are
// skipped, and archive members are extracted through the same size-gated
// iterator as the keyword checks (general.maxArchiveFileSize per member,
// general.maxTotalArchiveMemory per archive) into a private temp directory.
// The scanner itself never unpacks anything (--max-archive-depth 0).
func IsFreeOfSecrets(repo structs.Repository, cfg config.Config) []structs.Message {
	tc := cfg.Tests["IsFreeOfSecrets"]
	if tc == nil {
		return nil
	}
	attrs := leakAttrsFrom(tc)
	if !attrs.enabled {
		return nil
	}

	var messages []structs.Message

	// Partition the repository: excluded names drop out, oversized files are
	// acknowledged in one aggregate skip, archives go through extraction.
	nameFilter := newLeakNameFilter(tc)
	var plain, archives []structs.File
	oversized := 0
	for _, f := range repo.Files {
		if nameFilter.excluded(f.Name) {
			continue
		}
		size := f.Size
		if size <= 0 {
			size = structs.GetFileSize(f.Path)
		}
		if size > cfg.General.MaxContentScanFileSize {
			oversized++
			continue
		}
		if f.IsArchive {
			archives = append(archives, f)
		} else {
			plain = append(plain, f)
		}
	}
	if oversized > 0 {
		reason := fmt.Sprintf("Skipped leak scan of %d file(s): file size exceeds maximum (%d bytes).", oversized, cfg.General.MaxContentScanFileSize)
		messages = append(messages, structs.Message{Content: reason, Source: structs.Repository{}, Skipped: true, Reason: reason})
	}

	// sources maps every path handed to the scanner back to the File the
	// finding should be reported against.
	sources := make(map[string]structs.File, len(plain))
	var scanPaths []string
	for _, f := range plain {
		sources[f.Path] = f
		scanPaths = append(scanPaths, f.Path)
	}

	// Extract archive members (size-gated, text members only - the same
	// iterator the keyword checks use) into a temp dir for scanning.
	if len(archives) > 0 {
		tmpDir, err := os.MkdirTemp("", "pc-leakcheck-")
		if err != nil {
			output.GlobalLogger.Warning("IsFreeOfSecrets: cannot create temp dir: %v", err)
			reason := fmt.Sprintf("Skipped leak scan of %d archive(s): temp dir unavailable.", len(archives))
			messages = append(messages, structs.Message{Content: reason, Source: structs.Repository{}, Skipped: true, Reason: reason})
		} else {
			defer os.RemoveAll(tmpDir)
			memberPaths := extractArchivesForLeakScan(cfg, tc, archives, tmpDir, sources, &messages)
			scanPaths = append(scanPaths, memberPaths...)
		}
	}

	if len(scanPaths) == 0 {
		return messages
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(attrs.timeoutSeconds)*time.Second)
	defer cancel()

	findings, err := runBetterleaks(ctx, attrs.binary, scanPaths, attrs.maxProcs)
	if err != nil {
		output.GlobalLogger.Warning("IsFreeOfSecrets: %v", err)
		reason := "Leak scan did not run: " + err.Error()
		messages = append(messages, structs.Message{Content: reason, Source: structs.Repository{}, Skipped: true, Reason: reason})
		return messages
	}

	messages = append(messages, condenseLeakFindings(findings, sources)...)
	return messages
}

// isExtractedTextMember reports whether the iterator hands this member over
// as extracted text rather than raw bytes (pdf/xlsx/docx containers).
func isExtractedTextMember(memberName string) bool {
	if readers.OOXMLKind(memberName) != "" {
		return true
	}
	return strings.EqualFold(filepath.Ext(memberName), ".pdf")
}

// extractArchivesForLeakScan writes the size-gated text members of each
// archive to tmpDir, registers them in sources, and returns their paths.
// Iterator skip acknowledgements (member too large / memory budget reached)
// are appended to messages.
//
// NOTE: this iterator is independent of the keyword check's, so container
// members (pdf/xlsx/docx) are extracted a second time when both checks run,
// and the per-archive PDF time budget applies per iterator (two passes = two
// budgets). Acceptable while the secret scan stays opt-in; sharing one
// extraction across checks needs the fast/slow check split.
func extractArchivesForLeakScan(cfg config.Config, tc *config.TestConfig, archives []structs.File, tmpDir string, sources map[string]structs.File, messages *[]structs.Message) []string {
	limits := archiveLimits(cfg)
	// One list, two languages - honestly, for one more commit. leakNameFilter
	// above reads [test.IsFreeOfSecrets]'s lists as case-SENSITIVE regexes
	// joined with "|" and matched against f.Name; this gate reads the same two
	// lists as quoted, case-INSENSITIVE literals matched against the full member
	// path. A pattern like `.*\.log$` therefore filters top-level files and no
	// archive member. The next commit collapses both onto one selector; this
	// call is the minimal adaptation to the iterator's new parameter.
	//
	// Fail CLOSED, unlike leakNameFilter's warn-and-disable: a filter that
	// cannot be honoured must not turn into a wider scan.
	memberFilter, admitNone, err := selector.CompileLegacyLists("IsFreeOfSecrets", tc.Whitelist, tc.Blacklist)
	if err != nil {
		output.GlobalLogger.Warning("IsFreeOfSecrets: %v", err)
		reason := fmt.Sprintf("Skipped leak scan of %d archive(s): the [test.IsFreeOfSecrets] member filter is unusable.", len(archives))
		*messages = append(*messages, structs.Message{Content: reason, Source: structs.Repository{}, Skipped: true, Reason: reason})
		return nil
	}
	// A whitelist that selects nothing selects no member either.
	if admitNone {
		return nil
	}

	var memberPaths []string
	for ai, archive := range archives {
		it := readers.InitArchiveIterator(archive.Path, archive.Name, limits, memberFilter)
		if !it.HasFilesToUnpack() {
			*messages = append(*messages, it.SkipMessages()...)
			continue
		}
		archDir := filepath.Join(tmpDir, fmt.Sprintf("a%03d", ai))
		if err := os.Mkdir(archDir, 0o700); err != nil {
			output.GlobalLogger.FileWarning(archive.GetDisplayName(), "IsFreeOfSecrets: cannot create temp dir for archive '%s': %v", archive.Name, err)
			// Keep the acknowledgements recorded so far and release the archive
			// handles now (iteration will not continue for this archive).
			*messages = append(*messages, it.SkipMessages()...)
			it.Close()
			continue
		}
		idx := 0
		for it.HasNext() {
			it.Next()
			memberName, content, memberSize := it.UnpackedFile()
			base := leakTempName.ReplaceAllString(path.Base(memberName), "_")
			// Container members (pdf/xlsx/docx) arrive as EXTRACTED TEXT, so
			// the temp copy must not keep an extension claiming otherwise -
			// a scanner that skips binary formats by extension would skip
			// the very text we extracted for it.
			if isExtractedTextMember(memberName) {
				base += ".txt"
			}
			tmpPath := filepath.Join(archDir, fmt.Sprintf("%04d_%s", idx, base))
			idx++
			if err := os.WriteFile(tmpPath, content, 0o600); err != nil {
				output.GlobalLogger.FileWarning(archive.GetDisplayName(), "IsFreeOfSecrets: cannot write temp copy of '%s': %v", memberName, err)
				continue
			}
			member := structs.ToFileWithDisplay(
				archive.Path,             // path stays as archive path
				memberName,               // name is the path within archive
				memberName,               // display name
				int64(memberSize),        // size
				"",                       // suffix (auto-detected)
				archive.GetDisplayName(), // archive name reference
			)
			member.RelPath = memberName // the member path, verbatim
			sources[tmpPath] = member
			memberPaths = append(memberPaths, tmpPath)
		}
		*messages = append(*messages, it.SkipMessages()...)
	}
	return memberPaths
}

// condenseLeakFindings folds the scanner's per-secret findings into one
// message per file: every rule listed once with its match lines (capped at
// leakMaxLinesShown), so a file with many hits stays one readable issue.
func condenseLeakFindings(findings []blFinding, sources map[string]structs.File) []structs.Message {
	perFile := map[string]map[string][]int{}
	for _, fd := range findings {
		if _, ok := sources[fd.File]; !ok {
			output.GlobalLogger.Warning("IsFreeOfSecrets: finding for unexpected path '%s'", fd.File)
			sources[fd.File] = structs.File{Path: fd.File, Name: path.Base(fd.File)}
		}
		rules := perFile[fd.File]
		if rules == nil {
			rules = map[string][]int{}
			perFile[fd.File] = rules
		}
		rules[fd.RuleID] = append(rules[fd.RuleID], fd.StartLine)
	}

	fileKeys := make([]string, 0, len(perFile))
	for k := range perFile {
		fileKeys = append(fileKeys, k)
	}
	sort.Strings(fileKeys)

	var messages []structs.Message
	for _, fk := range fileKeys {
		rules := perFile[fk]
		ruleIDs := make([]string, 0, len(rules))
		for r := range rules {
			ruleIDs = append(ruleIDs, r)
		}
		sort.Strings(ruleIDs)

		parts := make([]string, 0, len(ruleIDs))
		for _, r := range ruleIDs {
			parts = append(parts, r+" ("+formatLeakLines(rules[r])+")")
		}
		messages = append(messages, structs.Message{
			Content: "Possible secret(s) detected: " + strings.Join(parts, "; "),
			Source:  sources[fk],
		})
	}
	return messages
}

func formatLeakLines(lines []int) string {
	sort.Ints(lines)
	label := "lines "
	if len(lines) == 1 {
		label = "line "
	}
	shown := lines
	extra := 0
	if len(lines) > leakMaxLinesShown {
		shown = lines[:leakMaxLinesShown]
		extra = len(lines) - leakMaxLinesShown
	}
	strs := make([]string, len(shown))
	for i, l := range shown {
		strs[i] = fmt.Sprintf("%d", l)
	}
	out := label + strings.Join(strs, ", ")
	if extra > 0 {
		out += fmt.Sprintf(" +%d more", extra)
	}
	return out
}
