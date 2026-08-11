package utils

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/optimization"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/readers"
	"github.com/eawag-rdm/pc/pkg/selector"
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

// BY_REPOSITORY_SECRETS runs regardless of checksAcrossFiles: IsFreeOfSecrets is
// a file-content check that is repository-scoped only so the whole file set
// shares one scanner invocation.
var BY_REPOSITORY_SECRETS = []func(repository structs.Repository, config config.Config) []structs.Message{
	checks.IsFreeOfSecrets,
}

// secretScanEnabled mirrors the leak check's own enabled gate so the pipeline
// neither counts nor announces a dormant secret scan (the check would return
// nil immediately anyway).
func secretScanEnabled(config config.Config) bool {
	tc := config.Tests["IsFreeOfSecrets"]
	if tc == nil || tc.Attrs == nil {
		return false
	}
	enabled, ok := tc.Attrs["enabled"].(bool)
	return ok && enabled
}

var BY_FILE_ON_ARCHIVE_FILE_LIST = []func(file structs.File, config config.Config) []structs.Message{
	checks.HasOnlyASCII,
	checks.HasNoWhiteSpace,
	checks.IsValidName,
}

// CheckSelectors holds the file filter of every file-scope [test.X] section that
// declares one, keyed by section name. It is compiled ONCE at startup by each
// frontend and is read-only afterwards, so all workers share it without locking -
// unlike the joined regex it replaces, which was recompiled per file x check.
// The zero value filters nothing.
type CheckSelectors struct {
	byCheck map[string]*selector.Selector
}

// filterSection names the [test.X] section a file check filters on.
func filterSection(check func(file structs.File, config config.Config) []structs.Message) string {
	name := optimization.FunctionName(check)
	// Handle special case: IsArchiveFreeOfKeywords uses IsFreeOfKeywords config
	if name == "IsArchiveFreeOfKeywords" {
		return "IsFreeOfKeywords"
	}
	return name
}

// fileFilterSections collects the sections file dispatch actually filters on.
// Repository-scoped sections (IsFreeOfSecrets, HasReadme) are deliberately not
// among them: skipFileCheck never consults those, and their lists belong to the
// checks themselves. The strictness no longer differs - leakcheck compiles its
// filter through the same constructor - so the exclusion stands on the FAILURE
// MODE alone: a repository check cannot report a load error (its signature
// returns messages only), so its lists are validated at boot by
// ValidateChecksConfig instead. Temporary; it dies when rules carry their own
// selectors.
func fileFilterSections() map[string]struct{} {
	tables := [][]func(file structs.File, config config.Config) []structs.Message{
		BY_FILE, BY_FILE_ON_ARCHIVE, BY_FILE_ON_ARCHIVE_FILE_LIST,
	}
	sections := make(map[string]struct{}, len(BY_FILE)+len(BY_FILE_ON_ARCHIVE)+len(BY_FILE_ON_ARCHIVE_FILE_LIST))
	for _, table := range tables {
		for _, check := range table {
			sections[filterSection(check)] = struct{}{}
		}
	}
	return sections
}

