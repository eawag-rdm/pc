//go:build !linux

package readers

import "os/exec"

// setPDFWorkerDeathSignal is a no-op: platforms other than Linux offer no
// parent-death signal, so they have no backstop for a parent killed outright -
// a worker ends on the EOF the pipes produce when its parent exits.
func setPDFWorkerDeathSignal(*exec.Cmd) {}
