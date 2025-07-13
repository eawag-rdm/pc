package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/output/tui"
	"github.com/eawag-rdm/pc/pkg/output"
)

func TestJSONParsing(t *testing.T) {
	// Create test JSON data
	scanResult := tui.ScanResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Scanned: []tui.ScannedFile{
			{Filename: "test.go", Issues: []tui.CheckSummary{{Checkname: "TestCheck", IssueCount: 1}}},
		},
		Skipped: []tui.SkippedFile{
			{Filename: "binary.bin", Reason: "Binary file detected"},
		},
		DetailsSubjectFocused: []tui.SubjectDetails{
			{Subject: "test.go", Path: "/path/test.go", Issues: []tui.CheckIssue{{Checkname: "TestCheck", Message: "Test issue"}}},
		},
		DetailsCheckFocused: []tui.CheckDetails{
			{Checkname: "TestCheck", Issues: []tui.SubjectIssue{{Subject: "test.go", Path: "/path/test.go", Message: "Test issue"}}},
		},
		Errors:   []output.LogMessage{{Level: "error", Message: "Test error", Timestamp: time.Now().UTC().Format(time.RFC3339)}},
		Warnings: []output.LogMessage{{Level: "warning", Message: "Test warning", Timestamp: time.Now().UTC().Format(time.RFC3339)}},
	}

	// Convert to JSON
	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	// Test parsing
	var parsed tui.ScanResult
	err = json.Unmarshal(jsonData, &parsed)
	if err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	// Verify parsed data
	if parsed.Timestamp != scanResult.Timestamp {
		t.Errorf("Timestamp mismatch")
	}

	if len(parsed.Scanned) != 1 {
		t.Errorf("Expected 1 scanned file, got %d", len(parsed.Scanned))
	}

	if len(parsed.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(parsed.Errors))
	}

	if len(parsed.Warnings) != 1 {
		t.Errorf("Expected 1 warning, got %d", len(parsed.Warnings))
	}
}

func TestEmptyJSONParsing(t *testing.T) {
	// Test with minimal/empty data
	scanResult := tui.ScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               []tui.ScannedFile{},
		Skipped:               []tui.SkippedFile{},
		DetailsSubjectFocused: []tui.SubjectDetails{},
		DetailsCheckFocused:   []tui.CheckDetails{},
		Errors:                []output.LogMessage{},
		Warnings:              []output.LogMessage{},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal empty data: %v", err)
	}

	var parsed tui.ScanResult
	err = json.Unmarshal(jsonData, &parsed)
	if err != nil {
		t.Fatalf("Failed to parse empty JSON: %v", err)
	}

	// Verify empty slices are preserved
	if len(parsed.Scanned) != 0 {
		t.Errorf("Expected empty scanned slice, got %d items", len(parsed.Scanned))
	}
}

func TestInvalidJSONHandling(t *testing.T) {
	invalidJSON := `{"invalid": json}`

	var scanResult tui.ScanResult
	err := json.Unmarshal([]byte(invalidJSON), &scanResult)
	if err == nil {
		t.Error("Expected error for invalid JSON, but got none")
	}
}

