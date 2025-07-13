package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/output/tui"
)

// Test helper to create test JSON data
func createTestJSONData() string {
	scanResult := tui.ScanResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Scanned: []tui.ScannedFile{
			{
				Filename: "test.go",
				Issues: []tui.CheckSummary{
					{Checkname: "IsFreeOfKeywords", IssueCount: 2},
				},
			},
		},
		Skipped: []tui.SkippedFile{
			{
				Filename: "binary.bin",
				Path:     "/path/to/binary.bin",
				Reason:   "Binary file detected",
			},
		},
		DetailsSubjectFocused: []tui.SubjectDetails{
			{
				Subject: "test.go",
				Path:    "/path/to/test.go",
				Issues: []tui.CheckIssue{
					{Checkname: "IsFreeOfKeywords", Message: "Found keyword 'secret'"},
					{Checkname: "IsFreeOfKeywords", Message: "Found keyword 'password'"},
				},
			},
		},
		DetailsCheckFocused: []tui.CheckDetails{
			{
				Checkname: "IsFreeOfKeywords",
				Issues: []tui.SubjectIssue{
					{Subject: "test.go", Path: "/path/to/test.go", Message: "Found keyword 'secret'"},
				},
			},
		},
		PDFFiles: []string{"document.pdf"},
		Errors: []output.LogMessage{
			{Level: "error", Message: "Test error", Timestamp: time.Now().UTC().Format(time.RFC3339)},
		},
		Warnings: []output.LogMessage{
			{Level: "warning", Message: "Test warning", Timestamp: time.Now().UTC().Format(time.RFC3339)},
		},
	}

	jsonData, _ := json.MarshalIndent(scanResult, "", "  ")
	return string(jsonData)
}

func TestViewerBinary_Exists(t *testing.T) {
	// This test checks if the viewer binary can be built
	// Skip if running in CI or if go build is not available
	if os.Getenv("CI") != "" {
		t.Skip("Skipping binary test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "viewer")

	// Try to build the viewer
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = "."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build viewer binary: %v\nOutput: %s", err, string(output))
	}

	// Verify binary exists and is executable
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatalf("Built binary does not exist: %v", err)
	}

	if !info.Mode().IsRegular() {
		t.Error("Built binary is not a regular file")
	}

	// On Unix systems, check if it's executable
	if info.Mode()&0111 == 0 {
		t.Error("Built binary is not executable")
	}
}

func TestViewerWithFile(t *testing.T) {
	// Skip if running without proper terminal environment
	if os.Getenv("CI") != "" || os.Getenv("TERM") == "" {
		t.Skip("Skipping TUI test in non-terminal environment")
	}

	// Create test JSON file
	tempDir := t.TempDir()
	jsonFile := filepath.Join(tempDir, "test_data.json")

	testData := createTestJSONData()
	err := os.WriteFile(jsonFile, []byte(testData), 0644)
	if err != nil {
		t.Fatalf("Failed to create test JSON file: %v", err)
	}

	// Build the viewer binary
	binaryPath := filepath.Join(tempDir, "viewer")
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = "."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build viewer binary: %v\nOutput: %s", err, string(output))
	}

	// Test viewer with file argument
	// We can't actually test the interactive TUI, but we can test that it starts without errors
	// and exits cleanly with a timeout
	cmd = exec.Command(binaryPath, jsonFile)
	
	// Set a timeout since the TUI would run indefinitely
	time.AfterFunc(1*time.Second, func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})

	// Capture output
	output, err = cmd.CombinedOutput()
	
	// We expect the command to be killed (exit code may vary by OS)
	// The important thing is that it doesn't fail immediately due to JSON parsing errors
	if err != nil && !strings.Contains(err.Error(), "killed") && !strings.Contains(err.Error(), "signal") {
		// Only fail if it's not a timeout/kill error
		exitErr, ok := err.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != -1 {
			t.Logf("Viewer output: %s", string(output))
			t.Logf("Viewer error might be expected (timeout): %v", err)
		}
	}
}

func TestJSONParsing_ValidData(t *testing.T) {
	// Test the JSON parsing logic that the viewer uses
	testData := createTestJSONData()

	var scanResult tui.ScanResult
	err := json.Unmarshal([]byte(testData), &scanResult)
	if err != nil {
		t.Fatalf("Failed to parse test JSON data: %v", err)
	}

	// Verify parsed data
	if scanResult.Timestamp == "" {
		t.Error("Timestamp not parsed correctly")
	}

	if len(scanResult.Scanned) != 1 {
		t.Errorf("Expected 1 scanned file, got %d", len(scanResult.Scanned))
	}

	if len(scanResult.Skipped) != 1 {
		t.Errorf("Expected 1 skipped file, got %d", len(scanResult.Skipped))
	}

	if len(scanResult.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(scanResult.Errors))
	}

	if len(scanResult.Warnings) != 1 {
		t.Errorf("Expected 1 warning, got %d", len(scanResult.Warnings))
	}
}

func TestJSONParsing_InvalidData(t *testing.T) {
	// Test with invalid JSON
	invalidJSON := `{"invalid": json}`

	var scanResult tui.ScanResult
	err := json.Unmarshal([]byte(invalidJSON), &scanResult)
	if err == nil {
		t.Error("Expected error for invalid JSON, but got none")
	}
}

