package utils

import (
	"sync"
	"testing"
)

func TestNewQueue(t *testing.T) {
	queue := NewQueue()

	if queue == nil {
		t.Fatal("NewQueue returned nil")
	}

	if queue.items == nil {
		t.Error("Queue items slice not initialized")
	}

	if !queue.IsEmpty() {
		t.Error("New queue should be empty")
	}

	if queue.Size() != 0 {
		t.Errorf("New queue size should be 0, got %d", queue.Size())
	}
}

func TestQueue_Enqueue(t *testing.T) {
	queue := NewQueue()

	// Test enqueuing a single item
	queue.Enqueue("test1")

	if queue.IsEmpty() {
		t.Error("Queue should not be empty after enqueue")
	}

	if queue.Size() != 1 {
		t.Errorf("Expected size 1, got %d", queue.Size())
	}

	// Test enqueuing multiple items
	queue.Enqueue("test2")
	queue.Enqueue(123)
	queue.Enqueue(true)

	if queue.Size() != 4 {
		t.Errorf("Expected size 4, got %d", queue.Size())
	}
}

func TestQueue_Dequeue(t *testing.T) {
	queue := NewQueue()

	// Test dequeue from empty queue
	item, ok := queue.Dequeue()
	if ok {
		t.Error("Dequeue from empty queue should return false")
	}
	if item != nil {
		t.Error("Dequeue from empty queue should return nil")
	}

	// Add items and test FIFO behavior
	queue.Enqueue("first")
	queue.Enqueue("second")
	queue.Enqueue("third")

	// Dequeue first item
	item, ok = queue.Dequeue()
	if !ok {
		t.Error("Dequeue should return true for non-empty queue")
	}
	if item != "first" {
		t.Errorf("Expected 'first', got %v", item)
	}
	if queue.Size() != 2 {
		t.Errorf("Expected size 2 after dequeue, got %d", queue.Size())
	}

	// Dequeue second item
	item, ok = queue.Dequeue()
	if item != "second" {
		t.Errorf("Expected 'second', got %v", item)
	}

	// Dequeue third item
	item, ok = queue.Dequeue()
	if item != "third" {
		t.Errorf("Expected 'third', got %v", item)
	}

	// Queue should now be empty
	if !queue.IsEmpty() {
		t.Error("Queue should be empty after dequeuing all items")
	}
}

func TestQueue_IsEmpty(t *testing.T) {
	queue := NewQueue()

	// Test empty queue
	if !queue.IsEmpty() {
		t.Error("New queue should be empty")
	}

	// Test non-empty queue
	queue.Enqueue("test")
	if queue.IsEmpty() {
		t.Error("Queue with items should not be empty")
	}

	// Test empty after dequeue
	queue.Dequeue()
	if !queue.IsEmpty() {
		t.Error("Queue should be empty after dequeuing last item")
	}
}

func TestQueue_Size(t *testing.T) {
	queue := NewQueue()

	// Test initial size
	if queue.Size() != 0 {
		t.Errorf("Initial size should be 0, got %d", queue.Size())
	}

	// Test size after enqueues
	queue.Enqueue("item1")
	if queue.Size() != 1 {
		t.Errorf("Size should be 1, got %d", queue.Size())
	}

	queue.Enqueue("item2")
	queue.Enqueue("item3")
	if queue.Size() != 3 {
		t.Errorf("Size should be 3, got %d", queue.Size())
	}

	// Test size after dequeue
	queue.Dequeue()
	if queue.Size() != 2 {
		t.Errorf("Size should be 2 after dequeue, got %d", queue.Size())
	}
}

func TestQueue_MixedOperations(t *testing.T) {
	queue := NewQueue()

	// Mixed enqueue and dequeue operations
	queue.Enqueue(1)
	queue.Enqueue(2)

	item, _ := queue.Dequeue()
	if item != 1 {
		t.Errorf("Expected 1, got %v", item)
	}

	queue.Enqueue(3)
	queue.Enqueue(4)

	if queue.Size() != 3 {
		t.Errorf("Expected size 3, got %d", queue.Size())
	}

	// Dequeue remaining items in order
	expectedOrder := []interface{}{2, 3, 4}
	for i, expected := range expectedOrder {
		item, ok := queue.Dequeue()
		if !ok {
			t.Fatalf("Dequeue %d failed", i)
		}
		if item != expected {
			t.Errorf("Item %d: expected %v, got %v", i, expected, item)
		}
	}
}

