package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/job"
	"github.com/user/mini_code/util"
)

var dangerousPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s+-[rf]{1,2}\b`),
	regexp.MustCompile(`(?i)\bdel\s+/[fq]\b`),
	regexp.MustCompile(`(?i)\brmdir\s+/s\b`),
	regexp.MustCompile(`(?i)(?:^|[;&|]\s*)format\b`),
	regexp.MustCompile(`(?i)\b(mkfs|diskpart)\b`),
	regexp.MustCompile(`(?i)\bdd\s+if=`),
	regexp.MustCompile(`(?i)>\s*/dev/sd`),
	regexp.MustCompile(`(?i)\b(shutdown|reboot|poweroff)\b`),
}

var destructivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:^|[;&|(\s])rm\s`),
	regexp.MustCompile(`(?i)(?:^|[;&|(\s])(del|erase)\s`),
	regexp.MustCompile(`(?i)(?:^|[;&|(\s])rmdir\s`),
	regexp.MustCompile(`(?i)(?:^|[;&|(\s])rd\s`),
	regexp.MustCompile(`(?i)\bremove-item\b`),
}

func IsHardBlocked(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, p := range dangerousPatterns {
		if p.MatchString(lower) {
			return true
		}
	}
	return false
}

func IsDestructive(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, p := range destructivePatterns {
		if p.MatchString(lower) {
			return true
		}
	}
	return false
}

type ExecTool struct {
	timeout int
	dirMu      sync.Mutex
	workingDir string

	psMu          sync.Mutex
	pool          *shellPool
	psFailures    int
	noPersistentS bool

	jobs *job.Registry
}

func NewExecTool(timeout int, workingDir string) *ExecTool {
	if timeout <= 0 {
		timeout = 60
	}
	t := &ExecTool{timeout: timeout, workingDir: workingDir, jobs: job.New()}
	t.noPersistentS = true
	return t
}

func (t *ExecTool) Jobs() *job.Registry { return t.jobs }

func (t *ExecTool) Name() string { return "pwsh" }
func (t *ExecTool) Description() string {
	if d := dshDesc("pwsh"); d != "" {
		return d
	}
	return "Execute a shell command and return stdout/stderr plus exit code."
}
func (t *ExecTool) IsHidden() bool { return false }
func (t *ExecTool) Parameters() map[string]interface{} {
	if d := dshParams("pwsh"); len(d) > 0 {
		return d
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
		"required":   []string{"command"},
	}
}

func (t *ExecTool) SetWorkingDir(dir string) {
	t.dirMu.Lock()
	t.workingDir = dir
	t.dirMu.Unlock()
}

func (t *ExecTool) WorkingDir() string {
	t.dirMu.Lock()
	defer t.dirMu.Unlock()
	return t.workingDir
}

func (t *ExecTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	return t.run(ctx, params, nil)
}

func (t *ExecTool) ExecuteStreamed(ctx context.Context, params map[string]interface{}, sink func(string)) (*ToolResult, error) {
	return t.run(ctx, params, sink)
}