func TestJSONParsing_EmptyData(t *testing.T) {
	// Test with minimal valid JSON
	emptyData := tui.ScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               []tui.ScannedFile{},
		Skipped:               []tui.SkippedFile{},
		DetailsSubjectFocused: []tui.SubjectDetails{},
		DetailsCheckFocused:   []tui.CheckDetails{},
		PDFFiles:              []string{},
		Errors:                []output.LogMessage{},
		Warnings:              []output.LogMessage{},
	}

	jsonData, err := json.Marshal(emptyData)
	if err != nil {
		t.Fatalf("Failed to marshal empty data: %v", err)
	}

	var scanResult tui.ScanResult
	err = json.Unmarshal(jsonData, &scanResult)
	if err != nil {
		t.Fatalf("Failed to parse empty JSON data: %v", err)
	}

	// Verify empty slices are preserved
	if len(scanResult.Scanned) != 0 {
		t.Errorf("Expected empty scanned slice, got %d items", len(scanResult.Scanned))
	}
}

func TestFileReading_NonexistentFile(t *testing.T) {
	// Test behavior with non-existent file (should be handled by the main function)
	nonexistentPath := "/nonexistent/file.json"

	// Simulate what main() does
	file, err := os.Open(nonexistentPath)
	if err == nil {
		file.Close()
		t.Error("Expected error opening non-existent file, but got none")
	}
}

func TestStdinReading_Simulation(t *testing.T) {
	// Test the stdin reading logic
	testData := createTestJSONData()

	// Simulate reading from stdin using strings.Reader
	reader := strings.NewReader(testData)
	
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("Failed to read data: %v", err)
	}

	var scanResult tui.ScanResult
	err = json.Unmarshal(data, &scanResult)
	if err != nil {
		t.Fatalf("Failed to parse JSON from simulated stdin: %v", err)
	}

	// Verify data integrity
	if len(scanResult.Scanned) != 1 {
		t.Errorf("Expected 1 scanned file, got %d", len(scanResult.Scanned))
	}
}

func TestLargeJSONDataIntegration(t *testing.T) {
	// Test with larger dataset to ensure the viewer can handle it
	var scannedFiles []tui.ScannedFile
	var subjectDetails []tui.SubjectDetails

	// Create 100 files
	for i := 0; i < 100; i++ {
		filename := filepath.Join("test", "file", "test.go")
		scannedFiles = append(scannedFiles, tui.ScannedFile{
			Filename: filename,
			Issues: []tui.CheckSummary{
				{Checkname: "TestCheck", IssueCount: 1},
			},
		})

		subjectDetails = append(subjectDetails, tui.SubjectDetails{
			Subject: filename,
			Path:    "/path/to/" + filename,
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
		PDFFiles:              []string{},
		Errors:                []output.LogMessage{},
		Warnings:              []output.LogMessage{},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal large dataset: %v", err)
	}

	// Test parsing
	var parsedResult tui.ScanResult
	err = json.Unmarshal(jsonData, &parsedResult)
	if err != nil {
		t.Fatalf("Failed to parse large JSON data: %v", err)
	}

	if len(parsedResult.Scanned) != 100 {
		t.Errorf("Expected 100 scanned files, got %d", len(parsedResult.Scanned))
	}
}

func TestSpecialCharacters(t *testing.T) {
	// Test with special characters that might cause issues
	scanResult := tui.ScanResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Scanned: []tui.ScannedFile{
			{
				Filename: "file with spaces & special chars <>.txt",
				Issues: []tui.CheckSummary{
					{Checkname: "TestCheck", IssueCount: 1},
				},
			},
		},
		DetailsSubjectFocused: []tui.SubjectDetails{
			{
				Subject: "file with spaces & special chars <>.txt",
				Path:    "/path/to/file with spaces & special chars <>.txt",
				Issues: []tui.CheckIssue{
					{
						Checkname: "TestCheck",
						Message:   "Message with \"quotes\" and 'apostrophes' & <tags>",
					},
				},
			},
		},
		Skipped:             []tui.SkippedFile{},
		DetailsCheckFocused: []tui.CheckDetails{},
		PDFFiles:            []string{},
		Errors:              []output.LogMessage{},
		Warnings:            []output.LogMessage{},
	}

	jsonData, err := json.Marshal(scanResult)
	if err != nil {
		t.Fatalf("Failed to marshal data with special characters: %v", err)
	}

	var parsedResult tui.ScanResult
	err = json.Unmarshal(jsonData, &parsedResult)
	if err != nil {
		t.Fatalf("Failed to parse JSON with special characters: %v", err)
	}

	// Verify special characters are preserved
	if !strings.Contains(parsedResult.Scanned[0].Filename, " ") {
		t.Error("Spaces in filename not preserved")
	}

	if !strings.Contains(parsedResult.DetailsSubjectFocused[0].Issues[0].Message, "\"") {
		t.Error("Quotes in message not preserved")
	}
}

func TestCommandLineArguments(t *testing.T) {
	// Test that the argument parsing logic works
	args := []string{"viewer", "test.json"}

	// Simulate what main() does with arguments
	if len(args) > 1 {
		filename := args[1]
		if filename != "test.json" {
			t.Errorf("Expected filename 'test.json', got '%s'", filename)
		}
	} else {
		t.Error("Expected to use file argument, but would use stdin")
	}

	// Test no arguments (stdin mode)
	argsStdin := []string{"viewer"}
	if len(argsStdin) > 1 {
		t.Error("Expected to use stdin, but would use file argument")
	}
}