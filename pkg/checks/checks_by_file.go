package checks

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
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
	var maxFileSize = 2 * 1024 * 1024 // 2 MB

	whitelist := config.Tests["IsFreeOfKeywords"].Whitelist
	blacklist := config.Tests["IsFreeOfKeywords"].Blacklist

	archiveIterator := readers.InitArchiveIterator(file.Path, file.Name, maxFileSize, whitelist, blacklist)
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

	var body [][]byte
	if isText {
		content, err := os.ReadFile(file.Path)
		if err != nil {
			panic(err)
		}
		body = append(body, content)
	} else {
		body = tryReadBinary(file)
	}

	for _, argumentSet := range config.Tests["IsFreeOfKeywords"].KeywordArguments {
		var keywords = strings.Join(argumentSet["keywords"].([]string), "|")
		var info = argumentSet["info"].(string)

		ret := IsFreeOfKeywordsCore(file, keywords, info, body, !isText)
		if ret != nil {
			messages = append(messages, ret...)
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
	regexp, err := regexp.Compile("(?i)" + patterns)
	if err != nil {
		panic(err)
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
			panic(err)
		}
		return content
	} else if strings.HasSuffix(file.Path, ".docx") {
		content, err := readers.ReadDOCXFile(file)
		if err != nil {
			panic(err)
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
