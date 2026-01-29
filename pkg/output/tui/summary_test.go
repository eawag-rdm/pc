package tui

import (
	"strings"
	"testing"
)

func TestSummaryGenerator_Generate_EmptyData(t *testing.T) {
	sg := NewSummaryGenerator(nil, "test-location")
	result := sg.Generate()

	if result != "No scan data available." {
		t.Errorf("Expected 'No scan data available.', got '%s'", result)
	}
}

func TestSummaryGenerator_Generate_NoIssues(t *testing.T) {
	data := &ScanResult{
		Timestamp:             "2024-01-14T10:30:00Z",
		DetailsCheckFocused:   []CheckDetails{},
	}

	sg := NewSummaryGenerator(data, "test-package")
	result := sg.Generate()

	if !strings.Contains(result, "No issues found.") {
		t.Errorf("Expected 'No issues found.' in output, got '%s'", result)
	}
	if !strings.Contains(result, "Location: test-package") {
		t.Errorf("Expected location in output, got '%s'", result)
	}
}

func TestSummaryGenerator_Generate_SingleCheck(t *testing.T) {
	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "IsFreeOfKeywords",
				Issues: []SubjectIssue{
					{Subject: "config.yaml", Path: "/path/config.yaml", Message: "Found 'PASSWORD'"},
				},
			},
		},
	}

	sg := NewSummaryGenerator(data, "my-package")
	result := sg.Generate()

	// Check header
	if !strings.Contains(result, "=== Package Checker Scan Summary ===") {
		t.Error("Missing header")
	}
	if !strings.Contains(result, "Location: my-package") {
		t.Error("Missing location")
	}

	// Check human-readable check name
	if !strings.Contains(result, "Possible sensitive content detected") {
		t.Errorf("Expected human-readable check name, got '%s'", result)
	}

	// Check issue count
	if !strings.Contains(result, "(1 issue)") {
		t.Error("Missing issue count")
	}

	// Check issue content
	if !strings.Contains(result, "config.yaml: Found 'PASSWORD'") {
		t.Errorf("Missing issue content in '%s'", result)
	}

	// Check summary
	if !strings.Contains(result, "Total: 1 issue in 1 file") {
		t.Errorf("Missing or incorrect summary in '%s'", result)
	}
}

func TestSummaryGenerator_Generate_MultipleChecks(t *testing.T) {
	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "IsFreeOfKeywords",
				Issues: []SubjectIssue{
					{Subject: "config.yaml", Message: "Found 'PASSWORD'"},
					{Subject: "secrets.txt", Message: "Found 'API_KEY'"},
				},
			},
			{
				Checkname: "HasValidFileName",
				Issues: []SubjectIssue{
					{Subject: "my file.txt", Message: "File name contains spaces"},
				},
			},
		},
	}

	sg := NewSummaryGenerator(data, "test")
	result := sg.Generate()

	// Check both check types are present
	if !strings.Contains(result, "Possible sensitive content detected (2 issues)") {
		t.Errorf("Missing first check in '%s'", result)
	}
	if !strings.Contains(result, "File name issues (1 issue)") {
		t.Errorf("Missing second check in '%s'", result)
	}

	// Check total
	if !strings.Contains(result, "Total: 3 issues in 3 files") {
		t.Errorf("Incorrect total in '%s'", result)
	}
}

func TestSummaryGenerator_Generate_ArchiveNesting(t *testing.T) {
	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "IsFreeOfKeywords",
				Issues: []SubjectIssue{
					{
						Subject:     "secrets/config.txt",
						Path:        "/path/archive.zip",
						ArchiveName: "archive.zip",
						Message:     "Possible credentials in file 'PASSWORD'",
					},
					{
						Subject:     "backup/settings.ini",
						Path:        "/path/data.tar.gz",
						ArchiveName: "data.tar.gz",
						Message:     "Possible credentials in file 'API_KEY'",
					},
				},
			},
		},
	}

	sg := NewSummaryGenerator(data, "test")
	result := sg.Generate()

	// Check archive nesting format
	if !strings.Contains(result, "archive.zip -> secrets/config.txt") {
		t.Errorf("Missing archive nesting for first issue in '%s'", result)
	}
	if !strings.Contains(result, "data.tar.gz -> backup/settings.ini") {
		t.Errorf("Missing archive nesting for second issue in '%s'", result)
	}

	// The message should not contain the "In archived file" suffix
	if strings.Contains(result, "In archived file") {
		t.Errorf("Should not contain raw 'In archived file' text in '%s'", result)
	}
}

func TestSummaryGenerator_Generate_RepositoryIssues(t *testing.T) {
	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "HasReadMe",
				Issues: []SubjectIssue{
					{Subject: "Repository", Path: "", Message: "No README file found"},
				},
			},
		},
	}

	sg := NewSummaryGenerator(data, "test")
	result := sg.Generate()

	// Check repository issue is formatted correctly
	if !strings.Contains(result, "Missing README file") {
		t.Errorf("Missing human-readable check name in '%s'", result)
	}
	if !strings.Contains(result, "Repository: No README file found") {
		t.Errorf("Missing repository issue in '%s'", result)
	}
}

