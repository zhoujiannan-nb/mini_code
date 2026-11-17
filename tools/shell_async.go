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


const bgHardCap = 30 * time.Minute

const fgPatienceCap = 110 * time.Second

func fgPatience(requestedSec int) time.Duration {
	d := time.Duration(requestedSec) * time.Second
	if d <= 0 || d > fgPatienceCap {
		return fgPatienceCap
	}
	return d
}

type execSpec struct {
	command  string
	cwd      string
	cwdParam string
	timeout  int
	input    string
	background bool
}

func (t *ExecTool) execSpec(params map[string]interface{}) (*execSpec, *ToolResult, error) {
	spec := &execSpec{}
	cmdStr, isStr := params["command"].(string)
	spec.command = cmdStr
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
	if wd := strParam(params, "workdir", "working_dir"); wd != "" {
		spec.cwd = wd
		spec.cwdParam = wd
	}
	spec.timeout = t.timeout
	if v, ok := numberParam(params["timeout"]); ok && v > 0 {
		spec.timeout = v
	}
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

	if sec := pureWaitSeconds(spec.command); sec >= waitInterceptSec {
		return nil, NewTextResult(waitLoopNote(sec)), nil
	}
	if err := t.guardCommand(spec.command); err != "" {
		return nil, NewTextResult(err), nil
	}
	if len(spec.command) > 8000 {
		return nil, NewTextResult(fmt.Sprintf("Error: command is too long (%d chars); the shell command-line limit is ~8191. Write the command to a script file (.bat/.py/.sh) with the write tool, then run that file instead.", len(spec.command))), nil
	}
	return spec, nil, nil
}

func (t *ExecTool) persistentForSpec(spec *execSpec) bool {
	if spec.input != "" {
		return false
	}
	return t.persistentWanted(nil, spec.command)
}

func (t *ExecTool) StartAsync(ctx context.Context, owner string, params map[string]interface{}, sink func(string)) (*AsyncCall, error) {
	spec, refused, err := t.execSpec(params)
	if err != nil {
		return nil, err
	}
	if spec == nil {
		if refused != nil {
			return nil, &refusedError{text: refused.Text}
		}
		return nil, fmt.Errorf("exec: no call to start")
	}
	if spec.command == "" {
		return nil, fmt.Errorf("missing command")
	}

	capped, cancelCap := context.WithTimeout(context.Background(), bgHardCap)
	jobCtx, cancelKill := context.WithCancel(capped)

	j := t.jobs.Start(owner, "exec", spec.command, spec.background)
	j.SetCancel(func() job.Status {
		cancelKill()
		return job.StatusKilled
	})

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
		return &AsyncCall{Job: j, Note: backgroundNote(j)}, nil
	}

	wait := fgPatience(spec.timeout)
	if j.Wait(ctx, wait) {
		return &AsyncCall{Job: j, Settled: true}, nil
	}
	if ctx.Err() != nil {
		j.Cancel("interrupted")
		j.Wait(context.Background(), 5*time.Second)
		gate.off()
		return &AsyncCall{Job: j, Settled: true}, nil
	}

	if !j.Promote() {
		return &AsyncCall{Job: j, Settled: true}, nil
	}
	gate.off()
	return &AsyncCall{Job: j, Note: promoteNote(j, wait)}, nil
}

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

func (t *ExecTool) runOneShotJob(ctx context.Context, spec *execSpec, j *job.Job, sink func(string)) (string, job.Status, int) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
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
		cmd.Stdout = io.MultiWriter(&stdout, j, out)
		cmd.Stderr = io.MultiWriter(&stderr, errf)
	} else {
		cmd.Stdout = io.MultiWriter(&stdout, j)
		cmd.Stderr = &stderr
	}
	if spec.input != "" {
		cmd.Stdin = strings.NewReader(spec.input)
	}
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		return "Error: failed to start command: " + err.Error(), job.StatusFailed, 0
	}
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

func numberParam(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func jsonLiteral(v interface{}) string {
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

func applyManagedEnv(cmd *exec.Cmd) {
	cmd.Env = append(os.Environ(),
		"NO_COLOR=1",
		"TERM=dumb",
		"PAGER=cat",
		"GIT_PAGER=cat",
	)
}

type liveGate struct {
	sink func(string)
	on   atomic.Bool
}

func (g *liveGate) off() { g.on.Store(false) }

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

type refusedError struct{ text string }

func (e *refusedError) Error() string { return e.text }
