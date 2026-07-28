package optimization

import (
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// TestWorkerPool_PanickingCheck_DoesNotKillProcess asserts the defence-in-depth
// contract behind the boot-time checks-config validation: even if a check
// panics inside a worker-pool goroutine (where no request-level recover can
// reach), the panic is converted into a logged failure — the pool keeps
// working, the other checks' messages survive, and the process stays alive.
// Without SafeRunCheck this test would crash the whole test binary.
func TestWorkerPool_PanickingCheck_DoesNotKillProcess(t *testing.T) {
	output.GlobalLogger.SetJSONMode(true)
	output.GlobalLogger.ClearMessages()

	panicking := func(file structs.File, cfg config.Config) []structs.Message {
		panic("boom: simulated check bug")
	}
	healthy := func(file structs.File, cfg config.Config) []structs.Message {
		return []structs.Message{{Content: "healthy ran", Source: file}}
	}

	pool := NewWorkerPool(2)
	pool.Start()
	defer pool.Stop()

	file := structs.File{Name: "a.txt", Path: "/tmp/a.txt"}
	if !pool.Submit(WorkItem{File: file, Checks: []func(structs.File, config.Config) []structs.Message{panicking, healthy}}) {
		t.Fatal("submit failed")
	}

	result := <-pool.Results()
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
