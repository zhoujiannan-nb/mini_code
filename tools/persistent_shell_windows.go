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
	inR.Close()
	outW.Close()
	errW.Close()
	release := attachProcessTree(cmd)
	return cmd, inW, outR, errR, release, nil
}

var (
	shellEncOnce  sync.Once
	shellEncPass  bool
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
		return nil, false
	}
	if shellEncPass {
		return []byte(s), true
	}
	return nil, false
}
