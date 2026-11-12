//go:build windows

package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/user/mini_code/job"
)

// The background-command machinery, tested through the exec tool the same way
// the persistent shell is tested: no fake process, no fake clock, real cmd.exe.
// These are the behaviors that make promotion worth shipping — a command that
// outlives its wait keeps running, its output is still collectable afterwards,
// the settlement is classified as awaited when someone was waiting for it, and
// session shell state (cwd + environment) survives having to use a second
// cmd.exe.

func startAsync(t *testing.T, tool *ExecTool, params map[string]interface{}) *AsyncCall {
	t.Helper()
	call, err := tool.StartAsync(context.Background(), "test-session", params, nil)
	if err != nil {
		t.Fatalf("StartAsync(%v): %v", params, err)
	}
	if call == nil || call.Job == nil {
		t.Fatalf("StartAsync returned no job")
	}
	return call
}

func TestExecSettledReadsLikeSync(t *testing.T) {
	tool := newShellTool(t, t.TempDir(), 30)
	call := startAsync(t, tool, map[string]interface{}{"command": "echo hello-async"})
	if !call.Settled {
		t.Fatalf("a fast command should settle in the foreground, got note %q", call.Note)
	}
	out := call.Job.Output(false)
	if !strings.Contains(out, "hello-async") || !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("settled output is not the normal exec text: %q", out)
	}
	if !call.Job.Awaited() {
		t.Fatal("a job collected by its own call must be marked awaited (or it would also announce itself)")
	}
	// A Go caller writes an int where JSON would give a float64; a timeout that
	// quietly fails to parse would silently override the caller's patience.
	if spec, _, err := tool.execSpec(map[string]interface{}{"command": "x", "timeout": 7}); err != nil || spec == nil || spec.timeout != 7 {
		t.Fatalf("an int timeout must be honored: %+v (err %v)", spec, err)
	}
}

func TestExecPromotedIsCollectable(t *testing.T) {
	tool := newShellTool(t, t.TempDir(), 30)
	call := startAsync(t, tool, map[string]interface{}{
		"command": "ping -n 6 127.0.0.1 >nul",
		"timeout": 1,
	})
	if call.Settled {
		t.Fatal("a 1s wait must not collect a ~5s command")
	}
	if !strings.Contains(call.Note, "moved to the background") {
		t.Fatalf("expected a handover note, got %q", call.Note)
	}
	snap := call.Job.Snapshot()
	if !snap.Promoted || snap.WaitedMS < 1000 {
		t.Fatalf("job should be promoted after its wait, got %+v", snap)
	}
	if !call.Job.Wait(context.Background(), 30*time.Second) {
		t.Fatal("promoted job never settled within 30s")
	}
	out := call.Job.Output(false)
	if !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("promoted job's output lost the exec format: %q", out)
	}
	if call.Job.Status() != job.StatusCompleted {
		t.Fatalf("status = %s, want completed", call.Job.Status())
	}
}

func TestExecRunInBackgroundIsNotAwaited(t *testing.T) {
	tool := newShellTool(t, t.TempDir(), 30)
	call := startAsync(t, tool, map[string]interface{}{
		"command":           "ping -n 3 127.0.0.1 >nul",
		"run_in_background": true,
	})
	if call.Settled {
		t.Fatal("run_in_background must return without waiting")
	}
	if !strings.Contains(call.Note, "Started in the background") {
		t.Fatalf("expected a start note, got %q", call.Note)
	}
	// Poll the state instead of waiting on the job: a blocked Wait is itself
	// somebody collecting the result, which would suppress the very notice this
	// path exists to deliver.
	deadline := time.Now().Add(30 * time.Second)
	for call.Job.Status() == job.StatusRunning && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if s := call.Job.Status(); s != job.StatusCompleted {
		t.Fatalf("background job did not complete: %s", s)
	}
	// The point of the flag: nobody was watching, so the settlement has to
	// announce itself. Awaited() is what the session's notice hook asks.
	if call.Job.Awaited() {
		t.Fatal("an unwatched job must not be marked awaited, or its notice is suppressed")
	}
}