func (t *ExecTool) run(ctx context.Context, params map[string]interface{}, sink func(string)) (*ToolResult, error) {
	spec, bad, err := t.execSpec(params)
	if err != nil {
		return nil, err
	}
	if bad != nil {
		return bad, nil
	}
	command, cwd, cwdExplicit, timeout := spec.command, spec.cwd, spec.cwdParam, spec.timeout

	if t.persistentWanted(params, command) {
		if res := t.runOnShell(ctx, command, cwdExplicit, timeout, sink); !res.fallback {
			return NewTextResult(res.text), nil
		}
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		if shellPath, lookErr := exec.LookPath("pwsh"); lookErr == nil {
			cmd = exec.CommandContext(ctx, shellPath, "-NoProfile", "-NonInteractive", "-NoLogo", "-Command", command)
		} else {
			cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-NoLogo", "-Command", command)
		}
		applyPwshWindow(cmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	applyManagedEnv(cmd)
	if cwd != "" {
		if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
			return NewTextResult(fmt.Sprintf("Error: working_dir %q does not exist or is not a directory. Create it first (e.g. mkdir) or omit working_dir to run in the default directory.", cwd)), nil
		}
		cmd.Dir = cwd
	}

	var stdout, stderr bytes.Buffer
	if sink != nil {
		out := newOutputFeeder(ctx, sink, "")
		defer out.close()
		errf := newOutputFeeder(ctx, sink, "[stderr] ")
		defer errf.close()
		cmd.Stdout = io.MultiWriter(&stdout, out)
		cmd.Stderr = io.MultiWriter(&stderr, errf)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	if in, _ := params["input"].(string); in != "" {
		cmd.Stdin = strings.NewReader(in)
	}

	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		return NewTextResult("Error: failed to start command: " + err.Error()), nil
	}
	detachTree := attachProcessTree(cmd)
	if ctx.Err() == nil {
		go func() { <-ctx.Done(); detachTree() }()
	}
	defer detachTree()

	err = cmd.Wait()
	exitCode, signal := exitOutcome(err)
	if ctx.Err() == context.DeadlineExceeded {
		text := buildExecResult(stdout.Bytes(), stderr.Bytes(), exitCode, signal,
			fmt.Sprintf("[timed out after %dms]", timeout*1000))
		return NewTextResult(text), nil
	}
	return NewTextResult(buildExecResult(stdout.Bytes(), stderr.Bytes(), exitCode, signal)), nil
}

func exitOutcome(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		if code < 0 {
			if sig := exitSignalName(exitErr); sig != "" {
				return 0, sig
			}
			return 1, ""
		}
		return code, ""
	}
	return 1, ""
}

func buildExecResult(stdout, stderr []byte, exitCode int, signal string, extraMarkers ...string) string {
	out := renderStream(stdout, "out")
	errText := renderStream(stderr, "err")

	body := out
	if errText != "" {
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += "[stderr]\n" + errText
	}
	if body == "" {
		body = "(no output)"
	}

	markers := append([]string{}, extraMarkers...)
	if signal != "" {
		markers = append(markers, "[killed by signal: "+signal+"]")
	} else if exitCode != 0 {
		markers = append(markers, fmt.Sprintf("[exit code: %d]", exitCode))
	}
	if len(markers) == 0 {
		return body
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + strings.Join(markers, "\n")
}

const streamCapBytes = 64000

func renderStream(full []byte, tag string) string {
	text := util.DecodeToUTF8(full)
	if len(full) <= streamCapBytes {
		return text
	}
	cut := len(text) - streamCapBytes
	if cut < 0 {
		cut = 0
	}
	for cut < len(text) && (text[cut]&0xC0) == 0x80 {
		cut++
	}
	kept := text[cut:]
	notice := "[output truncated; full output: (unavailable)]"
	if loc := spoolWrite("pwsh-"+tag, text); loc != "" {
		notice = fmt.Sprintf("[output truncated; full output: %s]", loc)
	}
	return kept + "\n" + notice
}

func (t *ExecTool) guardCommand(command string) string {
	if IsHardBlocked(command) {
		return "Error: Command blocked — dangerous pattern detected"
	}
	if deny := disciplineDeny(command); deny != "" {
		return "Error: Command blocked by the tool-discipline gate — instead of " + deny + ". (MC_EXEC_GATE=0 disables this gate.)"
	}
	return ""
}

type outputFeeder struct {
	ctx  context.Context
	sink func(string)
	pfx  string
	mu   sync.Mutex
	pend []byte
	stop chan struct{}
	once sync.Once
}

const (
	feedChunkBytes = 1024
	feedChunkWait  = 250 * time.Millisecond
)

func newOutputFeeder(ctx context.Context, sink func(string), prefix string) *outputFeeder {
	f := &outputFeeder{ctx: ctx, sink: sink, pfx: prefix, stop: make(chan struct{})}
	go func() {
		t := time.NewTicker(feedChunkWait)
		defer t.Stop()
		for {
			select {
			case <-f.stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				f.flush()
			}
		}
	}()
	return f
}

func (f *outputFeeder) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.pend = append(f.pend, p...)
	big := len(f.pend) >= feedChunkBytes
	f.mu.Unlock()
	if big {
		f.flush()
	}
	return len(p), nil
}

func (f *outputFeeder) flush() {
	f.mu.Lock()
	s := string(f.pend)
	f.pend = nil
	f.mu.Unlock()
	if strings.Trim(s, " \t\r\n") == "" {
		return
	}
	f.sink(f.pfx + s)
}

func (f *outputFeeder) close() {
	f.once.Do(func() {
		close(f.stop)
		f.flush()
	})
}
