package utils

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestNewWorkerPool(t *testing.T) {
	pool := newWorkerPool(context.Background(), 4)

	if pool == nil {
		t.Fatal("NewWorkerPool returned nil")
	}

	if pool.numWorkers != 4 {
		t.Errorf("Expected 4 workers, got %d", pool.numWorkers)
	}

	if pool.workChan == nil {
		t.Error("Work channel not initialized")
	}

	if pool.resultChan == nil {
		t.Error("Result channel not initialized")
	}

	if pool.ctx == nil {
		t.Error("Context not initialized")
	}

	pool.stop()
}

func TestNewWorkerPool_DefaultWorkers(t *testing.T) {
	pool := newWorkerPool(context.Background(), 0)

	if pool.numWorkers < 1 {
		t.Errorf("default sizing produced %d workers", pool.numWorkers)
	}
	if got, want := cap(pool.workChan), pool.numWorkers*2; got != want {
		t.Errorf("work channel buffered %d, want 2 per worker (%d)", got, want)
	}

	pool.stop()
}

// TestNewWorkerPool_RespectsCappedGOMAXPROCS pins the sizing SOURCE: GOMAXPROCS
// equals NumCPU unless lowered, so only a lowered value tells the two apart.
func TestNewWorkerPool_RespectsCappedGOMAXPROCS(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("needs a budget above 1: at GOMAXPROCS=1 the assertion is vacuous")
	}
	// Not parallel: GOMAXPROCS is process-wide. Setting it also pins the
	// runtime's automatic (cgroup-aware) updating off for the rest of the test
	// binary, which no test depends on.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	pool := newWorkerPool(context.Background(), 0)
	defer pool.stop()

	if pool.numWorkers != 1 {
		t.Errorf("pool sized %d workers under GOMAXPROCS=1, want 1", pool.numWorkers)
	}
}

// TestArchiveWorkers pins the sizing with real semantics behind it: half the
// budget for memory reasons, but never below two extractions and never above
// the budget itself. Plain halving returns 1 at a budget of 2 or 3.
func TestArchiveWorkers(t *testing.T) {
	cases := []struct{ procs, want int }{
		{1, 1}, // never more than the budget
		{2, 2}, // plain halving would serialise here
		{3, 2},
		{4, 2},
		{8, 4},
		{16, 8},
	}
	for _, tc := range cases {
		if got := archiveWorkers(tc.procs); got != tc.want {
			t.Errorf("archiveWorkers(%d) = %d, want %d", tc.procs, got, tc.want)
		}
	}
}

func TestWorkerPool_StartStop(t *testing.T) {
	pool := newWorkerPool(context.Background(), 2)

	// Start the pool
	pool.start()

	// Should be able to stop cleanly
	pool.stop()

	// Test stopping a fresh pool without starting
	pool2 := newWorkerPool(context.Background(), 1)
	pool2.stop() // Should not panic
}

func TestWorkerPool_ProcessWork(t *testing.T) {
	pool := newWorkerPool(context.Background(), 2)
	pool.start()
	defer pool.stop()

	// Mock check
	testCheck := mockEntry("testCheck", func(file structs.File) []structs.Message {
		return []structs.Message{
			{Content: "Test message", Source: file},
		}
	})

	// Create test work item
	testFile := structs.File{Name: "test.txt", Path: "/test/test.txt"}
	workItem := workItem{File: testFile, Checks: []checkRules{testCheck}}

	// Submit work
	success := pool.submit(workItem)
	if !success {
		t.Fatal("Failed to submit work item")
	}

	// Get result
	select {
	case result := <-pool.results():
		if len(result.Messages) != 1 {
			t.Errorf("Expected 1 message, got %d", len(result.Messages))
		}

		if result.Messages[0].Content != "Test message" {
			t.Errorf("Expected 'Test message', got '%s'", result.Messages[0].Content)
		}

	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for result")
	}
}

func TestWorkerPool_MultipleChecks(t *testing.T) {
	pool := newWorkerPool(context.Background(), 1)
	pool.start()
	defer pool.stop()

	// Multiple checks
	check1 := mockEntry("check1", func(file structs.File) []structs.Message {
		return []structs.Message{{Content: "Check1 message", Source: file}}
	})

	check2 := mockEntry("check2", func(file structs.File) []structs.Message {
		return []structs.Message{{Content: "Check2 message", Source: file}}
	})

	check3 := mockEntry("check3", func(file structs.File) []structs.Message {
		return []structs.Message{} // No messages
	})

	testFile := structs.File{Name: "test.txt", Path: "/test/test.txt"}
	workItem := workItem{File: testFile, Checks: []checkRules{check1, check2, check3}}

	pool.submit(workItem)

	result := <-pool.results()
	if len(result.Messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(result.Messages))
	}
}

