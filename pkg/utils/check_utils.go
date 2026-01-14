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
	"github.com/eawag-rdm/pc/pkg/optimization"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/structs"
)

var BY_FILE = []func(file structs.File, config config.Config) []structs.Message{
	checks.HasOnlyASCII,
	checks.HasNoWhiteSpace,
	checks.IsFreeOfKeywords,
	checks.IsValidName,
	checks.HasFileNameSpecialChars,
	checks.IsFileNameTooLong,
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
		output.GlobalLogger.Warning("Error compiling regex pattern '%s': %v", combinedPattern, err)
		return false
	}
	return combinedRegex.MatchString(str)
}

// this function will decide if a check runs or skipped depending on the
// configuration file whitelist and blacklist and the file being passed
// the functiion will return true or false
func skipFileCheck(config config.Config, fileCheck func(file structs.File, config config.Config) []structs.Message, file structs.File) bool {
	checkName := getFunctionName(fileCheck)
	
	// Handle special case: IsArchiveFreeOfKeywords uses IsFreeOfKeywords config
	configName := checkName
	if checkName == "IsArchiveFreeOfKeywords" {
		configName = "IsFreeOfKeywords"
	}
	
	if _, exists := config.Tests[configName]; !exists {
		return false
	}
	if len(config.Tests[configName].Whitelist) > 0 {
		return !matchPatterns(config.Tests[configName].Whitelist, file.Name)
	}

	if len(config.Tests[configName].Blacklist) > 0 {
		return matchPatterns(config.Tests[configName].Blacklist, file.Name)
	}
	return false
}

func ApplyChecksFilteredByFile(config config.Config, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	// Use parallel processing for multiple files, sequential for small workloads
	// Lowered threshold from 4 to 2 files to enable parallel processing sooner
	if len(files) >= 2 && runtime.NumCPU() > 1 {
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
			testName := getFunctionName(check)
			ret := check(file, config)
			if ret != nil {
				// Add test name to each message
				for i := range ret {
					ret[i].TestName = testName
				}
				messages = append(messages, ret...)
			}
		}
	}
	return messages
}

// ApplyChecksFilteredByFileWithProgress is like ApplyChecksFilteredByFile but reports progress per file
func ApplyChecksFilteredByFileWithProgress(config config.Config, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File, progressCallback func(int)) []structs.Message {
	// For progress reporting, we'll use sequential processing to get accurate file-by-file progress
	var messages = []structs.Message{}

	for i, file := range files {
		helpers.PDFTracker.AddFileIfPDF("", file)

		// Report progress for this file
		if progressCallback != nil {
			progressCallback(i + 1)
		}

		// apply checks by file but only for file.Name
		for _, check := range checks {
			if skipFileCheck(config, check, file) {
				continue
			}
			testName := getFunctionName(check)
			ret := check(file, config)
			if ret != nil {
				// Add test name to each message
				for j := range ret {
					ret[j].TestName = testName
				}
				messages = append(messages, ret...)
			}
		}
	}
	return messages
}

