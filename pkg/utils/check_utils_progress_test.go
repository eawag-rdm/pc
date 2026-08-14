package utils

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// progressFiles builds n files in memory. Nothing is written to disk: the mock
// check never opens them and dispatch selects on the name alone.
func progressFiles(n int) []structs.File {
	files := make([]structs.File, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file%04d.txt", i)
		files = append(files, structs.File{Name: name, Path: filepath.Join("testdata-none", name)})
	}
	return files
}

// TestRunChecksPool_ProgressTickRegimes drives the collect loop's progress gates
// across the regimes of stride = max(1, len/100): 0 (nothing to tick), 1 and 2
// (below one window), 99/100/199 (the len/100 boundary, stride still 1), 200
// (stride 2), 1000 (stride 10, final item ON a stride boundary) and 1005 (final
// item OFF it, so the count gate alone can never report the total). Every
// existing test runs a 5-file fixture, where stride is always 1 and none of this
// arithmetic is exercised.
//
// Only what the 50 ms wall clock cannot move is asserted - ticks increase, never
// exceed the item count, the LAST one equals it exactly (what the progress bar's
// "current == total" rests on), and the tick count stays inside the count gate's
// bound. An exact tick count or any timing would depend on how fast the machine
// runs the pass, i.e. would flake.
func TestRunChecksPool_ProgressTickRegimes(t *testing.T) {
	for _, items := range []int{0, 1, 2, 99, 100, 199, 200, 1000, 1005} {
		t.Run(fmt.Sprintf("items=%d", items), func(t *testing.T) {
			files := progressFiles(items)
			var ran atomic.Int32
			entry := mockEntry("progress", func(structs.File) []structs.Message {
				ran.Add(1)
				return nil
			})
			workItems := filterChecksForFiles([]checkRules{entry}, checks.ScopeFile, files, nil)
			if len(workItems) != items {
				t.Fatalf("work list holds %d items, want %d", len(workItems), items)
			}
			workers := max(2, runtime.GOMAXPROCS(0))

			// The collect loop ticks on THIS goroutine, so a bare slice is safe.
			var ticks []int
			runChecksPool(context.Background(), &diagSink{}, workItems, workers, func(current int) {
				ticks = append(ticks, current)
			})

			if items == 0 {
				if len(ticks) != 0 {
					t.Fatalf("empty work list ticked %d times: %v", len(ticks), ticks)
				}
				return
			}

			previous := 0
			for i, tick := range ticks {
				if tick <= previous {
					t.Fatalf("tick %d did not advance: %d after %d", i, tick, previous)
				}
				if tick > items {
					t.Fatalf("tick %d overran: %d > %d items", i, tick, items)
				}
				previous = tick
			}
			if len(ticks) == 0 || ticks[len(ticks)-1] != items {
				t.Fatalf("last tick must be exactly %d, got %v", items, ticks)
			}
			// Count gate: one admission per stride, plus the final item's.
			stride := max(1, items/100)
			if bound := items/stride + 1; len(ticks) > bound {
				t.Fatalf("%d ticks for %d items exceed the count gate's bound %d", len(ticks), items, bound)
			}
			if got := int(ran.Load()); got != items {
				t.Fatalf("check ran %d times, want %d", got, items)
			}

			// Same pass with a nil tick: the whole work list still runs.
			ran.Store(0)
			runChecksPool(context.Background(), &diagSink{}, workItems, workers, nil)
			if got := int(ran.Load()); got != items {
				t.Fatalf("nil-tick pass ran the check %d times, want %d", got, items)
			}
		})
	}
}

