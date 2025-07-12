package tui

import (
	"encoding/json"
	"testing"
	"time"
)

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
		Errors: []LogMessage{
			{Level: "error", Message: "Test error", Timestamp: timestamp},
		},
		Warnings: []LogMessage{
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
}

func TestEmptyScanResult(t *testing.T) {
	scanResult := ScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               []ScannedFile{},
		Skipped:               []SkippedFile{},
		DetailsSubjectFocused: []SubjectDetails{},
		DetailsCheckFocused:   []CheckDetails{},
		Errors:                []LogMessage{},
		Warnings:              []LogMessage{},
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