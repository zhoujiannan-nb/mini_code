//go:build !windows

package web

import (
	"os/exec"
	"runtime"
)

func openLocalPath(p string) error {
	bin := "xdg-open"
	if runtime.GOOS == "darwin" {
		bin = "open"
	}
	return exec.Command(bin, p).Start()
}
