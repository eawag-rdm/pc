package checks

import (
	"bufio"
	"os"
	"regexp"
	"unicode"

	"github.com/eawag-rdm/pc/pkg/structs"
)

const (
	SP = 0x20 //      Space
)

func HasOnlyASCII(file structs.File) []structs.Message {
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
func HasNoWhiteSpace(file structs.File) []structs.Message {
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
	const sampleSize = 512
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

func IsFreeOfKeywords(file structs.File, keywords []string, info string) []structs.Message {
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

		// Compile all keywords into a single regex pattern
		pattern := "(" + regexp.QuoteMeta(keywords[0])
		for _, keyword := range keywords[1:] {
			pattern += "|" + regexp.QuoteMeta(keyword)
		}
		pattern += ")"

		regexp, err := regexp.Compile(pattern)
		if err != nil {
			panic(err)
		}

		// Check if any of the keywords are present in the file
		foundKeywords := regexp.FindAll(body, -1)
		if len(foundKeywords) > 0 {
			uniqueKeywords := make(map[string]struct{})
			for _, keyword := range foundKeywords {
				uniqueKeywords[string(keyword)] = struct{}{}
			}

			var foundKeywordsStr string
			for keyword := range uniqueKeywords {
				if foundKeywordsStr != "" {
					foundKeywordsStr += ", "
				}
				foundKeywordsStr += keyword
			}

			return []structs.Message{{Content: info + " " + foundKeywordsStr, Source: file}}
		}
	}
	return nil
}

func IsValidName(file structs.File, invalidFileNames []string) []structs.Message {
	for _, invalidFileName := range invalidFileNames {
		if file.Name == invalidFileName {
			return []structs.Message{{Content: "File has an invalid name. " + invalidFileName, Source: file}}
		}
	}
	return nil
}
