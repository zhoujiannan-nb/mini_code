package tools

import (
	"bytes"
	"context"
	"encoding/json"
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

// fgPatienceCap is how long exec will stand and watch a foreground command
// before handing it over, whatever timeout the call asked for.
//
// The timeout parameter alone did not fix the disease it was meant to fix: asked
// for a patience number, models ask for a big one. In the run before this cap
// existed, the longest foreground wait was 539s on a command whose caller had
// written timeout=1200 — the wait was chosen by the same optimism that writes
// "600" into a test-suite call, and the turn paid for all of it. A promoted job
// costs the model one short note and a later notification; a foreground wait
// costs the whole wall clock and buys nothing the notification would not have.
// 110s is under the point where a turn stops being interactive and well under
// the shortest build anyone has to wait for.
const fgPatienceCap = 110 * time.Second

// fgPatience is the foreground wait for one call: what the caller asked for,
// never more than the cap. A short timeout stays short — a model that asked for
// 5s wants an answer in 5s, and getting a promotion note instead of a killed
// process is still strictly better than the old behavior.
func fgPatience(requestedSec int) time.Duration {
	d := time.Duration(requestedSec) * time.Second
	if d <= 0 || d > fgPatienceCap {
		return fgPatienceCap
	}
	return d
}

// execSpec is a validated exec call: everything both engines need to know, with
// the defaults already applied.
type execSpec struct {
	command  string
	cwd      string // effective directory (explicit working_dir or the tool default)
	cwdParam string // the explicit working_dir only ("" when the model did not name one)
	timeout  int    // foreground patience in seconds, as asked; fgPatience caps it
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
	cmdStr, isStr := params["command"].(string)
	spec.command = cmdStr
	// dsh's stable argument texts (index.ts), verbatim under the normalized
	// "Error: invalid arguments: " prefix.
	if !isStr || spec.command == "" {
		return nil, NewTextResult("Error: invalid arguments: invalid command: expected a non-empty string"), nil
	}
	if v, present := params["description"]; present {
		desc, _ := v.(string)
		if strings.TrimSpace(desc) == "" {
			return nil, NewTextResult("Error: invalid arguments: invalid description: expected a non-empty string"), nil
		}
	}
	if v, present := params["timeoutMs"]; present {
		n, ok := numberParam(v)
		if !ok || n <= 0 {
			return nil, NewTextResult(fmt.Sprintf("Error: invalid arguments: invalid timeoutMs: expected a positive number, got %s", jsonLiteral(v))), nil
		}
	}
	t.dirMu.Lock()
	spec.cwd = t.workingDir
	t.dirMu.Unlock()
	// dsh's spelling is `workdir`; the historical `working_dir` stays accepted.
	if wd := strParam(params, "workdir", "working_dir"); wd != "" {
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
	// dsh's spelling is `timeoutMs` (milliseconds); convert to the seconds the
	// engine works in, rounding up so a short patience never becomes zero.
	if v, ok := numberParam(params["timeoutMs"]); ok && v > 0 {
		sec := (v + 999) / 1000
		if sec < 1 {
			sec = 1
		}
		spec.timeout = sec
	}
	if spec.timeout > 600 {
		spec.timeout = 600
	}
	spec.input, _ = params["input"].(string)
	spec.background, _ = params["run_in_background"].(bool)

	// Sleeping to wait for work is not a command, it is a habit — and it is the
	// cost that survives every timeout mechanism, because nothing is timing out:
	// the model is choosing to burn the turn. Skipped above the threshold; see
	// waitloop.go for why this one is intercepted instead of annotated. It runs
	// before the discipline gate because its note explains the wait, where the
	// gate would only refuse it.
	if sec := pureWaitSeconds(spec.command); sec >= waitInterceptSec {
		return nil, NewTextResult(waitLoopNote(sec)), nil
	}
	if err := t.guardCommand(spec.command); err != "" {
		return nil, NewTextResult(err), nil
	}
	// Guard against commands that exceed the shell command-line limit. On
	// Windows, cmd.exe accepts at most 8191 characters for the whole command
	// line; a command this long fails with a cryptic "filename or extension is
	// too long" error that does not say what to do. Point the model at the real
	// fix: write the command to a script file and run that file.
	if len(spec.command) > 8000 {
		return nil, NewTextResult(fmt.Sprintf("Error: command is too long (%d chars); the shell command-line limit is ~8191. Write the command to a script file (.bat/.py/.sh) with the write tool, then run that file instead.", len(spec.command))), nil
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
	spec, refused, err := t.execSpec(params)
	if err != nil {
		return nil, err // rejected before anything started: the caller may retry synchronously
	}
	if spec == nil {
		// execSpec answered with a refusal instead of a call: the guard, or a
		// command longer than the shell accepts. There is no job and no work, so
		// this is the AsyncTool contract's "nothing started" case — and it must be
		// handled before spec is touched, because dereferencing a nil spec here is
		// how every guarded command (del /q, rmdir /s, git checkout of a
		// destructive kind) turned into a recovered panic instead of the refusal
		// text the model is owed.
		if refused != nil {
			return nil, &refusedError{text: refused.Text}
		}
		return nil, fmt.Errorf("exec: no call to start")
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

	j := t.jobs.Start(owner, "exec", spec.command, spec.background)
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

	// The foreground gets the shorter of what was asked for and the cap; running
	// out of it is a handover, never a kill.
	wait := fgPatience(spec.timeout)
	if j.Wait(ctx, wait) {
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
	return &AsyncCall{Job: j, Note: promoteNote(j, wait)}, nil
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
		// dsh parity: one fresh PowerShell per call (see run() in shell.go).
		if shellPath, lookErr := exec.LookPath("pwsh"); lookErr == nil {
			cmd = exec.CommandContext(ctx, shellPath, "-NoProfile", "-NonInteractive", "-NoLogo", "-Command", spec.command)
		} else {
			cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-NoLogo", "-Command", spec.command)
		}
		applyPwshWindow(cmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", spec.command)
	}
	applyManagedEnv(cmd)
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
	code, signal := exitOutcome(err)
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("Error: the command exceeded the %d-minute background limit and its process tree was killed.", int(bgHardCap/time.Minute)), job.StatusFailed, -1
	}
	text := buildExecResult(stdout.Bytes(), stderr.Bytes(), code, signal)
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

// jsonLiteral renders a rejected parameter the way dsh quotes it back in the
// invalid-timeoutMs message (JSON.stringify semantics for the values JSON
// can carry; anything else falls back to Go's rendering).
func jsonLiteral(v interface{}) string {
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

// applyManagedEnv gives every spawned shell the same non-interactive, no-
// color, no-pager environment dsh's managed shell env provides: colored
// tables and pagers burn tokens and stall pipes, and the model reads text,
// not ANSI.
func applyManagedEnv(cmd *exec.Cmd) {
	cmd.Env = append(os.Environ(),
		"NO_COLOR=1",
		"TERM=dumb",
		"PAGER=cat",
		"GIT_PAGER=cat",
	)
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

// refusedError is the AsyncTool contract's "nothing was started" answer for a
// call the tool itself declined — the dangerous-pattern guard, or a command over
// the shell's length limit. The loop falls through to the synchronous path on an
// error, and that path reproduces the same refusal text, so the model is never
// told a job exists for a command that never ran. It carries the text anyway: a
// caller that surfaces the error instead should surface the real reason, not a
// stack-shaped surprise.
type refusedError struct{ text string }

func (e *refusedError) Error() string { return e.text }
