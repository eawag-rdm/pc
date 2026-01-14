package optimization

import (
	"bytes"
	"sort"
	"strings"
	"sync"
)

// FastMatcher provides high-performance string matching using multiple algorithms
type FastMatcher struct {
	patterns      []string
	lowerPatterns []string   // pre-computed lowercased patterns
	patternBytes  [][]byte   // pre-computed pattern byte slices for large text search
	maxLen        int
	minLen        int
	caseMap       map[string]string // lowercase pattern -> original pattern
}

// NewFastMatcher creates a new fast string matcher optimized for the given patterns
func NewFastMatcher(patterns []string) *FastMatcher {
	if len(patterns) == 0 {
		return &FastMatcher{patterns: patterns, caseMap: make(map[string]string)}
	}

	fm := &FastMatcher{
		patterns:      make([]string, len(patterns)),
		lowerPatterns: make([]string, len(patterns)),
		patternBytes:  make([][]byte, len(patterns)),
		caseMap:       make(map[string]string),
		minLen:        1000000,
		maxLen:        0,
	}

	// Process patterns and build lookup structures
	for i, pattern := range patterns {
		if len(pattern) == 0 {
			continue
		}

		fm.patterns[i] = pattern
		lowerPattern := strings.ToLower(pattern)
		fm.lowerPatterns[i] = lowerPattern
		fm.patternBytes[i] = []byte(lowerPattern)
		fm.caseMap[lowerPattern] = pattern

		if len(pattern) > fm.maxLen {
			fm.maxLen = len(pattern)
		}
		if len(pattern) < fm.minLen {
			fm.minLen = len(pattern)
		}
	}

	return fm
}

// FindMatches returns all unique pattern matches found in the text
// This uses multiple optimized algorithms based on pattern characteristics
func (fm *FastMatcher) FindMatches(text []byte) []string {
	if len(text) == 0 || len(fm.patterns) == 0 {
		return nil
	}

	found := make(map[string]struct{})
	lowerText := bytes.ToLower(text)

	// Use different strategies based on pattern length and text size
	if len(text) < 1024 {
		// For small text, use simple but fast approach
		fm.findInSmallText(lowerText, found)
	} else {
		// For larger text, use optimized search
		fm.findInLargeText(lowerText, found)
	}

	// Convert to slice and sort for consistent ordering
	result := make([]string, 0, len(found))
	for match := range found {
		if original, exists := fm.caseMap[match]; exists {
			result = append(result, original)
		}
	}

	// Sort to ensure consistent ordering for tests
	sort.Strings(result)
	return result
}

// FindMatchesWithOriginalCase finds matches and returns them with their original case from the text
func (fm *FastMatcher) FindMatchesWithOriginalCase(text []byte) []string {
	found := make(map[string]string) // map[lowerPattern]originalFromText
	lowerText := bytes.ToLower(text)

	// Find all matches first
	matchSet := make(map[string]struct{})
	if len(text) < 1024 {
		fm.findInSmallText(lowerText, matchSet)
	} else {
		fm.findInLargeText(lowerText, matchSet)
	}

	// For each found pattern, find its original case in the text
	for lowerMatch := range matchSet {
		// Find the original case in the text
		originalCase := fm.findOriginalCase(text, lowerText, lowerMatch)
		found[lowerMatch] = originalCase
	}

	// Convert to slice and sort by original case for consistent ordering
	result := make([]string, 0, len(found))
	for _, original := range found {
		result = append(result, original)
	}

	sort.Strings(result)
	return result
}

// findOriginalCase finds the original case of a pattern in the text
func (fm *FastMatcher) findOriginalCase(text []byte, lowerText []byte, lowerPattern string) string {
	pattern := []byte(lowerPattern)

	// Find the first occurrence of the pattern
	idx := bytes.Index(lowerText, pattern)
	if idx == -1 {
		// Fallback to the pattern itself
		if original, exists := fm.caseMap[lowerPattern]; exists {
			return original
		}
		return lowerPattern
	}
	
	// Extract the original case from the text
	// Ensure we don't go out of bounds
	endIdx := idx + len(pattern)
	if endIdx > len(text) {
		// Fallback to original pattern if bounds would be exceeded
		if original, exists := fm.caseMap[lowerPattern]; exists {
			return original
		}
		return lowerPattern
	}
	return string(text[idx:endIdx])
}

// findInSmallText uses simple string contains for small texts
func (fm *FastMatcher) findInSmallText(lowerText []byte, found map[string]struct{}) {
	textStr := string(lowerText)

	for i, lowerPattern := range fm.lowerPatterns {
		if len(fm.patterns[i]) == 0 {
			continue
		}

		if strings.Contains(textStr, lowerPattern) {
			found[lowerPattern] = struct{}{}
		}
	}
}

// findInLargeText uses optimized algorithms for larger texts
func (fm *FastMatcher) findInLargeText(lowerText []byte, found map[string]struct{}) {
	// Use bytes.Contains which is optimized in Go's stdlib
	for i, patternBytes := range fm.patternBytes {
		if len(fm.patterns[i]) == 0 {
			continue
		}

		if bytes.Contains(lowerText, patternBytes) {
			found[fm.lowerPatterns[i]] = struct{}{}
		}
	}
}

// HasAnyMatch returns true if any pattern matches (faster than FindMatches when only presence is needed)
func (fm *FastMatcher) HasAnyMatch(text []byte) bool {
	if len(text) == 0 || len(fm.patterns) == 0 {
		return false
	}

	lowerText := bytes.ToLower(text)

	for i, patternBytes := range fm.patternBytes {
		if len(fm.patterns[i]) == 0 {
			continue
		}

		if bytes.Contains(lowerText, patternBytes) {
			return true
		}
	}

	return false
}

// MatcherCache provides thread-safe caching of FastMatcher instances
type MatcherCache struct {
	cache map[string]*FastMatcher
	mutex sync.RWMutex
}

var globalMatcherCache = &MatcherCache{
	cache: make(map[string]*FastMatcher),
}

// GetMatcher returns a cached FastMatcher for the given patterns
func GetMatcher(patterns []string) *FastMatcher {
	if len(patterns) == 0 {
		return NewFastMatcher(patterns)
	}

	// Create a cache key from patterns
	key := strings.Join(patterns, "|")
	
	globalMatcherCache.mutex.RLock()
	if matcher, exists := globalMatcherCache.cache[key]; exists {
		globalMatcherCache.mutex.RUnlock()
		return matcher
	}
	globalMatcherCache.mutex.RUnlock()

	// Create new matcher
	matcher := NewFastMatcher(patterns)
	
	globalMatcherCache.mutex.Lock()
	globalMatcherCache.cache[key] = matcher
	globalMatcherCache.mutex.Unlock()
	
	return matcher
}

// FastStringSearch provides Boyer-Moore-like fast string searching
func FastStringSearch(text []byte, pattern []byte) bool {
	if len(pattern) == 0 {
		return true
	}
	if len(text) < len(pattern) {
		return false
	}
	
	// Use Go's optimized bytes.Contains for most cases
	// Go's implementation uses a combination of algorithms including
	// a form of Boyer-Moore for larger patterns
	return bytes.Contains(text, pattern)
}