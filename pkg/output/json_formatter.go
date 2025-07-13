package output

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/eawag-rdm/pc/pkg/structs"
)

// ScanResult represents the complete output of a package check scan
type ScanResult struct {
	Timestamp              string           `json:"timestamp"`
	Scanned                []ScannedFile    `json:"scanned"`
	Skipped                []SkippedFile    `json:"skipped"`
	DetailsSubjectFocused  []SubjectDetails `json:"details_subject_focused"`
	DetailsCheckFocused    []CheckDetails   `json:"details_check_focused"`
	PDFFiles               []string         `json:"pdf_files"`
	Errors                 []LogMessage     `json:"errors"`
	Warnings               []LogMessage     `json:"warnings"`
}

// ScannedFile represents a file that was scanned with summary of issues
type ScannedFile struct {
	Filename string              `json:"filename"`
	Issues   []CheckSummary      `json:"issues"`
}

// SkippedFile represents a file that was skipped during scanning
type SkippedFile struct {
	Filename string `json:"filename"`
	Reason   string `json:"reason"`
}

// SubjectDetails represents detailed issues for a specific subject
type SubjectDetails struct {
	Subject string        `json:"subject"`
	Path    string        `json:"path"`
	Issues  []CheckIssue  `json:"issues"`
}

// CheckDetails represents detailed issues for a specific check
type CheckDetails struct {
	Checkname string         `json:"checkname"`
	Issues    []SubjectIssue `json:"issues"`
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
	Subject string `json:"subject"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// LogMessage represents warning/info messages from the scan
type LogMessage struct {
	Level     string `json:"level"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}



// Warning represents processing warnings
type Warning struct {
	Type    string `json:"type"`
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// Error represents processing errors
type Error struct {
	Type    string `json:"type"`
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// JSONFormatter handles conversion of results to JSON
type JSONFormatter struct {
	scanStart time.Time
	warnings  []Warning
	errors    []Error
}

// NewJSONFormatter creates a new JSON formatter
func NewJSONFormatter() *JSONFormatter {
	return &JSONFormatter{
		scanStart: time.Now(),
		warnings:  make([]Warning, 0),
		errors:    make([]Error, 0),
	}
}

// AddWarning adds a warning message
func (jf *JSONFormatter) AddWarning(warningType, file, message string) {
	jf.warnings = append(jf.warnings, Warning{
		Type:    warningType,
		File:    file,
		Message: message,
	})
}

// AddError adds an error message
func (jf *JSONFormatter) AddError(errorType, file, message string) {
	jf.errors = append(jf.errors, Error{
		Type:    errorType,
		File:    file,
		Message: message,
	})
}

// FormatResults converts messages to structured JSON output
func (jf *JSONFormatter) FormatResults(location, collector string, messages []structs.Message, totalFiles int, pdfFiles []string) (string, error) {
	result := ScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               make([]ScannedFile, 0),
		Skipped:               make([]SkippedFile, 0),
		DetailsSubjectFocused: make([]SubjectDetails, 0),
		DetailsCheckFocused:   make([]CheckDetails, 0),
		PDFFiles:              make([]string, 0),
		Errors:                make([]LogMessage, 0),
		Warnings:              make([]LogMessage, 0),
	}

	// Process messages into the new structured format
	result.processMessages(messages)

	// Separate logger messages by level and extract skipped files
	logMessages := GlobalLogger.GetMessages()
	for _, msg := range logMessages {
		switch msg.Level {
		case "error":
			result.Errors = append(result.Errors, msg)
		case "warning":
			result.Warnings = append(result.Warnings, msg)
		case "info":
			// Check if this is a binary file skip message
			if strings.Contains(msg.Message, "Not checking contents of file") && strings.Contains(msg.Message, "binary") {
				// Extract filename from message like "Not checking contents of file: 'filename'. The file seems to be binary."
				start := strings.Index(msg.Message, "'")
				end := strings.LastIndex(msg.Message, "'")
				if start != -1 && end != -1 && start < end {
					filename := msg.Message[start+1 : end]
					result.Skipped = append(result.Skipped, SkippedFile{
						Filename: filename,
						Reason:   "Binary file detected",
					})
				}
			}
		}
	}

	// Add PDF files passed from caller
	result.PDFFiles = pdfFiles

	// Generate JSON
	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return string(jsonBytes), nil
}

// processMessages analyzes messages and creates the new structured output
func (result *ScanResult) processMessages(messages []structs.Message) {
	// Maps to organize data
	fileIssueMap := make(map[string]map[string]int)         // filename -> checkname -> count (only for files)
	subjectDetailMap := make(map[string][]CheckIssue)       // subject -> []CheckIssue  
	checkDetailMap := make(map[string][]SubjectIssue)       // checkname -> []SubjectIssue
	subjectPathMap := make(map[string]string)               // subject -> path

	for _, msg := range messages {
		testName := msg.TestName
		if testName == "" {
			testName = "Unknown"
		}

		// Determine subject and path
		var subject, path string
		if file, isFile := msg.Source.(structs.File); isFile {
			subject = file.Name
			path = file.Path
			
			// Only track scanned files for actual files, not repository
			if fileIssueMap[subject] == nil {
				fileIssueMap[subject] = make(map[string]int)
			}
			fileIssueMap[subject][testName]++
		} else {
			subject = "repository"
			path = ""
		}

		subjectPathMap[subject] = path

		// Add to subject-focused details
		subjectDetailMap[subject] = append(subjectDetailMap[subject], CheckIssue{
			Checkname: testName,
			Message:   msg.Content,
		})

		// Add to check-focused details
		checkDetailMap[testName] = append(checkDetailMap[testName], SubjectIssue{
			Subject: subject,
			Path:    path,
			Message: msg.Content,
		})
	}

	// Build scanned files (only for actual files, not repository)
	for fileName, checks := range fileIssueMap {
		scanned := ScannedFile{
			Filename: fileName,
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
	for subject, issues := range subjectDetailMap {
		result.DetailsSubjectFocused = append(result.DetailsSubjectFocused, SubjectDetails{
			Subject: subject,
			Path:    subjectPathMap[subject],
			Issues:  issues,
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

