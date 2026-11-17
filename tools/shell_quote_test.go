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

func TestExecPreservesQuotes(t *testing.T) {
	t.Setenv("MC_EXEC_GATE", "0")
	dir := t.TempDir()
	tool := NewExecTool(30, dir)
	t.Cleanup(func() { _ = tool.Close() })

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
