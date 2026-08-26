package json

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/eawag-rdm/pc/pkg/metadata"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// ScanResult represents the complete output of a package check scan
type ScanResult struct {
	Timestamp             string              `json:"timestamp"`
	Scanned               []ScannedFile       `json:"scanned"`
	Skipped               []SkippedFile       `json:"skipped"`
	DetailsSubjectFocused []SubjectDetails    `json:"details_subject_focused"`
	DetailsCheckFocused   []CheckDetails      `json:"details_check_focused"`
	DetailsRuleFocused    []RuleDetails       `json:"details_rule_focused"`
	DetailsMetadata       []MetadataDetails   `json:"details_metadata"`
	PDFFiles              []string            `json:"pdf_files"`
	Errors                []output.LogMessage `json:"errors"`
	Warnings              []output.LogMessage `json:"warnings"`
}

// ScannedFile represents a file that was scanned with summary of issues
type ScannedFile struct {
	Filename string         `json:"filename"`
	Issues   []CheckSummary `json:"issues"`
}

// SkippedFile represents a file that was skipped during scanning
type SkippedFile struct {
	Filename string `json:"filename"`
	Path     string `json:"path"`
	Reason   string `json:"reason"`
}

// SubjectDetails represents detailed issues for a specific subject
type SubjectDetails struct {
	Subject     string       `json:"subject"`
	Path        string       `json:"path"`
	ArchiveName string       `json:"archive_name,omitempty"` // Parent archive if file is inside archive
	Issues      []CheckIssue `json:"issues"`
}

// CheckDetails represents detailed issues for a specific check
type CheckDetails struct {
	Checkname string         `json:"checkname"`
	Issues    []SubjectIssue `json:"issues"`
}

// RuleSubject is one subject a rule flagged: the same triple the other
// detail sections carry, with that subject's finding count instead of the
// message text - which stays in details_subject_focused and
// details_check_focused rather than being repeated a third time.
type RuleSubject struct {
	Subject     string `json:"subject"`
	Path        string `json:"path"`
	ArchiveName string `json:"archive_name,omitempty"`
	IssueCount  int    `json:"issue_count"`
}

// RuleDetails represents the findings of one configured rule. A rule that
// found nothing does not appear, exactly like a check that found nothing
// does not appear in details_check_focused: every section is built from the
// run's messages.
//
// This section counts ATTRIBUTIONS, not findings: a finding that names several
// rules is counted under every one of them. Per rule IssueCount is exact - it
// equals the sum of the subjects' counts - but over the rules of one check C the
// counts sum to at least the number of C's findings, with equality iff no
// finding of C names more than one rule. details_check_focused stays the
// authoritative finding count.
//
// Grouping is by rule name alone, which is sound because rule names are unique
// run-wide (enforced in pkg/utils/plan.go when the plan is compiled), so one
// rule belongs to exactly one check.
type RuleDetails struct {
	Rule       string        `json:"rule"`
	Checkname  string        `json:"checkname"`
	IssueCount int           `json:"issue_count"`
	Subjects   []RuleSubject `json:"subjects"`
}

// ruleAccum accumulates one rule's findings while the messages are walked.
// subjects is keyed by subjectKey, NOT by display name: two archives can
// carry a member of the same name, and keying on the display name would
// merge them into one subject while the counts kept saying two.
type ruleAccum struct {
	checkname string
	count     int
	subjects  map[string]int
}

// MetadataDetails represents the metadata-check findings for one entity
// (a package or one of its resources).
type MetadataDetails struct {
	Kind   string       `json:"kind"`
	Name   string       `json:"name"`
	Issues []CheckIssue `json:"issues"`
}

// CheckSummary represents a summary of issues for a check within a file
type CheckSummary struct {
	Checkname  string `json:"checkname"`
	IssueCount int    `json:"issue_count"`
}

// CheckIssue represents an issue from a specific check within a file
type CheckIssue struct {
	Checkname string `json:"checkname"`
	Message   string `json:"message"`
}

// SubjectIssue represents an issue in a specific subject for a check
type SubjectIssue struct {
	Subject     string `json:"subject"`
	Path        string `json:"path"`
	ArchiveName string `json:"archive_name,omitempty"` // Parent archive if file is inside archive
	Message     string `json:"message"`
}

// Using LogMessage from output package

// JSONFormatter handles conversion of results to JSON
type JSONFormatter struct{}

// NewJSONFormatter creates a new JSON formatter
func NewJSONFormatter() *JSONFormatter {
	return &JSONFormatter{}
}

