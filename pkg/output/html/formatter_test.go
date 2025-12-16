package html

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/output"
)

// Test data structures that match the expected JSON format
type TestScanResult struct {
	Timestamp             string                `json:"timestamp"`
	Scanned               []TestScannedFile     `json:"scanned"`
	Skipped               []TestSkippedFile     `json:"skipped"`
	DetailsSubjectFocused []TestSubjectDetails  `json:"details_subject_focused"`
	DetailsCheckFocused   []TestCheckDetails    `json:"details_check_focused"`
	PDFFiles              []string              `json:"pdf_files"`
	Errors                []output.LogMessage   `json:"errors"`
	Warnings              []output.LogMessage   `json:"warnings"`
}

type TestScannedFile struct {
	Filename string             `json:"filename"`
	Issues   []TestCheckSummary `json:"issues"`
}

type TestCheckSummary struct {
	Checkname  string `json:"checkname"`
	IssueCount int    `json:"issue_count"`
}

type TestSkippedFile struct {
	Filename string `json:"filename"`
	Path     string `json:"path,omitempty"`
	Reason   string `json:"reason"`
}

type TestSubjectDetails struct {
	Subject string           `json:"subject"`
	Path    string           `json:"path,omitempty"`
	Issues  []TestCheckIssue `json:"issues"`
}

type TestCheckIssue struct {
	Checkname string `json:"checkname"`
	Message   string `json:"message"`
}

type TestCheckDetails struct {
	Checkname string             `json:"checkname"`
	Issues    []TestSubjectIssue `json:"issues"`
}

