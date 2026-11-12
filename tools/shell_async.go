package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/user/mini_code/job"
)

// Background exec: the half of the tool that refuses to kill work.
//
// The synchronous path has one honest answer for a command that overruns its
// timeout: kill the tree, say so, lose the shell's state. On a long build that
// answer is worse than useless — the model pays for the whole wait, gets no
// output, and has to start the same build again. So exec also implements
// AsyncTool: the command is started under a context the TURN does not own, and
// the timeout becomes a decision about who is watching rather than a verdict on
// the process. Either way the model reads the same command output in the same
// format; what changes is whether it reads it now or when the work is done.

// bgHardCap is how long a backgrounded command may run before the runtime stops
// it. A promoted job has no user waiting on it and no timeout of its own left,
// so without a ceiling a wedged `go build` (or a REPL that ate the protocol)
// holds a shell and a process forever. Thirty minutes is far above anything the
// model has ever legitimately needed and far below "forever".
const bgHardCap = 30 * time.Minute

// execSpec is a validated exec call: everything both engines need to know, with
// the defaults already applied.
type execSpec struct {
	command  string
	cwd      string // effective directory (explicit working_dir or the tool default)
	cwdParam string // the explicit working_dir only ("" when the model did not name one)
	timeout  int    // foreground patience, in seconds
	input    string
	// background marks a call the model asked to start-and-leave, which changes
	// nothing about how the command runs and everything about who waits.
	background bool
}

// execSpec validates and normalizes one exec call. These are the checks run() has
// always made, in the same order with the same wording, because the synchronous
// and the asynchronous path must not disagree about what a bad call looks like —
// one command, one error text, whichever engine got it.
//
// The error return means "this call was never started", which is what licenses
// the loop to fall back to the synchronous path after a rejected StartAsync.
func (t *ExecTool) execSpec(params map[string]interface{}) (*execSpec, *ToolResult, error) {
	spec := &execSpec{}
	spec.command, _ = params["command"].(string)
	if spec.command == "" {
		return nil, nil, fmt.Errorf("missing command")
	}
	spec.cwd = t.workingDir
	if wd, ok := params["working_dir"].(string); ok && wd != "" {
		spec.cwd = wd
		spec.cwdParam = wd
	}
	spec.timeout = t.timeout
	// Accept both spellings of a number: the loop hands over JSON-decoded
	// parameters (float64), while a Go caller writes an int. Silently ignoring a
	// timeout the caller did supply is the worse failure — it would turn a 1s
	// patience into the tool's default and promote nothing.
	if v, ok := numberParam(params["timeout"]); ok && v > 0 {
		spec.timeout = v
	}
	if spec.timeout > 600 {
		spec.timeout = 600
	}
	spec.input, _ = params["input"].(string)
	spec.background, _ = params["run_in_background"].(bool)

	if err := t.guardCommand(spec.command); err != "" {
		return nil, NewTextResult(err), nil
	}
	// Guard against commands that exceed the shell command-line limit. On
	// Windows, cmd.exe accepts at most 8191 characters for the whole command
	// line; a command this long fails with a cryptic "filename or extension is
	// too long" error that does not say what to do. Point the model at the real
	// fix: write the command to a script file and run that file.
	if len(spec.command) > 8000 {
		return nil, NewTextResult(fmt.Sprintf("Error: command is too long (%d chars); the shell command-line limit is ~8191. Write the command to a script file (.bat/.py/.sh) with the write_file tool, then run that file instead.", len(spec.command))), nil
	}
	return spec, nil, nil
}

// persistentForSpec applies the persistent-shell gate to a validated call. The
// stdin case is decided here rather than in persistentWanted because the spec has
// already pulled `input` out: there the stdin IS the command channel, so a call
// that carries one can only run one-shot.
func (t *ExecTool) persistentForSpec(spec *execSpec) bool {
	if spec.input != "" {
		return false
	}
	return t.persistentWanted(nil, spec.command)
}

// StartAsync implements AsyncTool. The command runs under a context that the
// turn does not own, so neither the end of the call nor the end of the turn can
// take it down; only an explicit kill, the background ceiling, or the session's
// teardown ends it. The foreground then waits exactly as long as the model asked
// and hands the job over if that runs out.
func (t *ExecTool) StartAsync(ctx context.Context, owner string, params map[string]interface{}, sink func(string)) (*AsyncCall, error) {
	spec, _, err := t.execSpec(params)
	if err != nil {
		return nil, err // rejected before anything started: the caller may retry synchronously
	}
	if spec.command == "" {
		return nil, fmt.Errorf("missing command")
	}

	// Two nested contexts, because two different things can end this work. The
	// ceiling is the runtime's patience with an unwatched process; the kill is
	// somebody's decision. The job sees the union (whichever fires first cancels
	// the child), and the completion text is chosen by which one it was.
	capped, cancelCap := context.WithTimeout(context.Background(), bgHardCap)
	jobCtx, cancelKill := context.WithCancel(capped)

	j := t.jobs.Start(owner, "exec", spec.command)
	// One kill switch for both engines: the persistent shell's watchdog and the
	// one-shot process tree both watch this context, so canceling it is the whole
	// of "stop that command", whatever is running it.
	j.SetCancel(func() job.Status {
		cancelKill()
		return job.StatusKilled
	})

	// The live feed belongs to the call that is waiting, not to the command: once
	// the job is handed over there is no tool card left to draw on, and a feeder
	// nobody reads would only accumulate.
	gate := &liveGate{sink: sink}
	gate.on.Store(true)

	go func() {
		defer cancelCap()
		defer cancelKill()
		text, status, code := t.runJobCommand(jobCtx, spec, j, gate.sinkFn())
		gate.off()
		j.SetFinal(text)
		detail := ""
		if status == job.StatusCompleted || status == job.StatusFailed {
			detail = fmt.Sprintf("exit code: %d", code)
		}
		j.Settle(status, detail)
	}()

	if spec.background {
		// Nothing to wait for: the model asked for this one asynchronously, so
		// the promise is the whole result and the settlement will arrive as a
		// notice.
		return &AsyncCall{Job: j, Note: backgroundNote(j)}, nil
	}

	if j.Wait(ctx, time.Duration(spec.timeout)*time.Second) {
		return &AsyncCall{Job: j, Settled: true}, nil
	}
	if ctx.Err() != nil {
		// The user stopped the turn while this command WAS being waited on. This
		// is the one case where stopping is what was asked for.
		j.Cancel("interrupted")
		j.Wait(context.Background(), 5*time.Second)
		gate.off()
		return &AsyncCall{Job: j, Settled: true}, nil
	}

	if !j.Promote() {
		// It finished in the gap between the wait expiring and the handover.
		return &AsyncCall{Job: j, Settled: true}, nil
	}
	gate.off()
	return &AsyncCall{Job: j, Note: promoteNote(j, spec.timeout)}, nil
}