// CompileCheckSelectors compiles one selector per file-scope [test.X] section
// that carries a whitelist or a blacklist: whitelist -> include, blacklist ->
// exclude, matched case-sensitively against the file's base name. A section
// without lists gets no entry, and its check is therefore never skipped.
//
// The list policy itself belongs to selector.CompileLegacyRegexLists, which
// every legacy list site shares: each pattern is compiled on its own, never
// joined with "|", so an inline flag like (?i) scopes to the entry that spells
// it; patterns that do not compile, empty entries and sections that set both
// lists fail startup here, aggregated and named by section, instead of silently
// filtering everything (whitelist) or nothing (blacklist).
func CompileCheckSelectors(cfg config.Config) (CheckSelectors, error) {
	sections := fileFilterSections()
	names := make([]string, 0, len(cfg.Tests))
	for name, test := range cfg.Tests {
		if test == nil || (len(test.Whitelist) == 0 && len(test.Blacklist) == 0) {
			continue
		}
		if _, filters := sections[name]; !filters {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return CheckSelectors{}, nil
	}
	sort.Strings(names) // map order is random; error order must not be

	table := CheckSelectors{byCheck: make(map[string]*selector.Selector, len(names))}
	var errs []error
	for _, name := range names {
		test := cfg.Tests[name]
		sel, err := selector.CompileLegacyRegexLists(name, "name", test.Whitelist, test.Blacklist)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		table.byCheck[name] = &sel
	}
	if len(errs) > 0 {
		return CheckSelectors{}, errors.Join(errs...)
	}
	return table, nil
}

// resolve pairs each check of one dispatch call with its selector, ONCE per
// call: nil means "this check is unfiltered", and a nil result means "none of
// these checks is filtered". Reflection and the section lookup happen here, so
// the per-file loop costs an index and a nil test.
func (s CheckSelectors) resolve(checks []func(file structs.File, config config.Config) []structs.Message) []*selector.Selector {
	if len(s.byCheck) == 0 {
		return nil
	}
	sels := make([]*selector.Selector, len(checks))
	filtered := false
	for i, check := range checks {
		if sel, ok := s.byCheck[filterSection(check)]; ok {
			sels[i], filtered = sel, true
		}
	}
	if !filtered {
		return nil
	}
	return sels
}

// skipFileCheck decides whether the i-th check of a resolved dispatch runs for
// this file. The subject is File.Name - the base name at file scope, the member
// path inside archives.
func skipFileCheck(sels []*selector.Selector, i int, file structs.File) bool {
	if len(sels) == 0 || sels[i] == nil {
		return false
	}
	return !sels[i].Match(file.Name)
}

func applyChecksFilteredByFile(ctx context.Context, config config.Config, selectors CheckSelectors, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	// Use parallel processing for multiple files, sequential for small workloads
	// Lowered threshold from 4 to 2 files to enable parallel processing sooner
	if len(files) >= 2 && runtime.NumCPU() > 1 {
		return applyChecksParallel(ctx, config, selectors, checks, files)
	}

	sels := selectors.resolve(checks)

	// Sequential processing for small workloads
	var messages = []structs.Message{}
	for _, file := range files {
		// Stop between files once the analysis deadline fired / the caller
		// cancelled (coarse-grained: a file in progress finishes).
		if ctx.Err() != nil {
			return messages
		}
		helpers.PDFTracker.AddFileIfPDF("", file)
		// apply checks by file but only for file.Name
		for i, check := range checks {
			if skipFileCheck(sels, i, file) {
				continue
			}
			testName := optimization.FunctionName(check)
			ret := optimization.SafeRunCheck(check, file, config, testName)
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

// applyChecksFilteredByFileWithTestProgress reports progress per test (including skipped tests)
func applyChecksFilteredByFileWithTestProgress(ctx context.Context, config config.Config, selectors CheckSelectors, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File, progressCallback func(int)) []structs.Message {
	var messages = []structs.Message{}
	testsProcessed := 0
	sels := selectors.resolve(checks)

	for _, file := range files {
		if ctx.Err() != nil {
			return messages
		}
		helpers.PDFTracker.AddFileIfPDF("", file)

		// Process all checks for this file (including skipped ones)
		for i, check := range checks {
			// Count this test (whether run or skipped)
			testsProcessed++
			if progressCallback != nil {
				progressCallback(testsProcessed)
			}

			if skipFileCheck(sels, i, file) {
				continue // Skip this test, but we already counted it
			}

			testName := optimization.FunctionName(check)
			ret := optimization.SafeRunCheck(check, file, config, testName)
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

// checkWorkItem pairs one file with the checks that apply to it. All of a
// file's checks run in the same worker to avoid concurrent reads of one file.
type checkWorkItem struct {
	file   structs.File
	checks []func(structs.File, config.Config) []structs.Message
}

// filterChecksForFiles builds the work list: one entry per file that has at
// least one non-skipped check.
func filterChecksForFiles(sels []*selector.Selector, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []checkWorkItem {
	workItems := make([]checkWorkItem, 0, len(files))
	for _, file := range files {
		validChecks := make([]func(structs.File, config.Config) []structs.Message, 0, len(checks))
		for i, check := range checks {
			if !skipFileCheck(sels, i, file) {
				validChecks = append(validChecks, check)
			}
		}
		if len(validChecks) > 0 {
			workItems = append(workItems, checkWorkItem{file: file, checks: validChecks})
		}
	}
	return workItems
}

// runChecksPool fans workItems out over a worker pool and collects the results.
// Submission runs in a goroutine because Submit blocks when the pool's buffer
// is full; it reports how many items were actually submitted so the collect
// loop never waits for results that were never submitted (e.g. when the
// analysis deadline fires mid-submission and the submit loop stops early).
// Collecting exactly `submitted` results also guarantees every Submit has
// returned before the deferred Stop runs (the pool's Stop/Submit invariant).
func runChecksPool(ctx context.Context, cfg config.Config, workItems []checkWorkItem, numWorkers int) []structs.Message {
	if len(workItems) == 0 {
		return nil
	}
	if len(workItems) < numWorkers {
		numWorkers = len(workItems)
	}

	pool := optimization.NewWorkerPool(numWorkers)
	pool.Start()
	defer pool.Stop()

	submittedCh := make(chan int, 1)
	go func() {
		submitted := 0
		for _, entry := range workItems {
			// Stop submitting new files once the deadline fired / the caller
			// cancelled; files already submitted still finish.
			if ctx.Err() != nil {
				break
			}
			if !pool.Submit(optimization.WorkItem{File: entry.file, Checks: entry.checks, Config: cfg}) {
				break
			}
			submitted++
		}
		submittedCh <- submitted
	}()

	var allMessages []structs.Message
	collected := 0
	submitted := -1
	for submitted < 0 || collected < submitted {
		select {
		case result := <-pool.Results():
			allMessages = append(allMessages, result.Messages...)
			collected++
		case n := <-submittedCh:
			submitted = n
		}
	}
	return allMessages
}

// applyChecksParallel processes files concurrently using a worker pool.
func applyChecksParallel(ctx context.Context, cfg config.Config, selectors CheckSelectors, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	for _, file := range files {
		helpers.PDFTracker.AddFileIfPDF("", file)
	}
	return runChecksPool(ctx, cfg, filterChecksForFiles(selectors.resolve(checks), checks, files), runtime.NumCPU())
}

func applyChecksFilteredByFileOnArchiveFileList(ctx context.Context, config config.Config, selectors CheckSelectors, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {
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

	sels := selectors.resolve(checks)

	// Use parallel processing for multiple archives
	if len(archiveFiles) >= 2 && runtime.NumCPU() > 1 {
		return applyArchiveFileListChecksParallel(ctx, config, sels, checks, archiveFiles)
	}

	// Sequential processing for single archive or single CPU
	var messages = []structs.Message{}
	for _, file := range archiveFiles {
		if ctx.Err() != nil {
			return messages
		}
		msgs := processArchiveFileList(ctx, config, sels, checks, file)
		messages = append(messages, msgs...)
	}
	return messages
}

// processArchiveFileList processes all file list checks for a single archive
// This keeps files within each archive sequential while allowing parallelism across archives.
// The whole body runs under SafeRun (not just the check invocations):
// ReadArchiveFileList parses untrusted archive bytes, and on the parallel path
// this function runs in a bare worker goroutine where an unrecovered panic
// would kill the process.
func processArchiveFileList(ctx context.Context, cfg config.Config, sels []*selector.Selector, checks []func(file structs.File, config config.Config) []structs.Message, archiveFile structs.File) []structs.Message {
	return optimization.SafeRun("Processing archive '"+archiveFile.Name+"'", archiveFile.GetDisplayName(), func() []structs.Message {
		return archiveFileListChecks(ctx, cfg, sels, checks, archiveFile)
	})
}

// archiveWalkLimits derives the file-list walk bounds from the single defaulting
// site, so a hand-built config (General nil or zero fields) gets the documented
// defaults instead of "unlimited".
func archiveWalkLimits(cfg config.Config) (maxMembers int, maxTotalMemory int64) {
	general := cfg.General
	if general == nil {
		general = &config.GeneralConfig{}
	}
	_, totalMemory, memberCount := general.ArchiveLimits()
	return memberCount, totalMemory
}

// archiveWalkSkipMessage acknowledges an archive whose member list busts the
// walk bounds, mirroring the content path's member-count skip. It must be a
// Message, not a logger warning: warnings collapse into the generic unscanned
// acknowledgement in server responses, hiding the reason.
func archiveWalkSkipMessage(archiveFile structs.File, maxMembers int) structs.Message {
	reason := fmt.Sprintf("Skipped name checks of archive members: the archive holds more than %d members, or listing it would decompress more data than the archive walk budget allows.", maxMembers)
	return structs.Message{
		Content:  reason,
		Source:   archiveFile,
		TestName: "ArchiveFileList",
		Skipped:  true,
		Reason:   reason,
	}
}

func archiveFileListChecks(ctx context.Context, cfg config.Config, sels []*selector.Selector, checks []func(file structs.File, config config.Config) []structs.Message, archiveFile structs.File) []structs.Message {
	var messages []structs.Message

	maxMembers, maxTotalMemory := archiveWalkLimits(cfg)
	fileList, truncated, err := readers.ReadArchiveFileList(archiveFile, maxMembers, maxTotalMemory)
	if err != nil {
		output.GlobalLogger.FileWarning(archiveFile.GetDisplayName(), "Error (archive filelist checks) reading archive file list of '%s' -> %v", archiveFile.Name, err)
		return messages
	}
	if truncated {
		// The list is partial by construction: run no checks on it.
		return append(messages, archiveWalkSkipMessage(archiveFile, maxMembers))
	}

	for _, archivedFile := range fileList {
		// Stop between archived files once the deadline fired / the caller
		// cancelled.
		if ctx.Err() != nil {
			return messages
		}
		helpers.PDFTracker.AddFileIfPDF(archiveFile.Name+" -> ", archivedFile)

		for i, check := range checks {
			if skipFileCheck(sels, i, archivedFile) {
				continue
			}
			testName := optimization.FunctionName(check)
			ret := optimization.SafeRunCheck(check, archivedFile, cfg, testName)

			if ret != nil {
				for j := range ret {
					ret[j].TestName = testName
				}
				messages = append(messages, ret...)
			}
		}
	}
	return messages
}

// applyArchiveFileListChecksParallel processes archive file list checks in parallel across archives
// Each archive is processed by a single worker, keeping files within each archive sequential
func applyArchiveFileListChecksParallel(ctx context.Context, cfg config.Config, sels []*selector.Selector, checks []func(file structs.File, config config.Config) []structs.Message, archiveFiles []structs.File) []structs.Message {
	numWorkers := runtime.NumCPU()
	if len(archiveFiles) < numWorkers {
		numWorkers = len(archiveFiles)
	}

	// Channel for archive files to process
	archiveChan := make(chan structs.File, len(archiveFiles))
	// Channel for results
	resultChan := make(chan []structs.Message, len(archiveFiles))

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for archiveFile := range archiveChan {
				// After the deadline fires, drain the remaining archives without
				// processing them so the result count stays consistent.
				var messages []structs.Message
				if ctx.Err() == nil {
					messages = processArchiveFileList(ctx, cfg, sels, checks, archiveFile)
				}
				resultChan <- messages
			}
		}()
	}

	// Send work
	for _, file := range archiveFiles {
		archiveChan <- file
	}
	close(archiveChan)

	// Wait for workers to finish and close result channel
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	var allMessages []structs.Message
	for messages := range resultChan {
		allMessages = append(allMessages, messages...)
	}

	return allMessages
}

func applyChecksFilteredByFileOnArchive(ctx context.Context, config config.Config, selectors CheckSelectors, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {
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
		return applyArchiveChecksParallel(ctx, config, selectors, checks, archiveFiles)
	}

	sels := selectors.resolve(checks)

	// Sequential processing for single archives
	var messages = []structs.Message{}
	for _, file := range archiveFiles {
		if ctx.Err() != nil {
			return messages
		}
		for i, check := range checks {
			if skipFileCheck(sels, i, file) {
				continue
			}
			testName := optimization.FunctionName(check)
			ret := optimization.SafeRunCheck(check, file, config, testName)
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

// applyArchiveChecksParallel processes archive files in parallel. Archive
// extraction is memory-intensive, so it uses half the CPUs.
func applyArchiveChecksParallel(ctx context.Context, cfg config.Config, selectors CheckSelectors, checks []func(file structs.File, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	numWorkers := runtime.NumCPU() / 2
	if numWorkers < 1 {
		numWorkers = 1
	}
	return runChecksPool(ctx, cfg, filterChecksForFiles(selectors.resolve(checks), checks, files), numWorkers)
}

func ApplyChecksFilteredByRepository(ctx context.Context, config config.Config, checks []func(repository structs.Repository, config config.Config) []structs.Message, files []structs.File) []structs.Message {
	var messages = []structs.Message{}
	repo := structs.Repository{Files: files}
	for _, check := range checks {
		if ctx.Err() != nil {
			return messages
		}
		testName := optimization.FunctionName(check)
		ret := optimization.SafeRun("Check "+testName, "", func() []structs.Message { return check(repo, config) })
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

// noFilesNotice returns a skip-style acknowledgement that no files were found to
// analyse. ApplyAllChecks(WithProgress) appends it whenever the collected file
// set is empty, so every output (CLI plain/TUI/JSON and the server) surfaces the
// same clear, non-issue message - rather than the CLI erroring or the server
// returning a silent empty result. It is modelled on a per-file skip (Skipped =
// true, with a Reason) but is repository-scoped.
func noFilesNotice() structs.Message {
	const reason = "No files were found to analyse."
	return structs.Message{
		Content:  reason,
		Source:   structs.Repository{},
		TestName: "FilesPresent",
		Skipped:  true,
		Reason:   reason,
	}
}

// ApplyAllChecks runs every check group over the collected files. ctx bounds the
// work coarsely: once its deadline fires (the server's whole-analysis timeout)
// or it is cancelled, the loops stop between files - a file in progress
// finishes, no new one starts. CLI callers pass context.Background().
//
// selectors must come from CompileCheckSelectors (its zero value filters
// nothing); both frontends compile it once at startup. Intended entry point is
// internal/analysis.Run (progress == nil branch), which pairs this with the
// metadata checks; call it rather than this directly.
func ApplyAllChecks(ctx context.Context, config config.Config, selectors CheckSelectors, files []structs.File, checksAcrossFiles bool) []structs.Message {
	var messages []structs.Message

	messages = append(messages, applyChecksFilteredByFile(ctx, config, selectors, BY_FILE, files)...)
	messages = append(messages, applyChecksFilteredByFileOnArchiveFileList(ctx, config, selectors, BY_FILE_ON_ARCHIVE_FILE_LIST, files)...)
	messages = append(messages, applyChecksFilteredByFileOnArchive(ctx, config, selectors, BY_FILE_ON_ARCHIVE, files)...)
	if len(files) > 0 && secretScanEnabled(config) {
		messages = append(messages, ApplyChecksFilteredByRepository(ctx, config, BY_REPOSITORY_SECRETS, files)...)
	}
	if checksAcrossFiles {
		messages = append(messages, ApplyChecksFilteredByRepository(ctx, config, BY_REPOSITORY, files)...)
	}

	// Surface a clear, non-issue notice when there was nothing to analyse.
	if len(files) == 0 {
		messages = append(messages, noFilesNotice())
	}

	return messages
}

// ApplyAllChecksWithProgress is the progress-reporting twin of ApplyAllChecks
// (same check groups, same messages) for the TUI. Its file phase is
// SINGLE-THREADED by construction: per-test progress accounting needs a
// sequential walk, so the server must never be routed here - it would serialise
// every analysis silently. selectors is the same startup-built table
// ApplyAllChecks takes. Intended caller is internal/analysis.Run (progress !=
// nil branch); everything else takes ApplyAllChecks.
func ApplyAllChecksWithProgress(ctx context.Context, config config.Config, selectors CheckSelectors, files []structs.File, checksAcrossFiles bool, progressCallback ProgressCallback) []structs.Message {
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

	// Count the repository-scoped secret scan (only when enabled and files exist)
	if len(files) > 0 && secretScanEnabled(config) {
		totalTests += len(BY_REPOSITORY_SECRETS)
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

	messages = append(messages, applyChecksFilteredByFileWithTestProgress(ctx, config, selectors, BY_FILE, files, func(current int) {
		testsRun = current
		if progressCallback != nil {
			progressCallback(testsRun, totalTests, fmt.Sprintf("Running file tests... (%d/%d)", testsRun, totalTests))
		}
	})...)

	// Step 2: Archive file list checks
	if progressCallback != nil {
		progressCallback(testsRun, totalTests, "Running archive file list tests...")
	}
	archiveListTests := applyChecksFilteredByFileOnArchiveFileList(ctx, config, selectors, BY_FILE_ON_ARCHIVE_FILE_LIST, files)
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
	archiveContentTests := applyChecksFilteredByFileOnArchive(ctx, config, selectors, BY_FILE_ON_ARCHIVE, files)
	messages = append(messages, archiveContentTests...)
	// Update count for archive content tests (including skipped ones)
	for _, file := range files {
		if file.IsArchive {
			testsRun += len(BY_FILE_ON_ARCHIVE)
		}
	}

	// Step 4: Repository-scoped secret scan (when enabled and files exist)
	if len(files) > 0 && secretScanEnabled(config) {
		if progressCallback != nil {
			progressCallback(testsRun, totalTests, "Running secret scan...")
		}
		messages = append(messages, ApplyChecksFilteredByRepository(ctx, config, BY_REPOSITORY_SECRETS, files)...)
		testsRun += len(BY_REPOSITORY_SECRETS)
	}

	// Step 5: Repository checks (if enabled)
	if checksAcrossFiles {
		if progressCallback != nil {
			progressCallback(testsRun, totalTests, "Running repository tests...")
		}
		repoTests := ApplyChecksFilteredByRepository(ctx, config, BY_REPOSITORY, files)
		messages = append(messages, repoTests...)
		testsRun += len(BY_REPOSITORY)
	}

	// Final step
	if progressCallback != nil {
		progressCallback(testsRun, totalTests, "Finalizing results...")
	}
	// Surface a clear, non-issue notice when there was nothing to analyse.
	if len(files) == 0 {
		messages = append(messages, noFilesNotice())
	}

	return messages
}