// TestApplyAllChecksWithProgress_PanickingTick_KeepsScanning pins the tick's
// recover guard. The tick runs caller code inside the pool's collect loop, so an
// escaping panic skips the submit/collect handshake and runs the deferred
// pool.stop() while the submitter may still be parked on a full work channel -
// a send on a closed channel, in a goroutine no recover reaches. With the guard
// the call returns the same messages a well-behaved callback gets.
func TestApplyAllChecksWithProgress_PanickingTick_KeepsScanning(t *testing.T) {
	cfg, err := config.LoadConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatalf("parse test config: %v", err)
	}
	files := buildParityFixture(t) // >= 2 files: the file phase takes the pool
	plan := compilePlan(t, *cfg)

	resetGlobalScanState()
	want, _ := ApplyAllChecksWithProgress(context.Background(), *cfg, plan, files, func(structs.Progress) {})

	// Only the tick is guarded - the phase announcements run unguarded on this
	// goroutine - so panic on the tick alone: the file phase's reports with work
	// behind them, not the one that opens it. It ticks on this goroutine too, so
	// the counter needs no synchronisation.
	panics := 0
	resetGlobalScanState()
	got, _ := ApplyAllChecksWithProgress(context.Background(), *cfg, plan, files, func(p structs.Progress) {
		if p.Phase == structs.PhaseFileChecks && !p.Start {
			panics++
			panic("boom: simulated progress callback bug")
		}
	})
	resetGlobalScanState()

	if panics == 0 {
		t.Fatal("the tick never fired - the guard went untested (did the tick's phase change?)")
	}
	if wantSet, gotSet := messageMultiset(want), messageMultiset(got); !maps.Equal(wantSet, gotSet) {
		t.Fatalf("the panicking tick changed the result set:\nwant %v\ngot  %v", wantSet, gotSet)
	}
}

// TestApplyAllChecksWithProgress_ReportsPhasesAndFileCounts pins what the engine
// REPORTS, now that the frontend words it: the phase every report comes from,
// the Start flag the frontend tells an opening report from a count by, and - the
// one datum a frontend cannot reconstruct - the file phase's counts, which open
// at nothing done, advance, and end on the size of the work list that was
// dispatched. The mock check makes the whole run's total that same size, so the
// counters can be asserted exactly.
func TestApplyAllChecksWithProgress_ReportsPhasesAndFileCounts(t *testing.T) {
	files := progressFiles(6)
	plan := &Plan{}
	plan.scopes[checks.ScopeFile] = []checkRules{mockEntry("counted", func(structs.File) []structs.Message { return nil })}

	// Reports arrive on the collect loop's goroutine, which is this one, so an
	// unguarded slice is safe here.
	var reports []structs.Progress
	ApplyAllChecksWithProgress(context.Background(), config.Config{}, plan, files, func(p structs.Progress) {
		reports = append(reports, p)
	})

	opened := 0
	var ticks []int
	var tail []structs.ProgressPhase
	for _, p := range reports {
		if p.Phase != structs.PhaseFileChecks {
			tail = append(tail, p.Phase)
			continue
		}
		if p.Total != len(files) {
			t.Fatalf("a file phase report announced a total of %d, want the %d dispatched items", p.Total, len(files))
		}
		if p.Start {
			opened++
			if p.Current != 0 {
				t.Fatalf("the file phase opened with %d items already done", p.Current)
			}
			continue
		}
		ticks = append(ticks, p.Current)
	}

	if opened != 1 {
		t.Fatalf("the file phase must announce itself exactly once, got %d reports with nothing done", opened)
	}
	if len(ticks) == 0 {
		t.Fatal("the file phase never reported a count - a frontend cannot reconstruct one")
	}
	previous := 0
	for i, tick := range ticks {
		if tick <= previous {
			t.Fatalf("count %d did not advance: %d after %d", i, tick, previous)
		}
		previous = tick
	}
	if last := ticks[len(ticks)-1]; last != len(files) {
		t.Fatalf("the file phase ended at %d of %d dispatched items", last, len(files))
	}

	// The phases behind the file phase report once each, in dispatch order.
	want := []structs.ProgressPhase{structs.PhaseArchiveFileList, structs.PhaseArchiveContent, structs.PhaseRepository, structs.PhaseFinalizing}
	if !slices.Equal(tail, want) {
		t.Fatalf("phases reported after the file phase: got %v, want %v", tail, want)
	}
}
