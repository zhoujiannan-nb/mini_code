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
// Go's per-argument escaping plus cmd.exe's quote-stripping rules used to
// break every command containing double quotes:
//   - `echo "hello world"` printed `\"hello world\"`
//   - `type "path with space.txt"` failed with "syntax is incorrect"
//   - `python -c "print(1+1)"` printed nothing (python saw a quoted string)
// The command line must now reach cmd.exe verbatim (applyRawCmdLine).
func TestExecPreservesQuotes(t *testing.T) {
	dir := t.TempDir()
	tool := NewExecTool(30, dir)

	// 1. echo with a quoted string: cmd's echo keeps the quotes, so the
	//    output must contain exactly "hello world" (with quotes) and no
	//    stray backslashes.
	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": `echo "hello world"`})
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
		"command": `type "` + spaced + `" && echo END`,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Text, "spaced-ok") || !strings.Contains(res.Text, "END") {
		t.Errorf("type with quoted spaced path: output %q, want it to contain %q and %q", res.Text, "spaced-ok", "END")
	}
	if !strings.Contains(res.Text, "Exit code: 0") {
		t.Errorf("type with quoted spaced path: expected exit code 0: %q", res.Text)
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
