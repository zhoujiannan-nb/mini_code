//go:build !windows

package plugin

import "os/exec"

// hideWindow is a no-op outside Windows: only Windows allocates a visible
// console window for a child process of a GUI application.
func hideWindow(cmd *exec.Cmd) {}
