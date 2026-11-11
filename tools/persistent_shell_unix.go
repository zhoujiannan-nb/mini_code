//go:build !windows

package tools

import (
	"fmt"
	"os"
	"os/exec"
)

// Persistent shell is a v1 Windows-only feature (the whole cmd.exe protocol
// and the A/B evidence behind it are Windows-shaped). On other platforms the
// exec tool keeps using the one-shot sh -c path unchanged.
func persistentSupported() bool { return false }

func startShellProcess(dir string) (*exec.Cmd, *os.File, *os.File, *os.File, func(), error) {
	return nil, nil, nil, nil, nil, fmt.Errorf("persistent shell is not supported on this platform")
}

func encodeShellInput(s string) ([]byte, bool) { return nil, false }
