package checks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/optimization"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/structs"
)

var invalidFileNameChars [256]bool

func init() {
	// Control chars (0x00–0x1F)
	for c := byte(0); c < 32; c++ {
		invalidFileNameChars[c] = true
	}
	// Your full set of special chars:
	//  ~ ! @ # $ % ^ & * ( ) ` ; < > ? , [ ] { } ' "
	for _, c := range []byte{
		'~', '!', '@', '#', '$', '%', '^', '&', '*',
		'(', ')', '`', ';', '<', '>', '?', ',',
		'[', ']', '{', '}', '\'', '"',
	} {
		invalidFileNameChars[c] = true
	}
}

// hasFileNameSpecialChars returns a non-empty slice if file.Name contains
// any invalid/special characters.
func hasFileNameSpecialChars(file structs.File) []structs.Message {
	for i := 0; i < len(file.Name); i++ {
		if invalidFileNameChars[file.Name[i]] {
			return []structs.Message{{
				Content: fmt.Sprintf("File name contains invalid character: %q", file.Name[i]),
				Source:  file,
			}}
		}
	}
	return []structs.Message{}
}

func isFileNameTooLong(file structs.File) []structs.Message {
	if len(file.Name) > 64 {
		return []structs.Message{{Content: "File name is too long.", Source: file}}
	}
	return []structs.Message{}
}

// streamChunkSize is both the streamed read size and the streaming threshold:
// files strictly larger than one chunk are streamed, smaller ones are read
// whole. 1MB chunks (increased for better performance).
const streamChunkSize = 1024 * 1024

