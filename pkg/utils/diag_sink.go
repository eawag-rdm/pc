package utils

import (
	"fmt"
	"sync"
	"time"

	"github.com/eawag-rdm/pc/pkg/structs"
)

// diagSink collects the run-scoped diagnostics of ONE scan. It is created by
// the entry points (ApplyAllChecks / ApplyAllChecksWithProgress), threaded to
// every frame that can emit, and drained once the scan is over - it is never
// package-level, which is the whole point: what the engine reports about a run
// travels back to the caller by value instead of through a process global that
// only one frontend drains.
//
// A sink is never nil, and newWorkerPool panics on one: a nil sink is a CRASH,
// not a silent loss - logPanic runs inside a deferred recover, where a nil
// dereference is a second panic while unwinding that no recover catches.
//
// The lock is taken only when a diagnostic is actually emitted - a recovered
// panic or an unreadable archive file list - so a clean scan never touches it.
type diagSink struct {
	mu    sync.Mutex
	items []structs.Diagnostic
}

// add records one diagnostic. subject is a display name (never a path), empty
// for run-wide diagnostics; see structs.Diagnostic for the audience protocol.
func (d *diagSink) add(level structs.DiagLevel, subject, format string, args ...interface{}) {
	item := structs.Diagnostic{
		Level:     level,
		Message:   fmt.Sprintf(format, args...),
		Timestamp: time.Now().Format(time.RFC3339),
		Subject:   subject,
	}
	d.mu.Lock()
	d.items = append(d.items, item)
	d.mu.Unlock()
}

// drain returns the collected diagnostics and hands ownership of the slice to
// the caller: items is nilled under the lock, so a late add cannot append into
// the backing array the caller now holds.
func (d *diagSink) drain() []structs.Diagnostic {
	d.mu.Lock()
	defer d.mu.Unlock()
	items := d.items
	d.items = nil
	return items
}
