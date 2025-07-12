package structs

import (
	"testing"
)

func TestMessage_Format_FileSource(t *testing.T) {
	file := File{
		Name: "test.txt",
		Path: "/path/to/test.txt",
	}

	message := Message{
		Content:  "Found sensitive data",
		Source:   file,
		TestName: "TestCheck",
	}

	formatted := message.Format()
	expected := "- File issue in 'test.txt': Found sensitive data"

	if formatted != expected {
		t.Errorf("Expected '%s', got '%s'", expected, formatted)
	}
}

func TestMessage_Format_RepositorySource(t *testing.T) {
	repo := Repository{
		Files: []File{},
	}

	message := Message{
		Content:  "Repository configuration issue",
		Source:   repo,
		TestName: "RepoCheck",
	}

	formatted := message.Format()
	expected := "- Repository issue: Repository configuration issue"

	if formatted != expected {
		t.Errorf("Expected '%s', got '%s'", expected, formatted)
	}
}

// CustomSource is a test type that implements Source interface
type CustomSource struct{}

func (cs CustomSource) GetValue() []File {
	return []File{}
}

func TestMessage_Format_UnknownSource(t *testing.T) {
	// Create a custom source that implements the Source interface
	customSource := CustomSource{}

	message := Message{
		Content:  "Unknown source message",
		Source:   customSource,
		TestName: "CustomCheck",
	}

	formatted := message.Format()
	expected := "- Unknown source issue: Unknown source message"

	if formatted != expected {
		t.Errorf("Expected '%s', got '%s'", expected, formatted)
	}
}

func TestMessage_Format_EmptyContent(t *testing.T) {
	file := File{Name: "empty.txt"}

	message := Message{
		Content:  "",
		Source:   file,
		TestName: "EmptyTest",
	}

	formatted := message.Format()
	expected := "- File issue in 'empty.txt': "

	if formatted != expected {
		t.Errorf("Expected '%s', got '%s'", expected, formatted)
	}
}

func TestMessage_Format_SpecialCharacters(t *testing.T) {
	file := File{Name: "file with spaces & symbols!.txt"}

	message := Message{
		Content:  "Message with 'quotes' and \"double quotes\"",
		Source:   file,
		TestName: "SpecialCharTest",
	}

	formatted := message.Format()
	expected := "- File issue in 'file with spaces & symbols!.txt': Message with 'quotes' and \"double quotes\""

	if formatted != expected {
		t.Errorf("Expected '%s', got '%s'", expected, formatted)
	}
}

func TestFile_GetValue(t *testing.T) {
	file := File{
		Name: "test.txt",
		Path: "/path/test.txt",
	}

	result := file.GetValue()

	if len(result) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(result))
	}

	if result[0].Name != "test.txt" {
		t.Errorf("Expected file name 'test.txt', got '%s'", result[0].Name)
	}

	if result[0].Path != "/path/test.txt" {
		t.Errorf("Expected file path '/path/test.txt', got '%s'", result[0].Path)
	}
}

func TestRepository_GetValue(t *testing.T) {
	files := []File{
		{Name: "file1.txt", Path: "/path/file1.txt"},
		{Name: "file2.txt", Path: "/path/file2.txt"},
	}

	repo := Repository{
		Files: files,
	}

	result := repo.GetValue()

	if len(result) != 2 {
		t.Fatalf("Expected 2 files, got %d", len(result))
	}

	for i, file := range files {
		if result[i].Name != file.Name {
			t.Errorf("File %d: expected name '%s', got '%s'", i, file.Name, result[i].Name)
		}

		if result[i].Path != file.Path {
			t.Errorf("File %d: expected path '%s', got '%s'", i, file.Path, result[i].Path)
		}
	}
}

func TestRepository_GetValue_EmptyFiles(t *testing.T) {
	repo := Repository{
		Files: []File{},
	}

	result := repo.GetValue()

	if len(result) != 0 {
		t.Errorf("Expected 0 files for empty repository, got %d", len(result))
	}
}

func TestMessage_TestNameField(t *testing.T) {
	file := File{Name: "test.txt"}

	message := Message{
		Content:  "Test content",
		Source:   file,
		TestName: "IsFreeOfKeywords",
	}

	if message.TestName != "IsFreeOfKeywords" {
		t.Errorf("Expected TestName 'IsFreeOfKeywords', got '%s'", message.TestName)
	}

	// Test that TestName doesn't affect formatting (it's metadata)
	formatted := message.Format()
	expected := "- File issue in 'test.txt': Test content"

	if formatted != expected {
		t.Errorf("TestName should not affect formatting. Expected '%s', got '%s'", expected, formatted)
	}
}

func TestMessage_SourceInterface(t *testing.T) {
	// Test that both File and Repository implement Source interface
	var source Source

	file := File{Name: "test.txt"}
	source = file
	if source == nil {
		t.Error("File should implement Source interface")
	}

	repo := Repository{Files: []File{}}
	source = repo
	if source == nil {
		t.Error("Repository should implement Source interface")
	}
}

func TestMessage_ComplexScenarios(t *testing.T) {
	// Test with file containing no name
	file := File{Name: "", Path: "/path/to/unnamed"}
	message := Message{
		Content: "Issue in unnamed file",
		Source:  file,
	}

	formatted := message.Format()
	expected := "- File issue in '': Issue in unnamed file"
	if formatted != expected {
		t.Errorf("Expected '%s', got '%s'", expected, formatted)
	}

	// Test with very long content
	longContent := "This is a very long error message that contains a lot of details about what went wrong during the file analysis process and why it failed"
	message2 := Message{
		Content: longContent,
		Source:  file,
	}

	formatted2 := message2.Format()
	if !contains(formatted2, longContent) {
		t.Error("Long content should be preserved in formatted message")
	}
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}