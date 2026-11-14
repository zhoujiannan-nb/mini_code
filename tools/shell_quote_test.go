//go:build windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecPreservesQuotes is the regression test for the quote-mangling bug:
// Go's per-argument escaping plus the shell's quote-stripping rules used to
// break every command containing double quotes. The commands are written in
// the PowerShell dialect the pwsh tool now speaks: a quoted string must reach
// the child program with its quotes honoured, not backslash-escaped, and a
// quoted path containing a space must open the file.
func TestExecPreservesQuotes(t *testing.T) {
	t.Setenv("MC_EXEC_GATE", "0") // Get-Content here is the fixture, not model behaviour
	dir := t.TempDir()
	tool := NewExecTool(30, dir)
	t.Cleanup(func() { _ = tool.Close() })

	// 1. echo a string that contains literal double quotes: the output must
	//    contain exactly "hello world" (with quotes) and no stray backslashes.
	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": `echo '"hello world"'`})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Text, `"hello world"`) {
		t.Errorf("echo with quotes: output %q, want it to contain %q", res.Text, `"hello world"`)
	}
	if strings.Contains(res.Text, `\"`) {
		t.Errorf("echo with quotes: output contains escaped quotes: %q", res.Text)
	}

	// 2. A quoted path containing a space must be usable by a program:
	//    create a file whose name has a space and read it back through a
	//    quoted path. The old behavior failed with "syntax is incorrect".
	spaced := filepath.Join(dir, "my file.txt")
	if err := os.WriteFile(spaced, []byte("spaced-ok"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err = tool.Execute(context.Background(), map[string]interface{}{
		"command": `Get-Content "` + spaced + `"; echo END`,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Text, "spaced-ok") || !strings.Contains(res.Text, "END") {
		t.Errorf("quoted spaced path: output %q, want it to contain %q and %q", res.Text, "spaced-ok", "END")
	}
	if strings.Contains(res.Text, "[exit code:") {
		t.Errorf("quoted spaced path: expected a clean exit (no marker): %q", res.Text)
	}

	// 3. python -c with a quoted program text: python must receive the code
	//    without surrounding literal quotes and actually execute it.
	if _, lerr := exec.LookPath("python"); lerr != nil {
		t.Skip("python not on PATH")
	}
	res, err = tool.Execute(context.Background(), map[string]interface{}{"command": `python -c "print(1+1)"`})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Text, "2") {
		t.Errorf("python -c with quotes: output %q, want it to contain %q", res.Text, "2")
	}
}
