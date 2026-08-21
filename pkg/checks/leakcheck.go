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

// Defaults for the leak check's parameter set.
const (
	defaultLeakBinary   = "betterleaks"
	defaultLeakMaxProcs = 4
	// leakMaxLinesShown caps the line numbers listed per rule in one message.
	leakMaxLinesShown = 5
)

// leakAttrs holds the operational knobs of the leak check, read from its
// rule's parameter set and type-checked once, at load.
type leakAttrs struct {
	binary         string
	timeoutSeconds int
	maxProcs       int
	maxFileSize    int64 // the rule's own size cap; 0 = none, the [general] limits alone
}

// leakAttrsFrom reads the knobs and clamps maxProcs to general's maxCores: the
// scanner is a CHILD PROCESS, so it does not inherit this process's GOMAXPROCS
// and would otherwise be the one place that ignores [general] maxCores. The
// number is the CONFIGURED maxCores, never this process's settled runtime
// budget: reading the live runtime here would make the bind - and so all of
// config compilation - depend on the CPU cap having been applied first, an
// ordering no signature states. On a machine smaller than the configured cap
// the child is granted the configured number regardless, because the parent's
// own ceiling bounds only the parent.
func leakAttrsFrom(attrs map[string]interface{}, general *config.GeneralConfig) leakAttrs {
	a := leakAttrs{
		binary:         defaultLeakBinary,
		timeoutSeconds: config.DefaultSecretsTimeoutSeconds,
		maxProcs:       defaultLeakMaxProcs,
	}
	if v, ok := attrs["binary"].(string); ok && v != "" {
		a.binary = v
	}
	if v, ok := attrs["timeoutSeconds"].(int64); ok && v > 0 {
		a.timeoutSeconds = int(v)
	}
	if v, ok := attrs["maxProcs"].(int64); ok && v > 0 {
		a.maxProcs = int(v)
	}
	if v, ok := attrs["maxFileSize"].(int64); ok && v > 0 {
		a.maxFileSize = v
	}
	a.maxProcs = min(a.maxProcs, general.EffectiveMaxCores())
	return a
}

// bindSecrets binds the leak scan: its attrs, and nothing else. The ONE
// selector matched at both of the scan's gates - the repository narrowing that
// picks the files, and the archive iterator that picks the members - and the
// scan bounds are HANDED IN by the dispatch, which holds the rule and its
// batch. The closure must not read them back off the rule it lives on: that
// would make a BoundRule uncopyable and force a bind per scope.
//
// Typing is strict: an unknown or wrong-typed knob is a load error, validated
// for a DISABLED rule too (Compile binds those and then leaves them out of the
// plan), so a config the scan could not honour fails at load, not on the day
// the dormant scan is reactivated. A rule carries the knobs as its ONE
// parameter set, where "enabled" is the rule's own key and refused here.
func bindSecrets(spec config.RuleSpec, general *config.GeneralConfig) (*BoundRule, error) {
	if len(spec.Params) > 1 {
		return nil, fmt.Errorf("check %q takes one parameter set", spec.Check)
	}
	var table map[string]interface{}
	if len(spec.Params) == 1 {
		table = spec.Params[0]
	}
	if err := checkSecretAttrs(table); err != nil {
		return nil, err
	}
	// The child's cap comes from the CONFIG, never the live runtime - see leakAttrsFrom.
	bound := leakAttrsFrom(table, general)
	return &BoundRule{
		Rule:  spec.Name,
		Rules: []string{spec.Name},
		applyRepo: func(ctx context.Context, repository structs.Repository, batch *Batch, sel *selector.Selector) []structs.Message {
			return isFreeOfSecrets(ctx, repository, bound, batch.limits, batch.maxContentScan, sel)
		},
	}, nil
}

