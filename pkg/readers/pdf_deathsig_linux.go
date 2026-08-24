//go:build linux

package readers

import (
	"os/exec"
	"syscall"
)

// setPDFWorkerDeathSignal asks the kernel to SIGKILL a worker when the thread
// that spawned it exits (golang/go#27505) - nothing here locks an OS thread, so
// that normally coincides with the death of the process. It is the Linux-only
// backstop for a parent killed outright (SIGKILL, an OOM kill): a worker wedged
// inside wasm is not reading its pipe and would never learn it is alone. On
// every orderly path the reaper is the EOF the closed pipes produce.
func setPDFWorkerDeathSignal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
