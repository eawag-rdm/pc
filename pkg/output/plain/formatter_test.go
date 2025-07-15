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