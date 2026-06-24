package optimization

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestNewWorkerPool(t *testing.T) {
	pool := NewWorkerPool(4)

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

	pool.Stop()
}

func TestNewWorkerPool_DefaultWorkers(t *testing.T) {
	pool := NewWorkerPool(0)

	expectedWorkers := runtime.NumCPU()
	if pool.numWorkers != expectedWorkers {
		t.Errorf("Expected %d workers (NumCPU), got %d", expectedWorkers, pool.numWorkers)
	}

	pool.Stop()
}

func TestWorkerPool_StartStop(t *testing.T) {
	pool := NewWorkerPool(2)

	// Start the pool
	pool.Start()

	// Should be able to stop cleanly
	pool.Stop()

	// Test stopping a fresh pool without starting
	pool2 := NewWorkerPool(1)
	pool2.Stop() // Should not panic
}

func TestWorkerPool_ProcessWork(t *testing.T) {
	pool := NewWorkerPool(2)
	pool.Start()
	defer pool.Stop()

	// Mock check function
	testCheck := func(file structs.File, cfg config.Config) []structs.Message {
		return []structs.Message{
			{Content: "Test message", Source: file},
		}
	}

	// Create test work item
	testFile := structs.File{Name: "test.txt", Path: "/test/test.txt"}
	workItem := WorkItem{
		File:   testFile,
		Checks: []func(structs.File, config.Config) []structs.Message{testCheck},
		Config: config.Config{},
	}

	// Submit work
	success := pool.Submit(workItem)
	if !success {
		t.Fatal("Failed to submit work item")
	}

	// Get result
	select {
	case result := <-pool.Results():
		if result.Error != nil {
			t.Errorf("Unexpected error: %v", result.Error)
		}

		if len(result.Messages) != 1 {
			t.Errorf("Expected 1 message, got %d", len(result.Messages))
		}

		if result.Messages[0].Content != "Test message" {
			t.Errorf("Expected 'Test message', got '%s'", result.Messages[0].Content)
		}

		if result.Duration <= 0 {
			t.Error("Duration should be positive")
		}

	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for result")
	}
}

func TestWorkerPool_MultipleChecks(t *testing.T) {
	pool := NewWorkerPool(1)
	pool.Start()
	defer pool.Stop()

	// Multiple check functions
	check1 := func(file structs.File, cfg config.Config) []structs.Message {
		return []structs.Message{{Content: "Check1 message", Source: file}}
	}

	check2 := func(file structs.File, cfg config.Config) []structs.Message {
		return []structs.Message{{Content: "Check2 message", Source: file}}
	}

	check3 := func(file structs.File, cfg config.Config) []structs.Message {
		return []structs.Message{} // No messages
	}

	testFile := structs.File{Name: "test.txt", Path: "/test/test.txt"}
	workItem := WorkItem{
		File:   testFile,
		Checks: []func(structs.File, config.Config) []structs.Message{check1, check2, check3},
		Config: config.Config{},
	}

	pool.Submit(workItem)

	result := <-pool.Results()
	if len(result.Messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(result.Messages))
	}
}

func TestWorkerPool_ConcurrentProcessing(t *testing.T) {
	pool := NewWorkerPool(4)
	pool.Start()
	defer pool.Stop()

	testCheck := func(file structs.File, cfg config.Config) []structs.Message {
		// Simulate some processing time
		time.Sleep(10 * time.Millisecond)
		return []structs.Message{{Content: "Message for " + file.Name, Source: file}}
	}

	numJobs := 10
	submitted := 0

	// Submit multiple work items
	for i := 0; i < numJobs; i++ {
		testFile := structs.File{Name: fmt.Sprintf("test%d.txt", i), Path: "/test/"}
		workItem := WorkItem{
			File:   testFile,
			Checks: []func(structs.File, config.Config) []structs.Message{testCheck},
			Config: config.Config{},
		}

		if pool.Submit(workItem) {
			submitted++
		}
	}

	// Collect results
	results := 0
	timeout := time.After(5 * time.Second)

	for results < submitted {
		select {
		case result := <-pool.Results():
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
	pool := NewWorkerPool(1)

	testFile := structs.File{Name: "test.txt", Path: "/test/test.txt"}
	testCheck := func(file structs.File, cfg config.Config) []structs.Message {
		return []structs.Message{}
	}

	workItem := WorkItem{
		File:   testFile,
		Checks: []func(structs.File, config.Config) []structs.Message{testCheck},
		Config: config.Config{},
	}

	// Submit should succeed when workers are running
	success := pool.Submit(workItem)
	if !success {
		t.Error("Expected Submit to return true when pool is running")
	}

	// Test that Submit returns false after Stop is called
	pool.Stop()

	// After stop, context is cancelled so Submit should return false
	success = pool.Submit(workItem)
	if success {
		t.Error("Expected Submit to return false after pool is stopped")
	}
}

func TestGetFunctionName(t *testing.T) {
	testFunc := func(file structs.File, cfg config.Config) []structs.Message {
		return []structs.Message{}
	}

	name := getFunctionName(testFunc)

	// Function name should contain something meaningful
	if name == "" {
		t.Error("Function name should not be empty")
	}

	// Should work with actual check functions
	mockCheck := func(structs.File, config.Config) []structs.Message { return nil }
	name2 := getFunctionName(mockCheck)

	if name2 == "" {
		t.Error("Mock check function name should not be empty")
	}
}

func TestNewArchiveWorkerPool(t *testing.T) {
	pool := NewArchiveWorkerPool(2, 100) // 100MB limit

	if pool == nil {
		t.Fatal("NewArchiveWorkerPool returned nil")
	}

	if pool.WorkerPool == nil {
		t.Error("Base WorkerPool not initialized")
	}

	pool.Stop()
}

func TestNewArchiveWorkerPool_DefaultWorkers(t *testing.T) {
	pool := NewArchiveWorkerPool(0, 100)

	expectedWorkers := runtime.NumCPU() / 2
	if expectedWorkers < 1 {
		expectedWorkers = 1
	}

	if pool.numWorkers != expectedWorkers {
		t.Errorf("Expected %d workers, got %d", expectedWorkers, pool.numWorkers)
	}

	pool.Stop()
}
