//go:build windows

package tools

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func applyRawCmdLine(cmd *exec.Cmd, command string) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	prog := cmd.Path
	if prog == "" {
		prog = "cmd"
	}
	cmd.SysProcAttr.CmdLine = prog + " /C " + command
	cmd.SysProcAttr.HideWindow = true
}

func applyPwshWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
}

func exitSignalName(_ *exec.ExitError) string { return "" }

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
					if err := windows.AssignProcessToJobObject(j, ph); err == nil {
						job = j
					}
					_ = windows.CloseHandle(ph)
				}
			}
			if job == 0 {
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
