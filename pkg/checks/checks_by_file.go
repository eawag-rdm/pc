package checks

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/structs"
)

const (
	SP = 0x20 //      Space
)

func HasOnlyASCII(file structs.File, config config.Config) []structs.Message {
	var nonASCII string
	for _, r := range file.Name {
		if r > unicode.MaxASCII {
			nonASCII += string(r)
		}
	}
	if nonASCII != "" {
		return []structs.Message{{Content: "File contains non-ASCII character: " + nonASCII, Source: file}}
	}
	return nil
}

// Return true if c is a space character; otherwise, return false.
func HasNoWhiteSpace(file structs.File, config config.Config) []structs.Message {
	for i := 0; i < len(file.Name); i++ {
		if file.Name[i] == SP {
			return []structs.Message{{Content: "File contains spaces.", Source: file}}
		}
	}
	return nil
}

// isBinaryFile checks if a file is likely a binary or unreadable file.
func isBinaryFile(filePath string) (bool, error) {
	// Open the file for reading
	file, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer file.Close()

	// Read a small sample of the file
	const sampleSize = 2048
	reader := bufio.NewReader(file)
	buffer := make([]byte, sampleSize)

	n, err := reader.Read(buffer)
	if err != nil && err.Error() != "EOF" {
		return false, err
	}

	// Analyze the sample for non-printable characters
	for i := 0; i < n; i++ {
		// Allow common printable characters (ASCII and some Unicode spaces)
		if !unicode.IsPrint(rune(buffer[i])) && !unicode.IsSpace(rune(buffer[i])) {
			return true, nil // Non-printable character found, likely binary
		}
	}
	return false, nil // All characters are printable, likely not binary
}

func IsFreeOfKeywords(file structs.File, config config.Config) []structs.Message {
	var messages []structs.Message

	for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
		// Process argumentSet here
		var keywords = strings.Join(argumentSet["keywords"].([]string), ",")
		var info = argumentSet["info"].(string)

		ret := IsFreeOfKeywordsCore(file, keywords, info)
		if ret != nil {
			messages = append(messages, ret...)
		}
	}
	return messages
}

func IsFreeOfKeywordsCore(file structs.File, keywords string, info string) []structs.Message {
	isBinary, err := isBinaryFile(file.Path)
	if err != nil {
		return nil
	}
	if !isBinary {
		// Open the file for reading
		body, err := os.ReadFile(file.Path)
		if err != nil {
			panic(err)
		}

		foundKeywordsStr := matchPatterns(keywords, body)
		if foundKeywordsStr != "" {
			return []structs.Message{{Content: info + " '" + foundKeywordsStr + "'", Source: file}}
		}
	} else {
		var messages []structs.Message
		content := tryReadXSLX(file)
		for idx, entry := range content {
			foundKeywordsStr := matchPatterns(keywords, entry)
			if foundKeywordsStr != "" {
				messages = append(messages, structs.Message{Content: info + " '" + foundKeywordsStr + "' in sheet " + fmt.Sprintf("%d", idx), Source: file})
			}
		}
		return messages
	}
	return nil
}

func matchPatterns(patterns string, body []byte) string {
	// "(?i)" for case-insensitive matching
	regexp, err := regexp.Compile("(?i)" + patterns)
	if err != nil {
		panic(err)
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

func tryReadXSLX(file structs.File) [][]byte {
	if strings.HasSuffix(file.Path, ".xlsx") {
		content, err := readers.ReadXLSXFile(file.Path)
		if err != nil {
			panic(err)
		}
		return content
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
	for _, invalidFileName := range invalidFileNames {
		if file.Name == invalidFileName {
			return []structs.Message{{Content: "File has an invalid name. " + invalidFileName, Source: file}}
		}
	}
	return nil
}