// TestExecStateCarriesToNewShell is the pool's promise: the session's cwd and
// environment belong to the pool, so a command that has to use a different
// cmd.exe still finds the shell state it expects.
func TestExecStateCarriesToNewShell(t *testing.T) {
	dir := t.TempDir()
	tool := newShellTool(t, dir, 30)
	if out := execCmd(t, tool, "mkdir sub && cd sub && set MCREPLAY=777"); !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("setup failed: %s", out)
	}

	// Park a background command on the warm shell so the next call cannot have
	// it, and wait until the pool really has handed one out. Polling the pool is
	// honest here: the test would otherwise pass without ever exercising replay.
	hold := startAsync(t, tool, map[string]interface{}{
		"command":           "ping -n 6 127.0.0.1 >nul",
		"run_in_background": true,
	})
	pool := tool.poolFor()
	leased := 0
	for i := 0; i < 200 && leased == 0; i++ {
		pool.mu.Lock()
		leased = len(pool.leased)
		pool.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	if leased == 0 {
		t.Fatal("the background command never took a shell lease")
	}

	out := execCmd(t, tool, "echo MCREPLAY=%MCREPLAY% & cd")
	if !strings.Contains(out, "MCREPLAY=777") {
		t.Fatalf("environment was not replayed into the new shell: %s", out)
	}
	if !strings.Contains(out, filepathTail(dir)+`\sub`) {
		t.Fatalf("the session directory did not follow the new shell: %s", out)
	}
	if !hold.Job.Cancel("test done") {
		t.Fatal("the held job refused to be canceled")
	}
	if !hold.Job.Wait(context.Background(), 15*time.Second) {
		t.Fatal("canceled job did not settle")
	}
	if s := hold.Job.Status(); s != job.StatusKilled {
		t.Fatalf("canceled job status = %s, want killed", s)
	}
}

func TestJobToolsReadAndKill(t *testing.T) {
	tool := newShellTool(t, t.TempDir(), 30)
	call := startAsync(t, tool, map[string]interface{}{
		"command":           "ping -n 20 127.0.0.1 >nul",
		"run_in_background": true,
	})
	id := call.Job.ID

	var list, output, kill *JobTool
	for _, jt := range NewJobTools(tool.Jobs()) {
		switch jt.Name() {
		case "job_list":
			list = jt
		case "job_output":
			output = jt
		case "job_kill":
			kill = jt
		}
	}
	ctx := context.Background()
	res, err := list.Execute(ctx, map[string]interface{}{})
	if err != nil || !strings.Contains(res.Text, id) {
		t.Fatalf("job_list did not show the job: %v %v", err, res)
	}
	res, _ = output.Execute(ctx, map[string]interface{}{"job_id": id})
	if !strings.Contains(res.Text, "still running") {
		t.Fatalf("job_output on a running job should say so: %s", res.Text)
	}
	res, _ = output.Execute(ctx, map[string]interface{}{"job_id": "exec-nosuch"})
	if !strings.HasPrefix(res.Text, "Error:") {
		t.Fatalf("unknown job id should be an error: %s", res.Text)
	}
	res, _ = kill.Execute(ctx, map[string]interface{}{"job_id": id, "reason": "test"})
	if !strings.Contains(res.Text, "told to stop") {
		t.Fatalf("job_kill did not accept: %s", res.Text)
	}
	if !call.Job.Wait(ctx, 15*time.Second) {
		t.Fatal("killed job did not settle")
	}
	if s := call.Job.Status(); s != job.StatusKilled {
		t.Fatalf("status = %s, want killed", s)
	}
	res, _ = kill.Execute(ctx, map[string]interface{}{"job_id": id})
	if !strings.Contains(res.Text, "not running") {
		t.Fatalf("killing a settled job should be refused politely: %s", res.Text)
	}
}

// filepathTail keeps the assertion independent of how long the temp path is.
func filepathTail(dir string) string {
	d := strings.ReplaceAll(dir, "/", `\`)
	if i := strings.LastIndexByte(d, '\\'); i >= 0 {
		return d[i+1:]
	}
	return d
}