// streamChunks reads a file too large to hold in one piece and hands each chunk
// to scan together with its lowercase copy. Chunks overlap by 2KB so a keyword
// spanning a boundary is still found. The file is read ONCE however many rules
// scan it. ctx is observed between chunks: a fired ctx stops the read and the
// chunks scanned so far stand.
func streamChunks(ctx context.Context, filePath string, scan func(chunk, lowered []byte)) error {
	const maxFileSize = 2 * 1024 * 1024 * 1024 // 2GB limit for streaming (increased)

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Check file size
	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}
	if fileInfo.Size() > maxFileSize {
		return fmt.Errorf("file too large: %d bytes (max %d)", fileInfo.Size(), maxFileSize)
	}

	buffer := make([]byte, streamChunkSize)
	var overlapBuf [2048]byte // Dedicated tail copy so the combined buffer can be reused
	overlap := overlapBuf[:0]
	// Reused across chunks: rebuilding and re-lowering combined per chunk
	// allocated ~2MB of garbage per 1MB read.
	combined := make([]byte, 0, streamChunkSize+len(overlapBuf))
	lowerScratch := make([]byte, 0, streamChunkSize+len(overlapBuf))

	for {
		if ctx.Err() != nil {
			return nil // partial scan: the messages collected so far stand
		}
		n, err := file.Read(buffer)
		if n == 0 {
			break
		}

		// Combine overlap with new data
		combined = append(append(combined[:0], overlap...), buffer[:n]...)
		lowerScratch = lowerInto(lowerScratch, combined)
		scan(combined, lowerScratch)

		// Keep last 2KB as overlap for next chunk to ensure patterns spanning chunks are caught
		overlapSize := len(overlapBuf)
		if n < overlapSize {
			overlapSize = n
		}
		// overlapSize <= n <= len(combined), so the tail slice is always valid.
		overlap = overlapBuf[:overlapSize]
		copy(overlap, combined[len(combined)-overlapSize:])

		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// lowerInto lowercases src into dst's backing array (reused across chunks,
// grown by append only when the output outgrows it) and returns the result,
// byte-identical to bytes.ToLower: ASCII runs are bulk-copied and lowered
// byte-wise; non-ASCII runes are mapped through unicode.ToLower, and each
// invalid UTF-8 byte becomes one U+FFFD (so the output can be longer than the
// input). The caller must not retain the result past the next call, and dst
// and src must not share a backing array.
func lowerInto(dst, src []byte) []byte {
	dst = dst[:0]
	for i := 0; i < len(src); {
		if src[i] < utf8.RuneSelf {
			// Bulk-copy the ASCII run, then lower it in place.
			j := i + 1
			for j < len(src) && src[j] < utf8.RuneSelf {
				j++
			}
			mark := len(dst)
			dst = append(dst, src[i:j]...)
			run := dst[mark:]
			for k, c := range run {
				if 'A' <= c && c <= 'Z' {
					run[k] = c + ('a' - 'A')
				}
			}
			i = j
			continue
		}
		// Mirrors bytes.Map(unicode.ToLower, src): utf8.DecodeRune yields one
		// RuneError of width 1 per invalid byte.
		r, wid := utf8.DecodeRune(src[i:])
		dst = utf8.AppendRune(dst, unicode.ToLower(r))
		i += wid
	}
	return dst
}

func hasOnlyASCII(file structs.File) []structs.Message {
	var nonASCII string
	for _, r := range file.Name {
		if r > unicode.MaxASCII {
			nonASCII += string(r)
		}
	}
	if nonASCII != "" {
		return []structs.Message{{Content: "File name contains non-ASCII character: " + nonASCII, Source: file}}
	}
	return []structs.Message{}
}

// Return true if c is a space character; otherwise, return false.
func hasNoWhiteSpace(file structs.File) []structs.Message {
	for i := 0; i < len(file.Name); i++ {
		if file.Name[i] == ' ' {
			return []structs.Message{{Content: "File name contains spaces.", Source: file}}
		}
	}
	return []structs.Message{}
}

// Common text file extensions
var textExtensions = map[string]bool{
	".txt": true, ".log": true, ".md": true, ".csv": true, ".json": true,
	".xml": true, ".html": true, ".css": true, ".js": true, ".py": true,
	".go": true, ".java": true, ".cpp": true, ".c": true, ".h": true,
	".sql": true, ".yml": true, ".yaml": true, ".toml": true, ".ini": true,
	".conf": true, ".config": true, ".properties": true, ".sh": true,
	".bat": true, ".ps1": true, ".rb": true, ".php": true, ".pl": true,
}

// isTextFile checks if a file is a text file using DetectContentType from the http package.
// Enhanced to handle large files and improve detection accuracy.
func isTextFile(filePath string) (bool, error) {
	// Check file extension first for common text types
	ext := strings.ToLower(filepath.Ext(filePath))
	if textExtensions[ext] {
		return true, nil
	}

	// Open the file for reading
	file, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer file.Close()

	// Read a larger sample for better detection
	const sampleSize = 8192 // Increased from 512 to 8KB
	buffer := make([]byte, sampleSize)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return false, err
	}

	if n == 0 {
		return true, nil // Empty files are considered text
	}

	// Check for null bytes (common in binary files)
	for i := 0; i < n; i++ {
		if buffer[i] == 0 {
			return false, nil // Binary file likely
		}
	}

	// Use HTTP detection as secondary check
	filetype := http.DetectContentType(buffer[:n])
	if strings.HasPrefix(filetype, "text/") {
		return true, nil
	}

	// Additional heuristic: check if most bytes are printable ASCII or common UTF-8
	printableCount := 0
	for i := 0; i < n; i++ {
		b := buffer[i]
		if (b >= 32 && b <= 126) || b == '\t' || b == '\n' || b == '\r' || b >= 128 {
			printableCount++
		}
	}

	// If more than 95% of sampled bytes are printable, consider it text
	textRatio := float64(printableCount) / float64(n)
	return textRatio >= 0.95, nil
}

// keywordSet is one bound keyword parameter set: the matcher built at load and
// the info string every finding of the set is reported with. Keyword lists are
// never merged into one automaton - each match must stay attributable to its
// own info string - so there is one matcher per set, not per rule.
//
// It doubles as that set's dedup key: optimization.GetMatcher interns matchers
// by keyword list, so two sets with the same keywords hold the same pointer and
// two rules that bound the same set are one unit.
type keywordSet struct {
	matcher *optimization.FastMatcher
	info    string
}

