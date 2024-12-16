package utils

import (
	"reflect"
	"regexp"
	"runtime"
	"strings"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

var BY_FILE = []func(file structs.File, config config.Config) []structs.Message{checks.HasOnlyASCII, checks.HasNoWhiteSpace, checks.IsFreeOfKeywords, checks.IsValidName}
var BY_REPOSITORY = []func(repository structs.Repository, config config.Config) []structs.Message{checks.HasReadme}

var BY_FILE_ON_ARCHIVE = []func(file structs.File, config config.Config) []structs.Message{checks.HasOnlyASCII, checks.HasNoWhiteSpace, checks.IsValidName}

func getFunctionName(i interface{}) string {
	fullName := runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	parts := strings.Split(fullName, ".")
	return parts[len(parts)-1]
}

func matchPatterns(list []string, str string) bool {
	combinedPattern := strings.Join(list, "|")
	combinedRegex := regexp.MustCompile(combinedPattern)
	return combinedRegex.MatchString(str)

}

// this function will decide if a check runs or skipped depending on the
// configuration file whitelist and blacklist and the file being passed
// the functiion will return true or false
func skipFileCheck(config config.Config, fileCheck func(file structs.File, config config.Config) []structs.Message, file structs.File) bool {
	checkName := getFunctionName(fileCheck)
	if _, exists := config.Tests[checkName]; !exists {
		return false
	}
	if len(config.Tests[checkName].Whitelist) > 0 {
		return !matchPatterns(config.Tests[checkName].Whitelist, file.Name)
	}

	if len(config.Tests[checkName].Blacklist) > 0 {
		return matchPatterns(config.Tests[checkName].Blacklist, file.Name)
	}
	return false
}

func ApplyChecksFilteredByFile(config config.Config, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	var messages = []structs.Message{}
	for _, file := range files {
		// apply checks by file but only for file.Name
		for _, check := range checks {
			if skipFileCheck(config, check, file) {
				continue
			}
			ret := check(file, config)
			if ret != nil {
				messages = append(messages, ret...)
			}
		}
	}
	return messages
}

func ApplyChecksFilteredByFileOnArchive(config config.Config, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {

	var messages = []structs.Message{}
	for _, file := range files {
		fileList, err := ReadArchiveFileList(file)
		if err != nil {
			// handle the error appropriately, e.g., log it or return it
			continue
		}
		for _, archivedFile := range fileList {
			for _, check := range checks {
				if skipFileCheck(config, check, archivedFile) {
					continue
				}
				ret := check(archivedFile, config)
				if ret != nil {
					messages = append(messages, ret...)
				}
			}
		}
	}
	return messages

}

func ApplyAllChecks(config config.Config, files []structs.File) []structs.Message {
	var messages []structs.Message

	messages = append(messages, ApplyChecksFilteredByFile(config, BY_FILE, files)...)
	messages = append(messages, ApplyChecksFilteredByFileOnArchive(config, BY_FILE_ON_ARCHIVE, files)...)

	return messages

}
