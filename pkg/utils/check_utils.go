package utils

import (
	"reflect"
	"regexp"

	"github.com/eawag-rdm/pc/pkg/collectors"
)

var BY_FILE = []string{"HasOnlyASCII", "HasNoWhiteSpace", "FreeOfKeywords", "ValidName"}
var ACROSS_FILES = []string{"HasReadme"}
var COMPLEX = []string{"ReadmeFileHasTableOfContents"}

// this function will decide if a check runs or s skipped depending on the
// configuration file whitelist and blacklist and the file being passed
// the functiion will return true or false
func skipCheck(config Config, checkName string, file File) bool {
	if testConfig, exists := config.Tests[checkName]; !exists || (len(testConfig.Whitelist) == 0 && len(testConfig.Blacklist) == 0) {
		return false
	}
	for _, regexString := range config.Tests[checkName].Whitelist {
		regex := regexp.MustCompile(regexString)
		if regex.MatchString(file.Name) {
			return false
		}
	}
	for _, regexString := range config.Tests[checkName].Blacklist {
		regex := regexp.MustCompile(regexString)
		if regex.MatchString(file.Name) {
			return true
		}
	}
	return false
}

func ApplyChecksFiltered(config Config, checks map[string]reflect.Value, files []File) []Message {
	checks, err := collectors.CollectChecks()
	if err != nil {
		panic(err)
	}

	for _, file := range files {
		// apply checks by file but only for file.Name
		for _, checkName := range BY_FILE {
			if skipCheck(config, checkName, file) {
				continue
			}
			collectors.CallFunctionByName(checkName, checks, file)
		}
	}

	return nil
}