// runJobCommand runs the command to completion under the job's own context and
// renders the result exactly as the synchronous path would have. Choosing the
// engine here (rather than at StartAsync time) keeps the fallback honest: a
// persistent attempt that fails before the command ran has not executed
// anything, so the one-shot path can still take the call.
func (t *ExecTool) runJobCommand(ctx context.Context, spec *execSpec, j *job.Job, sink func(string)) (string, job.Status, int) {
	if t.persistentForSpec(spec) {
		res := t.runOnShell(ctx, spec.command, spec.cwdParam, int(bgHardCap/time.Second), sink)
		if !res.fallback {
			status := job.StatusCompleted
			if res.outcome.code != 0 || res.bad {
				status = job.StatusFailed
			}
			return res.text, status, res.outcome.code
		}
	}
	return t.runOneShotJob(ctx, spec, j, sink)
}

// runOneShotJob is the one-shot engine under a job: same cmd.exe, same raw
// command line, same process-tree cleanup, same result builder. Two things
// differ from the synchronous version — the output is teed into the job so a
// background command can be read while it runs, and nothing here kills the tree
// on a deadline, because the deadline that matters is the ceiling on ctx.
func (t *ExecTool) runOneShotJob(ctx context.Context, spec *execSpec, j *job.Job, sink func(string)) (string, job.Status, int) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", spec.command)
		// Pass the command line to cmd.exe verbatim (see applyRawCmdLine): Go's
		// default argument escaping plus cmd.exe's quote-stripping rules mangle
		// any command containing double quotes.
		applyRawCmdLine(cmd, spec.command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", spec.command)
	}
	if spec.cwd != "" {
		if fi, err := os.Stat(spec.cwd); err != nil || !fi.IsDir() {
			return fmt.Sprintf("Error: working_dir %q does not exist or is not a directory. Create it first (e.g. mkdir) or omit working_dir to run in the default directory.", spec.cwd), job.StatusFailed, 0
		}
		cmd.Dir = spec.cwd
	}

	var stdout, stderr bytes.Buffer
	if sink != nil {
		out := newOutputFeeder(ctx, sink, "")
		defer out.close()
		errf := newOutputFeeder(ctx, sink, "[stderr] ")
		defer errf.close()
		// The job gets stdout as it arrives so a background command can be peeked
		// at; the result text is still built from the complete buffers.
		cmd.Stdout = io.MultiWriter(&stdout, j, out)
		cmd.Stderr = io.MultiWriter(&stderr, errf)
	} else {
		cmd.Stdout = io.MultiWriter(&stdout, j)
		cmd.Stderr = &stderr
	}
	if spec.input != "" {
		cmd.Stdin = strings.NewReader(spec.input)
	}
	// WaitDelay bounds how long Wait keeps copying I/O after the process exits:
	// an orphaned grandchild holding the pipes open cannot hang the job.
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		return "Error: failed to start command: " + err.Error(), job.StatusFailed, 0
	}
	// attachProcessTree: kill the whole tree on cancel or completion, so orphaned
	// children can neither survive nor hold the output pipes open.
	detachTree := attachProcessTree(cmd)
	if ctx.Err() == nil {
		go func() { <-ctx.Done(); detachTree() }()
	}
	defer detachTree()

	err := cmd.Wait()
	code := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("Error: the command exceeded the %d-minute background limit and its process tree was killed.", int(bgHardCap/time.Minute)), job.StatusFailed, -1
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	text := buildExecResult(spec.command, stdout.Bytes(), stderr.Bytes(), code, "")
	status := job.StatusCompleted
	if code != 0 {
		status = job.StatusFailed
	}
	return text, status, code
}

// numberParam reads a tool parameter that may arrive as a JSON number (float64)
// or as a plain Go int from an in-process caller.
func numberParam(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// liveGate keeps a UI feed attached to the call that is waiting, and not to
// whatever happens to the command afterwards. Writes after the handover are
// dropped instead of accumulating in a feeder nobody will ever read.
type liveGate struct {
	sink func(string)
	on   atomic.Bool
}

func (g *liveGate) off() { g.on.Store(false) }

// sinkFn returns the function to hand an engine, or nil when there is no UI to
// feed (which makes the engines take their plain-buffered path).
func (g *liveGate) sinkFn() func(string) {
	if g.sink == nil {
		return nil
	}
	return func(s string) {
		if g.on.Load() {
			g.sink(s)
		}
	}
}
