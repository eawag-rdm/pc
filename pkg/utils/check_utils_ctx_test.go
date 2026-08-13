package utils

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// writeTempFiles creates n small real files so the file checks have something
// to stat/read, and returns them as structs.File values.
func writeTempFiles(t *testing.T, n int) []structs.File {
	t.Helper()
	dir := t.TempDir()
	files := make([]structs.File, 0, n)
	for i := 0; i < n; i++ {
		path := filepath.Join(dir, "file"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		files = append(files, structs.File{Name: filepath.Base(path), Path: path})
	}
	return files
}

// TestApplyAllChecks_CancelledContext_ReturnsPromptly asserts the analysis hard
// timeout is real for the checks phase: with an already-expired context the
// engine stops between files instead of scanning the whole set.
func TestApplyAllChecks_CancelledContext_ReturnsPromptly(t *testing.T) {
	files := writeTempFiles(t, 4)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // expired before the checks start

	done := make(chan []structs.Message, 1)
	go func() {
		messages, _ := ApplyAllChecks(ctx, config.Config{}, compilePlan(t, config.Config{}), files, true)
		done <- messages
	}()

	select {
	case <-done:
		// Returned promptly - content does not matter (a file in progress may
		// finish); the contract is coarse-grained early exit, not zero output.
	case <-time.After(5 * time.Second):
		t.Fatal("ApplyAllChecks did not return promptly on a cancelled context")
	}
}

// TestApplyChecksFilteredByFile_CancelledMidScan_ReturnsPromptly is the same
// contract for a context cancelled while the pool is busy, not before it
// starts. The pool's context derives from the caller's, so cancellation makes
// the workers drop whatever is still queued; the collect loop must not go on
// waiting for those results. Without the ctx case in that loop this hangs
// forever - and on the server, which admits one analysis at a time, a hang here
// is fatal. The 200-file set is far above the 2-file threshold, so the pass
// takes the pool path, not the sequential walk.
func TestApplyChecksFilteredByFile_CancelledMidScan_ReturnsPromptly(t *testing.T) {
	// No files are created on disk: the mock check below never opens them, and
	// dispatch selects on the name alone.
	files := make([]structs.File, 0, 200)
	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("file%03d.txt", i)
		files = append(files, structs.File{Name: name, Path: filepath.Join("testdata-none", name)})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // the mock cancels mid-scan; this covers the paths where it does not
	var run atomic.Int32
	slow := mockEntry("slow", func(structs.File) []structs.Message {
		if run.Add(1) == 4 { // cancel once the pool is under way
			cancel()
		}
		time.Sleep(2 * time.Millisecond)
		return nil
	})

	done := make(chan struct{})
	go func() {
		applyChecksFilteredByFile(ctx, &diagSink{}, []checkRules{slow}, files)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("applyChecksFilteredByFile did not return after mid-scan cancellation")
	}
	// Returning is not enough: it must have returned because the scan was cut
	// short. Only the items already queued when the cancel landed can still run,
	// which is a small fraction of the file set.
	if ran := run.Load(); ran >= int32(len(files)) {
		t.Fatalf("cancellation did not cut the scan short: %d of %d files were checked", ran, len(files))
	}
}

// TestApplyChecksFilteredByFile_PanickingCheck_Parallel asserts a panicking
// check on the parallel (worker-pool) path is converted into a logged failure:
// the call returns normally with the healthy check's messages and the process
// survives. Before the safeRunCheck guard this panicked in a pool goroutine and
// killed the whole process.
func TestApplyChecksFilteredByFile_PanickingCheck_Parallel(t *testing.T) {
	files := writeTempFiles(t, 3) // >= 2 files selects the parallel path

	panicking := mockEntry("panicking", func(structs.File) []structs.Message {
		panic("boom: simulated check bug")
	})
	healthy := mockEntry("healthy", func(file structs.File) []structs.Message {
		return []structs.Message{{Content: "healthy ran", Source: file}}
	})

	messages := applyChecksFilteredByFile(context.Background(), &diagSink{}, []checkRules{panicking, healthy}, files)

	healthyCount := 0
	for _, m := range messages {
		if m.Content == "healthy ran" {
			healthyCount++
		}
	}
	if healthyCount != len(files) {
		t.Errorf("expected the healthy check to run for all %d files despite the panicking sibling, got %d messages: %v", len(files), healthyCount, messages)
	}
}
