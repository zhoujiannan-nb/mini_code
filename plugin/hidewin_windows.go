//go:build windows

package plugin

import (
	"os/exec"
	"syscall"
)

// hideWindow stops a console window from flashing on screen for every plugin
// tool call. The desktop build has no console of its own (-H windowsgui), so
// Windows would allocate a fresh visible one for each child process.
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
}
