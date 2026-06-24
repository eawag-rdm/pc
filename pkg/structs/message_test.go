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

// CustomSource is a test type used as an unrecognized Message source.
type CustomSource struct{}

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

func TestMessage_SkippedFields(t *testing.T) {
	file := File{Name: "big.bin", Path: "/path/to/big.bin"}
	reason := "Skipped content scan of file: file size (5 bytes) exceeds maximum (1 bytes)."

	skipped := Message{Content: reason, Source: file, TestName: "IsFreeOfKeywords", Skipped: true, Reason: reason}
	plain := Message{Content: reason, Source: file, TestName: "IsFreeOfKeywords"}

	// The skip metadata is carried for downstream routing (the JSON skipped[]),
	// but must NOT leak into the human-rendered text: a skipped message has to
	// render identically to an ordinary file-sourced message.
	if got, want := skipped.Format(), plain.Format(); got != want {
		t.Errorf("skip flag changed Format() output: got %q, want %q", got, want)
	}
	if got, want := skipped.Format(), "- File issue in 'big.bin': "+reason; got != want {
		t.Errorf("Format() = %q, want %q", got, want)
	}
}

func TestMessage_NonSkipDefaults(t *testing.T) {
	// The zero value of Message must be a non-skip message (Skipped false, empty
	// Reason) so ordinary check output is never mis-routed as a size-skip.
	var message Message
	if message.Skipped {
		t.Error("zero-value Message.Skipped must be false")
	}
	if message.Reason != "" {
		t.Errorf("zero-value Message.Reason must be empty, got %q", message.Reason)
	}
}

func TestMessage_SourceInterface(t *testing.T) {
	// Test that both File and Repository are usable as a Message Source.
	// Compile-time check: these assignments verify interface satisfaction.
	var _ Source = File{Name: "test.txt"}
	var _ Source = Repository{Files: []File{}}
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
