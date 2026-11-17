//go:build !windows

package plugin

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