func TestSummaryGenerator_Generate_MixedIssues(t *testing.T) {
	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "IsFreeOfKeywords",
				Issues: []SubjectIssue{
					{Subject: "config.yaml", Message: "Found 'PASSWORD'"},
					{Subject: "nested/file.txt", ArchiveName: "archive.zip", Message: "Found 'SECRET'"},
				},
			},
			{
				Checkname: "HasReadMe",
				Issues: []SubjectIssue{
					{Subject: "Repository", Message: "No README file found"},
				},
			},
		},
	}

	sg := NewSummaryGenerator(data, "mixed-test")
	result := sg.Generate()

	// Regular file issue
	if !strings.Contains(result, "config.yaml: Found 'PASSWORD'") {
		t.Error("Missing regular file issue")
	}

	// Archive issue with nesting
	if !strings.Contains(result, "archive.zip -> nested/file.txt") {
		t.Error("Missing archive nested issue")
	}

	// Repository issue
	if !strings.Contains(result, "Repository: No README file found") {
		t.Error("Missing repository issue")
	}

	// Total should count unique files (config.yaml, archive.zip, Repository)
	if !strings.Contains(result, "Total: 3 issues in 3 files") {
		t.Errorf("Incorrect total count in '%s'", result)
	}
}

func TestParseIssueItem_RegularIssue(t *testing.T) {
	issue := SubjectIssue{
		Subject: "test.txt",
		Path:    "/path/test.txt",
		Message: "Some issue found",
	}

	item := parseIssueItem(issue)

	if item.Subject != "test.txt" {
		t.Errorf("Expected subject 'test.txt', got '%s'", item.Subject)
	}
	if item.ArchivePath != "" {
		t.Errorf("Expected empty archive path, got '%s'", item.ArchivePath)
	}
	if item.Message != "Some issue found" {
		t.Errorf("Expected message 'Some issue found', got '%s'", item.Message)
	}
}

func TestParseIssueItem_ArchiveIssue(t *testing.T) {
	issue := SubjectIssue{
		Subject:     "config/secrets.txt",
		Path:        "/path/archive.zip",
		ArchiveName: "archive.zip",
		Message:     "Found keyword 'PASSWORD'",
	}

	item := parseIssueItem(issue)

	if item.Subject != "archive.zip" {
		t.Errorf("Expected subject 'archive.zip', got '%s'", item.Subject)
	}
	if item.ArchivePath != "config/secrets.txt" {
		t.Errorf("Expected archive path 'config/secrets.txt', got '%s'", item.ArchivePath)
	}
	if item.Message != "Found keyword 'PASSWORD'" {
		t.Errorf("Expected message without archive suffix, got '%s'", item.Message)
	}
}

func TestHumanizeCheckName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"IsFreeOfKeywords", "Possible sensitive content detected"},
		{"HasValidFileName", "File name issues"},
		{"HasReadMe", "Missing README file"},
		{"UnknownCheck", "UnknownCheck"}, // Fallback
	}

	for _, tt := range tests {
		result := humanizeCheckName(tt.input)
		if result != tt.expected {
			t.Errorf("humanizeCheckName(%s) = '%s', expected '%s'", tt.input, result, tt.expected)
		}
	}
}

