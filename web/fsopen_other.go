//go:build !windows

package web

import (
	"os/exec"
	"runtime"
)

// openLocalPath hands a path to the OS default handler: `open` on macOS,
// `xdg-open` elsewhere. exec.Command (not a shell) is used on purpose — the
// argument is passed as one argv entry, so a file name containing spaces, &
// or quotes stays a file name and is never re-parsed as a command.
func openLocalPath(p string) error {
	bin := "xdg-open"
	if runtime.GOOS == "darwin" {
		bin = "open"
	}
	return exec.Command(bin, p).Start()
}