func TestWorkerPool_ConcurrentProcessing(t *testing.T) {
	pool := newWorkerPool(context.Background(), 4)
	pool.start()
	defer pool.stop()

	testCheck := mockEntry("testCheck", func(file structs.File) []structs.Message {
		// Simulate some processing time
		time.Sleep(10 * time.Millisecond)
		return []structs.Message{{Content: "Message for " + file.Name, Source: file}}
	})

	numJobs := 10
	submitted := 0

	// Submit multiple work items
	for i := 0; i < numJobs; i++ {
		testFile := structs.File{Name: fmt.Sprintf("test%d.txt", i), Path: "/test/"}
		if pool.submit(workItem{File: testFile, Checks: []checkRules{testCheck}}) {
			submitted++
		}
	}

	// Collect results
	results := 0
	timeout := time.After(5 * time.Second)

	for results < submitted {
		select {
		case result := <-pool.results():
			if len(result.Messages) != 1 {
				t.Errorf("Expected 1 message, got %d", len(result.Messages))
			}
			results++

		case <-timeout:
			t.Fatalf("Timeout waiting for results. Got %d/%d", results, submitted)
		}
	}
}

func TestWorkerPool_ChannelFullHandling(t *testing.T) {
	// Create pool with small buffer (but start workers so it processes)
	pool := newWorkerPool(context.Background(), 1)

	testFile := structs.File{Name: "test.txt", Path: "/test/test.txt"}
	testCheck := mockEntry("testCheck", func(file structs.File) []structs.Message {
		return []structs.Message{}
	})

	workItem := workItem{File: testFile, Checks: []checkRules{testCheck}}

	// Submit should succeed when workers are running
	success := pool.submit(workItem)
	if !success {
		t.Error("Expected Submit to return true when pool is running")
	}

	// Test that Submit returns false after Stop is called
	pool.stop()

	// After stop, context is cancelled so Submit should return false
	success = pool.submit(workItem)
	if success {
		t.Error("Expected Submit to return false after pool is stopped")
	}
}

// TestWorkerPool_PanickingCheck_DoesNotKillProcess asserts the defence-in-depth
// contract behind the boot-time checks-config validation: even if a check
// panics inside a worker-pool goroutine (where no request-level recover can
// reach), the panic is converted into a logged failure - the pool keeps
// working, the other checks' messages survive, and the process stays alive.
// Without safeRunCheck this test would crash the whole test binary.
func TestWorkerPool_PanickingCheck_DoesNotKillProcess(t *testing.T) {
	output.GlobalLogger.SetJSONMode(true)
	output.GlobalLogger.ClearMessages()

	panicking := mockEntry("panicking", func(structs.File) []structs.Message {
		panic("boom: simulated check bug")
	})
	healthy := mockEntry("healthy", func(file structs.File) []structs.Message {
		return []structs.Message{{Content: "healthy ran", Source: file}}
	})

	pool := newWorkerPool(context.Background(), 2)
	pool.start()
	defer pool.stop()

	file := structs.File{Name: "a.txt", Path: "/tmp/a.txt"}
	if !pool.submit(workItem{File: file, Checks: []checkRules{panicking, healthy}}) {
		t.Fatal("submit failed")
	}

	result := <-pool.results()
	if len(result.Messages) != 1 || result.Messages[0].Content != "healthy ran" {
		t.Fatalf("expected only the healthy check's message, got %v", result.Messages)
	}

	// The failure must be acknowledged in the buffered logger (as a short,
	// path-free error), not silently swallowed.
	found := false
	for _, msg := range output.GlobalLogger.GetMessages() {
		if msg.Level == "error" && strings.Contains(msg.Message, "internal error") {
			found = true
			if strings.Contains(msg.Message, "/tmp/") {
				t.Errorf("panic notice must not leak the file path, got %q", msg.Message)
			}
		}
	}
	if !found {
		t.Error("expected an error-level log message acknowledging the failed check")
	}
}
