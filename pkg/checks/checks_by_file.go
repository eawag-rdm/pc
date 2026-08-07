package checks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

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

// HasFileNameSpecialChars returns a non-empty slice if file.Name contains
// any invalid/special characters.
func HasFileNameSpecialChars(file structs.File, cfg config.Config) []structs.Message {
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

func IsFileNameTooLong(file structs.File, config config.Config) []structs.Message {
	if len(file.Name) > 64 {
		return []structs.Message{{Content: "File name is too long.", Source: file}}
	}
	return []structs.Message{}
}

// streamingReadFile reads a file in chunks and applies pattern matching
// This is more memory-efficient for large files
// streamingReadFileList is an optimized version that takes a pattern slice directly
func streamingReadFileList(filePath string, patternList []string) ([]string, error) {
	const maxFileSize = 2 * 1024 * 1024 * 1024 // 2GB limit for streaming (increased)
	const chunkSize = 1024 * 1024              // 1MB chunks (increased for better performance)

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Check file size
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}

	// Use fast matcher directly with pattern list
	if len(patternList) == 0 {
		return []string{}, nil
	}

	matcher := optimization.GetMatcher(patternList)

	// For small files (under 1MB), read normally
	if fileInfo.Size() < chunkSize {
		content, err := io.ReadAll(file)
		if err != nil {
			return nil, err
		}
		matches := matcher.FindMatches(content)
		return matches, nil
	}

	// For larger files, use streaming
	if fileInfo.Size() > maxFileSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", fileInfo.Size(), maxFileSize)
	}

	foundMatches := make(map[string]struct{})
	buffer := make([]byte, chunkSize)
	overlap := make([]byte, 0, 4096) // Increased overlap for better pattern detection

	for {
		n, err := file.Read(buffer)
		if n == 0 {
			break
		}

		// Combine overlap with new data
		combined := append(overlap, buffer[:n]...)

		// Check for patterns in combined data using fast matcher
		matches := matcher.FindMatches(combined)
		for _, match := range matches {
			foundMatches[match] = struct{}{}
		}

		// Keep last 2KB as overlap for next chunk to ensure patterns spanning chunks are caught
		overlapSize := 2048
		if n < overlapSize {
			overlapSize = n
		}
		if len(combined) >= overlapSize {
			overlap = combined[len(combined)-overlapSize:]
		} else {
			overlap = combined
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	// Convert map to slice
	result := make([]string, 0, len(foundMatches))
	for match := range foundMatches {
		result = append(result, match)
	}

	return result, nil
}

func HasOnlyASCII(file structs.File, config config.Config) []structs.Message {
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
func HasNoWhiteSpace(file structs.File, config config.Config) []structs.Message {
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

func IsArchiveFreeOfKeywords(file structs.File, config config.Config) []structs.Message {
	var messages []structs.Message

	// Check if the archive file itself exceeds the configured maximum size for content scanning
	// This prevents conflicting behavior where archive is listed as "skipped" but contents still scanned
	fileInfo, err := os.Stat(file.Path)
	if err != nil {
		output.GlobalLogger.FileWarning(file.GetDisplayName(), "Error getting file info '%s': %v", file.Path, err)
		return messages
	}

	if fileInfo.Size() > config.General.MaxContentScanFileSize {
		// Archive too large for content scanning. Emit a skip acknowledgement so every
		// output (CLI plain, TUI, JSON) surfaces that the archive was not scanned.
		reason := fmt.Sprintf("Skipped content scan of archive: file size (%d bytes) exceeds maximum (%d bytes).", fileInfo.Size(), config.General.MaxContentScanFileSize)
		messages = append(messages, structs.Message{
			Content: reason,
			Source:  file,
			Skipped: true,
			Reason:  reason,
		})
		return messages
	}

	whitelist := config.Tests["IsFreeOfKeywords"].Whitelist
	blacklist := config.Tests["IsFreeOfKeywords"].Blacklist

	archiveIterator := readers.InitArchiveIterator(file.Path, file.Name, archiveLimits(config), whitelist, blacklist)
	defer archiveIterator.Close()
	if !archiveIterator.HasFilesToUnpack() {
		// Even with no scannable members, the iterator may have skipped members
		// (too large / over memory budget). Surface those acknowledgements.
		messages = append(messages, archiveIterator.SkipMessages()...)
		return messages
	}

	// Get the archive's display name for consistent output
	archiveDisplayName := file.GetDisplayName()

	for archiveIterator.HasNext() {

		archiveIterator.Next()
		fileName, fileContent, fileSize := archiveIterator.UnpackedFile()
		// Lower once per member; every keyword set scans the shared copy.
		loweredContent := bytes.ToLower(fileContent)

		for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
			var keywordList = argumentSet["keywords"].([]string)
			var info = argumentSet["info"].(string)
			foundKeywordsStr := matchPatternsListLowered(keywordList, fileContent, loweredContent)

			if foundKeywordsStr != "" {
				// Create a File struct for the archived file with proper archive reference
				archivedFile := structs.ToFileWithDisplay(
					file.Path,          // path stays as archive path
					fileName,           // name is the path within archive
					fileName,           // display name
					int64(fileSize),    // size
					"",                 // suffix (auto-detected)
					archiveDisplayName, // archive name reference
				)
				messages = append(messages, structs.Message{
					Content: info + " '" + foundKeywordsStr + "'",
					Source:  archivedFile,
				})
			}
		}

	}

	// Thread out skip acknowledgements collected while iterating archive members.
	messages = append(messages, archiveIterator.SkipMessages()...)
	return messages
}

func IsFreeOfKeywords(file structs.File, config config.Config) []structs.Message {
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
	if fileInfo.Size() > config.General.MaxContentScanFileSize {
		reason := fmt.Sprintf("Skipped content scan of file: file size (%d bytes) exceeds maximum (%d bytes).", fileInfo.Size(), config.General.MaxContentScanFileSize)
		messages = append(messages, structs.Message{
			Content: reason,
			Source:  file,
			Skipped: true,
			Reason:  reason,
		})
		return messages
	}

	// Known OOXML containers route by extension BEFORE the text sniff:
	// deterministic, saves the sniff read, and a container that happens to
	// pass the printable heuristic is never raw-scanned. scanOOXMLFile falls
	// through (handled = false) when the file does not open as a zip, so a
	// text file misnamed .xlsx keeps being scanned as text below.
	if kind := readers.OOXMLKind(file.Path); kind != "" {
		if msgs, handled := scanOOXMLFile(file, config, kind); handled {
			return append(messages, msgs...)
		}
	}
	if strings.EqualFold(filepath.Ext(file.Path), ".pdf") {
		if msgs, handled := scanPDFFile(file, config); handled {
			return append(messages, msgs...)
		}
	}

	isText, err := isTextFile(file.Path)
	if err != nil {
		return messages
	}

	if isText {
		// Use streaming for files larger than 1MB (reduced threshold for better performance)
		if fileInfo.Size() > 1024*1024 {
			for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
				var keywordList = argumentSet["keywords"].([]string)
				var info = argumentSet["info"].(string)

				foundMatches, err := streamingReadFileList(file.Path, keywordList)
				if err != nil {
					output.GlobalLogger.FileWarning(file.GetDisplayName(), "Error streaming file '%s': %v", file.Path, err)
					continue
				}

				for _, match := range foundMatches {
					messages = append(messages, structs.Message{
						Content: info + " '" + match + "'",
						Source:  file,
					})
				}
			}
		} else {
			// Use regular reading for smaller files
			content, err := os.ReadFile(file.Path)
			if err != nil {
				output.GlobalLogger.FileWarning(file.GetDisplayName(), "Error reading file '%s': %v", file.Path, err)
				return messages
			}
			body := [][]byte{content}
			lowered := lowerAll(body)

			for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
				var keywordList = argumentSet["keywords"].([]string)
				var info = argumentSet["info"].(string)

				ret := isFreeOfKeywordsCoreLowered(file, keywordList, info, body, lowered, false)
				if ret != nil {
					messages = append(messages, ret...)
				}
			}
		}
	} else {
		// Handle binary files
		body, skipMsg := tryReadBinary(file)
		if skipMsg != nil {
			messages = append(messages, *skipMsg)
		}
		lowered := lowerAll(body)
		for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
			var keywordList = argumentSet["keywords"].([]string)
			var info = argumentSet["info"].(string)

			ret := isFreeOfKeywordsCoreLowered(file, keywordList, info, body, lowered, true)
			if ret != nil {
				messages = append(messages, ret...)
			}
		}
	}
	return messages
}

