package optimization

import (
	"strings"
	"testing"
)

func TestFindOriginalCase_BoundsProtection(t *testing.T) {
	// Test case that could trigger bounds error due to Unicode normalization differences
	patterns := []string{"test"}
	matcher := NewFastMatcher(patterns)

	// Create a text where lowerText and original text might have different byte lengths
	// This can happen with Unicode characters
	text := []byte("This is a TEST string with unicode characters: café")
	
	// This should not panic
	original := matcher.findOriginalCase(text, "test")
	
	if original != "TEST" {
		t.Errorf("Expected 'TEST', got '%s'", original)
	}
}

func TestFindOriginalCase_LargeText(t *testing.T) {
	patterns := []string{"password"}
	matcher := NewFastMatcher(patterns)

	// Create a large text that could trigger bounds issues
	largePrefix := strings.Repeat("x", 100000)
	largeSuffix := strings.Repeat("y", 100000)
	text := []byte(largePrefix + " PASSWORD " + largeSuffix)
	
	// This should not panic
	original := matcher.findOriginalCase(text, "password")
	
	if original != "PASSWORD" {
		t.Errorf("Expected 'PASSWORD', got '%s'", original)
	}
}

func TestFindOriginalCase_EdgeCaseBounds(t *testing.T) {
	patterns := []string{"end"}
	matcher := NewFastMatcher(patterns)

	// Text where pattern is at the very end
	text := []byte("This text ends with END")
	
	// This should not panic even if bounds calculation is wrong
	original := matcher.findOriginalCase(text, "end")
	
	// The function finds lowercase 'end' first, so it returns 'end', not 'END'
	if original != "end" {
		t.Errorf("Expected 'end', got '%s'", original)
	}
}

func TestFindOriginalCase_PatternNotFound(t *testing.T) {
	patterns := []string{"missing", "found"}
	matcher := NewFastMatcher(patterns)

	text := []byte("This text has found but not the other")
	
	// Should fallback to original pattern when not found
	original := matcher.findOriginalCase(text, "missing")
	
	if original != "missing" {
		t.Errorf("Expected fallback 'missing', got '%s'", original)
	}
}

func TestFindMatchesWithOriginalCase_LargeFile(t *testing.T) {
	patterns := []string{"secret", "password", "api_key"}
	matcher := NewFastMatcher(patterns)

	// Simulate a large file with multiple patterns
	content := strings.Repeat("normal content ", 10000) +
		" SECRET data " +
		strings.Repeat("more content ", 5000) +
		" PASSWORD here " +
		strings.Repeat("final content ", 10000)
	
	text := []byte(content)
	
	// This should not panic
	matches := matcher.FindMatchesWithOriginalCase(text)
	
	if len(matches) != 2 {
		t.Errorf("Expected 2 matches, got %d: %v", len(matches), matches)
	}
	
	// Check that original case is preserved
	expectedMatches := []string{"PASSWORD", "SECRET"}
	for i, expected := range expectedMatches {
		if matches[i] != expected {
			t.Errorf("Match %d: expected '%s', got '%s'", i, expected, matches[i])
		}
	}
}