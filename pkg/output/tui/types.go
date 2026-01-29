package tui

import "github.com/eawag-rdm/pc/pkg/output"

// ScanResult represents the JSON structure from PC scanner
type ScanResult struct {
	Timestamp             string           `json:"timestamp"`
	Scanned               []ScannedFile    `json:"scanned"`
	Skipped               []SkippedFile    `json:"skipped"`
	DetailsSubjectFocused []SubjectDetails `json:"details_subject_focused"`
	DetailsCheckFocused   []CheckDetails   `json:"details_check_focused"`
	PDFFiles              []string         `json:"pdf_files"`
	Errors                []output.LogMessage `json:"errors"`
	Warnings              []output.LogMessage `json:"warnings"`

	// Lookup maps (built once, used for O(1) access)
	subjectIndex map[string]*SubjectDetails // key: subject or "archive > subject"
	checkIndex   map[string]*CheckDetails   // key: checkname

	// Cached counts (computed once)
	cachedTotalIssues   int
	cachedHasRepository bool
	cacheBuilt          bool
}

// BuildCache builds lookup maps and cached values for O(1) access.
// This should be called once when data is loaded or updated.
func (sr *ScanResult) BuildCache() {
	if sr.cacheBuilt {
		return
	}

	// Build subject index
	sr.subjectIndex = make(map[string]*SubjectDetails, len(sr.DetailsSubjectFocused))
	for i := range sr.DetailsSubjectFocused {
		subject := &sr.DetailsSubjectFocused[i]
		key := subject.Subject
		if subject.ArchiveName != "" {
			key = subject.ArchiveName + " > " + subject.Subject
		}
		sr.subjectIndex[key] = subject

		if subject.Subject == "repository" {
			sr.cachedHasRepository = true
		}
	}

	// Build check index
	sr.checkIndex = make(map[string]*CheckDetails, len(sr.DetailsCheckFocused))
	for i := range sr.DetailsCheckFocused {
		check := &sr.DetailsCheckFocused[i]
		sr.checkIndex[check.Checkname] = check
	}

	// Calculate total issues once
	sr.cachedTotalIssues = 0
	for _, file := range sr.Scanned {
		for _, issue := range file.Issues {
			sr.cachedTotalIssues += issue.IssueCount
		}
	}
	if repo, ok := sr.subjectIndex["repository"]; ok {
		sr.cachedTotalIssues += len(repo.Issues)
	}

	sr.cacheBuilt = true
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
	Subject     string       `json:"subject"`
	Path        string       `json:"path"`
	ArchiveName string       `json:"archive_name,omitempty"` // Parent archive if file is inside archive
	Issues      []CheckIssue `json:"issues"`
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
	Subject     string `json:"subject"`
	Path        string `json:"path"`
	ArchiveName string `json:"archive_name,omitempty"` // Parent archive if file is inside archive
	Message     string `json:"message"`
}

// Using LogMessage from output package