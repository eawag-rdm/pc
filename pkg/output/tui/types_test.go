package tui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/output"
	jsonformatter "github.com/eawag-rdm/pc/pkg/output/json"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// TestScanResultDecodesRuleSectionFromFormatter is the ONLY compile-independent
// link between the two copies of this schema: the TUI's ScanResult hand-mirrors
// the JSON formatter's response types, and nothing but this test fails when the
// two drift. Renaming details_rule_focused (or any key under it) on the
// formatter side would otherwise leave the whole suite green while the TUI
// silently rendered an empty Rules section in production.
func TestScanResultDecodesRuleSectionFromFormatter(t *testing.T) {
	file := structs.File{Name: "config.yaml", Path: "/path/config.yaml"}

	messages := []structs.Message{
		{Content: "Found 'PASSWORD'", Source: file, TestName: "IsFreeOfKeywords", Rules: []string{"sensitive-content"}},
		{Content: "Found 'SECRET'", Source: file, TestName: "IsFreeOfKeywords", Rules: []string{"sensitive-content"}},
	}

	encoded, err := jsonformatter.NewJSONFormatter().FormatResults("test/path", "LocalCollector", messages, 1, nil, nil)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	var decoded ScanResult
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("Failed to unmarshal formatter output into tui.ScanResult: %v", err)
	}

	if len(decoded.DetailsRuleFocused) != 1 {
		t.Fatalf("Expected 1 rule entry, got %d, from:\n%s", len(decoded.DetailsRuleFocused), encoded)
	}

	rule := decoded.DetailsRuleFocused[0]
	if rule.Rule != "sensitive-content" {
		t.Errorf("Rule name mismatch: got %q, want sensitive-content", rule.Rule)
	}
	if rule.Checkname != "IsFreeOfKeywords" {
		t.Errorf("Checkname mismatch: got %q, want IsFreeOfKeywords", rule.Checkname)
	}
	if rule.IssueCount != 2 {
		t.Errorf("IssueCount mismatch: got %d, want 2", rule.IssueCount)
	}

	if len(rule.Subjects) != 1 {
		t.Fatalf("Expected 1 rule subject, got %d, from:\n%s", len(rule.Subjects), encoded)
	}
	if rule.Subjects[0].Subject != "config.yaml" || rule.Subjects[0].IssueCount != 2 {
		t.Errorf("Rule subject mismatch: got %+v, want config.yaml with 2 issues", rule.Subjects[0])
	}
}

func TestScanResult_JSONSerialization(t *testing.T) {
	// Test data
	timestamp := time.Now().UTC().Format(time.RFC3339)
	scanResult := ScanResult{
		Timestamp: timestamp,
		Scanned: []ScannedFile{
			{
				Filename: "test.go",
				Issues: []CheckSummary{
					{Checkname: "IsFreeOfKeywords", IssueCount: 2},
				},
			},
		},
		Skipped: []SkippedFile{
			{Filename: "binary.bin", Reason: "Binary file detected"},
		},
		DetailsSubjectFocused: []SubjectDetails{
			{
				Subject: "test.go",
				Path:    "/path/to/test.go",
				Issues: []CheckIssue{
					{Checkname: "IsFreeOfKeywords", Message: "Found keyword 'secret'"},
				},
			},
		},
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "IsFreeOfKeywords",
				Issues: []SubjectIssue{
					{Subject: "test.go", Path: "/path/to/test.go", Message: "Found keyword 'secret'"},
				},
			},
		},
		PDFFiles: []string{"document.pdf", "report.pdf"},
		Errors: []output.LogMessage{
			{Level: "error", Message: "Test error", Timestamp: timestamp},
		},
		Warnings: []output.LogMessage{
			{Level: "warning", Message: "Test warning", Timestamp: timestamp},
		},
	}

	// Test JSON marshaling
	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal ScanResult: %v", err)
	}

	// Test JSON unmarshaling
	var unmarshaled ScanResult
	err = json.Unmarshal(jsonData, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal ScanResult: %v", err)
	}

	// Verify data integrity
	if unmarshaled.Timestamp != scanResult.Timestamp {
		t.Errorf("Timestamp mismatch: got %s, want %s", unmarshaled.Timestamp, scanResult.Timestamp)
	}

	if len(unmarshaled.Scanned) != 1 {
		t.Fatalf("Expected 1 scanned file, got %d", len(unmarshaled.Scanned))
	}

	if unmarshaled.Scanned[0].Filename != "test.go" {
		t.Errorf("Scanned filename mismatch: got %s, want test.go", unmarshaled.Scanned[0].Filename)
	}

	if len(unmarshaled.Skipped) != 1 {
		t.Fatalf("Expected 1 skipped file, got %d", len(unmarshaled.Skipped))
	}

	if unmarshaled.Skipped[0].Reason != "Binary file detected" {
		t.Errorf("Skip reason mismatch: got %s, want 'Binary file detected'", unmarshaled.Skipped[0].Reason)
	}

	if len(unmarshaled.Errors) != 1 {
		t.Fatalf("Expected 1 error, got %d", len(unmarshaled.Errors))
	}

	if len(unmarshaled.Warnings) != 1 {
		t.Fatalf("Expected 1 warning, got %d", len(unmarshaled.Warnings))
	}

	if len(unmarshaled.PDFFiles) != 2 {
		t.Fatalf("Expected 2 PDF files, got %d", len(unmarshaled.PDFFiles))
	}
}