// FormatResults converts messages to structured JSON output. diagnostics are
// the run's operator-facing notes, classified into errors[]/warnings[] here;
// the formatter reads no process state, so what it renders is exactly what it
// was handed.
func (jf *JSONFormatter) FormatResults(location, collector string, messages []structs.Message, totalFiles int, pdfFiles []string, diagnostics []structs.Diagnostic) (string, error) {
	result := buildScanResult(messages, pdfFiles, diagnostics)

	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return string(jsonBytes), nil
}

// FormatResultsCompact renders the same document as FormatResults, without the
// indentation: for machine consumers the whitespace is only bytes on the wire.
func (jf *JSONFormatter) FormatResultsCompact(messages []structs.Message, pdfFiles []string, diagnostics []structs.Diagnostic) (string, error) {
	result := buildScanResult(messages, pdfFiles, diagnostics)

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return string(jsonBytes), nil
}

// buildScanResult assembles the result both formatters render.
func buildScanResult(messages []structs.Message, pdfFiles []string, diagnostics []structs.Diagnostic) ScanResult {
	result := ScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               make([]ScannedFile, 0),
		Skipped:               make([]SkippedFile, 0),
		DetailsSubjectFocused: make([]SubjectDetails, 0),
		DetailsCheckFocused:   make([]CheckDetails, 0),
		DetailsRuleFocused:    make([]RuleDetails, 0),
		DetailsMetadata:       make([]MetadataDetails, 0),
		PDFFiles:              make([]string, 0),
		Errors:                make([]output.LogMessage, 0),
		Warnings:              make([]output.LogMessage, 0),
	}

	// Process messages into the new structured format. Skip-flagged Messages are
	// routed into result.Skipped and excluded from the issue maps.
	result.processMessages(messages)

	// Separate diagnostics by level. Skipped files are now sourced from
	// skip-flagged structs.Messages (see processMessages), not scraped from logs.
	for _, msg := range diagnostics {
		switch msg.Level {
		case structs.DiagError:
			result.Errors = append(result.Errors, msg)
		case structs.DiagWarning:
			result.Warnings = append(result.Warnings, msg)
		}
	}

	// Add PDF files passed from caller. A nil slice (e.g. an empty
	// FileTracker.SnapshotFiles) must not replace the empty slice initialized
	// above, or pdf_files serializes as JSON null instead of [].
	if pdfFiles != nil {
		result.PDFFiles = pdfFiles
	}

	return result
}

// subjectKey creates a unique key for a subject considering archive context
func subjectKey(displayName, archiveName string) string {
	if archiveName != "" {
		return archiveName + " > " + displayName
	}
	return displayName
}