func TestQueue_DifferentTypes(t *testing.T) {
	queue := NewQueue()

	// Test with different data types
	queue.Enqueue("string")
	queue.Enqueue(42)
	queue.Enqueue(3.14)
	queue.Enqueue(true)
	queue.Enqueue([]int{1, 2, 3})

	// Verify order and types
	expectedItems := []interface{}{"string", 42, 3.14, true, []int{1, 2, 3}}

	for i, expected := range expectedItems {
		item, ok := queue.Dequeue()
		if !ok {
			t.Fatalf("Dequeue %d failed", i)
		}

		// For slice comparison, we need special handling
		if i == 4 {
			if slice, ok := item.([]int); ok {
				expectedSlice := expected.([]int)
				if len(slice) != len(expectedSlice) {
					t.Errorf("Slice length mismatch: expected %d, got %d", len(expectedSlice), len(slice))
				}
				for j, v := range slice {
					if v != expectedSlice[j] {
						t.Errorf("Slice element %d: expected %d, got %d", j, expectedSlice[j], v)
					}
				}
			} else {
				t.Error("Expected slice type")
			}
		} else {
			if item != expected {
				t.Errorf("Item %d: expected %v, got %v", i, expected, item)
			}
		}
	}
}

func TestQueue_ThreadSafety(t *testing.T) {
	queue := NewQueue()
	numGoroutines := 10
	itemsPerGoroutine := 100

	var wg sync.WaitGroup

	// Concurrent enqueues
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < itemsPerGoroutine; j++ {
				queue.Enqueue(id*itemsPerGoroutine + j)
			}
		}(i)
	}

	wg.Wait()

	// Verify all items were enqueued
	expectedSize := numGoroutines * itemsPerGoroutine
	if queue.Size() != expectedSize {
		t.Errorf("Expected size %d, got %d", expectedSize, queue.Size())
	}

	// Concurrent dequeues
	items := make([]interface{}, expectedSize)
	itemIndex := 0
	var indexMutex sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < itemsPerGoroutine; j++ {
				item, ok := queue.Dequeue()
				if ok {
					indexMutex.Lock()
					items[itemIndex] = item
					itemIndex++
					indexMutex.Unlock()
				}
			}
		}()
	}

	wg.Wait()

	// Verify all items were dequeued
	if queue.Size() != 0 {
		t.Errorf("Expected empty queue, got size %d", queue.Size())
	}

	if !queue.IsEmpty() {
		t.Error("Queue should be empty after concurrent dequeues")
	}

	// Verify we got all items (though order may vary due to concurrency)
	if itemIndex != expectedSize {
		t.Errorf("Expected %d items dequeued, got %d", expectedSize, itemIndex)
	}
}

func TestQueue_ConcurrentEnqueueDequeue(t *testing.T) {
	queue := NewQueue()
	numOperations := 1000

	var wg sync.WaitGroup

	// Producer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numOperations; i++ {
			queue.Enqueue(i)
		}
	}()

	// Consumer goroutine
	wg.Add(1)
	dequeued := 0
	go func() {
		defer wg.Done()
		for dequeued < numOperations {
			if _, ok := queue.Dequeue(); ok {
				dequeued++
			}
		}
	}()

	wg.Wait()

	// Final state should be empty
	if !queue.IsEmpty() {
		t.Error("Queue should be empty after equal enqueues and dequeues")
	}
}

func TestQueue_NilItems(t *testing.T) {
	queue := NewQueue()

	// Test enqueuing nil
	queue.Enqueue(nil)

	if queue.IsEmpty() {
		t.Error("Queue should not be empty after enqueuing nil")
	}

	if queue.Size() != 1 {
		t.Errorf("Expected size 1, got %d", queue.Size())
	}

	// Test dequeuing nil
	item, ok := queue.Dequeue()
	if !ok {
		t.Error("Should be able to dequeue nil")
	}

	if item != nil {
		t.Errorf("Expected nil, got %v", item)
	}
}