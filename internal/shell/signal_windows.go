//go:build windows

package shell

// SignalName returns the signal that terminated a command. Windows has no
// POSIX signals, so a terminated process is reported by its exit code.
func SignalName(err error) string {
	return ""
}
