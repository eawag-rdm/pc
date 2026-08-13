package cpucap

import (
	"runtime"
	"testing"
)

// TestApply pins the cap's two halves: it lowers the budget every pool then
// sizes from, and it is a CEILING - a cap above what the process may run is
// ignored rather than oversubscribing the machine.
func TestApply(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		// The assertions need a budget above 1 to tell a cap from a no-op; a
		// -cpu=1 run (or GOMAXPROCS=1 in the environment) would pass vacuously.
		t.Skip("needs a CPU budget above 1")
	}
	// Not parallel: GOMAXPROCS is process-wide.
	start := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(start)

	if got := Apply(1); got != 1 || runtime.GOMAXPROCS(0) != 1 {
		t.Errorf("Apply(1) settled on %d (GOMAXPROCS %d), want 1", got, runtime.GOMAXPROCS(0))
	}
	// Above the current budget: ignored, never raised.
	if got := Apply(1024); got != 1 || runtime.GOMAXPROCS(0) != 1 {
		t.Errorf("Apply(1024) raised the budget to %d (GOMAXPROCS %d); the cap is a ceiling only", got, runtime.GOMAXPROCS(0))
	}
	// Nothing to lower (the config defaults before this, so <= 0 is defensive
	// only): report the budget, leave it where it is - still pinned.
	if got := Apply(0); got != 1 || runtime.GOMAXPROCS(0) != 1 {
		t.Errorf("Apply(0) settled on %d (GOMAXPROCS %d), want the unchanged budget 1", got, runtime.GOMAXPROCS(0))
	}
}

// TestApplyPinsEqualBudget covers the case the ceiling check alone would skip:
// a cap EQUAL to the budget must still call GOMAXPROCS, because that call is
// what stops the runtime raising the budget past the cap later. Nothing about
// the settled budget can show this - the numbers are identical either way - so
// the call itself is the observable, recorded through apply's setter parameter.
func TestApplyPinsEqualBudget(t *testing.T) {
	start := runtime.GOMAXPROCS(0)
	orig := runtime.GOMAXPROCS
	pinned := []int{}
	set := func(n int) int {
		pinned = append(pinned, n)
		return orig(n)
	}
	defer runtime.GOMAXPROCS(start)

	if got := apply(set, start); got != start {
		t.Errorf("apply(set, %d) settled on %d, want the budget unchanged", start, got)
	}
	if len(pinned) != 1 || pinned[0] != start {
		t.Errorf("apply(set, %d) pinned %v, want exactly one call with %d: a cap that only lowers leaves the budget free to rise past maxCores later", start, pinned, start)
	}
}
