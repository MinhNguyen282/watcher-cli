//go:build !windows

package shell

import (
	"errors"
	"os/exec"
	"syscall"
)

// SignalName returns the signal that terminated a command, if one did.
// The name is derived from the process state, never from error text.
func SignalName(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return ""
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}
