//go:build windows

package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// These tests exercise the persistent-shell engine through the normal exec
// tool entry points (NewExecTool+Execute), which is also the integration
// test that the fallback wiring keeps both paths answering in one format.
// Every tool gets Close() through t.Cleanup: the live shell holds its
// working directory, and t.TempDir cleanup would fail on the locked dir.

func newShellTool(t *testing.T, dir string, timeout int) *ExecTool {
	t.Helper()
	tool := NewExecTool(timeout, dir)
	t.Cleanup(func() { _ = tool.Close() })
	return tool
}

func execCmd(t *testing.T, tool *ExecTool, command string, extra ...map[string]interface{}) string {
	t.Helper()
	params := map[string]interface{}{"command": command}
	for _, m := range extra {
		for k, v := range m {
			params[k] = v
		}
	}
	res, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("exec %q: %v", command, err)
	}
	return res.Text
}

func TestPersistentShellEnvPersists(t *testing.T) {
	tool := newShellTool(t, t.TempDir(), 30)
	if out := execCmd(t, tool, "set MCTEST=abc123"); !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("set failed: %s", out)
	}
	out := execCmd(t, tool, "echo %MCTEST%")
	if !strings.Contains(out, "abc123") {
		t.Fatalf("env var did not survive to the next call: %s", out)
	}
}

func TestPersistentShellCwdPersists(t *testing.T) {
	dir := t.TempDir()
	tool := newShellTool(t, dir, 30)
	if out := execCmd(t, tool, "mkdir sub"); !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("mkdir failed: %s", out)
	}
	out := execCmd(t, tool, "cd sub")
	if !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("cd failed: %s", out)
	}
	// the result itself should tell the model where it landed
	if !strings.Contains(out, filepath.Join(dir, "sub")) {
		t.Fatalf("cwd note missing from cd result: %s", out)
	}
	out = execCmd(t, tool, "echo %CD%")
	if !strings.Contains(out, "sub") {
		t.Fatalf("cwd did not persist: %s", out)
	}
}

func TestPersistentShellExitCode(t *testing.T) {
	tool := newShellTool(t, t.TempDir(), 30)
	out := execCmd(t, tool, `dir nope_missing_dir_zz 2>nul`)
	if !strings.Contains(out, "Exit code: 1") {
		t.Fatalf("expected exit code 1, got: %s", out)
	}
}

// `exit N` closes the shell itself: the engine must report the shell's own
// exit code, then transparently boot a fresh one.
func TestPersistentShellExitAndReborn(t *testing.T) {
	tool := newShellTool(t, t.TempDir(), 30)
	out := execCmd(t, tool, "exit 3")
	if !strings.Contains(out, "Exit code: 3") {
		t.Fatalf("expected the shell's exit code 3, got: %s", out)
	}
	out = execCmd(t, tool, "echo alive")
	if !strings.Contains(out, "alive") || !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("shell did not restart for the next call: %s", out)
	}
}

// A timed-out command must be killed with the shell, and the next call must
// find a working shell again.
func TestPersistentShellTimeoutRecovery(t *testing.T) {
	tool := newShellTool(t, t.TempDir(), 30)
	out := execCmd(t, tool, "ping -n 8 127.0.0.1 >nul", map[string]interface{}{"timeout": float64(1)})
	if !strings.Contains(out, "timed out") {
		t.Fatalf("expected a timeout report, got: %s", out)
	}
	out = execCmd(t, tool, "echo alive")
	if !strings.Contains(out, "alive") || !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("shell did not recover after timeout: %s", out)
	}
}

// `more` without an input param reads stdin: in the persistent shell stdin
// is the protocol channel, so the engine must starve it with NUL instead of
// letting it eat the sentinel lines (the one-shot path's NUL/devnull
// behavior, preserved).
func TestPersistentShellStdinStarved(t *testing.T) {
	tool := newShellTool(t, t.TempDir(), 15)
	out := execCmd(t, tool, "more")
	if strings.Contains(out, "timed out") {
		t.Fatalf("`more` ate the protocol stream instead of NUL: %s", out)
	}
	if !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("unexpected result: %s", out)
	}
}

