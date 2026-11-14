//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// attachProcessTree is a no-op off Windows: the pipe-inheritance orphan
// problem that forces process-tree kills there does not apply the same way
// on Unix (children typically inherit the same terminal/pipe semantics and
// WaitDelay still bounds the wait).
func attachProcessTree(cmd *exec.Cmd) func() { return func() {} }

// applyRawCmdLine is a no-op off Windows: sh -c receives the command as a
// single argv entry and there is no quote-mangling layer to bypass.
func applyRawCmdLine(cmd *exec.Cmd, command string) {}

// applyPwshWindow is a no-op off Windows: there is no console window to hide.
func applyPwshWindow(cmd *exec.Cmd) {}

// exitSignalName names the signal that ended a process, for dsh's
// `[killed by signal: <name>]` marker.
func exitSignalName(err *exec.ExitError) string {
	if ws, ok := err.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ws.Signal().String()
	}
	return ""
}