// bindKeywords binds one keyword rule: its matchers, built here and never
// looked up again. The scan bounds are the batch's, not the rule's - one
// acquisition serves every rule.
func bindKeywords(spec config.RuleSpec, _ *config.GeneralConfig) (*BoundRule, error) {
	sets, err := ruleSets(spec, "keywords", "info")
	if err != nil {
		return nil, err
	}
	units := make([]unit, 0, len(sets))
	for i, set := range sets {
		keywords, err := stringList(set, "keywords", i)
		if err != nil {
			return nil, err
		}
		info, err := stringParam(set, "info", i)
		if err != nil {
			return nil, err
		}
		if len(keywords) == 0 {
			continue // an empty list matched nothing before and matches nothing now
		}
		bound := keywordSet{matcher: optimization.GetMatcher(keywords), info: info}
		units = append(units, unit{
			scan: func(ctx context.Context, _ structs.File, body, lowered [][]byte, report reporting) []structs.Message {
				return scanKeywords(ctx, bound, body, lowered, report)
			},
			key: unitKeyOf(bound),
		})
	}
	return &BoundRule{
		Rule:  spec.Name,
		Rules: []string{spec.Name},
		units: units,
	}, nil
}

// scanKeywords matches one bound set against every body entry and reports each
// finding the way the acquisition demands. ctx is observed per body entry
// (OOXML blocks, PDF pages) - never inside the byte scan - and a fired ctx
// returns the findings collected so far.
//
// The findings carry NO Source: boxing a File into one costs an allocation, so
// the caller stamps it through sourceAll and the acquisition pays for one box
// however many units report on it.
func scanKeywords(ctx context.Context, set keywordSet, body, lowered [][]byte, report reporting) []structs.Message {
	var messages []structs.Message
	for idx, entry := range body {
		if ctx.Err() != nil {
			return messages
		}
		if len(entry) == 0 {
			continue
		}
		matches := set.matcher.FindMatchesWithOriginalCaseLowered(entry, lowered[idx])
		if len(matches) == 0 {
			continue
		}
		if report == reportEach {
			for _, match := range matches {
				messages = append(messages, structs.Message{Content: set.info + " '" + match + "'"})
			}
			continue
		}
		found := joinMatches(matches)
		switch report {
		case reportIndexed:
			messages = append(messages, structs.Message{Content: set.info + " '" + found + "' in sheet/paragraph/table " + fmt.Sprintf("%d", idx)})
		case reportPaged:
			messages = append(messages, structs.Message{Content: fmt.Sprintf("%s '%s' (page %d)", set.info, found, idx+1)})
		default:
			messages = append(messages, structs.Message{Content: set.info + " '" + found + "'"})
		}
	}
	return messages
}

// sourceAll stamps the file one acquisition scanned onto its findings, boxing
// it at most once per acquisition however many rules reported: src carries the
// box across the calls of one acquisition, and is boxed on the first finding
// rather than eagerly, so a clean file allocates nothing.
func sourceAll(src *structs.Source, file structs.File, found []structs.Message) []structs.Message {
	if len(found) == 0 {
		return found
	}
	if *src == nil {
		*src = file
	}
	for i := range found {
		found[i].Source = *src
	}
	return found
}

// joinMatches deduplicates the findings of one entry and formats them the way a
// message lists them.
func joinMatches(matches []string) string {
	keywordSet := make(map[string]struct{})
	var foundKeywordsStr string
	for _, match := range matches {
		if _, exists := keywordSet[match]; !exists {
			if foundKeywordsStr != "" {
				foundKeywordsStr += "', '"
			}
			foundKeywordsStr += match
			keywordSet[match] = struct{}{}
		}
	}
	return foundKeywordsStr
}

// runKeywords is RunFile for the keyword check: the file's own content at file
// scope, its members at archive-member scope. Either way the content is
// acquired ONCE here and handed to every rule that matched.
func runKeywords(ctx context.Context, file structs.File, scope Scope, batch *Batch, rules []*BoundRule) []structs.Message {
	if scope == ScopeArchiveMember {
		return keywordsInArchive(ctx, file, batch, rules)
	}
	return keywordsInFile(ctx, file, batch, rules)
}

