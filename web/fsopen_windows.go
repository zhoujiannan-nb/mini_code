//go:build windows

package web

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// openLocalPath hands a path to the OS default handler without going through a
// shell. ShellExecuteW is what Explorer itself uses: it opens a file with its
// default application, a directory in Explorer, and an .html file in the
// browser. No cmd.exe is involved, so the path is never re-parsed as a command
// (a file name containing & or quotes would otherwise be a live shell
// injection, and the model writes whatever it likes into a chat message).
func openLocalPath(p string) error {
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	// ShellExecuteW reports success as any value > 32 (an HINSTANCE-style
	// code). The x/sys wrapper turns the low values into an error already.
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
