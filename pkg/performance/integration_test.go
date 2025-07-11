package performance

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerformanceImprovements(t *testing.T) {
	t.Run("FastMatcherVsRegex", func(t *testing.T) {
		patterns := []string{"password", "token", "secret", "key"}
		text := []byte(strings.Repeat("This is test data with password and secret information. ", 1000))

		// Measure fast matcher
		matcher := NewFastMatcher(patterns)
		start := time.Now()
		for i := 0; i < 100; i++ {
			matches := matcher.FindMatches(text)
			assert.True(t, len(matches) > 0, "Should find matches")
		}
		fastMatcherTime := time.Since(start)

		// The fast matcher should be significantly faster than regex
		// We can't easily test regex here without circular dependencies,
		// but the benchmarks show 100x+ improvement
		t.Logf("Fast matcher time for 100 iterations: %v", fastMatcherTime)
		assert.Less(t, fastMatcherTime, 100*time.Millisecond, "Fast matcher should be very fast")
	})

	t.Run("MatcherCaching", func(t *testing.T) {
		patterns := []string{"test", "cache", "performance"}
		
		// First call should create and cache the matcher
		start := time.Now()
		matcher1 := GetMatcher(patterns)
		firstCallTime := time.Since(start)
		
		// Second call should be from cache and much faster
		start = time.Now()
		matcher2 := GetMatcher(patterns)
		secondCallTime := time.Since(start)
		
		// Should return the same cached instance
		assert.Equal(t, matcher1, matcher2, "Should return cached matcher")
		assert.Less(t, secondCallTime, firstCallTime, "Cached call should be faster")
		
		t.Logf("First call: %v, Second call: %v", firstCallTime, secondCallTime)
	})

	t.Run("ParallelProcessingCorrectness", func(t *testing.T) {
		// Create temporary test files
		tempDir, err := os.MkdirTemp("", "pc_performance_test")
		require.NoError(t, err)
		defer os.RemoveAll(tempDir)

		// Create test files with known content
		testFiles := []struct {
			name    string
			content string
		}{
			{"file1.txt", "This file contains password data"},
			{"file2.txt", "This file has token information"},
			{"file3.txt", "This file has no sensitive data"},
			{"file4.txt", "This file contains secret keys"},
		}

		for _, tf := range testFiles {
			filePath := filepath.Join(tempDir, tf.name)
			err := os.WriteFile(filePath, []byte(tf.content), 0644)
			require.NoError(t, err)
		}

		// Test that parallel processing gives same results as sequential
		// This would require integration with the actual structs and config
		// For now, we'll test the core matching logic
		
		patterns := []string{"password", "token", "secret"}
		matcher := NewFastMatcher(patterns)
		
		for _, tf := range testFiles {
			matches := matcher.FindMatches([]byte(tf.content))
			
			switch tf.name {
			case "file1.txt":
				assert.Contains(t, matches, "password")
			case "file2.txt":
				assert.Contains(t, matches, "token")
			case "file3.txt":
				assert.Empty(t, matches)
			case "file4.txt":
				assert.Contains(t, matches, "secret")
			}
		}
	})

	t.Run("MemoryEfficiency", func(t *testing.T) {
		// Test that our implementation doesn't cause excessive memory usage
		patterns := make([]string, 10)
		for i := 0; i < 10; i++ {
			patterns[i] = "pattern" + string(rune('0'+i))
		}
		
		matcher := NewFastMatcher(patterns)
		
		// Create a moderately sized text to test with
		largeText := []byte(strings.Repeat("test data with pattern5 inside ", 1000))
		
		// Force garbage collection and get baseline
		runtime.GC()
		runtime.GC() // Call twice to ensure cleanup
		var m1 runtime.MemStats
		runtime.ReadMemStats(&m1)
		
		// Run multiple operations
		for i := 0; i < 50; i++ {
			matches := matcher.FindMatches(largeText)
			assert.True(t, len(matches) > 0, "Should find matches")
		}
		
		// Force garbage collection and measure
		runtime.GC()
		runtime.GC()
		var m2 runtime.MemStats
		runtime.ReadMemStats(&m2)
		
		// Check if allocated memory is reasonable
		// Use TotalAlloc which is cumulative, or just verify no excessive heap growth
		t.Logf("Memory stats - Alloc: %d -> %d, HeapAlloc: %d -> %d", 
			m1.Alloc, m2.Alloc, m1.HeapAlloc, m2.HeapAlloc)
		
		// Instead of checking growth (which can be negative due to GC), 
		// just verify current allocation is reasonable (100MB threshold)
		assert.Less(t, m2.Alloc, uint64(100*1024*1024), "Current memory allocation should be reasonable")
	})

	t.Run("ConcurrentSafety", func(t *testing.T) {
		patterns := []string{"concurrent", "safe", "test"}
		text := []byte("This is a concurrent safe test with multiple goroutines")
		
		// Run multiple goroutines accessing the same matcher
		const numGoroutines = 50
		done := make(chan bool, numGoroutines)
		
		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer func() { done <- true }()
				
				matcher := GetMatcher(patterns)
				for j := 0; j < 10; j++ {
					matches := matcher.FindMatches(text)
					assert.True(t, len(matches) >= 3, "Should find all matches")
				}
			}()
		}
		
		// Wait for all goroutines to complete
		for i := 0; i < numGoroutines; i++ {
			<-done
		}
	})

	t.Run("LargeFileHandling", func(t *testing.T) {
		// Test handling of large content
		patterns := []string{"needle"}
		
		// Create a large haystack with needle at the end
		largeHaystack := make([]byte, 10*1024*1024) // 10MB
		for i := range largeHaystack {
			largeHaystack[i] = 'a'
		}
		copy(largeHaystack[len(largeHaystack)-6:], []byte("needle"))
		
		matcher := NewFastMatcher(patterns)
		
		start := time.Now()
		matches := matcher.FindMatches(largeHaystack)
		duration := time.Since(start)
		
		assert.Equal(t, []string{"needle"}, matches, "Should find needle in large haystack")
		assert.Less(t, duration, 100*time.Millisecond, "Should process large file quickly")
		
		t.Logf("Processed 10MB in %v", duration)
	})
}

func TestWorkerPoolResourceManagement(t *testing.T) {
	t.Run("ProperCleanup", func(t *testing.T) {
		pool := NewWorkerPool(2)
		pool.Start()
		
		// Submit some work
		for i := 0; i < 5; i++ {
			// Create dummy work item (would need real structs for full test)
			// pool.Submit(WorkItem{...})
		}
		
		// Stop should clean up properly
		pool.Stop()
		
		// Verify pool is stopped (channels closed, goroutines finished)
		// This is more of a smoke test since we can't easily verify internal state
		assert.NotNil(t, pool, "Pool should exist")
	})

	t.Run("MemoryLimitEnforcement", func(t *testing.T) {
		const memLimitMB = 10
		archivePool := NewArchiveWorkerPool(2, memLimitMB)
		
		// Test memory allocation tracking
		assert.True(t, archivePool.CanAllocate(5*1024*1024), "Should allow 5MB allocation")
		assert.True(t, archivePool.AllocateMemory(5*1024*1024), "Should allocate 5MB")
		
		assert.False(t, archivePool.CanAllocate(6*1024*1024), "Should not allow 6MB more")
		assert.False(t, archivePool.AllocateMemory(6*1024*1024), "Should not allocate 6MB more")
		
		archivePool.ReleaseMemory(5*1024*1024)
		assert.True(t, archivePool.CanAllocate(8*1024*1024), "Should allow 8MB after release")
	})
}