// oversizeSkip acknowledges content the whole-file gate refuses, so every
// output surfaces that it was not scanned.
func oversizeSkip(file structs.File, subject string, size, limit int64) structs.Message {
	reason := fmt.Sprintf("Skipped content scan of %s: file size (%d bytes) exceeds maximum (%d bytes).", subject, size, limit)
	return structs.Message{Content: reason, Source: file, Skipped: true, Reason: reason}
}

func keywordsInArchive(ctx context.Context, file structs.File, batch *Batch, rules []*BoundRule) []structs.Message {
	var messages []structs.Message

	// Check if the archive file itself exceeds the configured maximum size for content scanning
	// This prevents conflicting behavior where archive is listed as "skipped" but contents still scanned
	fileInfo, err := os.Stat(file.Path)
	if err != nil {
		output.GlobalLogger.FileWarning(file.GetDisplayName(), "Error getting file info '%s': %v", file.Path, err)
		return messages
	}

	// The acquisition reads its bounds from the batch: they belong to the whole
	// (check, scope) entry, not to whichever rule happens to be first.
	if fileInfo.Size() > batch.maxContentScan {
		return append(messages, oversizeSkip(file, "archive", fileInfo.Size(), batch.maxContentScan))
	}

	archiveIterator := readers.InitArchiveIterator(file.Path, file.Name, batch.limits, batch.admit)
	defer archiveIterator.Close()
	if !archiveIterator.HasFilesToUnpack() {
		// Even with no scannable members, the iterator may have skipped members
		// (too large / over memory budget). Surface those acknowledgements.
		messages = append(messages, archiveIterator.SkipMessages()...)
		return messages
	}

	// Get the archive's display name for consistent output
	archiveDisplayName := file.GetDisplayName()
	// Whether the per-rule member gates still have to run is a property of what
	// the iterator was given (batch.admit), which Compile decided over the whole
	// plan - never of how many rules this archive happened to match.
	perRule := batch.perRule
	// One body per archive, not per member: a scan reads it and never retains it.
	body, lowered := make([][]byte, 1), make([][]byte, 1)
	var memberLower []byte

	for archiveIterator.HasNext() {
		// Cancellation is observed per MEMBER: the member in progress finishes,
		// the walk stops, and the messages (plus the iterator's
		// acknowledgements below) stand as partial results.
		if ctx.Err() != nil {
			break
		}
		archiveIterator.Next()
		fileName, fileContent, fileSize := archiveIterator.UnpackedFile()
		// Lower once per member, into a scratch reused across members; every
		// rule and every keyword set scans the shared copy.
		memberLower = lowerInto(memberLower, fileContent)
		body[0], lowered[0] = fileContent, memberLower

		// The member's File is built only when a unit actually reports: it costs
		// more than scanning a small member, and no unit here reads the file it
		// is handed (the member gate matched the member path directly), so the
		// scans are given the zero File. The scan leaves Source unset, so this
		// stamp is what attributes the finding to the member it was found in.
		var archivedFile structs.File
		var src structs.Source // boxed once per member, shared by all findings
		built := false

		for _, rule := range rules {
			if perRule && !rule.matchMember(fileName) {
				continue
			}
			for _, u := range rule.units {
				found := u.scan(ctx, structs.File{}, body, lowered, reportJoined)
				if len(found) == 0 {
					continue
				}
				if !built {
					archivedFile = structs.ToFileWithDisplay(
						file.Path,          // path stays as archive path
						fileName,           // name is the path within archive
						fileName,           // display name
						int64(fileSize),    // size
						"",                 // suffix (auto-detected)
						archiveDisplayName, // archive name reference
					)
					archivedFile.RelPath = fileName // the member path, verbatim
					src = archivedFile
					built = true
				}
				for i := range found {
					found[i].Source = src
				}
				messages = append(messages, tag(rule.Rules, found)...)
			}
		}
	}

	// Thread out skip acknowledgements collected while iterating archive members.
	messages = append(messages, archiveIterator.SkipMessages()...)
	return messages
}

