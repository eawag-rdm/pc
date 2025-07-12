package optimization

import (
	"testing"
)

func TestFastMatcher(t *testing.T) {
	patterns := []string{"test", "pattern", "match"}
	matcher := NewFastMatcher(patterns)
	
	text := []byte("This is a test string with a pattern to match")
	matches := matcher.FindMatches(text)
	
	if len(matches) != 3 {
		t.Errorf("Expected 3 matches, got %d", len(matches))
	}
	
	expected := []string{"match", "pattern", "test"}
	for i, match := range matches {
		if match != expected[i] {
			t.Errorf("Expected %s, got %s", expected[i], match)
		}
	}
}

func TestMatcherCaching(t *testing.T) {
	patterns := []string{"cached", "test"}
	
	matcher1 := GetMatcher(patterns)
	matcher2 := GetMatcher(patterns)
	
	// Should return the same cached instance
	if matcher1 != matcher2 {
		t.Error("Expected cached matcher to return same instance")
	}
}

func TestHasAnyMatchCaching(t *testing.T) {
	patterns := []string{"needle"}
	matcher := NewFastMatcher(patterns)
	
	haystack := []byte("finding a needle in a haystack")
	if !matcher.HasAnyMatch(haystack) {
		t.Error("Expected to find match")
	}
	
	empty := []byte("no match here")
	if matcher.HasAnyMatch(empty) {
		t.Error("Expected no match")
	}
}