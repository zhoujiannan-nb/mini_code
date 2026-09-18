//go:build windows

package tools

import (
	"os/exec"
	"testing"
)

// TestApplyRawCmdLineHidesWindow guards the CREATE_NO_WINDOW flag.
//
// The desktop build (-H windowsgui) has no console of its own, so without
// HideWindow Windows allocates a fresh VISIBLE console for every cmd.exe
// the exec tool starts — a black popup window for the command's whole
// lifetime (most noticeable on long commands like web searches). Output is
// captured through pipes, so hiding the window loses nothing.
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