func TestFileReading(t *testing.T) {
	// Create temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.json")

	// Create test data
	scanResult := tui.ScanResult{
		Timestamp: "2023-07-12T10:00:00Z",
		Scanned:   []tui.ScannedFile{{Filename: "test.go", Issues: []tui.CheckSummary{}}},
		Skipped:   []tui.SkippedFile{},
		DetailsSubjectFocused: []tui.SubjectDetails{},
		DetailsCheckFocused:   []tui.CheckDetails{},
		Errors:    []output.LogMessage{},
		Warnings:  []output.LogMessage{},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	// Write to temp file
	err = os.WriteFile(testFile, jsonData, 0644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Test reading the file
	file, err := os.Open(testFile)
	if err != nil {
		t.Fatalf("Failed to open test file: %v", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("Failed to read test file: %v", err)
	}

	var parsed tui.ScanResult
	err = json.Unmarshal(data, &parsed)
	if err != nil {
		t.Fatalf("Failed to parse JSON from file: %v", err)
	}

	if parsed.Timestamp != "2023-07-12T10:00:00Z" {
		t.Errorf("Expected timestamp '2023-07-12T10:00:00Z', got '%s'", parsed.Timestamp)
	}
}

func TestStdinReading(t *testing.T) {
	// Create test data
	scanResult := tui.ScanResult{
		Timestamp: "2023-07-12T10:00:00Z",
		Scanned:   []tui.ScannedFile{},
		Skipped:   []tui.SkippedFile{},
		DetailsSubjectFocused: []tui.SubjectDetails{},
		DetailsCheckFocused:   []tui.CheckDetails{},
		Errors:    []output.LogMessage{},
		Warnings:  []output.LogMessage{},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	// Simulate stdin with bytes.Reader
	input := bytes.NewReader(jsonData)

	data, err := io.ReadAll(input)
	if err != nil {
		t.Fatalf("Failed to read from simulated stdin: %v", err)
	}

	var parsed tui.ScanResult
	err = json.Unmarshal(data, &parsed)
	if err != nil {
		t.Fatalf("Failed to parse JSON from stdin: %v", err)
	}

	if parsed.Timestamp != "2023-07-12T10:00:00Z" {
		t.Errorf("Expected timestamp '2023-07-12T10:00:00Z', got '%s'", parsed.Timestamp)
	}
}

func TestLargeJSONData(t *testing.T) {
	// Create larger test data
	var scannedFiles []tui.ScannedFile
	var subjectDetails []tui.SubjectDetails

	// Add many files
	for i := 0; i < 100; i++ {
		filename := fmt.Sprintf("file%d.go", i)
		scannedFiles = append(scannedFiles, tui.ScannedFile{
			Filename: filename,
			Issues: []tui.CheckSummary{
				{Checkname: "TestCheck", IssueCount: 1},
			},
		})

		subjectDetails = append(subjectDetails, tui.SubjectDetails{
			Subject: filename,
			Path:    "/path/" + filename,
			Issues: []tui.CheckIssue{
				{Checkname: "TestCheck", Message: "Test issue in " + filename},
			},
		})
	}

	scanResult := tui.ScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               scannedFiles,
		Skipped:               []tui.SkippedFile{},
		DetailsSubjectFocused: subjectDetails,
		DetailsCheckFocused:   []tui.CheckDetails{},
		Errors:                []output.LogMessage{},
		Warnings:              []output.LogMessage{},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal large test data: %v", err)
	}

	var parsed tui.ScanResult
	err = json.Unmarshal(jsonData, &parsed)
	if err != nil {
		t.Fatalf("Failed to parse large JSON: %v", err)
	}

	if len(parsed.Scanned) != 100 {
		t.Errorf("Expected 100 scanned files, got %d", len(parsed.Scanned))
	}

	if len(parsed.DetailsSubjectFocused) != 100 {
		t.Errorf("Expected 100 subject details, got %d", len(parsed.DetailsSubjectFocused))
	}
}

func TestSpecialCharactersInJSON(t *testing.T) {
	// Test with special characters that might cause JSON issues
	scanResult := tui.ScanResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Scanned: []tui.ScannedFile{
			{Filename: "file with spaces.txt", Issues: []tui.CheckSummary{}},
			{Filename: "file-with-unicode-™.txt", Issues: []tui.CheckSummary{}},
		},
		DetailsSubjectFocused: []tui.SubjectDetails{
			{
				Subject: "file with spaces.txt",
				Path:    "/path/to/file with spaces.txt",
				Issues: []tui.CheckIssue{
					{Checkname: "TestCheck", Message: "Message with \"quotes\" and 'apostrophes'"},
				},
			},
		},
		Skipped:             []tui.SkippedFile{},
		DetailsCheckFocused: []tui.CheckDetails{},
		Errors:              []output.LogMessage{},
		Warnings:            []output.LogMessage{},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal data with special characters: %v", err)
	}

	var parsed tui.ScanResult
	err = json.Unmarshal(jsonData, &parsed)
	if err != nil {
		t.Fatalf("Failed to parse JSON with special characters: %v", err)
	}

	// Verify special characters are preserved
	if !strings.Contains(parsed.Scanned[0].Filename, " ") {
		t.Error("Spaces in filename not preserved")
	}

	if !strings.Contains(parsed.Scanned[1].Filename, "™") {
		t.Error("Unicode characters not preserved")
	}

	if !strings.Contains(parsed.DetailsSubjectFocused[0].Issues[0].Message, "\"") {
		t.Error("Quotes in message not preserved")
	}
}