// checkSecretAttrs type-checks the scan's knobs once, at load.
func checkSecretAttrs(attrs map[string]interface{}) error {
	keys := make([]string, 0, len(attrs))
	for key := range attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys) // map order is random; the reported key must not be
	for _, key := range keys {
		v := attrs[key]
		var typeOK bool
		switch key {
		case "enabled":
			return fmt.Errorf("params: %q is the rule's own key, not a parameter", key)
		case "binary":
			s, isStr := v.(string)
			typeOK = isStr && s != ""
		case "timeoutSeconds", "maxProcs":
			n, isInt := v.(int64)
			typeOK = isInt && n > 0
		case "maxFileSize":
			// Zero is the omission - no cap of this rule's own - so only a
			// negative size is a value no gate could honour.
			n, isInt := v.(int64)
			typeOK = isInt && n >= 0
		default:
			return fmt.Errorf("unknown key %q (allowed: binary, timeoutSeconds, maxProcs, maxFileSize)", key)
		}
		if !typeOK {
			return fmt.Errorf("%q has the wrong type or an invalid value (%v)", key, v)
		}
	}
	return nil
}

// leakTempName strips characters that are unsafe in a temp file name.
var leakTempName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// isFreeOfSecrets scans file contents for secrets with the external betterleaks
// binary. It is repository-scoped so the whole file set goes to one scanner
// invocation (the scanner's startup cost is paid once, not per file).
//
// Size gating follows pc.toml exactly, enforced by pc rather than scanner
// flags: top-level files larger than general.maxContentScanFileSize - or than
// the rule's own maxFileSize, where it sets one - are skipped, archives among
// them, and the members of the rest are extracted through the same size-gated
// iterator as the keyword checks (general.maxArchiveFileSize per member,
// general.maxTotalArchiveMemory per archive) into a private temp directory.
// The scanner itself never unpacks anything (--max-archive-depth 0).
func isFreeOfSecrets(ctx context.Context, repo structs.Repository, attrs leakAttrs, limits readers.ArchiveLimits, maxContentScan int64, memberFilter *selector.Selector) []structs.Message {
	var messages []structs.Message

	// Partition the repository: oversized files are acknowledged in one
	// aggregate skip, archives go through extraction. The excluded paths are
	// already gone - the rule's selector narrowed the file set before the
	// check ran.
	//
	// The size limit is measured on the archive CONTAINER as on any other file,
	// before anything is extracted from it.
	sizeLimit, limitName := maxContentScan, "maximum"
	if attrs.maxFileSize > 0 && attrs.maxFileSize < sizeLimit {
		sizeLimit, limitName = attrs.maxFileSize, "the rule's maxFileSize"
	}
	var plain, archives []structs.File
	oversized := 0
	for _, f := range repo.Files {
		size := f.Size
		if size <= 0 {
			size = structs.GetFileSize(f.Path)
		}
		if size > sizeLimit {
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
		reason := fmt.Sprintf("Skipped leak scan of %d file(s): file size exceeds %s (%d bytes).", oversized, limitName, sizeLimit)
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
			memberPaths := extractArchivesForLeakScan(ctx, limits, memberFilter, archives, tmpDir, sources, &messages)
			scanPaths = append(scanPaths, memberPaths...)
		}
	}

	if len(scanPaths) == 0 {
		return messages
	}

	// The scan's own timeout nests under the caller's ctx, so a cancelled
	// request also cancels a running scanner; the timeout semantics stand.
	scanCtx, cancel := context.WithTimeout(ctx, time.Duration(attrs.timeoutSeconds)*time.Second)
	defer cancel()

	findings, err := runBetterleaks(scanCtx, attrs.binary, scanPaths, attrs.maxProcs)
	if err != nil {
		if ctx.Err() != nil {
			// The CALLER cancelled (not the scan's own timeout, which is still
			// acknowledged below): a scan the caller stopped did not fail, so
			// the messages collected so far stand, like every other ctx exit.
			return messages
		}
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
//
// memberFilter is the rule's own selector, matched against the member path by
// the iterator. nil means "no filter, admit every member" - a convenience for
// callers that have no lists to honour; production callers pass the selector
// the top-level files were narrowed with.
func extractArchivesForLeakScan(ctx context.Context, limits readers.ArchiveLimits, memberFilter *selector.Selector, archives []structs.File, tmpDir string, sources map[string]structs.File, messages *[]structs.Message) []string {
	var memberPaths []string
	for ai, archive := range archives {
		it := readers.InitArchiveIterator(ctx, archive.Path, archive.Name, limits, memberFilter)
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
