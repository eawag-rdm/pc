// Package cpucap applies the operator's CPU ceiling to this process.
//
// It is a composition-root concern, not a library one: it mutates
// process-global state, exactly once, at startup, before anything sizes itself
// from the budget. The packages that CONSUME the budget only read it.
package cpucap

import "runtime"

// Apply pins this process's CPU budget at maxCores, once, at a composition
// root, and reports the budget it settled on.
//
// It is a CEILING, never a request: a cap above the current budget is ignored.
//
// The budget is PINNED even when the cap does not lower it, because setting
// GOMAXPROCS at all is what stops the Go 1.25 runtime raising the budget again
// from its cgroup quota tracking. The cost is that the budget no longer follows
// a quota LOWERED after start either; runtime.SetDefaultGOMAXPROCS is the way
// back to automatic tracking.
func Apply(maxCores int) int {
	return apply(runtime.GOMAXPROCS, maxCores)
}

// apply is Apply with the setter as a parameter, so a test can prove the pin
// happens when the cap does NOT lower the budget - the guarantee this package
// exists for, and one the runtime exposes no way to observe after the fact.
func apply(set func(int) int, maxCores int) int {
	budget := runtime.GOMAXPROCS(0)
	if maxCores > 0 && maxCores < budget {
		budget = maxCores
	}
	set(budget)
	return budget
}