func keywordsInFile(ctx context.Context, file structs.File, batch *Batch, rules []*BoundRule) []structs.Message {
	var messages []structs.Message

	// Large file warning removed - processing continues without notification

	// Check file size limit for content scanning
	fileInfo, err := os.Stat(file.Path)
	if err != nil {
		output.GlobalLogger.FileWarning(file.GetDisplayName(), "Error getting file info '%s': %v", file.Path, err)
		return messages
	}

	// Check if file exceeds the configured maximum size for content scanning.
	// Emit a skip acknowledgement Message so every output (CLI plain, TUI, JSON)
	// surfaces that the file's content was not scanned.
	if fileInfo.Size() > batch.maxContentScan {
		return append(messages, oversizeSkip(file, "file", fileInfo.Size(), batch.maxContentScan))
	}

	// Known OOXML containers route by extension BEFORE the text sniff:
	// deterministic, saves the sniff read, and a container that happens to
	// pass the printable heuristic is never raw-scanned. scanOOXMLFile falls
	// through (handled = false) when the file does not open as a zip, so a
	// text file misnamed .xlsx keeps being scanned as text below.
	if kind := readers.OOXMLKind(file.Path); kind != "" {
		if msgs, handled := scanOOXMLFile(ctx, file, batch, rules, kind); handled {
			return append(messages, msgs...)
		}
	}
	if strings.EqualFold(filepath.Ext(file.Path), ".pdf") {
		if msgs, handled := scanPDFFile(ctx, file, batch, rules); handled {
			return append(messages, msgs...)
		}
	}

	isText, err := isTextFile(file.Path)
	if err != nil {
		return messages
	}

	if isText {
		// Stream files larger than one chunk (reduced threshold for better performance)
		if fileInfo.Size() > streamChunkSize {
			return append(messages, streamKeywords(ctx, file, batch, rules)...)
		}
		// Use regular reading for smaller files
		content, err := os.ReadFile(file.Path)
		if err != nil {
			output.GlobalLogger.FileWarning(file.GetDisplayName(), "Error reading file '%s': %v", file.Path, err)
			return messages
		}
		body := [][]byte{content}
		lowered := lowerAll(body)
		var src structs.Source // the file boxed once, shared by every finding
		messages = scanUnits(ctx, messages, file, batch, rules, body, lowered, reportJoined, &src)
	} else {
		// Handle binary files
		body, skipMsg := tryReadBinary(file)
		if skipMsg != nil {
			messages = append(messages, *skipMsg)
		}
		lowered := lowerAll(body)
		// tryReadBinary hands over an empty body, so no unit can report here and
		// there is nothing to stamp.
		messages = scanUnits(ctx, messages, file, batch, rules, body, lowered, reportIndexed, nil)
	}
	return messages
}

// streamKey identifies one finding of a streamed file across its chunks: the
// unit that reported it - its position in this file's walk - and its lowered
// content. The pair IS the key, as a comparable struct - joining the two into
// one string allocated per finding per chunk, and every chunk re-finds what the
// chunks before it already reported. Keying on the unit keeps two units'
// verdicts on one file two findings, the way every acquisition that does not
// deduplicate at all reports them; two rules that bound the SAME unit report
// once, naming both.
type streamKey struct {
	unit    int
	content string
}

// keepNew appends the findings one unit reported that the chunks before it did
// not already carry.
func keepNew(messages []structs.Message, seen map[streamKey]struct{}, unit int, found []structs.Message) []structs.Message {
	for _, message := range found {
		// The content half is the LOWERED message: findings carry the original
		// case of the chunk they were found in, so "Admin" in one chunk and
		// "ADMIN" in another are one finding, once.
		key := streamKey{unit: unit, content: strings.ToLower(message.Content)}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		messages = append(messages, message)
	}
	return messages
}

// streamUnit is one unit of a streamed file's walk, resolved before the first
// chunk: which units run, and the names their findings carry, are properties of
// the FILE - and a partially matched unit's names are BUILT, so resolving them
// per chunk would rebuild them for every chunk of a multi-gigabyte file.
type streamUnit struct {
	unit  *unit
	names []string
}

