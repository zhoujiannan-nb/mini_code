//go:build !windows

package tools

import "os/exec"

// attachProcessTree is a no-op off Windows: the pipe-inheritance orphan
// problem that forces process-tree kills there does not apply the same way
// on Unix (children typically inherit the same terminal/pipe semantics and
// WaitDelay still bounds the wait).
func attachProcessTree(cmd *exec.Cmd) func() { return func() {} }