// Output that ends without a trailing newline merges into the sentinel line;
// the parser must still find the code and keep the text.
func TestPersistentShellSentinelJoin(t *testing.T) {
	tool := newShellTool(t, t.TempDir(), 30)
	out := execCmd(t, tool, `set /p=nolinemarker<nul`)
	if !strings.Contains(out, "nolinemarker") {
		t.Fatalf("no-newline output lost: %s", out)
	}
	if strings.Contains(out, "__MCS") || strings.Contains(out, "__MCD") {
		t.Fatalf("protocol tokens leaked into the result: %s", out)
	}
}

func TestPersistentShellWorkingDirSwitch(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	tool := newShellTool(t, dirA, 30)
	out := execCmd(t, tool, "echo %CD%", map[string]interface{}{"working_dir": dirB})
	base := filepath.Base(dirB)
	if !strings.Contains(out, base) {
		t.Fatalf("working_dir switch did not take effect: %s", out)
	}
	// and it persists even when the next call passes no working_dir
	out = execCmd(t, tool, "echo %CD%")
	if !strings.Contains(out, base) {
		t.Fatalf("working_dir change did not persist: %s", out)
	}
}

// The batch-spelling for-loop must fall back to the one-shot path (where
// `%f` is the correct spelling) instead of wedging the persistent shell.
func TestPersistentShellForLoopFallsBack(t *testing.T) {
	dir := t.TempDir()
	tool := newShellTool(t, dir, 30)
	if out := execCmd(t, tool, "echo first"); !strings.Contains(out, "first") {
		t.Fatalf("warm-up failed: %s", out)
	}
	out := execCmd(t, tool, `for %f in (a b c) do @echo item_%f`)
	if !strings.Contains(out, "item_a item_b item_c") && !(strings.Contains(out, "item_a") && strings.Contains(out, "item_c")) {
		t.Fatalf("for-loop fallback did not run the command correctly: %s", out)
	}
	// the shell must still be healthy afterwards
	out = execCmd(t, tool, "echo after")
	if !strings.Contains(out, "after") || !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("shell unhealthy after for-loop fallback: %s", out)
	}
}

// The streaming contract (early chunks, [stderr] marking) must hold on the
// persistent engine too.
func TestPersistentShellStreamed(t *testing.T) {
	dir := t.TempDir()
	tool := newShellTool(t, dir, 30)
	var log chunkLog
	res, err := tool.ExecuteStreamed(context.Background(),
		map[string]interface{}{"command": "echo out_line & echo err_line 1>&2"}, log.add)
	if err != nil {
		t.Fatalf("ExecuteStreamed: %v", err)
	}
	feed := log.String()
	if !strings.Contains(feed, "out_line") || !strings.Contains(feed, "[stderr]") || !strings.Contains(feed, "err_line") {
		t.Fatalf("live feed lost stdout/stderr labelling: %q", feed)
	}
	if !strings.Contains(res.Text, "out_line") || !strings.Contains(res.Text, "err_line") {
		t.Fatalf("final result incomplete: %q", res.Text)
	}
}

func TestInsertNulStdin(t *testing.T) {
	cases := []struct{ in, want string }{
		{"python x.py", "python x.py < NUL"},
		{"more", "more < NUL"},                             // more.com is external: must be starved
		{"findstr a f.txt | more", "findstr a f.txt < NUL | more"},
		{"mkdir x && python y", "mkdir x && python y < NUL"}, // mkdir is internal: no fork-trick
		{"echo bad 1>&2", "echo bad 1>&2"},                 // redirection ampersand is not a clause join
		{"python x.py 2>&1", "python x.py 2>&1 < NUL"},
		{"more < file.txt", "more < file.txt"},   // already redirects stdin
		{"type a ^\nb", "type a ^\nb"},          // caret continuation: hands off
		{`echo "a & b"`, `echo "a & b"`},        // echo is internal
		{"set /p A=go", "set /p A=go < NUL"},    // the internal that DOES read stdin
		{"", ""},
		{"  ", "  "},
	}
	for _, c := range cases {
		if got := insertNulStdin(c.in); got != c.want {
			t.Errorf("insertNulStdin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