// streamKeywords scans a text file too large to hold: it is read ONCE, chunk by
// chunk, and every unit sees every chunk. Findings are deduplicated across
// chunks, so a keyword on every line is still reported once. It walks the units
// itself rather than through scanUnits, because the dedup happens per message.
func streamKeywords(ctx context.Context, file structs.File, batch *Batch, rules []*BoundRule) []structs.Message {
	var messages []structs.Message
	var src structs.Source // the file boxed once, shared by every finding
	seen := make(map[streamKey]struct{})
	// One wrapper pair for the whole file, not one per chunk: a scan borrows
	// both and never retains them.
	body, loweredBody := make([][]byte, 1), make([][]byte, 1)
	merged := batch.merged
	active := merged.active(rules)
	scans := make([]streamUnit, 0, len(merged.units))
	for i := range merged.units {
		u := &merged.units[i]
		hit := u.mask & active
		if hit == 0 {
			continue
		}
		scans = append(scans, streamUnit{unit: u, names: u.attribution(hit)})
	}
	err := streamChunks(ctx, file.Path, func(chunk, lowered []byte) {
		body[0], loweredBody[0] = chunk, lowered
		for i, s := range scans {
			found := sourceAll(&src, file, s.unit.scan(ctx, file, body, loweredBody, reportEach))
			messages = keepNew(messages, seen, i, tag(s.names, found))
		}
	})
	if err != nil {
		output.GlobalLogger.FileWarning(file.GetDisplayName(), "Error streaming file '%s': %v", file.Path, err)
	}
	return messages
}

// archiveLimits packs the config-derived effective limits into the readers
// struct (readers stays config-free, so the tuple crosses here).
func archiveLimits(general *config.GeneralConfig) readers.ArchiveLimits {
	memberSize, totalMemory, memberCount := general.ArchiveLimits()
	return readers.ArchiveLimits{
		MaxMemberSize:  memberSize,
		MaxTotalMemory: totalMemory,
		MaxMemberCount: memberCount,
		MaxPDFPages:    general.EffectiveMaxPDFPages(),
		MaxPDFFileSize: general.EffectiveMaxPDFFileSize(),
	}
}

// lowerAll lowercases each body entry once so every keyword set scans the
// shared copies instead of re-lowering per set.
func lowerAll(body [][]byte) [][]byte {
	lowered := make([][]byte, len(body))
	for i, entry := range body {
		lowered[i] = bytes.ToLower(entry)
	}
	return lowered
}

// scanOOXMLFile extracts and keyword-scans a top-level OOXML container.
// handled = false means the file did not open as a zip container at all and
// the caller's generic text/binary flow should decide instead.
func scanOOXMLFile(ctx context.Context, file structs.File, batch *Batch, rules []*BoundRule, kind string) ([]structs.Message, bool) {
	limits := batch.limits
	var content [][]byte
	var truncated bool
	var err error
	if kind == "xlsx" {
		content, truncated, err = readers.ReadXLSXFile(file, limits)
	} else {
		content, truncated, err = readers.ReadDOCXFile(file, limits)
	}
	if errors.Is(err, readers.ErrOOXMLNotZip) {
		return nil, false
	}

	var messages []structs.Message
	if errors.Is(err, readers.ErrOOXMLDeclaredSize) {
		reason := fmt.Sprintf("Skipped content scan of file: container declares more data than allowed (member limit %d bytes, total limit %d bytes).", limits.MaxMemberSize, limits.MaxTotalMemory)
		return append(messages, structs.Message{Content: reason, Source: file, Skipped: true, Reason: reason}), true
	}
	if err != nil {
		output.GlobalLogger.FileWarning(file.GetDisplayName(), "Error reading %s file '%s': %v", kind, file.Path, err)
		reason := "Skipped content scan of file: container could not be parsed."
		return append(messages, structs.Message{Content: reason, Source: file, Skipped: true, Reason: reason}), true
	}
	if truncated {
		// Partial content WAS scanned - walk-cap wording, not "Skipped".
		reason := fmt.Sprintf("Stopped content scan of file: extracted text exceeds %d bytes; scanned the first part only.", limits.MaxMemberSize)
		messages = append(messages, structs.Message{Content: reason, Source: file, Skipped: true, Reason: reason})
	}

	lowered := lowerAll(content)
	var src structs.Source // the file boxed once, shared by every finding
	return scanUnits(ctx, messages, file, batch, rules, content, lowered, reportIndexed, &src), true
}