type TestSubjectIssue struct {
	Subject string `json:"subject"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

func TestNewHTMLFormatter(t *testing.T) {
	formatter := NewHTMLFormatter()
	
	if formatter == nil {
		t.Fatal("NewHTMLFormatter returned nil")
	}
}

func TestGenerateReport_Success(t *testing.T) {
	// Create test data
	timestamp := time.Now().UTC().Format(time.RFC3339)
	scanResult := TestScanResult{
		Timestamp: timestamp,
		Scanned: []TestScannedFile{
			{
				Filename: "test.go",
				Issues: []TestCheckSummary{
					{Checkname: "IsFreeOfKeywords", IssueCount: 2},
				},
			},
		},
		Skipped: []TestSkippedFile{
			{
				Filename: "binary.bin",
				Path:     "/path/to/binary.bin",
				Reason:   "Binary file detected",
			},
		},
		DetailsSubjectFocused: []TestSubjectDetails{
			{
				Subject: "test.go",
				Path:    "/path/to/test.go",
				Issues: []TestCheckIssue{
					{Checkname: "IsFreeOfKeywords", Message: "Found keyword 'secret'"},
					{Checkname: "IsFreeOfKeywords", Message: "Found keyword 'password'"},
				},
			},
		},
		DetailsCheckFocused: []TestCheckDetails{
			{
				Checkname: "IsFreeOfKeywords",
				Issues: []TestSubjectIssue{
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

	// Convert to JSON
	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	// Create temporary output file
	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "test_report.html")

	// Generate report
	formatter := NewHTMLFormatter()
	err = formatter.GenerateReport(string(jsonData), outputPath)
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Fatal("HTML report file was not created")
	}

	// Read and verify content
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read generated HTML file: %v", err)
	}

	htmlContent := string(content)

	// Verify basic HTML structure
	if !strings.Contains(htmlContent, "<!DOCTYPE html>") {
		t.Error("Generated HTML is missing DOCTYPE declaration")
	}

	if !strings.Contains(htmlContent, "<html lang=\"en\">") {
		t.Error("Generated HTML is missing html tag")
	}

	if !strings.Contains(htmlContent, "Package Checker Scanner Report") {
		t.Error("Generated HTML is missing title")
	}

	// Verify embedded data
	if !strings.Contains(htmlContent, "const scanData = ") {
		t.Error("Generated HTML is missing embedded scan data")
	}

	// Verify navigation sections are present
	expectedSections := []string{"Subjects", "Checks", "Skipped Files", "Warnings", "Errors"}
	for _, section := range expectedSections {
		if !strings.Contains(htmlContent, section) {
			t.Errorf("Generated HTML is missing navigation section: %s", section)
		}
	}

	// Verify CSS is embedded
	if !strings.Contains(htmlContent, ".app-layout") {
		t.Error("Generated HTML is missing CSS styles")
	}

	// Verify JavaScript is embedded
	if !strings.Contains(htmlContent, "function toggleTheme()") {
		t.Error("Generated HTML is missing JavaScript functionality")
	}
}

func TestGenerateReport_EmptyData(t *testing.T) {
	// Test with minimal/empty data
	scanResult := TestScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               []TestScannedFile{},
		Skipped:               []TestSkippedFile{},
		DetailsSubjectFocused: []TestSubjectDetails{},
		DetailsCheckFocused:   []TestCheckDetails{},
		PDFFiles:              []string{},
		Errors:                []output.LogMessage{},
		Warnings:              []output.LogMessage{},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal empty test data: %v", err)
	}

	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "empty_report.html")

	formatter := NewHTMLFormatter()
	err = formatter.GenerateReport(string(jsonData), outputPath)
	if err != nil {
		t.Fatalf("GenerateReport failed with empty data: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Fatal("HTML report file was not created for empty data")
	}

	// Read content
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read generated HTML file: %v", err)
	}

	// Verify basic structure is still present
	htmlContent := string(content)
	if !strings.Contains(htmlContent, "Package Checker Scanner Report") {
		t.Error("Generated HTML with empty data is missing title")
	}
}

func TestGenerateReport_InvalidJSON(t *testing.T) {
	invalidJSON := `{"invalid": json}`
	
	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "invalid_report.html")

	formatter := NewHTMLFormatter()
	err := formatter.GenerateReport(invalidJSON, outputPath)
	
	if err == nil {
		t.Error("Expected error for invalid JSON, but got none")
	}

	if !strings.Contains(err.Error(), "failed to parse JSON data") {
		t.Errorf("Expected JSON parsing error, got: %v", err)
	}
}

func TestGenerateReport_InvalidOutputPath(t *testing.T) {
	scanResult := TestScanResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	// Use path that cannot be written to even as root
	// /dev/null is a device, not a directory, so /dev/null/anything will always fail
	invalidPath := "/dev/null/report.html"

	formatter := NewHTMLFormatter()
	err = formatter.GenerateReport(string(jsonData), invalidPath)

	if err == nil {
		t.Error("Expected error for invalid output path, but got none")
	}
}

func TestGenerateReport_DirectoryCreation(t *testing.T) {
	scanResult := TestScanResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	tempDir := t.TempDir()
	// Create nested directory path that doesn't exist yet
	outputPath := filepath.Join(tempDir, "reports", "2023", "test_report.html")

	formatter := NewHTMLFormatter()
	err = formatter.GenerateReport(string(jsonData), outputPath)
	if err != nil {
		t.Fatalf("GenerateReport failed to create nested directories: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Fatal("HTML report file was not created in nested directory")
	}

	// Verify directory structure was created
	dirPath := filepath.Dir(outputPath)
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		t.Fatal("Nested directories were not created")
	}
}

func TestGenerateReport_SpecialCharacters(t *testing.T) {
	// Test with special characters that might cause HTML issues
	scanResult := TestScanResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Scanned: []TestScannedFile{
			{
				Filename: "file with spaces & special chars <>.txt",
				Issues: []TestCheckSummary{
					{Checkname: "TestCheck", IssueCount: 1},
				},
			},
		},
		DetailsSubjectFocused: []TestSubjectDetails{
			{
				Subject: "file with spaces & special chars <>.txt",
				Path:    "/path/to/file with spaces & special chars <>.txt",
				Issues: []TestCheckIssue{
					{
						Checkname: "TestCheck",
						Message:   "Message with \"quotes\" and 'apostrophes' & <tags>",
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal test data with special characters: %v", err)
	}

	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "special_chars_report.html")

	formatter := NewHTMLFormatter()
	err = formatter.GenerateReport(string(jsonData), outputPath)
	if err != nil {
		t.Fatalf("GenerateReport failed with special characters: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Fatal("HTML report file was not created with special characters")
	}

	// Read and verify content doesn't break HTML structure
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read generated HTML file: %v", err)
	}

	htmlContent := string(content)
	
	// Verify basic HTML structure is intact
	if !strings.Contains(htmlContent, "<!DOCTYPE html>") {
		t.Error("HTML structure broken with special characters")
	}

	// Verify the data is properly escaped in JavaScript
	if !strings.Contains(htmlContent, "const scanData = ") {
		t.Error("JavaScript data embedding failed with special characters")
	}
}

func TestGenerateReport_LargeDataset(t *testing.T) {
	// Create larger test dataset
	var scannedFiles []TestScannedFile
	var subjectDetails []TestSubjectDetails

	// Add many files
	for i := 0; i < 50; i++ {
		filename := filepath.Join("dir", "subdir", "file", "test", "file.go")
		scannedFiles = append(scannedFiles, TestScannedFile{
			Filename: filename,
			Issues: []TestCheckSummary{
				{Checkname: "TestCheck1", IssueCount: 2},
				{Checkname: "TestCheck2", IssueCount: 1},
			},
		})

		subjectDetails = append(subjectDetails, TestSubjectDetails{
			Subject: filename,
			Path:    "/path/to/" + filename,
			Issues: []TestCheckIssue{
				{Checkname: "TestCheck1", Message: "First issue in " + filename},
				{Checkname: "TestCheck1", Message: "Second issue in " + filename},
				{Checkname: "TestCheck2", Message: "Third issue in " + filename},
			},
		})
	}

	scanResult := TestScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               scannedFiles,
		Skipped:               []TestSkippedFile{},
		DetailsSubjectFocused: subjectDetails,
		DetailsCheckFocused:   []TestCheckDetails{},
		PDFFiles:              []string{},
		Errors:                []output.LogMessage{},
		Warnings:              []output.LogMessage{},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal large test data: %v", err)
	}

	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "large_report.html")

	formatter := NewHTMLFormatter()
	err = formatter.GenerateReport(string(jsonData), outputPath)
	if err != nil {
		t.Fatalf("GenerateReport failed with large dataset: %v", err)
	}

	// Verify file was created and has reasonable size
	fileInfo, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal("HTML report file was not created for large dataset")
	}

	// File should be reasonably large (> 50KB for 50 files with embedded CSS/JS)
	if fileInfo.Size() < 50000 {
		t.Errorf("Generated HTML file seems too small for large dataset: %d bytes", fileInfo.Size())
	}
}

func TestGenerateReport_ContentValidation(t *testing.T) {
	// Test specific content validation
	timestamp := "2023-07-12T10:00:00Z"
	scanResult := TestScanResult{
		Timestamp: timestamp,
		Scanned: []TestScannedFile{
			{Filename: "validation_test.go", Issues: []TestCheckSummary{}},
		},
		Skipped: []TestSkippedFile{
			{Filename: "skipped.bin", Reason: "Binary file detected", Path: "/path/skipped.bin"},
		},
		DetailsSubjectFocused: []TestSubjectDetails{},
		DetailsCheckFocused:   []TestCheckDetails{},
		PDFFiles:              []string{"doc1.pdf", "doc2.pdf"},
		Errors: []output.LogMessage{
			{Level: "error", Message: "Critical error occurred", Timestamp: timestamp},
		},
		Warnings: []output.LogMessage{
			{Level: "warning", Message: "Warning: potential issue", Timestamp: timestamp},
		},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal validation test data: %v", err)
	}

	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "validation_report.html")

	formatter := NewHTMLFormatter()
	err = formatter.GenerateReport(string(jsonData), outputPath)
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	// Read content
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read generated HTML file: %v", err)
	}

	htmlContent := string(content)

	// Verify timestamp is embedded
	if !strings.Contains(htmlContent, timestamp) {
		t.Error("Generated HTML does not contain the timestamp")
	}

	// Verify generation timestamp is present
	if !strings.Contains(htmlContent, "Generated on") {
		t.Error("Generated HTML is missing generation timestamp")
	}

	// Verify the embedded JSON contains our test data
	if !strings.Contains(htmlContent, "validation_test.go") {
		t.Error("Generated HTML does not contain scanned file data")
	}

	if !strings.Contains(htmlContent, "skipped.bin") {
		t.Error("Generated HTML does not contain skipped file data")
	}

	if !strings.Contains(htmlContent, "Critical error occurred") {
		t.Error("Generated HTML does not contain error data")
	}

	if !strings.Contains(htmlContent, "Warning: potential issue") {
		t.Error("Generated HTML does not contain warning data")
	}
}

func TestGenerateReport_FilePermissions(t *testing.T) {
	scanResult := TestScanResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "permissions_test.html")

	formatter := NewHTMLFormatter()
	err = formatter.GenerateReport(string(jsonData), outputPath)
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	// Check file permissions
	fileInfo, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("Failed to stat generated file: %v", err)
	}

	// File should be readable
	file, err := os.Open(outputPath)
	if err != nil {
		t.Errorf("Generated file is not readable: %v", err)
	}
	defer file.Close()

	// Should be able to read content
	_, err = io.ReadAll(file)
	if err != nil {
		t.Errorf("Failed to read generated file content: %v", err)
	}

	// Verify it's a regular file
	if !fileInfo.Mode().IsRegular() {
		t.Error("Generated file is not a regular file")
	}
}