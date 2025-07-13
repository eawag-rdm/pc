package output

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestNewJSONFormatter(t *testing.T) {
	formatter := NewJSONFormatter()

	if formatter == nil {
		t.Fatal("NewJSONFormatter returned nil")
	}

	if formatter.warnings == nil {
		t.Error("Warnings slice not initialized")
	}

	if formatter.errors == nil {
		t.Error("Errors slice not initialized")
	}

	if formatter.scanStart.IsZero() {
		t.Error("Scan start time not set")
	}
}

func TestAddWarning(t *testing.T) {
	formatter := NewJSONFormatter()

	formatter.AddWarning("test_type", "test_file.txt", "Test warning message")

	if len(formatter.warnings) != 1 {
		t.Fatalf("Expected 1 warning, got %d", len(formatter.warnings))
	}

	warning := formatter.warnings[0]
	if warning.Type != "test_type" {
		t.Errorf("Expected type 'test_type', got '%s'", warning.Type)
	}

	if warning.File != "test_file.txt" {
		t.Errorf("Expected file 'test_file.txt', got '%s'", warning.File)
	}

	if warning.Message != "Test warning message" {
		t.Errorf("Expected message 'Test warning message', got '%s'", warning.Message)
	}
}

func TestAddError(t *testing.T) {
	formatter := NewJSONFormatter()

	formatter.AddError("test_error", "error_file.txt", "Test error message")

	if len(formatter.errors) != 1 {
		t.Fatalf("Expected 1 error, got %d", len(formatter.errors))
	}

	error := formatter.errors[0]
	if error.Type != "test_error" {
		t.Errorf("Expected type 'test_error', got '%s'", error.Type)
	}

	if error.File != "error_file.txt" {
		t.Errorf("Expected file 'error_file.txt', got '%s'", error.File)
	}

	if error.Message != "Test error message" {
		t.Errorf("Expected message 'Test error message', got '%s'", error.Message)
	}
}

func TestFormatResults_EmptyMessages(t *testing.T) {
	formatter := NewJSONFormatter()
	messages := []structs.Message{}

	result, err := formatter.FormatResults("/test/location", "LocalCollector", messages, 0, []string{})
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	// Verify it's valid JSON
	var scanResult ScanResult
	err = json.Unmarshal([]byte(result), &scanResult)
	if err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}

	// Verify basic structure
	if scanResult.Timestamp == "" {
		t.Error("Timestamp not set")
	}

	if scanResult.Scanned == nil {
		t.Error("Scanned slice is nil")
	} else if len(scanResult.Scanned) != 0 {
		t.Errorf("Expected empty scanned slice, got %d items", len(scanResult.Scanned))
	}

	if scanResult.Skipped == nil {
		t.Error("Skipped slice is nil")
	}
}

func TestFormatResults_WithMessages(t *testing.T) {
	formatter := NewJSONFormatter()
	
	// Create test file
	testFile := structs.File{
		Name: "test.go",
		Path: "/path/to/test.go",
	}

	messages := []structs.Message{
		{
			Content:  "Found keyword 'password'",
			Source:   testFile,
			TestName: "IsFreeOfKeywords",
		},
		{
			Content:  "File contains secrets",
			Source:   testFile,
			TestName: "SecretDetection",
		},
	}

	result, err := formatter.FormatResults("/test/location", "LocalCollector", messages, 1, []string{})
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	// Parse result
	var scanResult ScanResult
	err = json.Unmarshal([]byte(result), &scanResult)
	if err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}

	// Verify scanned files
	if len(scanResult.Scanned) != 1 {
		t.Fatalf("Expected 1 scanned file, got %d", len(scanResult.Scanned))
	}

	scannedFile := scanResult.Scanned[0]
	if scannedFile.Filename != "test.go" {
		t.Errorf("Expected filename 'test.go', got '%s'", scannedFile.Filename)
	}

	if len(scannedFile.Issues) != 2 {
		t.Fatalf("Expected 2 issues, got %d", len(scannedFile.Issues))
	}

	// Verify subject-focused details
	if len(scanResult.DetailsSubjectFocused) != 1 {
		t.Fatalf("Expected 1 subject detail, got %d", len(scanResult.DetailsSubjectFocused))
	}

	subjectDetail := scanResult.DetailsSubjectFocused[0]
	if subjectDetail.Subject != "test.go" {
		t.Errorf("Expected subject 'test.go', got '%s'", subjectDetail.Subject)
	}

	if len(subjectDetail.Issues) != 2 {
		t.Fatalf("Expected 2 issues in subject detail, got %d", len(subjectDetail.Issues))
	}

	// Verify check-focused details
	if len(scanResult.DetailsCheckFocused) != 2 {
		t.Fatalf("Expected 2 check details, got %d", len(scanResult.DetailsCheckFocused))
	}
}