// pdfLimits packs the effective PDF extraction bounds: the new page knob,
// the shared per-member text cap, and the internal wall-time backstop.
func pdfLimits(limits readers.ArchiveLimits) readers.PDFLimits {
	return readers.PDFLimits{
		MaxFileBytes: limits.MaxPDFFileSize,
		MaxPages:     limits.MaxPDFPages,
		MaxTextBytes: limits.MaxMemberSize,
		Timeout:      readers.DefaultPDFTimeout,
	}
}

// totalTextLen sums extracted page blocks (nil placeholders keep page
// numbering aligned, so len(pages) alone says nothing about content).
func totalTextLen(pages [][]byte) int {
	n := 0
	for _, p := range pages {
		n += len(p)
	}
	return n
}

// pdfTooLargeReason acknowledges documents past the configured size gate;
// shared by the pre-read Stat gate and the ReadPDF sentinel mapping.
func pdfTooLargeReason(limit int64) string {
	return fmt.Sprintf("Skipped content scan of file: PDF exceeds the maximum PDF size (%d bytes); not scanned.", limit)
}

// scanPDFFile extracts and keyword-scans a top-level PDF. handled = false
// means the bytes carry no PDF magic (PDFium accepts "%PDF" at any offset up
// to 1024 - a prefix-only check would be a one-byte-prepend evasion vector)
// and the generic text/binary flow should decide instead. The magic is
// sniffed from the first 1028 bytes (1024 + the 4 magic bytes) BEFORE the
// whole-file read, so a large non-PDF named .pdf costs ~1 KiB of I/O here
// instead of a full read that gets discarded.
func scanPDFFile(ctx context.Context, file structs.File, batch *Batch, rules []*BoundRule) ([]structs.Message, bool) {
	ack := func(reason string) []structs.Message {
		return []structs.Message{{Content: reason, Source: file, Skipped: true, Reason: reason}}
	}
	readAck := func(err error) []structs.Message {
		output.GlobalLogger.FileWarning(file.GetDisplayName(), "Error reading file '%s': %v", file.Path, err)
		return ack("Skipped content scan of file: file could not be read.")
	}

	f, err := os.Open(file.Path)
	if err != nil {
		return readAck(err), true
	}
	defer f.Close()

	head := make([]byte, readers.PDFMagicWindow)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return readAck(err), true
	}
	head = head[:n]
	if !bytes.Contains(head, readers.PDFMagic) {
		return nil, false
	}

	limits := pdfLimits(batch.limits)

	// Stat failure is fail-closed: without a size the oversize gate cannot
	// run, and an unbounded read is exactly what it exists to prevent.
	st, serr := f.Stat()
	if serr != nil {
		return readAck(serr), true
	}
	// Gate on size before reading the body at all: an over-limit PDF costs
	// one stat, not a full read.
	if st.Size() > limits.MaxFileBytes {
		return ack(pdfTooLargeReason(limits.MaxFileBytes)), true
	}
	// Exact-size buffer, filled in one read: bytes.Buffer.ReadFrom would
	// reallocate to 2x and memcpy the whole document (it always grows by
	// MinRead past the end).
	data := make([]byte, st.Size())
	copy(data, head)
	if int64(len(head)) < st.Size() {
		rest, err := io.ReadFull(f, data[len(head):])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return readAck(err), true
		}
		data = data[:len(head)+rest] // file shrank between stat and read
	}

	pages, truncated, err := readers.ReadPDF(data, limits)

	var messages []structs.Message
	switch {
	case errors.Is(err, readers.ErrPDFTooLarge):
		return ack(pdfTooLargeReason(limits.MaxFileBytes)), true
	case errors.Is(err, readers.ErrPDFTooManyPages):
		return ack(fmt.Sprintf("Skipped content scan of file: PDF exceeds the maximum page count (%d); not scanned.", limits.MaxPages)), true
	case errors.Is(err, readers.ErrPDFRuntime):
		output.GlobalLogger.FileWarning(file.GetDisplayName(), "PDF engine unavailable: %v", err)
		return ack("Skipped content scan of file: PDF engine unavailable."), true
	case errors.Is(err, readers.ErrPDFPassword):
		return ack("Skipped content scan of file: PDF is password-protected."), true
	case errors.Is(err, readers.ErrPDFTimeout):
		return ack("Skipped content scan of file: PDF extraction timed out."), true
	case err != nil:
		output.GlobalLogger.FileWarning(file.GetDisplayName(), "Error reading PDF '%s': %v", file.Path, err)
		return ack("Skipped content scan of file: PDF could not be parsed."), true
	}
	if truncated {
		reason := fmt.Sprintf("Stopped content scan of file: PDF exceeds %d pages or %d extracted bytes; scanned the first part only.", limits.MaxPages, limits.MaxTextBytes)
		messages = append(messages, structs.Message{Content: reason, Source: file, Skipped: true, Reason: reason})
	}
	if totalTextLen(pages) == 0 {
		// Scanned/image-only PDF: the highest-risk shape (secrets live in
		// the image), so it must not read as "scanned and clean".
		return ack("Skipped content scan of file: PDF contains no extractable text (image-only or scanned)."), true
	}

	lowered := lowerAll(pages)
	var src structs.Source // the file boxed once, shared by every finding
	return scanUnits(ctx, messages, file, batch, rules, pages, lowered, reportPaged, &src), true
}

