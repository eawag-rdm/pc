package utils

import (
	"fmt"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

func TestGetMessageType(t *testing.T) {
	tests := []struct {
		content  string
		expected string
	}{
		{
			content:  "File name contains non-ASCII character: ñ",
			expected: "non-ascii-filename",
		},
		{
			content:  "File name contains spaces",
			expected: "filename-spaces",
		},
		{
			content:  "Sensitive data in File? Found suspicious keyword(s): password",
			expected: "sensitive-data",
		},
		{
			content:  "Do you have Eawag internal information in your files?",
			expected: "eawag-internal",
		},
		{
			content:  "Do you have hardcoded filepaths in your files?",
			expected: "hardcoded-paths",
		},
		{
			content:  "File or Folder has an invalid name: .DS_Store",
			expected: "invalid-name",
		},
		{
			content:  "File has an invalid suffix: test.tmp",
			expected: "invalid-suffix",
		},
		{
			content:  "Found issue. In archived file: test.txt",
			expected: "archived-file-issue",
		},
		{
			content:  "Some very long message that exceeds fifty characters and should be truncated",
			expected: "Some very long message that exceeds fifty characte",
		},
		{
			content:  "Short message",
			expected: "Short message",
		},
	}

	for _, test := range tests {
		result := getMessageType(test.content)
		assert.Equal(t, test.expected, result, "Message type for: %s", test.content)
	}
}

func TestTruncateMessages(t *testing.T) {
	// Create test file source
	testFile := structs.File{Name: "test.txt", Path: "/test/test.txt"}

	t.Run("No truncation when under limit", func(t *testing.T) {
		messages := []structs.Message{
			{Content: "File name contains spaces", Source: testFile},
			{Content: "File name contains non-ASCII character: ñ", Source: testFile},
		}

		result := TruncateMessages(messages, 5)
		assert.Len(t, result, 2)
	})

	t.Run("Truncation when over limit", func(t *testing.T) {
		// Simulate 5 files with spaces in their names - same check type
		messages := []structs.Message{
			{Content: "File name contains spaces", Source: structs.File{Name: "file 1.txt"}},
			{Content: "File name contains spaces", Source: structs.File{Name: "file 2.txt"}},
			{Content: "File name contains spaces", Source: structs.File{Name: "file 3.txt"}},
			{Content: "File name contains spaces", Source: structs.File{Name: "file 4.txt"}},
			{Content: "File name contains spaces", Source: structs.File{Name: "file 5.txt"}},
		}

		result := TruncateMessages(messages, 3)
		assert.Len(t, result, 3) // 2 original + 1 truncation message
		
		// Check that the last message is a truncation message
		lastMsg := result[len(result)-1]
		assert.Contains(t, lastMsg.Content, "and 3 more similar messages (truncated)")
	})

	t.Run("Real scenario - many files with spaces", func(t *testing.T) {
		// Simulate finding 15 files with spaces in names
		var messages []structs.Message
		for i := 1; i <= 15; i++ {
			messages = append(messages, structs.Message{
				Content: "File name contains spaces",
				Source:  structs.File{Name: fmt.Sprintf("file %d.txt", i)},
			})
		}

		result := TruncateMessages(messages, 5)
		assert.Len(t, result, 5) // 4 original + 1 truncation message
		
		// Verify the truncation message
		lastMsg := result[len(result)-1]
		assert.Contains(t, lastMsg.Content, "and 11 more similar messages (truncated)")
	})

	t.Run("Different message types are not grouped", func(t *testing.T) {
		messages := []structs.Message{
			{Content: "File name contains spaces", Source: testFile},
			{Content: "File name contains spaces", Source: testFile},
			{Content: "File name contains non-ASCII character: ñ", Source: testFile},
			{Content: "File name contains non-ASCII character: ü", Source: testFile},
		}

		result := TruncateMessages(messages, 2)
		assert.Len(t, result, 4) // All messages should remain as they're different types
	})

	t.Run("Zero or negative limit returns original messages", func(t *testing.T) {
		messages := []structs.Message{
			{Content: "File name contains spaces", Source: testFile},
			{Content: "File name contains spaces", Source: testFile},
		}

		result1 := TruncateMessages(messages, 0)
		assert.Equal(t, messages, result1)

		result2 := TruncateMessages(messages, -1)
		assert.Equal(t, messages, result2)
	})

	t.Run("Exact limit boundary", func(t *testing.T) {
		messages := []structs.Message{
			{Content: "File name contains spaces", Source: testFile},
			{Content: "File name contains spaces", Source: testFile},
			{Content: "File name contains spaces", Source: testFile},
		}

		result := TruncateMessages(messages, 3)
		assert.Len(t, result, 3) // Exactly at limit, no truncation
		
		// Ensure no truncation message
		for _, msg := range result {
			assert.NotContains(t, msg.Content, "truncated")
		}
	})
}