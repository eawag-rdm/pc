package utils

import (
	"fmt"
	"reflect"
	"regexp"
	"runtime"
	"strings"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/performance"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/structs"
)

var BY_FILE = []func(file structs.File, config config.Config) []structs.Message{
	checks.HasOnlyASCII,
	checks.HasNoWhiteSpace,
	checks.IsFreeOfKeywords,
	checks.IsValidName,
}
var BY_REPOSITORY = []func(repository structs.Repository, config config.Config) []structs.Message{
	checks.HasReadme,
	checks.ReadMeContainsTOC,
}

var BY_FILE_ON_ARCHIVE = []func(file structs.File, config config.Config) []structs.Message{
	checks.IsArchiveFreeOfKeywords,
}

var BY_FILE_ON_ARCHIVE_FILE_LIST = []func(file structs.File, config config.Config) []structs.Message{
	checks.HasOnlyASCII,
	checks.HasNoWhiteSpace,
	checks.IsValidName,
}


func getFunctionName(i interface{}) string {
	fullName := runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	parts := strings.Split(fullName, ".")
	return parts[len(parts)-1]
}

func matchPatterns(list []string, str string) bool {
	combinedPattern := strings.Join(list, "|")
	combinedRegex, err := regexp.Compile(combinedPattern)
	if err != nil {
		fmt.Printf("Error compiling regex pattern '%s': %v\n", combinedPattern, err)
		return false
	}
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
	// Use parallel processing for multiple files, sequential for small workloads
	if len(files) >= 4 && runtime.NumCPU() > 1 {
		return applyChecksParallel(config, checks, files)
	}
	
	// Sequential processing for small workloads
	var messages = []structs.Message{}
	for _, file := range files {
		helpers.PDFTracker.AddFileIfPDF("", file)
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

// applyChecksParallel processes files concurrently using worker pools
// Each file is processed by a single worker with all its checks to avoid IO conflicts
func applyChecksParallel(cfg config.Config, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	// Create work items where each item contains one file with all its applicable checks
	// This ensures all checks for a single file run in the same worker thread,
	// avoiding concurrent file access that could cause IO conflicts
	
	numWorkers := runtime.NumCPU()
	if len(files) < numWorkers {
		numWorkers = len(files)
	}

	pool := performance.NewWorkerPool(numWorkers)
	pool.Start()
	defer pool.Stop()

	// Submit work items - one per file with all applicable checks
	go func() {
		for _, file := range files {
			helpers.PDFTracker.AddFileIfPDF("", file)
			
			// Filter checks for this specific file
			var validChecks []func(structs.File, config.Config) []structs.Message
			for _, check := range checks {
				if !skipFileCheck(cfg, check, file) {
					validChecks = append(validChecks, check)
				}
			}
			
			if len(validChecks) > 0 {
				work := performance.WorkItem{
					File:   file,
					Checks: validChecks,
					Config: cfg,
				}
				
				// If we can't submit (queue full), this will block
				// ensuring we don't lose work items
				for !pool.Submit(work) {
					// Small delay to prevent busy waiting
					runtime.Gosched()
				}
			}
		}
	}()

	// Collect results
	var allMessages []structs.Message
	resultsCollected := 0
	expectedResults := 0
	
	// Count expected results
	for _, file := range files {
		hasValidChecks := false
		for _, check := range checks {
			if !skipFileCheck(cfg, check, file) {
				hasValidChecks = true
				break
			}
		}
		if hasValidChecks {
			expectedResults++
		}
	}

	for resultsCollected < expectedResults {
		result := <-pool.Results()
		if len(result.Messages) > 0 {
			allMessages = append(allMessages, result.Messages...)
		}
		resultsCollected++
	}

	return allMessages
}


func ApplyChecksFilteredByFileOnArchiveFileList(config config.Config, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {

	var messages = []structs.Message{}
	for _, file := range files {
		fileList, err := readers.ReadArchiveFileList(file)
		if err != nil {
			// handle the error appropriately, e.g., log it or return it
			fmt.Printf("Error (archive filelist checks) reading archive file list of '%s' -> %v\n", file.Name, err)
			continue
		}
		for _, archivedFile := range fileList {
			helpers.PDFTracker.AddFileIfPDF(file.Name+" -> ", archivedFile)

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

func ApplyChecksFilteredByFileOnArchive(config config.Config, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {

	var messages = []structs.Message{}
	for _, file := range files {

		for _, check := range checks {
			if skipFileCheck(config, check, file) {
				continue
			}
			if !file.IsArchive {
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

func ApplyChecksFilteredByRepository(config config.Config, checks []func(repository structs.Repository, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	var messages = []structs.Message{}
	repo := structs.Repository{Files: files}
	for _, check := range checks {
		ret := check(repo, config)
		if ret != nil {
			messages = append(messages, ret...)
		}
	}
	return messages
}

func ApplyAllChecks(config config.Config, files []structs.File, checksAcrossFiles bool) []structs.Message {
	var messages []structs.Message

	messages = append(messages, ApplyChecksFilteredByFile(config, BY_FILE, files)...)
	messages = append(messages, ApplyChecksFilteredByFileOnArchiveFileList(config, BY_FILE_ON_ARCHIVE_FILE_LIST, files)...)
	messages = append(messages, ApplyChecksFilteredByFileOnArchive(config, BY_FILE_ON_ARCHIVE, files)...)
	if checksAcrossFiles {
		messages = append(messages, ApplyChecksFilteredByRepository(config, BY_REPOSITORY, files)...)
	}

	// Apply message truncation
	messages = TruncateMessages(messages, config.General.MaxMessagesPerType)

	return messages

}

// getMessageType extracts a type identifier from a message content
// Groups similar messages together for truncation
func getMessageType(content string) string {
	// Extract the main part of the message before specific details
	if strings.Contains(content, "File name contains non-ASCII character:") {
		return "non-ascii-filename"
	}
	if strings.Contains(content, "File name contains spaces") {
		return "filename-spaces"
	}
	if strings.Contains(content, "Sensitive data in File? Found suspicious keyword(s):") {
		return "sensitive-data"
	}
	if strings.Contains(content, "Do you have Eawag internal information") {
		return "eawag-internal"
	}
	if strings.Contains(content, "Do you have hardcoded filepaths") {
		return "hardcoded-paths"
	}
	if strings.Contains(content, "File or Folder has an invalid name:") {
		return "invalid-name"
	}
	if strings.Contains(content, "File has an invalid suffix:") {
		return "invalid-suffix"
	}
	if strings.Contains(content, "In archived file:") {
		return "archived-file-issue"
	}
	// Default: use first 50 characters as type
	if len(content) > 50 {
		return content[:50]
	}
	return content
}

// TruncateMessages groups messages by type and truncates them if they exceed the limit
func TruncateMessages(messages []structs.Message, maxPerType int) []structs.Message {
	if maxPerType <= 0 {
		return messages
	}

	// Group messages by type
	messageGroups := make(map[string][]structs.Message)
	for _, msg := range messages {
		msgType := getMessageType(msg.Content)
		messageGroups[msgType] = append(messageGroups[msgType], msg)
	}

	var result []structs.Message
	for _, msgs := range messageGroups {
		if len(msgs) <= maxPerType {
			// Add all messages if under the limit
			result = append(result, msgs...)
		} else {
			// Add first (maxPerType-1) messages and a truncation notice
			result = append(result, msgs[:maxPerType-1]...)
			
			// Create truncation message
			truncationMsg := structs.Message{
				Content: fmt.Sprintf("... and %d more similar messages (truncated)", len(msgs)-(maxPerType-1)),
				Source:  msgs[0].Source, // Use the same source as the first message
			}
			result = append(result, truncationMsg)
		}
	}

	return result
}
