package json

import (
	"encoding/json"
	"fmt"
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
	result := ScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               make([]ScannedFile, 0),
		Skipped:               make([]SkippedFile, 0),
		DetailsSubjectFocused: make([]SubjectDetails, 0),
		DetailsCheckFocused:   make([]CheckDetails, 0),
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

	// Generate JSON
	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return string(jsonBytes), nil
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
