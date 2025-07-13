package tui

// ScanResult represents the JSON structure from PC scanner
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

type ScannedFile struct {
	Filename string         `json:"filename"`
	Issues   []CheckSummary `json:"issues"`
}

type SkippedFile struct {
	Filename string `json:"filename"`
	Path     string `json:"path"`
	Reason   string `json:"reason"`
}

type SubjectDetails struct {
	Subject string       `json:"subject"`
	Path    string       `json:"path"`
	Issues  []CheckIssue `json:"issues"`
}

type CheckDetails struct {
	Checkname string         `json:"checkname"`
	Issues    []SubjectIssue `json:"issues"`
}

type CheckSummary struct {
	Checkname  string `json:"checkname"`
	IssueCount int    `json:"issue_count"`
}

type CheckIssue struct {
	Checkname string `json:"checkname"`
	Message   string `json:"message"`
}

type SubjectIssue struct {
	Subject string `json:"subject"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type LogMessage struct {
	Level     string `json:"level"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}