//go:build windows

package tools

import (
	"os/exec"
	"testing"
)

func TestApplyRawCmdLineHidesWindow(t *testing.T) {
	cmd := exec.Command("cmd", "/C", `dir "C:\Program Files"`)
	applyRawCmdLine(cmd, `dir "C:\Program Files"`)
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("HideWindow not set: exec'd commands would pop up a visible console window")
	}
	want := cmd.Path + ` /C dir "C:\Program Files"`
	if cmd.SysProcAttr.CmdLine != want {
		t.Errorf("CmdLine = %q, want %q", cmd.SysProcAttr.CmdLine, want)
	}
}
