//go:build !windows

package tools

import (
	"fmt"
	"os"
	"os/exec"
)

func persistentSupported() bool { return false }

func startShellProcess(dir string) (*exec.Cmd, *os.File, *os.File, *os.File, func(), error) {
	return nil, nil, nil, nil, nil, fmt.Errorf("persistent shell is not supported on this platform")
}

func encodeShellInput(s string) ([]byte, bool) { return nil, false }
