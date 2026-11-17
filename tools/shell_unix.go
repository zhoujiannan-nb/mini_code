//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

func attachProcessTree(cmd *exec.Cmd) func() { return func() {} }

func applyRawCmdLine(cmd *exec.Cmd, command string) {}

func applyPwshWindow(cmd *exec.Cmd) {}

func exitSignalName(err *exec.ExitError) string {
	if ws, ok := err.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ws.Signal().String()
	}
	return ""
}
