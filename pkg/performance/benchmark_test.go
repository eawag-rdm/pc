package performance

import (
	"regexp"
	"strings"
	"testing"
)

// Benchmark data
var (
	testPatterns = []string{
		"password", "token", "secret", "key", "id_rsa", "id_ed25519",
		"Q:", "/Users/", "private", "confidential", "api_key", "auth",
	}
	
	smallText = []byte(`This is a small test file with some password and token data.
It contains various keywords that should be detected by the pattern matching.
The file has secret information and private keys like id_rsa.`)

	largeText = func() []byte {
		base := string(smallText)
		var builder strings.Builder
		for i := 0; i < 1000; i++ {
			builder.WriteString(base)
			builder.WriteString("\nLine ")
			builder.WriteString(strings.Repeat("data ", 100))
		}
		return []byte(builder.String())
	}()

	hugeText = func() []byte {
		base := string(largeText)
		var builder strings.Builder
		for i := 0; i < 100; i++ {
			builder.WriteString(base)
		}
		return []byte(builder.String())
	}()
)

// BenchmarkOriginalRegexApproach benchmarks the current regex-based matching
func BenchmarkOriginalRegexApproach(b *testing.B) {
	patterns := strings.Join(testPatterns, "|")
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate the original approach: compile regex every time
		regex, err := regexp.Compile("(?i)" + patterns)
		if err != nil {
			b.Fatal(err)
		}
		matches := regex.FindAll(largeText, -1)
		_ = matches
	}
}

// BenchmarkCachedRegexApproach benchmarks regex with caching
func BenchmarkCachedRegexApproach(b *testing.B) {
	patterns := strings.Join(testPatterns, "|")
	regex, err := regexp.Compile("(?i)" + patterns)
	if err != nil {
		b.Fatal(err)
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matches := regex.FindAll(largeText, -1)
		_ = matches
	}
}

// BenchmarkFastMatcherSmallText benchmarks our fast matcher on small text
func BenchmarkFastMatcherSmallText(b *testing.B) {
	matcher := NewFastMatcher(testPatterns)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matches := matcher.FindMatches(smallText)
		_ = matches
	}
}

// BenchmarkFastMatcherLargeText benchmarks our fast matcher on large text
func BenchmarkFastMatcherLargeText(b *testing.B) {
	matcher := NewFastMatcher(testPatterns)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matches := matcher.FindMatches(largeText)
		_ = matches
	}
}

// BenchmarkFastMatcherHugeText benchmarks our fast matcher on huge text
func BenchmarkFastMatcherHugeText(b *testing.B) {
	matcher := NewFastMatcher(testPatterns)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matches := matcher.FindMatches(hugeText)
		_ = matches
	}
}

// BenchmarkFastMatcherPresenceOnly benchmarks presence detection (faster than full matching)
func BenchmarkFastMatcherPresenceOnly(b *testing.B) {
	matcher := NewFastMatcher(testPatterns)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hasMatch := matcher.HasAnyMatch(largeText)
		_ = hasMatch
	}
}

// BenchmarkCachedMatcher benchmarks the cached matcher lookup
func BenchmarkCachedMatcher(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matcher := GetMatcher(testPatterns)
		matches := matcher.FindMatches(largeText)
		_ = matches
	}
}

// BenchmarkMemoryAllocation benchmarks memory allocation patterns
func BenchmarkMemoryAllocation(b *testing.B) {
	b.Run("StringSliceAllocation", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			results := make([]string, 0, len(testPatterns))
			for _, pattern := range testPatterns {
				results = append(results, pattern)
			}
			_ = results
		}
	})
	
	b.Run("MapAllocation", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			results := make(map[string]struct{}, len(testPatterns))
			for _, pattern := range testPatterns {
				results[pattern] = struct{}{}
			}
			_ = results
		}
	})
}

// BenchmarkStringOperations compares different string operation approaches
func BenchmarkStringOperations(b *testing.B) {
	text := string(largeText)
	pattern := "password"
	patternBytes := []byte(pattern)
	
	b.Run("StringsContains", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			found := strings.Contains(text, pattern)
			_ = found
		}
	})
	
	b.Run("BytesContains", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			found := FastStringSearch(largeText, patternBytes)
			_ = found
		}
	})
	
	b.Run("RegexMatch", func(b *testing.B) {
		regex := regexp.MustCompile("(?i)" + pattern)
		for i := 0; i < b.N; i++ {
			found := regex.Match(largeText)
			_ = found
		}
	})
}

// BenchmarkParallelVsSequential compares parallel vs sequential processing
func BenchmarkParallelVsSequential(b *testing.B) {
	// This would need to be implemented once we integrate with the actual file structures
	// For now, we'll benchmark the core matching logic
	
	b.Run("Sequential", func(b *testing.B) {
		matcher := NewFastMatcher(testPatterns)
		texts := [][]byte{smallText, largeText, smallText, largeText}
		
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, text := range texts {
				matches := matcher.FindMatches(text)
				_ = matches
			}
		}
	})
	
	// We'll add parallel benchmark once the integration is complete
}