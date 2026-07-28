package plain

import (
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestPlainFormatter_FormatResults_NoIssues(t *testing.T) {
	formatter := NewPlainFormatter()

	result := formatter.FormatResults("test/path", "LocalCollector", []structs.Message{}, 5, []string{})

	if !strings.Contains(result, "✅ No issues found!") {
		t.Errorf("Expected no issues message, got: %s", result)
	}

	if !strings.Contains(result, "Files scanned: 5") {
		t.Errorf("Expected files scanned count, got: %s", result)
	}
}

func TestPlainFormatter_FormatResults_WithIssues(t *testing.T) {
	formatter := NewPlainFormatter()

	file1 := structs.File{Name: "test1.txt", Path: "/path/test1.txt"}
	file2 := structs.File{Name: "test2.txt", Path: "/path/test2.txt"}

	messages := []structs.Message{
		{
			Content:  "Test issue 1",
			Source:   file1,
			TestName: "TestCheck1",
		},
		{
			Content:  "Test issue 2",
			Source:   file1,
			TestName: "TestCheck1",
		},
		{
			Content:  "Different issue",
			Source:   file2,
			TestName: "TestCheck2",
		},
	}

	result := formatter.FormatResults("test/path", "LocalCollector", messages, 10, []string{})

	// Check header
	if !strings.Contains(result, "=== PC Scan Results ===") {
		t.Errorf("Expected header, got: %s", result)
	}

	// Check location and file count
	if !strings.Contains(result, "Location: test/path") {
		t.Errorf("Expected location, got: %s", result)
	}

	if !strings.Contains(result, "Files scanned: 10") {
		t.Errorf("Expected files scanned count, got: %s", result)
	}

	// Check issue count
	if !strings.Contains(result, "Found 3 issues") {
		t.Errorf("Expected 3 issues found, got: %s", result)
	}

	// Check file sections
	if !strings.Contains(result, "📄 test1.txt (2 issues)") {
		t.Errorf("Expected test1.txt section, got: %s", result)
	}

	if !strings.Contains(result, "📄 test2.txt (1 issues)") {
		t.Errorf("Expected test2.txt section, got: %s", result)
	}

	// Check summary section
	if !strings.Contains(result, "=== Summary ===") {
		t.Errorf("Expected summary section, got: %s", result)
	}

	if !strings.Contains(result, "Total issues: 3") {
		t.Errorf("Expected total issues count, got: %s", result)
	}

	// Check issue types breakdown
	if !strings.Contains(result, "TestCheck1: 2") {
		t.Errorf("Expected TestCheck1 breakdown, got: %s", result)
	}

	if !strings.Contains(result, "TestCheck2: 1") {
		t.Errorf("Expected TestCheck2 breakdown, got: %s", result)
	}
}

func TestPlainFormatter_FormatResults_RepositoryIssues(t *testing.T) {
	formatter := NewPlainFormatter()

	repo := structs.Repository{Files: []structs.File{}}

	messages := []structs.Message{
		{
			Content:  "Repository issue",
			Source:   repo,
			TestName: "RepoCheck",
		},
	}

	result := formatter.FormatResults("test/path", "LocalCollector", messages, 5, []string{})

	// Check repository section
	if !strings.Contains(result, "📁 Repository Issues:") {
		t.Errorf("Expected repository section, got: %s", result)
	}

	if !strings.Contains(result, "Repository issue") {
		t.Errorf("Expected repository issue content, got: %s", result)
	}
}

// TestPlainFormatter_FormatResults_SkippedOnly verifies that skip-flagged
// Messages are never counted as issues (spec §6): a clean-but-oversized file
// must render "No issues found!" plus a separate "Skipped files" section, not an
// inflated issue count.
func TestPlainFormatter_FormatResults_SkippedOnly(t *testing.T) {
	formatter := NewPlainFormatter()

	huge := structs.File{Name: "huge.bin", Path: "/path/huge.bin"}
	reason := "Skipped content scan of file: file size (2000 bytes) exceeds maximum (1000 bytes)."
	messages := []structs.Message{
		{
			Content:  reason,
			Source:   huge,
			TestName: "IsFreeOfKeywords",
			Skipped:  true,
			Reason:   reason,
		},
	}

	result := formatter.FormatResults("test/path", "LocalCollector", messages, 1, []string{})

	// Skip-only input must not be reported as issues.
	if !strings.Contains(result, "✅ No issues found!") {
		t.Errorf("Expected 'No issues found!' for skip-only input, got: %s", result)
	}
	if strings.Contains(result, "❌ Found") {
		t.Errorf("Skip-only input must not produce a 'Found N issues' header, got: %s", result)
	}
	if strings.Contains(result, "Total issues:") {
		t.Errorf("Skip-only input must not produce an issue total, got: %s", result)
	}
	if strings.Contains(result, "Issue types:") {
		t.Errorf("Skip-only input must not produce an issue-type breakdown, got: %s", result)
	}

	// The skip must still be surfaced in its own section with its reason.
	if !strings.Contains(result, "Skipped files (1)") {
		t.Errorf("Expected a 'Skipped files' section, got: %s", result)
	}
	if !strings.Contains(result, "huge.bin") || !strings.Contains(result, reason) {
		t.Errorf("Expected skipped file name and reason, got: %s", result)
	}
}

// TestPlainFormatter_FormatResults_SkipDoesNotInflateIssues verifies that skip
// Messages mixed with real issues do not inflate the issue count.
func TestPlainFormatter_FormatResults_SkipDoesNotInflateIssues(t *testing.T) {
	formatter := NewPlainFormatter()

	huge := structs.File{Name: "huge.bin", Path: "/path/huge.bin"}
	ok := structs.File{Name: "ok.txt", Path: "/path/ok.txt"}
	messages := []structs.Message{
		{Content: "skip reason", Source: huge, TestName: "IsFreeOfKeywords", Skipped: true, Reason: "skip reason"},
		{Content: "Found keyword 'secret'", Source: ok, TestName: "IsFreeOfKeywords"},
	}

	result := formatter.FormatResults("test/path", "LocalCollector", messages, 2, []string{})

	if !strings.Contains(result, "❌ Found 1 issues in 1 files") {
		t.Errorf("Expected exactly 1 counted issue, got: %s", result)
	}
	if !strings.Contains(result, "Total issues: 1") {
		t.Errorf("Expected total issues of 1, got: %s", result)
	}
	if strings.Contains(result, "📄 huge.bin") {
		t.Errorf("Skipped file must not be grouped as an issue file, got: %s", result)
	}
	if !strings.Contains(result, "Skipped files (1)") {
		t.Errorf("Expected skipped file to appear in skipped section, got: %s", result)
	}
}