func TestFormatIssueItem_Regular(t *testing.T) {
	item := IssueItem{
		Subject: "test.txt",
		Message: "Some issue",
	}

	result := formatIssueItem(item)
	expected := "  - test.txt: Some issue\n"

	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

func TestFormatIssueItem_Archive(t *testing.T) {
	item := IssueItem{
		Subject:     "archive.zip",
		ArchivePath: "inner/file.txt",
		Message:     "Found issue",
	}

	result := formatIssueItem(item)
	expected := "  - archive.zip -> inner/file.txt: Found issue\n"

	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

func TestFormatIssueItem_Repository(t *testing.T) {
	item := IssueItem{
		Subject: "Repository",
		Message: "No README found",
	}

	result := formatIssueItem(item)
	expected := "  - Repository: No README found\n"

	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

func TestExtractParentPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"archive.zip -> folder/subfolder/file.txt", "archive.zip -> folder/subfolder"},
		{"folder/file.txt", "folder"},
		{"file.txt", ""},
		{"archive.zip -> Level0/2022-09-02T122044.xml", "archive.zip -> Level0"},
		{"Lake Hallwil data.zip -> Level0/not used/file.xml", "Lake Hallwil data.zip -> Level0/not used"},
	}

	for _, tt := range tests {
		result := extractParentPath(tt.input)
		if result != tt.expected {
			t.Errorf("extractParentPath(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestNormalizeMessageKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Security credentials detected 'password'", "Security credentials detected"},
		{"Hardcoded file paths detected 'Q:'", "Hardcoded file paths detected"},
		{"File name contains spaces", "File name contains spaces"},
		{"Found keyword \"SECRET\"", "Found keyword"},
	}

	for _, tt := range tests {
		result := normalizeMessageKey(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeMessageKey(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestTruncation_SameParentPath(t *testing.T) {
	// Create 10 issues in the same parent folder
	issues := make([]SubjectIssue, 10)
	for i := 0; i < 10; i++ {
		issues[i] = SubjectIssue{
			Subject:     "Level0/not used/file" + string(rune('A'+i)) + ".xml",
			ArchiveName: "data.zip",
			Message:     "File name contains spaces",
		}
	}

	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "HasValidFileName",
				Issues:    issues,
			},
		},
	}

	sg := NewSummaryGenerator(data, "test")
	result := sg.Generate()

	// Should show 5 issues and truncation message
	if !strings.Contains(result, "... and 5 more in") {
		t.Errorf("Expected truncation message, got:\n%s", result)
	}

	// Should mention the parent path
	if !strings.Contains(result, "data.zip -> Level0/not used") {
		t.Errorf("Expected parent path in truncation message, got:\n%s", result)
	}
}

func TestTruncation_DifferentMessageTypes(t *testing.T) {
	// Create issues with DIFFERENT error types in the same parent folder
	issues := []SubjectIssue{}

	// 8 issues with keyword detection
	for i := 0; i < 8; i++ {
		issues = append(issues, SubjectIssue{
			Subject:     "Level0/file" + string(rune('A'+i)) + ".xml",
			ArchiveName: "data.zip",
			Message:     "Hardcoded file paths detected 'Q:'",
		})
	}

	// 8 issues with different error type (spaces in filename)
	for i := 0; i < 8; i++ {
		issues = append(issues, SubjectIssue{
			Subject:     "Level0/my file" + string(rune('A'+i)) + ".xml",
			ArchiveName: "data.zip",
			Message:     "File name contains spaces",
		})
	}

	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "IsFreeOfKeywords",
				Issues:    issues,
			},
		},
	}

	sg := NewSummaryGenerator(data, "test")
	result := sg.Generate()

	// Should have two separate truncation messages (one per message type)
	count := strings.Count(result, "... and")
	if count != 2 {
		t.Errorf("Expected 2 truncation messages (one per message type), got %d in:\n%s", count, result)
	}

	// Both groups should reference the parent path
	if !strings.Contains(result, "data.zip -> Level0") {
		t.Errorf("Expected parent path in truncation message, got:\n%s", result)
	}
}

func TestTruncation_SameMessageNormalized(t *testing.T) {
	// Create issues with same error TYPE but different specific values
	// These should be grouped together since the normalized message is the same
	issues := []SubjectIssue{}

	// 8 issues with 'Q:' detected
	for i := 0; i < 8; i++ {
		issues = append(issues, SubjectIssue{
			Subject:     "Level0/file" + string(rune('A'+i)) + ".xml",
			ArchiveName: "data.zip",
			Message:     "Hardcoded file paths detected 'Q:'",
		})
	}

	// 8 issues with '/Users/' detected (same error type, different value)
	for i := 0; i < 8; i++ {
		issues = append(issues, SubjectIssue{
			Subject:     "Level0/file" + string(rune('A'+8+i)) + ".xml",
			ArchiveName: "data.zip",
			Message:     "Hardcoded file paths detected '/Users/'",
		})
	}

	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "IsFreeOfKeywords",
				Issues:    issues,
			},
		},
	}

	sg := NewSummaryGenerator(data, "test")
	result := sg.Generate()

	// Should have ONE truncation message (normalized message groups them together)
	count := strings.Count(result, "... and")
	if count != 1 {
		t.Errorf("Expected 1 truncation message (same normalized message type), got %d in:\n%s", count, result)
	}

	// Should mention the normalized message type
	if !strings.Contains(result, "Hardcoded file paths detected") {
		t.Errorf("Expected normalized message in truncation, got:\n%s", result)
	}
}

func TestTruncation_NoTruncationForSmallGroups(t *testing.T) {
	// Create only 4 issues - below truncation threshold
	issues := make([]SubjectIssue, 4)
	for i := 0; i < 4; i++ {
		issues[i] = SubjectIssue{
			Subject: "file" + string(rune('A'+i)) + ".txt",
			Message: "Some issue",
		}
	}

	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "HasValidFileName",
				Issues:    issues,
			},
		},
	}

	sg := NewSummaryGenerator(data, "test")
	result := sg.Generate()

	// Should NOT have truncation message
	if strings.Contains(result, "... and") {
		t.Errorf("Should not truncate small groups, got:\n%s", result)
	}

	// Should contain all 4 files
	for i := 0; i < 4; i++ {
		expected := "file" + string(rune('A'+i)) + ".txt"
		if !strings.Contains(result, expected) {
			t.Errorf("Missing %s in:\n%s", expected, result)
		}
	}
}
