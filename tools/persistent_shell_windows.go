//go:build windows

package tools

import (
	"os"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func persistentSupported() bool { return true }

// startShellProcess spawns the long-lived cmd.exe.
//
// /Q starts with echo off so commands read from the redirected stdin never
// get echoed back into the protocol stream. A registry AutoRun still runs
// once here (dev-box conveniences like clinit keep working); the bootstrap
// exchange drains its banner. HideWindow keeps the desktop build from
// flashing a console for the (single) shell process, and attachProcessTree
// puts the shell plus every program it ever runs into one kill-on-close job,
// which both implements the timeout kill and guarantees no orphan outlives
// the mini_code process itself.
//
// Pipes are hand-rolled os.Pipe (not cmd.Stdin=strings.Reader style) so we
// keep both ends: the process gets an inherited dup, we keep our end for the
// whole session; the extra child-side copies on our handle are closed right
// after Start so stream EOF still means "shell exited".
func startShellProcess(dir string) (*exec.Cmd, *os.File, *os.File, *os.File, func(), error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, nil, nil, nil, nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, nil, nil, nil, nil, err
	}
	cmd := exec.Command("cmd.exe", "/Q")
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = errW
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return nil, nil, nil, nil, nil, err
	}
	// The child holds inherited copies; our copies must go, or the read ends
	// would never see EOF when the shell exits.
	inR.Close()
	outW.Close()
	errW.Close()
	release := attachProcessTree(cmd)
	return cmd, inW, outR, errR, release, nil
}

// Shell command input must be written in the console OEM code page: cmd.exe
// decodes redirected stdin with it, so UTF-8 bytes with non-ASCII text would
// arrive as mojibake. ASCII passes straight through; CP936 (the dominant
// Chinese-system OEM page) encodes through GBK; UTF-8 consoles pass through;
// anything else declines and the caller falls back to the one-shot path,
// which hands the command line over as UTF-16 and has no such issue.
var (
	shellEncOnce  sync.Once
	shellEncPass  bool // true: non-ASCII may be written as UTF-8
	shellEncGBK   *encoding.Encoder
	shellEncOEMCP uint32
	modKernel32Enc = windows.NewLazySystemDLL("kernel32.dll")
	procGetOEMCP   = modKernel32Enc.NewProc("GetOEMCP")
)

func getOEMCP() uint32 {
	r, _, _ := procGetOEMCP.Call()
	return uint32(r)
}

func encodeShellInput(s string) ([]byte, bool) {
	if isASCIIStr(s) {
		return []byte(s), true
	}
	shellEncOnce.Do(func() {
		shellEncOEMCP = getOEMCP()
		switch shellEncOEMCP {
		case 65001:
			shellEncPass = true
		case 936:
			shellEncGBK = simplifiedchinese.GBK.NewEncoder()
		}
	})
	if shellEncGBK != nil {
		if b, err := shellEncGBK.Bytes([]byte(s)); err == nil {
			return b, true
		}
		return nil, false // emoji and other GBK-foreign runes: one-shot path
	}
	if shellEncPass {
		return []byte(s), true
	}
	return nil, false
}
