package utils

import (
	"sync"
)

// Queue represents a thread-safe queue
type Queue struct {
	items []interface{}
	lock  sync.Mutex
}

// NewQueue creates a new Queue
func NewQueue() *Queue {
	return &Queue{
		items: []interface{}{},
	}
}

// Enqueue adds an item to the queue
func (q *Queue) Enqueue(item interface{}) {
	q.lock.Lock()
	defer q.lock.Unlock()
	q.items = append(q.items, item)
}

// Dequeue removes and returns the first item from the queue
func (q *Queue) Dequeue() (interface{}, bool) {
	q.lock.Lock()
	defer q.lock.Unlock()
	if len(q.items) == 0 {
		return nil, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	return item, true
}

// IsEmpty checks if the queue is empty
func (q *Queue) IsEmpty() bool {
	q.lock.Lock()
	defer q.lock.Unlock()
	return len(q.items) == 0
}

// Size returns the number of items in the queue
func (q *Queue) Size() int {
	q.lock.Lock()
	defer q.lock.Unlock()
	return len(q.items)
}