func TestFormatResults_RepositoryMessage(t *testing.T) {
	formatter := NewJSONFormatter()
	
	// Create repository message (not associated with a file)
	repo := structs.Repository{Files: []structs.File{}}
	messages := []structs.Message{
		{
			Content:  "Repository-level issue",
			Source:   repo, // Repository source, not string
			TestName: "RepositoryCheck",
		},
	}

	result, err := formatter.FormatResults("/test/location", "LocalCollector", messages, 0, []string{})
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	// Parse result
	var scanResult ScanResult
	err = json.Unmarshal([]byte(result), &scanResult)
	if err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}

	// Repository messages shouldn't create scanned files
	if len(scanResult.Scanned) != 0 {
		t.Errorf("Expected 0 scanned files for repository message, got %d", len(scanResult.Scanned))
	}

	// But should appear in subject-focused details as "repository"
	if len(scanResult.DetailsSubjectFocused) != 1 {
		t.Fatalf("Expected 1 subject detail, got %d", len(scanResult.DetailsSubjectFocused))
	}

	if scanResult.DetailsSubjectFocused[0].Subject != "repository" {
		t.Errorf("Expected subject 'repository', got '%s'", scanResult.DetailsSubjectFocused[0].Subject)
	}
}

func TestProcessMessages(t *testing.T) {
	result := &ScanResult{}
	
	testFile := structs.File{
		Name: "example.txt",
		Path: "/path/to/example.txt",
	}

	messages := []structs.Message{
		{
			Content:  "Test message 1",
			Source:   testFile,
			TestName: "TestCheck1",
		},
		{
			Content:  "Test message 2",
			Source:   testFile,
			TestName: "TestCheck2",
		},
	}

	result.processMessages(messages)

	// Verify scanned files
	if len(result.Scanned) != 1 {
		t.Fatalf("Expected 1 scanned file, got %d", len(result.Scanned))
	}

	if result.Scanned[0].Filename != "example.txt" {
		t.Errorf("Expected filename 'example.txt', got '%s'", result.Scanned[0].Filename)
	}

	if len(result.Scanned[0].Issues) != 2 {
		t.Fatalf("Expected 2 issues, got %d", len(result.Scanned[0].Issues))
	}

	// Verify subject details
	if len(result.DetailsSubjectFocused) != 1 {
		t.Fatalf("Expected 1 subject detail, got %d", len(result.DetailsSubjectFocused))
	}

	if len(result.DetailsSubjectFocused[0].Issues) != 2 {
		t.Fatalf("Expected 2 issues in subject detail, got %d", len(result.DetailsSubjectFocused[0].Issues))
	}

	// Verify check details
	if len(result.DetailsCheckFocused) != 2 {
		t.Fatalf("Expected 2 check details, got %d", len(result.DetailsCheckFocused))
	}
}

func TestJSONStructureIntegrity(t *testing.T) {
	formatter := NewJSONFormatter()
	
	testFile := structs.File{
		Name: "integrity_test.go",
		Path: "/test/integrity_test.go",
	}

	messages := []structs.Message{
		{
			Content:  "Integrity test message",
			Source:   testFile,
			TestName: "IntegrityCheck",
		},
	}

	result, err := formatter.FormatResults("/test", "LocalCollector", messages, 1, []string{})
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	// Verify the JSON is properly formatted and contains expected fields
	if !strings.Contains(result, "timestamp") {
		t.Error("JSON missing timestamp field")
	}

	if !strings.Contains(result, "scanned") {
		t.Error("JSON missing scanned field")
	}

	if !strings.Contains(result, "skipped") {
		t.Error("JSON missing skipped field")
	}

	if !strings.Contains(result, "details_subject_focused") {
		t.Error("JSON missing details_subject_focused field")
	}

	if !strings.Contains(result, "details_check_focused") {
		t.Error("JSON missing details_check_focused field")
	}

	if !strings.Contains(result, "errors") {
		t.Error("JSON missing errors field")
	}

	if !strings.Contains(result, "warnings") {
		t.Error("JSON missing warnings field")
	}
}