func TestEmptyScanResult(t *testing.T) {
	scanResult := ScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               []ScannedFile{},
		Skipped:               []SkippedFile{},
		DetailsSubjectFocused: []SubjectDetails{},
		DetailsCheckFocused:   []CheckDetails{},
		PDFFiles:              []string{},
		Errors:                []output.LogMessage{},
		Warnings:              []output.LogMessage{},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal empty ScanResult: %v", err)
	}

	var unmarshaled ScanResult
	err = json.Unmarshal(jsonData, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal empty ScanResult: %v", err)
	}

	// Verify empty slices are preserved
	if len(unmarshaled.Scanned) != 0 {
		t.Errorf("Expected empty scanned slice, got %d items", len(unmarshaled.Scanned))
	}
	if len(unmarshaled.Errors) != 0 {
		t.Errorf("Expected empty errors slice, got %d items", len(unmarshaled.Errors))
	}
}

func TestSubjectDetails_Structure(t *testing.T) {
	subject := SubjectDetails{
		Subject: "example.txt",
		Path:    "/path/to/example.txt",
		Issues: []CheckIssue{
			{Checkname: "TestCheck", Message: "Test message"},
			{Checkname: "AnotherCheck", Message: "Another message"},
		},
	}

	if subject.Subject != "example.txt" {
		t.Errorf("Subject mismatch: got %s, want example.txt", subject.Subject)
	}

	if len(subject.Issues) != 2 {
		t.Errorf("Expected 2 issues, got %d", len(subject.Issues))
	}
}

// TestRuleSectionDoesNotDoubleCountTotal pins the header total against the
// rule section. DetailsRuleFocused is a re-grouping of findings already counted
// through Scanned, the repository subject and DetailsMetadata, so adding its
// counts to cachedTotalIssues would double every issue shown in the header.
func TestRuleSectionDoesNotDoubleCountTotal(t *testing.T) {
	base := func() ScanResult {
		return ScanResult{
			Timestamp: "2024-01-14T10:30:00Z",
			Scanned: []ScannedFile{
				{Filename: "config.yaml", Issues: []CheckSummary{{Checkname: "IsFreeOfKeywords", IssueCount: 2}}},
			},
			DetailsSubjectFocused: []SubjectDetails{
				{Subject: "config.yaml", Path: "/path/config.yaml", Issues: []CheckIssue{
					{Checkname: "IsFreeOfKeywords", Message: "Found 'PASSWORD'"},
					{Checkname: "IsFreeOfKeywords", Message: "Found 'SECRET'"},
				}},
				{Subject: "repository", Issues: []CheckIssue{{Checkname: "HasReadme", Message: "No README"}}},
			},
			DetailsMetadata: []MetadataDetails{
				{Kind: "package", Name: "my-package", Issues: []CheckIssue{{Checkname: "MetadataCheck", Message: "Missing author"}}},
			},
		}
	}

	withoutRules := base()
	withoutRules.BuildCache()

	withRules := base()
	// Same findings, only re-grouped by the rule that produced them.
	withRules.DetailsRuleFocused = []RuleDetails{
		{
			Rule:       "sensitive-content",
			Checkname:  "IsFreeOfKeywords",
			IssueCount: 2,
			Subjects: []RuleSubject{
				{Subject: "config.yaml", Path: "/path/config.yaml", IssueCount: 2},
			},
		},
		{
			Rule:       "documentation",
			Checkname:  "HasReadme",
			IssueCount: 1,
			Subjects: []RuleSubject{
				{Subject: "repository", IssueCount: 1},
			},
		},
	}
	withRules.BuildCache()

	if withRules.cachedTotalIssues != withoutRules.cachedTotalIssues {
		t.Errorf("Rule section changed the header total: got %d, want %d",
			withRules.cachedTotalIssues, withoutRules.cachedTotalIssues)
	}

	if withoutRules.cachedTotalIssues != 4 {
		t.Errorf("Baseline total mismatch: got %d, want 4", withoutRules.cachedTotalIssues)
	}
}

func TestCheckDetails_Structure(t *testing.T) {
	check := CheckDetails{
		Checkname: "IsFreeOfKeywords",
		Issues: []SubjectIssue{
			{Subject: "file1.txt", Path: "/path/file1.txt", Message: "Issue in file1"},
			{Subject: "file2.txt", Path: "/path/file2.txt", Message: "Issue in file2"},
		},
	}

	if check.Checkname != "IsFreeOfKeywords" {
		t.Errorf("Checkname mismatch: got %s, want IsFreeOfKeywords", check.Checkname)
	}

	if len(check.Issues) != 2 {
		t.Errorf("Expected 2 issues, got %d", len(check.Issues))
	}
}
