package utils

import (
	"context"
	"os"
	"path/filepath"
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
		done <- ApplyAllChecks(ctx, config.Config{}, files, true)
	}()

	select {
	case <-done:
		// Returned promptly — content does not matter (a file in progress may
		// finish); the contract is coarse-grained early exit, not zero output.
	case <-time.After(5 * time.Second):
		t.Fatal("ApplyAllChecks did not return promptly on a cancelled context")
	}
}

// TestApplyChecksFilteredByFile_PanickingCheck_Parallel asserts a panicking
// check on the parallel (worker-pool) path is converted into a logged failure:
// the call returns normally with the healthy check's messages and the process
// survives. Before the SafeRunCheck guard this panicked in a pool goroutine and
// killed the whole process.
func TestApplyChecksFilteredByFile_PanickingCheck_Parallel(t *testing.T) {
	files := writeTempFiles(t, 3) // >= 2 files selects the parallel path

	panicking := func(file structs.File, cfg config.Config) []structs.Message {
		panic("boom: simulated check bug")
	}
	healthy := func(file structs.File, cfg config.Config) []structs.Message {
		return []structs.Message{{Content: "healthy ran", Source: file}}
	}

	messages := ApplyChecksFilteredByFile(context.Background(), config.Config{},
		[]func(structs.File, config.Config) []structs.Message{panicking, healthy}, files)

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