// processMessages analyzes messages and creates the new structured output
func (result *ScanResult) processMessages(messages []structs.Message) {
	// Maps to organize data
	fileIssueMap := make(map[string]map[string]int)   // subject_key -> checkname -> count (only for files)
	subjectDetailMap := make(map[string][]CheckIssue) // subject_key -> []CheckIssue
	checkDetailMap := make(map[string][]SubjectIssue) // checkname -> []SubjectIssue
	ruleDetailMap := make(map[string]*ruleAccum)      // rule name -> accumulator
	subjectPathMap := make(map[string]string)         // subject_key -> path
	subjectArchiveMap := make(map[string]string)      // subject_key -> archive_name
	subjectDisplayMap := make(map[string]string)      // subject_key -> display_name
	metadataIndex := make(map[*metadata.Entity]int)   // entity -> index in DetailsMetadata

	for _, msg := range messages {
		// Skip acknowledgements are not issues: route them into the Skipped slice
		// and keep them out of the Scanned/Details issue maps.
		if msg.Skipped {
			result.appendSkipped(msg)
			continue
		}

		testName := msg.TestName
		if testName == "" {
			testName = "Unknown"
		}

		// Metadata entities form their own section, separate from files.
		if ent, ok := msg.Source.(*metadata.Entity); ok {
			idx, exists := metadataIndex[ent]
			if !exists {
				idx = len(result.DetailsMetadata)
				result.DetailsMetadata = append(result.DetailsMetadata, MetadataDetails{
					Kind:   ent.Kind,
					Name:   ent.Name,
					Issues: []CheckIssue{},
				})
				metadataIndex[ent] = idx
			}
			result.DetailsMetadata[idx].Issues = append(result.DetailsMetadata[idx].Issues, CheckIssue{
				Checkname: testName,
				Message:   msg.Content,
			})
			continue
		}

		// Determine subject and path
		var subject, displayName, filePath, archiveName string
		if file, isFile := msg.Source.(structs.File); isFile {
			displayName = file.GetDisplayName()
			filePath = file.Path
			archiveName = file.ArchiveName
			subject = subjectKey(displayName, archiveName)

			// Only track scanned files for actual files, not repository
			if fileIssueMap[subject] == nil {
				fileIssueMap[subject] = make(map[string]int)
			}
			fileIssueMap[subject][testName]++
		} else {
			subject = "repository"
			displayName = "repository"
			filePath = ""
			archiveName = ""
		}

		subjectPathMap[subject] = filePath
		subjectArchiveMap[subject] = archiveName
		subjectDisplayMap[subject] = displayName

		// Add to subject-focused details
		subjectDetailMap[subject] = append(subjectDetailMap[subject], CheckIssue{
			Checkname: testName,
			Message:   msg.Content,
		})

		// Add to check-focused details
		checkDetailMap[testName] = append(checkDetailMap[testName], SubjectIssue{
			Subject:     displayName,
			Path:        filePath,
			ArchiveName: archiveName,
			Message:     msg.Content,
		})

		// Add to rule-focused details, once per rule the finding names: a finding
		// several rules produced is counted under each of them. A finding with no
		// rule of its own creates none; skip acknowledgements and metadata findings
		// never reach here, having been routed out earlier in this loop.
		for _, ruleName := range msg.Rules {
			acc := ruleDetailMap[ruleName]
			if acc == nil {
				acc = &ruleAccum{checkname: testName, subjects: make(map[string]int)}
				ruleDetailMap[ruleName] = acc
			}
			acc.count++
			acc.subjects[subject]++
		}
	}

	// Build scanned files (only for actual files, not repository)
	for subjectKey, checks := range fileIssueMap {
		displayName := subjectDisplayMap[subjectKey]
		archiveName := subjectArchiveMap[subjectKey]

		// For scanned list, show archive context in filename if present
		filename := displayName
		if archiveName != "" {
			filename = archiveName + " > " + displayName
		}

		scanned := ScannedFile{
			Filename: filename,
			Issues:   []CheckSummary{},
		}
		for checkname, count := range checks {
			scanned.Issues = append(scanned.Issues, CheckSummary{
				Checkname:  checkname,
				IssueCount: count,
			})
		}
		result.Scanned = append(result.Scanned, scanned)
	}

	// Build subject-focused details
	for subjectKey, issues := range subjectDetailMap {
		displayName := subjectDisplayMap[subjectKey]
		result.DetailsSubjectFocused = append(result.DetailsSubjectFocused, SubjectDetails{
			Subject:     displayName,
			Path:        subjectPathMap[subjectKey],
			ArchiveName: subjectArchiveMap[subjectKey],
			Issues:      issues,
		})
	}

	// Build check-focused details
	for checkname, issues := range checkDetailMap {
		result.DetailsCheckFocused = append(result.DetailsCheckFocused, CheckDetails{
			Checkname: checkname,
			Issues:    issues,
		})
	}

	// Build rule-focused details. Message order is worker-completion order, so
	// an unsorted array would vary run to run inside a published response: both
	// the subjects and the rules themselves are sorted before they are emitted.
	for rule, acc := range ruleDetailMap {
		subjects := make([]RuleSubject, 0, len(acc.subjects))
		for key, count := range acc.subjects {
			// Identity is read from the same maps the subject- and check-focused
			// sections use, so one artefact cannot report two paths for one subject.
			subjects = append(subjects, RuleSubject{
				Subject:     subjectDisplayMap[key],
				Path:        subjectPathMap[key],
				ArchiveName: subjectArchiveMap[key],
				IssueCount:  count,
			})
		}
		sort.Slice(subjects, func(i, j int) bool {
			if subjects[i].ArchiveName != subjects[j].ArchiveName {
				return subjects[i].ArchiveName < subjects[j].ArchiveName
			}
			return subjects[i].Subject < subjects[j].Subject
		})
		result.DetailsRuleFocused = append(result.DetailsRuleFocused, RuleDetails{
			Rule:       rule,
			Checkname:  acc.checkname,
			IssueCount: acc.count,
			Subjects:   subjects,
		})
	}
	sort.Slice(result.DetailsRuleFocused, func(i, j int) bool {
		return result.DetailsRuleFocused[i].Rule < result.DetailsRuleFocused[j].Rule
	})
}

// appendSkipped converts a skip-flagged Message into a SkippedFile entry. The
// reason prefers the explicit Reason field, falling back to the Content.
func (result *ScanResult) appendSkipped(msg structs.Message) {
	reason := msg.Reason
	if reason == "" {
		reason = msg.Content
	}

	skipped := SkippedFile{Reason: reason}
	if file, isFile := msg.Source.(structs.File); isFile {
		displayName := file.GetDisplayName()
		filename := displayName
		if file.ArchiveName != "" {
			filename = file.ArchiveName + " > " + displayName
		}
		skipped.Filename = filename
		skipped.Path = file.Path
	}
	result.Skipped = append(result.Skipped, skipped)
}
