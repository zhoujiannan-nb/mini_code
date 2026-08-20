package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecWorkingDirNotExist: a non-existent working_dir must produce a clear
// error that names the cause, instead of the cryptic "failed to start
// command" that cmd.Start used to return.
func TestExecWorkingDirNotExist(t *testing.T) {
	tool := NewExecTool(60, t.TempDir())
	badDir := filepath.Join(t.TempDir(), "no-such-dir-xyz")
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":     "echo hello",
		"working_dir": badDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Text, "working_dir") || !strings.Contains(result.Text, "does not exist") {
		t.Errorf("expected a clear working_dir error, got: %s", result.Text)
	}
}

// TestExecWorkingDirIsFile: a working_dir that points at a file (not a
// directory) must also be rejected with a clear error.
func TestExecWorkingDirIsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "afile.txt")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewExecTool(60, dir)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":     "echo hello",
		"working_dir": f,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Text, "working_dir") {
		t.Errorf("expected a working_dir error, got: %s", result.Text)
	}
}

// TestExecWorkingDirValid: a valid working_dir must still run the command.
func TestExecWorkingDirValid(t *testing.T) {
	dir := t.TempDir()
	tool := NewExecTool(60, dir)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":     "echo hello",
		"working_dir": dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.Text, "working_dir") || strings.Contains(result.Text, "Error") {
		t.Errorf("expected a clean run, got: %s", result.Text)
	}
}

// TestExecTooLongCommand: a command over the shell command-line limit must be
// rejected with a clear error pointing at the script-file workaround, instead
// of failing with a cryptic "filename or extension is too long".
func TestExecTooLongCommand(t *testing.T) {
	tool := NewExecTool(60, t.TempDir())
	longCmd := "echo " + strings.Repeat("a", 9000)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"command": longCmd})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Text, "too long") || !strings.Contains(result.Text, "script") {
		t.Errorf("expected a 'too long' error mentioning a script file, got: %s", result.Text)
	}
}

// TestExecNormalCommandUnaffected: a normal-length command must run as before.
func TestExecNormalCommandUnaffected(t *testing.T) {
	tool := NewExecTool(60, t.TempDir())
	result, err := tool.Execute(context.Background(), map[string]interface{}{"command": "echo ok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.Text, "too long") {
		t.Errorf("expected a clean run, got: %s", result.Text)
	}
}