// archiveLimits packs the config-derived effective limits into the readers
// struct (readers stays config-free, so the tuple crosses here).
func archiveLimits(cfg config.Config) readers.ArchiveLimits {
	memberSize, totalMemory, memberCount := cfg.General.ArchiveLimits()
	return readers.ArchiveLimits{
		MaxMemberSize:  memberSize,
		MaxTotalMemory: totalMemory,
		MaxMemberCount: memberCount,
		MaxPDFPages:    cfg.General.EffectiveMaxPDFPages(),
		MaxPDFFileSize: cfg.General.EffectiveMaxPDFFileSize(),
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

func IsFreeOfKeywordsCoreList(file structs.File, keywordList []string, info string, body [][]byte, isBinary bool) []structs.Message {
	return isFreeOfKeywordsCoreLowered(file, keywordList, info, body, lowerAll(body), isBinary)
}

func isFreeOfKeywordsCoreLowered(file structs.File, keywordList []string, info string, body, lowered [][]byte, isBinary bool) []structs.Message {
	var messages []structs.Message

	for idx, entry := range body {
		foundKeywordsStr := matchPatternsListLowered(keywordList, entry, lowered[idx])
		if foundKeywordsStr != "" {
			if isBinary {
				messages = append(messages, structs.Message{Content: info + " '" + foundKeywordsStr + "' in sheet/paragraph/table " + fmt.Sprintf("%d", idx), Source: file})
			} else {
				messages = append(messages, structs.Message{Content: info + " '" + foundKeywordsStr + "'", Source: file})
			}
		}
	}
	return messages
}

// matchPatternsListLowered scans with a caller-provided lowercase copy of body,
// so loops over several keyword sets lower the content once instead of per set.
func matchPatternsListLowered(patternList []string, body, loweredBody []byte) string {
	if len(body) == 0 || len(patternList) == 0 {
		return ""
	}

	// Use fast matcher for pattern detection with original case preservation
	matcher := optimization.GetMatcher(patternList)
	foundMatches := matcher.FindMatchesWithOriginalCaseLowered(body, loweredBody)

	if len(foundMatches) > 0 {
		// Deduplicate and format results
		keywordSet := make(map[string]struct{})
		var foundKeywordsStr string

		for _, match := range foundMatches {
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

	return ""
}

// scanOOXMLFile extracts and keyword-scans a top-level OOXML container.
// handled = false means the file did not open as a zip container at all and
// the caller's generic text/binary flow should decide instead.
func scanOOXMLFile(file structs.File, config config.Config, kind string) ([]structs.Message, bool) {
	limits := archiveLimits(config)
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
	for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
		var keywordList = argumentSet["keywords"].([]string)
		var info = argumentSet["info"].(string)
		if ret := isFreeOfKeywordsCoreLowered(file, keywordList, info, content, lowered, true); ret != nil {
			messages = append(messages, ret...)
		}
	}
	return messages, true
}

// pdfLimits packs the effective PDF extraction bounds: the new page knob,
// the shared per-member text cap, and the internal wall-time backstop.
func pdfLimits(cfg config.Config) readers.PDFLimits {
	memberSize, _, _ := cfg.General.ArchiveLimits()
	return readers.PDFLimits{
		MaxFileBytes: cfg.General.EffectiveMaxPDFFileSize(),
		MaxPages:     cfg.General.EffectiveMaxPDFPages(),
		MaxTextBytes: memberSize,
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
func scanPDFFile(file structs.File, config config.Config) ([]structs.Message, bool) {
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

	limits := pdfLimits(config)

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
	for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
		var keywordList = argumentSet["keywords"].([]string)
		var info = argumentSet["info"].(string)
		for idx, page := range pages {
			if len(page) == 0 {
				continue
			}
			if found := matchPatternsListLowered(keywordList, page, lowered[idx]); found != "" {
				messages = append(messages, structs.Message{
					Content: fmt.Sprintf("%s '%s' (page %d)", info, found, idx+1),
					Source:  file,
				})
			}
		}
	}
	return messages, true
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

func IsValidName(file structs.File, config config.Config) []structs.Message {
	var messages []structs.Message

	for _, argumentSet := range config.Tests["IsValidName"].KeywordArguments {
		invalidFileNames := argumentSet["disallowed_names"].([]string)
		messages = append(messages, IsValidNameCore(file, invalidFileNames)...)
	}
	return messages
}

func IsValidNameCore(file structs.File, invalidFileNames []string) []structs.Message {

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
