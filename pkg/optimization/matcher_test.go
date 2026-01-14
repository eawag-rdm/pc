package optimization

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestNewFastMatcher(t *testing.T) {
	patterns := []string{"password", "secret", "api_key"}
	matcher := NewFastMatcher(patterns)

	if matcher == nil {
		t.Fatal("NewFastMatcher returned nil")
	}

	if len(matcher.patterns) != len(patterns) {
		t.Errorf("Expected %d patterns, got %d", len(patterns), len(matcher.patterns))
	}

	if matcher.minLen == 0 {
		t.Error("minLen should be set")
	}

	if matcher.maxLen == 0 {
		t.Error("maxLen should be set")
	}

	if len(matcher.caseMap) == 0 {
		t.Error("caseMap should be populated")
	}
}

func TestNewFastMatcher_EmptyPatterns(t *testing.T) {
	matcher := NewFastMatcher([]string{})

	if matcher == nil {
		t.Fatal("NewFastMatcher returned nil for empty patterns")
	}

	if len(matcher.patterns) != 0 {
		t.Errorf("Expected 0 patterns, got %d", len(matcher.patterns))
	}
}

func TestNewFastMatcher_WithEmptyPattern(t *testing.T) {
	patterns := []string{"password", "", "secret"}
	matcher := NewFastMatcher(patterns)

	if matcher == nil {
		t.Fatal("NewFastMatcher returned nil")
	}

	// Should handle empty patterns gracefully
	if len(matcher.patterns) != len(patterns) {
		t.Errorf("Expected %d patterns, got %d", len(patterns), len(matcher.patterns))
	}
}

func TestFindMatches_BasicSearch(t *testing.T) {
	patterns := []string{"password", "secret"}
	matcher := NewFastMatcher(patterns)

	text := []byte("This file contains a password and a secret key")
	matches := matcher.FindMatches(text)

	if len(matches) != 2 {
		t.Fatalf("Expected 2 matches, got %d: %v", len(matches), matches)
	}

	// Results should be sorted
	expectedMatches := []string{"password", "secret"}
	if !reflect.DeepEqual(matches, expectedMatches) {
		t.Errorf("Expected matches %v, got %v", expectedMatches, matches)
	}
}

func TestFindMatches_CaseInsensitive(t *testing.T) {
	patterns := []string{"Password", "SECRET"}
	matcher := NewFastMatcher(patterns)

	text := []byte("This file contains a password and a secret key")
	matches := matcher.FindMatches(text)

	if len(matches) != 2 {
		t.Fatalf("Expected 2 matches, got %d: %v", len(matches), matches)
	}

	// Should return original pattern case
	expectedMatches := []string{"Password", "SECRET"}
	if !reflect.DeepEqual(matches, expectedMatches) {
		t.Errorf("Expected matches %v, got %v", expectedMatches, matches)
	}
}

func TestFindMatches_NoMatches(t *testing.T) {
	patterns := []string{"password", "secret"}
	matcher := NewFastMatcher(patterns)

	text := []byte("This is a clean file with no sensitive data")
	matches := matcher.FindMatches(text)

	if len(matches) != 0 {
		t.Errorf("Expected 0 matches, got %d: %v", len(matches), matches)
	}
}

func TestFindMatches_EmptyText(t *testing.T) {
	patterns := []string{"password", "secret"}
	matcher := NewFastMatcher(patterns)

	text := []byte("")
	matches := matcher.FindMatches(text)

	if matches != nil {
		t.Errorf("Expected nil for empty text, got %v", matches)
	}
}

func TestFindMatches_SmallText(t *testing.T) {
	patterns := []string{"key"}
	matcher := NewFastMatcher(patterns)

	// Text under 1024 bytes should use small text algorithm
	text := []byte("api_key")
	matches := matcher.FindMatches(text)

	if len(matches) != 1 {
		t.Fatalf("Expected 1 match, got %d: %v", len(matches), matches)
	}

	if matches[0] != "key" {
		t.Errorf("Expected match 'key', got '%s'", matches[0])
	}
}

func TestFindMatches_LargeText(t *testing.T) {
	patterns := []string{"password"}
	matcher := NewFastMatcher(patterns)

	// Create text over 1024 bytes to trigger large text algorithm
	largeText := strings.Repeat("Lorem ipsum dolor sit amet, ", 50) + "password"
	text := []byte(largeText)

	matches := matcher.FindMatches(text)

	if len(matches) != 1 {
		t.Fatalf("Expected 1 match, got %d: %v", len(matches), matches)
	}

	if matches[0] != "password" {
		t.Errorf("Expected match 'password', got '%s'", matches[0])
	}
}

func TestFindMatchesWithOriginalCase(t *testing.T) {
	patterns := []string{"password", "secret"}
	matcher := NewFastMatcher(patterns)

	text := []byte("This file contains a PASSWORD and a Secret key")
	matches := matcher.FindMatchesWithOriginalCase(text)

	if len(matches) != 2 {
		t.Fatalf("Expected 2 matches, got %d: %v", len(matches), matches)
	}

	// Should return case from the text, sorted
	expectedMatches := []string{"PASSWORD", "Secret"}
	if !reflect.DeepEqual(matches, expectedMatches) {
		t.Errorf("Expected matches %v, got %v", expectedMatches, matches)
	}
}

