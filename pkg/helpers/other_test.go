package helpers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestWarnForLargeFile_SmallFile(t *testing.T) {
	// Create a temporary small file
	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "small.txt")

	content := []byte("small content")
	err := os.WriteFile(tempFile, content, 0644)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	// Set up logger to capture warnings
	originalMode := output.GlobalLogger.GetMessages()
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(true)

	file := structs.File{
		Name: "small.txt",
		Path: tempFile,
	}

	// Set limit larger than file size
	limitSize := int64(1000)
	WarnForLargeFile(file, limitSize, "Test warning message")

	// Should not generate any warnings
	messages := output.GlobalLogger.GetMessages()
	if len(messages) != 0 {
		t.Errorf("Expected no warnings for small file, got %d", len(messages))
	}

	// Restore original state
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(false)
	// Restore original messages if any
	for _, msg := range originalMode {
		switch msg.Level {
		case "warning":
			output.GlobalLogger.Warning(msg.Message)
		case "error":
			output.GlobalLogger.Error(msg.Message)
		case "info":
			output.GlobalLogger.Info(msg.Message)
		}
	}
}

func TestWarnForLargeFile_LargeFile(t *testing.T) {
	// Create a temporary large file
	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "large.txt")

	// Create content larger than our limit
	content := make([]byte, 2000)
	for i := range content {
		content[i] = 'a'
	}

	err := os.WriteFile(tempFile, content, 0644)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	// Set up logger to capture warnings
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(true)

	file := structs.File{
		Name: "large.txt",
		Path: tempFile,
	}

	// Set limit smaller than file size
	limitSize := int64(1000)
	WarnForLargeFile(file, limitSize, "File is too large")

	// Should generate a warning
	messages := output.GlobalLogger.GetMessages()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 warning for large file, got %d", len(messages))
	}

	warning := messages[0]
	if warning.Level != "warning" {
		t.Errorf("Expected warning level, got '%s'", warning.Level)
	}

	if warning.Message != "Warning for file 'large.txt': File is too large" {
		t.Errorf("Unexpected warning message: %s", warning.Message)
	}

	// Clean up
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(false)
}

func TestWarnForLargeFile_ExactLimit(t *testing.T) {
	// Create a file exactly at the limit
	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "exact.txt")

	content := make([]byte, 1000)
	err := os.WriteFile(tempFile, content, 0644)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	// Set up logger
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(true)

	file := structs.File{
		Name: "exact.txt",
		Path: tempFile,
	}

	// Set limit exactly equal to file size
	limitSize := int64(1000)
	WarnForLargeFile(file, limitSize, "Test message")

	// Should not generate a warning (only greater than, not equal)
	messages := output.GlobalLogger.GetMessages()
	if len(messages) != 0 {
		t.Errorf("Expected no warnings for file at exact limit, got %d", len(messages))
	}

	// Clean up
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(false)
}

func TestWarnForLargeFile_NonExistentFile(t *testing.T) {
	file := structs.File{
		Name: "nonexistent.txt",
		Path: "/path/to/nonexistent/file.txt",
	}

	// Should panic when file doesn't exist
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for non-existent file, but didn't panic")
		}
	}()

	WarnForLargeFile(file, 1000, "Test message")
}

func TestWarnForLargeFile_ZeroLimit(t *testing.T) {
	// Create a temporary file
	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "test.txt")

	content := []byte("any content")
	err := os.WriteFile(tempFile, content, 0644)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	// Set up logger
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(true)

	file := structs.File{
		Name: "test.txt",
		Path: tempFile,
	}

	// Zero limit means any file with content should trigger warning
	WarnForLargeFile(file, 0, "Zero limit test")

	messages := output.GlobalLogger.GetMessages()
	if len(messages) != 1 {
		t.Errorf("Expected 1 warning with zero limit, got %d", len(messages))
	}

	// Clean up
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(false)
}

func TestWarnForLargeFile_EmptyFile(t *testing.T) {
	// Create an empty file
	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "empty.txt")

	err := os.WriteFile(tempFile, []byte{}, 0644)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	// Set up logger
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(true)

	file := structs.File{
		Name: "empty.txt",
		Path: tempFile,
	}

	// Empty file should not trigger warning regardless of limit
	WarnForLargeFile(file, 0, "Empty file test")

	messages := output.GlobalLogger.GetMessages()
	if len(messages) != 0 {
		t.Errorf("Expected no warnings for empty file, got %d", len(messages))
	}

	// Clean up
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(false)
}

func TestWarnForLargeFile_MessageFormatting(t *testing.T) {
	// Test that the warning message is formatted correctly
	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "formatting_test.txt")

	content := make([]byte, 2000)
	err := os.WriteFile(tempFile, content, 0644)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	// Set up logger
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(true)

	file := structs.File{
		Name: "formatting_test.txt",
		Path: tempFile,
	}

	customMessage := "Custom warning: performance may be affected"
	WarnForLargeFile(file, 1000, customMessage)

	messages := output.GlobalLogger.GetMessages()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 warning, got %d", len(messages))
	}

	expectedMessage := "Warning for file 'formatting_test.txt': Custom warning: performance may be affected"
	if messages[0].Message != expectedMessage {
		t.Errorf("Expected message '%s', got '%s'", expectedMessage, messages[0].Message)
	}

	// Clean up
	output.GlobalLogger.ClearMessages()
	output.GlobalLogger.SetJSONMode(false)
}