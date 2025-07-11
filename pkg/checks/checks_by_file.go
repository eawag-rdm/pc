package checks

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/structs"
)

/*
This file contains tests that run on single files and do not need any other information. They especially do not need other files.
*/

const (
	SP = 0x20 //      Space
)

// RegexCache provides thread-safe caching of compiled regex patterns
type regexCache struct {
	cache map[string]*regexp.Regexp
	mutex sync.RWMutex
}

var globalRegexCache = &regexCache{
	cache: make(map[string]*regexp.Regexp),
}

// getOrCompile returns a cached compiled regex or compiles and caches a new one
func (rc *regexCache) getOrCompile(pattern string) (*regexp.Regexp, error) {
	// First try to read from cache
	rc.mutex.RLock()
	if compiled, exists := rc.cache[pattern]; exists {
		rc.mutex.RUnlock()
		return compiled, nil
	}
	rc.mutex.RUnlock()

	// Compile the pattern
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	// Store in cache
	rc.mutex.Lock()
	rc.cache[pattern] = compiled
	rc.mutex.Unlock()

	return compiled, nil
}

// streamingReadFile reads a file in chunks and applies pattern matching
// This is more memory-efficient for large files
func streamingReadFile(filePath string, patterns string) ([]string, error) {
	const maxFileSize = 100 * 1024 * 1024 // 100MB limit for streaming
	const chunkSize = 64 * 1024            // 64KB chunks
	
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

	// For small files, read normally
	if fileInfo.Size() < chunkSize {
		content, err := io.ReadAll(file)
		if err != nil {
			return nil, err
		}
		match := matchPatterns(patterns, content)
		if match != "" {
			return []string{match}, nil
		}
		return []string{}, nil
	}

	// For larger files, use streaming
	if fileInfo.Size() > maxFileSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", fileInfo.Size(), maxFileSize)
	}

	var foundMatches []string
	buffer := make([]byte, chunkSize)
	overlap := make([]byte, 0, 1024) // Keep some overlap for patterns spanning chunks
	
	for {
		n, err := file.Read(buffer)
		if n == 0 {
			break
		}
		
		// Combine overlap with new data
		combined := append(overlap, buffer[:n]...)
		
		// Check for patterns in combined data
		match := matchPatterns(patterns, combined)
		if match != "" {
			// Deduplicate matches
			found := false
			for _, existing := range foundMatches {
				if existing == match {
					found = true
					break
				}
			}
			if !found {
				foundMatches = append(foundMatches, match)
			}
		}
		
		// Keep last 512 bytes as overlap for next chunk
		if n >= 512 {
			overlap = buffer[n-512 : n]
		} else {
			overlap = buffer[:n]
		}
		
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	
	return foundMatches, nil
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
		if file.Name[i] == SP {
			return []structs.Message{{Content: "File name contains spaces.", Source: file}}
		}
	}
	return []structs.Message{}
}

// isTextFile checks if a file is a text file using DetectContentType from the http package.
func isTextFile(filePath string) (bool, error) {
	// Open the file for reading
	file, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer file.Close()

	// Read a small sample of the file
	const sampleSize = 512
	buffer := make([]byte, sampleSize)
	n, err := file.Read(buffer)
	if err != nil && err.Error() != "EOF" {
		return false, err
	}

	filetype := http.DetectContentType(buffer[:n])
	if strings.HasPrefix(filetype, "text/") {
		return true, nil
	}
	return false, nil
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
			var keywords = strings.Join(argumentSet["keywords"].([]string), "|")
			var info = argumentSet["info"].(string)
			foundKeywordsStr := matchPatterns(keywords, fileContent)

			if foundKeywordsStr != "" {
				messages = append(messages, structs.Message{Content: info + " '" + foundKeywordsStr + "'. In archived file: '" + fileName + "'", Source: file})
			}
		}

	}
	return messages
}

func IsFreeOfKeywords(file structs.File, config config.Config) []structs.Message {
	var messages []structs.Message

	helpers.WarnForLargeFile(file, 10*1024*1024, "pretty big file, this may take a little longer.")

	isText, err := isTextFile(file.Path)
	if err != nil {
		return messages
	}

	if isText {
		// Use streaming for large text files
		fileInfo, err := os.Stat(file.Path)
		if err != nil {
			fmt.Printf("Error getting file info '%s': %v\n", file.Path, err)
			return messages
		}

		// Use streaming for files larger than 10MB
		if fileInfo.Size() > 10*1024*1024 {
			for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
				var keywords = strings.Join(argumentSet["keywords"].([]string), "|")
				var info = argumentSet["info"].(string)

				foundMatches, err := streamingReadFile(file.Path, keywords)
				if err != nil {
					fmt.Printf("Error streaming file '%s': %v\n", file.Path, err)
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
				fmt.Printf("Error reading file '%s': %v\n", file.Path, err)
				return messages
			}
			body := [][]byte{content}

			for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
				var keywords = strings.Join(argumentSet["keywords"].([]string), "|")
				var info = argumentSet["info"].(string)

				ret := IsFreeOfKeywordsCore(file, keywords, info, body, false)
				if ret != nil {
					messages = append(messages, ret...)
				}
			}
		}
	} else {
		// Handle binary files
		body := tryReadBinary(file)
		for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
			var keywords = strings.Join(argumentSet["keywords"].([]string), "|")
			var info = argumentSet["info"].(string)

			ret := IsFreeOfKeywordsCore(file, keywords, info, body, true)
			if ret != nil {
				messages = append(messages, ret...)
			}
		}
	}
	return messages
}

func IsFreeOfKeywordsCore(file structs.File, keywords string, info string, body [][]byte, isBinary bool) []structs.Message {
	var messages []structs.Message

	for idx, entry := range body {
		foundKeywordsStr := matchPatterns(keywords, entry)
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

func matchPatterns(patterns string, body []byte) string {
	// "(?i)" for case-insensitive matching
	fullPattern := "(?i)" + patterns
	regexp, err := globalRegexCache.getOrCompile(fullPattern)
	if err != nil {
		fmt.Printf("Error compiling regex pattern '%s': %v\n", patterns, err)
		return "" // Return empty string instead of panicking
	}

	if len(body) == 0 {
		return ""
	}

	// Check if any of the keywords are present in the file
	foundKeywords := regexp.FindAll(body, -1)
	if len(foundKeywords) > 0 {
		keywordSet := make(map[string]struct{})
		var foundKeywordsStr string
		for _, keyword := range foundKeywords {
			keywordStr := string(keyword)
			if _, exists := keywordSet[keywordStr]; !exists {
				if foundKeywordsStr != "" {
					foundKeywordsStr += "', '"
				}
				foundKeywordsStr += keywordStr
				keywordSet[keywordStr] = struct{}{}
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
			fmt.Printf("Error reading XLSX file '%s': %v\n", file.Path, err)
			return [][]byte{} // Return empty instead of panicking
		}
		return content
	} else if strings.HasSuffix(file.Path, ".docx") {
		content, err := readers.ReadDOCXFile(file)
		if err != nil {
			fmt.Printf("Error reading DOCX file '%s': %v\n", file.Path, err)
			return [][]byte{} // Return empty instead of panicking
		}
		return content
	} else if !readers.IsSupportedArchive(file.Name) {
		fmt.Printf("Not checking contents of file: '%s'. The file seems to be binary.\n", file.Name)
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
