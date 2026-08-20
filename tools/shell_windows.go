//go:build windows

package tools

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// applyRawCmdLine makes the started cmd.exe receive exactly the command
// string the model wrote, bypassing Go's per-argument escaping.
//
// Why: exec.Command("cmd", "/C", command) lets Go escape each argument
// (inner double quotes become \", and the whole command gets wrapped in
// extra quotes when it contains spaces). cmd.exe then applies its own
// quote-stripping rules to that mangled line, so commands containing
// double quotes break in the field:
//   - `python -c "print('a b')"` reaches python as `print('a` -> SyntaxError
//   - `dir "C:\Program Files"` fails with "syntax is incorrect"
//   - `echo "hi"` prints `\"hi\"`
// Setting SysProcAttr.CmdLine passes the command line verbatim to
// CreateProcess (the first token is ignored because the program name is
// given separately), so cmd.exe parses exactly what the model wrote.
func applyRawCmdLine(cmd *exec.Cmd, command string) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	prog := cmd.Path
	if prog == "" {
		prog = "cmd"
	}
	cmd.SysProcAttr.CmdLine = prog + " /C " + command
}

// attachProcessTree places the started process (and every descendant it
// spawns) into a Windows job object configured with KILL_ON_JOB_CLOSE.
//
// Why: on cancellation, os/exec kills only the DIRECT child (cmd.exe). Its
// children (python, ping, ...) survive as orphans and keep the stdout/stderr
// pipes open, so cmd.Wait blocks until the orphan exits — a "stop" click
// during `pip install` would appear to do nothing for minutes. Closing the
// job handle kills the entire tree, which unblocks the wait immediately.
//
// The returned function must be called (exactly once) both when the command
// context is done and when the command finishes normally; it closes the job
// and therefore terminates any still-running tree members. Any failure to
// set up the job degrades gracefully to a no-op (WaitDelay in shell.go
// still bounds the wait).
func attachProcessTree(cmd *exec.Cmd) func() {
	var closeOnce sync.Once
	job := windows.Handle(0)

	if cmd.Process != nil {
		j, err := windows.CreateJobObject(nil, nil)
		if err == nil {
			var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
			info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
			if _, err := windows.SetInformationJobObject(j, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err == nil {
				if ph, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(cmd.Process.Pid)); err == nil {
					err = windows.AssignProcessToJobObject(j, ph)
					_ = windows.CloseHandle(ph)
				}
				if err == nil {
					job = j
				} else {
					_ = windows.CloseHandle(j)
				}
			} else {
				_ = windows.CloseHandle(j)
			}
		}
	}

	return func() {
		closeOnce.Do(func() {
			if job != 0 {
				_ = windows.CloseHandle(job)
			}
		})
	}
}