func TestFindOriginalCase(t *testing.T) {
	patterns := []string{"password"}
	matcher := NewFastMatcher(patterns)

	text := []byte("My PASSWORD is secret")
	lowerText := bytes.ToLower(text)
	original := matcher.findOriginalCase(text, lowerText, "password")

	if original != "PASSWORD" {
		t.Errorf("Expected 'PASSWORD', got '%s'", original)
	}
}

func TestFindOriginalCase_NotFound(t *testing.T) {
	patterns := []string{"password"}
	matcher := NewFastMatcher(patterns)

	text := []byte("No sensitive data here")
	lowerText := bytes.ToLower(text)
	original := matcher.findOriginalCase(text, lowerText, "password")

	// Should fallback to original pattern
	if original != "password" {
		t.Errorf("Expected fallback 'password', got '%s'", original)
	}
}

func TestHasAnyMatch(t *testing.T) {
	patterns := []string{"password", "secret"}
	matcher := NewFastMatcher(patterns)

	// Test with match
	text := []byte("This contains a password")
	if !matcher.HasAnyMatch(text) {
		t.Error("Expected HasAnyMatch to return true")
	}

	// Test without match
	text = []byte("This is clean")
	if matcher.HasAnyMatch(text) {
		t.Error("Expected HasAnyMatch to return false")
	}

	// Test empty text
	text = []byte("")
	if matcher.HasAnyMatch(text) {
		t.Error("Expected HasAnyMatch to return false for empty text")
	}
}

func TestGetMatcher_Caching(t *testing.T) {
	patterns := []string{"password", "secret"}

	// Get matcher twice with same patterns
	matcher1 := GetMatcher(patterns)
	matcher2 := GetMatcher(patterns)

	// Should return the same cached instance
	if matcher1 != matcher2 {
		t.Error("Expected cached matcher to be returned")
	}
}

func TestGetMatcher_EmptyPatterns(t *testing.T) {
	matcher := GetMatcher([]string{})

	if matcher == nil {
		t.Fatal("GetMatcher returned nil for empty patterns")
	}

	// Should not be cached for empty patterns
	matcher2 := GetMatcher([]string{})
	if matcher == matcher2 {
		t.Error("Empty pattern matchers should not be cached")
	}
}

func TestFastStringSearch(t *testing.T) {
	text := []byte("This is a test string")
	
	// Test with existing pattern
	if !FastStringSearch(text, []byte("test")) {
		t.Error("Expected to find 'test' in text")
	}

	// Test with non-existing pattern
	if FastStringSearch(text, []byte("missing")) {
		t.Error("Expected not to find 'missing' in text")
	}

	// Test with empty pattern
	if !FastStringSearch(text, []byte("")) {
		t.Error("Expected empty pattern to always match")
	}

	// Test with pattern longer than text
	if FastStringSearch([]byte("short"), []byte("longer pattern")) {
		t.Error("Expected pattern longer than text to not match")
	}
}

func TestMatcher_ConcurrentAccess(t *testing.T) {
	patterns := []string{"password", "secret", "api_key"}
	matcher := NewFastMatcher(patterns)

	// Test concurrent access
	done := make(chan bool, 10)
	
	for i := 0; i < 10; i++ {
		go func() {
			text := []byte("This contains password and secret")
			matches := matcher.FindMatches(text)
			if len(matches) != 2 {
				t.Errorf("Expected 2 matches, got %d", len(matches))
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestMatcher_PerformanceCharacteristics(t *testing.T) {
	// Test that small and large text use different algorithms
	patterns := []string{"password"}
	matcher := NewFastMatcher(patterns)

	// Small text
	smallText := []byte("password")
	matches := matcher.FindMatches(smallText)
	if len(matches) != 1 {
		t.Errorf("Small text: expected 1 match, got %d", len(matches))
	}

	// Large text
	largeText := make([]byte, 2000)
	copy(largeText[1000:], []byte("password"))
	matches = matcher.FindMatches(largeText)
	if len(matches) != 1 {
		t.Errorf("Large text: expected 1 match, got %d", len(matches))
	}
}

func TestMatcher_EdgeCases(t *testing.T) {
	// Test with very short patterns
	patterns := []string{"a", "I"}
	matcher := NewFastMatcher(patterns)

	text := []byte("I am a test")
	matches := matcher.FindMatches(text)

	if len(matches) < 1 {
		t.Error("Expected to find short patterns")
	}

	// Test with overlapping patterns
	patterns = []string{"test", "testing"}
	matcher = NewFastMatcher(patterns)

	text = []byte("testing")
	matches = matcher.FindMatches(text)

	// Should find both patterns
	if len(matches) != 2 {
		t.Errorf("Expected 2 overlapping matches, got %d: %v", len(matches), matches)
	}
}