// tryReadBinary acknowledges genuine binary files (not archives - those go
// through the archive checks). OOXML containers are routed before the text
// sniff and never reach this point.
func tryReadBinary(file structs.File) ([][]byte, *structs.Message) {
	if !readers.IsSupportedArchive(file.Name) {
		skip := structs.Message{
			Content: "Binary file detected",
			Source:  file,
			Skipped: true,
			Reason:  "Binary file detected",
		}
		return [][]byte{}, &skip
	}
	return [][]byte{}, nil
}

// bindValidName binds one name rule: one unit per parameter set, over the
// disallowed-name list type-checked here and never asserted again.
func bindValidName(spec config.RuleSpec, _ *config.GeneralConfig) (*BoundRule, error) {
	sets, err := ruleSets(spec, "disallowed_names")
	if err != nil {
		return nil, err
	}
	units := make([]unit, 0, len(sets))
	for i, set := range sets {
		disallowed, err := stringList(set, "disallowed_names", i)
		if err != nil {
			return nil, err
		}
		units = append(units, unit{
			scan: func(_ context.Context, file structs.File, _, _ [][]byte, _ reporting) []structs.Message {
				return isValidNameCore(file, disallowed)
			},
			// The list itself, quoted at load: two rules forbidding the same
			// names are one unit. Quoting is what keeps that faithful - a
			// separator alone spells [] and [""] the same way, and they are
			// opposites, "" being a suffix of every name.
			key: unitKeyOf(fmt.Sprintf("%q", disallowed)),
		})
	}
	return &BoundRule{
		Rule:  spec.Name,
		Rules: []string{spec.Name},
		units: units,
	}, nil
}

func isValidNameCore(file structs.File, invalidFileNames []string) []structs.Message {

	var folders []string
	var name string
	var messages []structs.Message

	name = file.Name
	// Check if the file name is a path and if it is, split it
	if strings.Contains(file.Name, "/") || strings.Contains(file.Name, "\\") {
		folders = strings.Split(file.Name, "/")
		name = folders[len(folders)-1]
		// remove the file name from the path
		folders = folders[:len(folders)-1]
	}

	for _, invalidFileName := range invalidFileNames {
		// Check 'exact' match
		if strings.EqualFold(name, invalidFileName) {
			messages = append(messages, structs.Message{Content: "File or Folder has an invalid name: " + file.Name, Source: file})
		} else if strings.HasSuffix(name, invalidFileName) {
			messages = append(messages, structs.Message{Content: "File has an invalid suffix: " + file.Name, Source: file})
		}
		if len(folders) > 0 {
			for _, folder := range folders {
				if strings.EqualFold(folder, invalidFileName) {
					messages = append(messages, structs.Message{Content: "File or Folder has an invalid name: " + file.Name, Source: file})
				}
			}
		}
	}
	return messages
}
