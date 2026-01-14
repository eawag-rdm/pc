package checks

import (
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

	// Use configurable memory limits
	maxFileSize := int(config.General.MaxArchiveFileSize)
	if maxFileSize <= 0 {
		maxFileSize = 10 * 1024 * 1024 // Default to 10MB if not configured
	}

	whitelist := config.Tests["IsFreeOfKeywords"].Whitelist
	blacklist := config.Tests["IsFreeOfKeywords"].Blacklist

	// Use configurable total memory limit
	maxTotalMemory := config.General.MaxTotalArchiveMemory
	if maxTotalMemory <= 0 {
		maxTotalMemory = 100 * 1024 * 1024 // Default to 100MB if not configured
	}

	archiveIterator := readers.InitArchiveIteratorWithMemoryLimit(file.Path, file.Name, maxFileSize, whitelist, blacklist, maxTotalMemory)
	if !archiveIterator.HasFilesToUnpack() {
		return messages
	}
	for archiveIterator.HasNext() {

		archiveIterator.Next()
		fileName, fileContent, _ := archiveIterator.UnpackedFile()

		for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
			var keywordList = argumentSet["keywords"].([]string)
			var info = argumentSet["info"].(string)
			foundKeywordsStr := matchPatternsList(keywordList, fileContent)

			if foundKeywordsStr != "" {
				messages = append(messages, structs.Message{Content: info + " '" + foundKeywordsStr + "'. In archived file: '" + fileName + "'", Source: file})
			}
		}

	}
	return messages
}

func IsFreeOfKeywords(file structs.File, config config.Config) []structs.Message {
	var messages []structs.Message

	// Large file warning removed - processing continues without notification

	// Check file size limit for content scanning
	fileInfo, err := os.Stat(file.Path)
	if err != nil {
		output.GlobalLogger.Warning("Error getting file info '%s': %v", file.Path, err)
		return messages
	}

	// Check if file exceeds the configured maximum size for content scanning
	if fileInfo.Size() > config.General.MaxContentScanFileSize {
		output.GlobalLogger.Info("Skipping content scan of file: '%s' (path: '%s'). File size (%d bytes) exceeds maximum (%d bytes).",
			file.Name, file.Path, fileInfo.Size(), config.General.MaxContentScanFileSize)
		return messages
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
					output.GlobalLogger.Warning("Error streaming file '%s': %v", file.Path, err)
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
				output.GlobalLogger.Warning("Error reading file '%s': %v", file.Path, err)
				return messages
			}
			body := [][]byte{content}

			for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
				var keywordList = argumentSet["keywords"].([]string)
				var info = argumentSet["info"].(string)

				ret := IsFreeOfKeywordsCoreList(file, keywordList, info, body, false)
				if ret != nil {
					messages = append(messages, ret...)
				}
			}
		}
	} else {
		// Handle binary files
		body := tryReadBinary(file)
		for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
			var keywordList = argumentSet["keywords"].([]string)
			var info = argumentSet["info"].(string)

			ret := IsFreeOfKeywordsCoreList(file, keywordList, info, body, true)
			if ret != nil {
				messages = append(messages, ret...)
			}
		}
	}
	return messages
}

func IsFreeOfKeywordsCore(file structs.File, keywords string, info string, body [][]byte, isBinary bool) []structs.Message {
	// Split patterns and delegate to optimized version
	patternList := strings.Split(keywords, "|")
	return IsFreeOfKeywordsCoreList(file, patternList, info, body, isBinary)
}

func IsFreeOfKeywordsCoreList(file structs.File, keywordList []string, info string, body [][]byte, isBinary bool) []structs.Message {
	var messages []structs.Message

	for idx, entry := range body {
		foundKeywordsStr := matchPatternsList(keywordList, entry)
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

// matchPatternsList is an optimized version that takes a pattern slice directly
func matchPatternsList(patternList []string, body []byte) string {
	if len(body) == 0 || len(patternList) == 0 {
		return ""
	}

	// Use fast matcher for pattern detection with original case preservation
	matcher := optimization.GetMatcher(patternList)
	foundMatches := matcher.FindMatchesWithOriginalCase(body)

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

func tryReadBinary(file structs.File) [][]byte {
	if strings.HasSuffix(file.Path, ".xlsx") {
		content, err := readers.ReadXLSXFile(file)
		if err != nil {
			output.GlobalLogger.Warning("Error reading XLSX file '%s': %v", file.Path, err)
			return [][]byte{} // Return empty instead of panicking
		}
		return content
	} else if strings.HasSuffix(file.Path, ".docx") {
		content, err := readers.ReadDOCXFile(file)
		if err != nil {
			output.GlobalLogger.Warning("Error reading DOCX file '%s': %v", file.Path, err)
			return [][]byte{} // Return empty instead of panicking
		}
		return content
	} else if !readers.IsSupportedArchive(file.Name) {
		output.GlobalLogger.Info("Not checking contents of file: '%s' (path: '%s'). The file seems to be binary.", file.Name, file.Path)
	}
	return [][]byte{}
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
