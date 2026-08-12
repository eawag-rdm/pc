package json

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

func TestFormatResults_NilPDFFiles(t *testing.T) {
	formatter := NewJSONFormatter()

	result, err := formatter.FormatResults("/test/location", "CkanCollector", []structs.Message{}, 0, nil)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	// pdf_files must serialize as [] even when the caller passes nil (e.g. an
	// empty FileTracker.SnapshotFiles), never as JSON null.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result), &raw); err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}
	if string(raw["pdf_files"]) == "null" {
		t.Error("pdf_files serialized as null; want []")
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

func TestFormatResults_SkippedMessageRoutedToSkipped(t *testing.T) {
	formatter := NewJSONFormatter()

	oversizedFile := structs.File{
		Name:        "huge.bin",
		Path:        "/path/to/huge.bin",
		DisplayName: "huge.bin",
	}

	reason := "Skipped content scan of file: file size (2000 bytes) exceeds maximum (1000 bytes)."
	normalFile := structs.File{Name: "ok.txt", Path: "/path/to/ok.txt"}

	messages := []structs.Message{
		{
			Content:  reason,
			Source:   oversizedFile,
			TestName: "IsFreeOfKeywords",
			Skipped:  true,
			Reason:   reason,
		},
		{
			Content:  "Found keyword 'secret'",
			Source:   normalFile,
			TestName: "IsFreeOfKeywords",
		},
	}

	result, err := formatter.FormatResults("/test/location", "LocalCollector", messages, 2, []string{})
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	var scanResult ScanResult
	if err := json.Unmarshal([]byte(result), &scanResult); err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}

	// The skip message must land in skipped[].
	if len(scanResult.Skipped) != 1 {
		t.Fatalf("Expected 1 skipped file, got %d", len(scanResult.Skipped))
	}
	if scanResult.Skipped[0].Filename != "huge.bin" {
		t.Errorf("Expected skipped filename 'huge.bin', got '%s'", scanResult.Skipped[0].Filename)
	}
	if scanResult.Skipped[0].Path != "/path/to/huge.bin" {
		t.Errorf("Expected skipped path '/path/to/huge.bin', got '%s'", scanResult.Skipped[0].Path)
	}
	if scanResult.Skipped[0].Reason != reason {
		t.Errorf("Expected skipped reason '%s', got '%s'", reason, scanResult.Skipped[0].Reason)
	}

	// The skip message must NOT appear as a scanned file or as an issue.
	for _, scanned := range scanResult.Scanned {
		if scanned.Filename == "huge.bin" {
			t.Errorf("Skipped file 'huge.bin' must not appear in scanned[]")
		}
	}
	for _, detail := range scanResult.DetailsSubjectFocused {
		if detail.Subject == "huge.bin" {
			t.Errorf("Skipped file 'huge.bin' must not appear in details_subject_focused[]")
		}
	}

	// The non-skip message must still be processed as a real issue.
	if len(scanResult.Scanned) != 1 || scanResult.Scanned[0].Filename != "ok.txt" {
		t.Errorf("Expected only 'ok.txt' in scanned[], got %+v", scanResult.Scanned)
	}
}

func TestFormatResults_SkippedArchiveMemberFilenameIncludesArchive(t *testing.T) {
	formatter := NewJSONFormatter()

	member := structs.ToFileWithDisplay(
		"/path/to/archive.zip",
		"inner/big.txt",
		"inner/big.txt",
		5000,
		"",
		"archive.zip",
	)
	reason := "Skipped content scan of archive member: would exceed total archive memory limit (100 bytes)."
	messages := []structs.Message{
		{Content: reason, Source: member, TestName: "IsFreeOfKeywords", Skipped: true, Reason: reason},
	}

	result, err := formatter.FormatResults("/loc", "LocalCollector", messages, 1, []string{})
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	var scanResult ScanResult
	if err := json.Unmarshal([]byte(result), &scanResult); err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}

	if len(scanResult.Skipped) != 1 {
		t.Fatalf("Expected 1 skipped entry, got %d", len(scanResult.Skipped))
	}
	if scanResult.Skipped[0].Filename != "archive.zip > inner/big.txt" {
		t.Errorf("Expected archive-qualified filename, got '%s'", scanResult.Skipped[0].Filename)
	}
	if len(scanResult.Scanned) != 0 {
		t.Errorf("Skip-only input must not produce scanned[] entries, got %d", len(scanResult.Scanned))
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