// ApplyChecksFilteredByFileWithTestProgress reports progress per test (including skipped tests)
func ApplyChecksFilteredByFileWithTestProgress(config config.Config, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File, progressCallback func(int)) []structs.Message {
	var messages = []structs.Message{}
	testsProcessed := 0

	for _, file := range files {
		helpers.PDFTracker.AddFileIfPDF("", file)

		// Process all checks for this file (including skipped ones)
		for _, check := range checks {
			// Count this test (whether run or skipped)
			testsProcessed++
			if progressCallback != nil {
				progressCallback(testsProcessed)
			}

			if skipFileCheck(config, check, file) {
				continue // Skip this test, but we already counted it
			}

			testName := getFunctionName(check)
			ret := check(file, config)
			if ret != nil {
				// Add test name to each message
				for j := range ret {
					ret[j].TestName = testName
				}
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

	pool := optimization.NewWorkerPool(numWorkers)
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
				work := optimization.WorkItem{
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
			output.GlobalLogger.Warning("Error (archive filelist checks) reading archive file list of '%s' -> %v", file.Name, err)
			continue
		}
		for _, archivedFile := range fileList {
			helpers.PDFTracker.AddFileIfPDF(file.Name+" -> ", archivedFile)

			for _, check := range checks {
				if skipFileCheck(config, check, archivedFile) {
					continue
				}
				testName := getFunctionName(check)
				ret := check(archivedFile, config)

				if ret != nil {
					// Add test name to each message
					for i := range ret {
						ret[i].TestName = testName
					}
					messages = append(messages, ret...)
				}
			}
		}
	}
	return messages

}

func ApplyChecksFilteredByFileOnArchive(config config.Config, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	// Filter to only archive files
	var archiveFiles []structs.File
	for _, file := range files {
		if file.IsArchive {
			archiveFiles = append(archiveFiles, file)
		}
	}

	if len(archiveFiles) == 0 {
		return []structs.Message{}
	}

	// Use parallel processing for archives as they are CPU-intensive
	if len(archiveFiles) >= 2 && runtime.NumCPU() > 1 {
		return applyArchiveChecksParallel(config, checks, archiveFiles)
	}

	// Sequential processing for single archives
	var messages = []structs.Message{}
	for _, file := range archiveFiles {
		for _, check := range checks {
			if skipFileCheck(config, check, file) {
				continue
			}
			testName := getFunctionName(check)
			ret := check(file, config)
			if ret != nil {
				// Add test name to each message
				for i := range ret {
					ret[i].TestName = testName
				}
				messages = append(messages, ret...)
			}
		}
	}
	return messages
}

// applyArchiveChecksParallel processes archive files in parallel
func applyArchiveChecksParallel(cfg config.Config, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	numWorkers := runtime.NumCPU() / 2
	if numWorkers < 1 {
		numWorkers = 1
	}
	if len(files) < numWorkers {
		numWorkers = len(files)
	}

	// Use ArchiveWorkerPool for memory management
	memoryLimitMB := cfg.General.MaxTotalArchiveMemory / (1024 * 1024)
	if memoryLimitMB <= 0 {
		memoryLimitMB = 100
	}
	pool := optimization.NewArchiveWorkerPool(numWorkers, memoryLimitMB)
	pool.Start()
	defer pool.Stop()

	// Pre-calculate work items BEFORE submission (fixes race condition)
	type workEntry struct {
		file   structs.File
		checks []func(structs.File, config.Config) []structs.Message
	}
	workItems := make([]workEntry, 0, len(files))

	for _, file := range files {
		var validChecks []func(structs.File, config.Config) []structs.Message
		for _, check := range checks {
			if !skipFileCheck(cfg, check, file) {
				validChecks = append(validChecks, check)
			}
		}
		if len(validChecks) > 0 {
			workItems = append(workItems, workEntry{file: file, checks: validChecks})
		}
	}

	expectedResults := len(workItems)
	if expectedResults == 0 {
		return []structs.Message{}
	}

	// Submit work items
	go func() {
		for _, entry := range workItems {
			work := optimization.WorkItem{
				File:   entry.file,
				Checks: entry.checks,
				Config: cfg,
			}
			for !pool.Submit(work) {
				runtime.Gosched()
			}
		}
	}()

	// Collect results
	var allMessages []structs.Message
	for i := 0; i < expectedResults; i++ {
		result := <-pool.Results()
		if len(result.Messages) > 0 {
			allMessages = append(allMessages, result.Messages...)
		}
	}

	return allMessages
}

func ApplyChecksFilteredByRepository(config config.Config, checks []func(repository structs.Repository, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	var messages = []structs.Message{}
	repo := structs.Repository{Files: files}
	for _, check := range checks {
		testName := getFunctionName(check)
		ret := check(repo, config)
		if ret != nil {
			// Add test name to each message
			for i := range ret {
				ret[i].TestName = testName
			}
			messages = append(messages, ret...)
		}
	}
	return messages
}

// ProgressCallback is called during scanning to report progress
type ProgressCallback func(current, total int, message string)

func ApplyAllChecks(config config.Config, files []structs.File, checksAcrossFiles bool) []structs.Message {
	var messages []structs.Message

	messages = append(messages, ApplyChecksFilteredByFile(config, BY_FILE, files)...)
	messages = append(messages, ApplyChecksFilteredByFileOnArchiveFileList(config, BY_FILE_ON_ARCHIVE_FILE_LIST, files)...)
	messages = append(messages, ApplyChecksFilteredByFileOnArchive(config, BY_FILE_ON_ARCHIVE, files)...)
	if checksAcrossFiles {
		messages = append(messages, ApplyChecksFilteredByRepository(config, BY_REPOSITORY, files)...)
	}

	// Message truncation disabled to prevent archive messages from being lost
	// messages = TruncateMessages(messages, config.General.MaxMessagesPerType)

	return messages
}

func ApplyAllChecksWithProgress(config config.Config, files []structs.File, checksAcrossFiles bool, progressCallback ProgressCallback) []structs.Message {
	var messages []structs.Message

	// Calculate total number of tests (including skipped tests)
	totalTests := 0

	// Count ALL file-based tests (including skipped ones)
	for range files {
		totalTests += len(BY_FILE)
	}

	// Count ALL archive file list tests (including skipped ones)
	for _, file := range files {
		if file.IsArchive {
			totalTests += len(BY_FILE_ON_ARCHIVE_FILE_LIST)
		}
	}

	// Count ALL archive content tests (including skipped ones)
	for _, file := range files {
		if file.IsArchive {
			totalTests += len(BY_FILE_ON_ARCHIVE)
		}
	}

	// Count repository tests
	if checksAcrossFiles {
		totalTests += len(BY_REPOSITORY)
	}

	testsRun := 0

	// Step 1: File checks (with per-test progress)
	if progressCallback != nil {
		progressCallback(testsRun, totalTests, "Running file checks...")
	}

	messages = append(messages, ApplyChecksFilteredByFileWithTestProgress(config, BY_FILE, files, func(current int) {
		testsRun = current
		if progressCallback != nil {
			progressCallback(testsRun, totalTests, fmt.Sprintf("Running file tests... (%d/%d)", testsRun, totalTests))
		}
	})...)

	// Step 2: Archive file list checks
	if progressCallback != nil {
		progressCallback(testsRun, totalTests, "Running archive file list tests...")
	}
	archiveListTests := ApplyChecksFilteredByFileOnArchiveFileList(config, BY_FILE_ON_ARCHIVE_FILE_LIST, files)
	messages = append(messages, archiveListTests...)
	// Update count for archive list tests (including skipped ones)
	for _, file := range files {
		if file.IsArchive {
			testsRun += len(BY_FILE_ON_ARCHIVE_FILE_LIST)
		}
	}

	// Step 3: Archive content checks
	if progressCallback != nil {
		progressCallback(testsRun, totalTests, "Running archive content tests...")
	}
	archiveContentTests := ApplyChecksFilteredByFileOnArchive(config, BY_FILE_ON_ARCHIVE, files)
	messages = append(messages, archiveContentTests...)
	// Update count for archive content tests (including skipped ones)
	for _, file := range files {
		if file.IsArchive {
			testsRun += len(BY_FILE_ON_ARCHIVE)
		}
	}

	// Step 4: Repository checks (if enabled)
	if checksAcrossFiles {
		if progressCallback != nil {
			progressCallback(testsRun, totalTests, "Running repository tests...")
		}
		repoTests := ApplyChecksFilteredByRepository(config, BY_REPOSITORY, files)
		messages = append(messages, repoTests...)
		testsRun += len(BY_REPOSITORY)
	}

	// Final step: Finalize results (message truncation disabled)
	if progressCallback != nil {
		progressCallback(testsRun, totalTests, "Finalizing results...")
	}
	// Message truncation disabled to prevent archive messages from being lost
	// messages = TruncateMessages(messages, config.General.MaxMessagesPerType)

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
				Content:  fmt.Sprintf("... and %d more similar messages (truncated)", len(msgs)-(maxPerType-1)),
				Source:   msgs[0].Source,   // Use the same source as the first message
				TestName: msgs[0].TestName, // Use the same test name as the first message
			}
			result = append(result, truncationMsg)
		}
	}

	return result
}
