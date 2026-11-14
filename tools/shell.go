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

// destructivePatterns marks commands that destroy data but are recoverable
// (or limited in blast radius): plain rm/del/rmdir and the like, WITHOUT
// the catastrophic flags (those are caught by dangerousPatterns first).
// In cautious mode these trigger a UI approval instead of running
// directly. The command word must be followed by whitespace so that file
// names like "del.txt" or "rd.txt" do not false-positive.
var destructivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:^|[;&|(\s])rm\s`),
	regexp.MustCompile(`(?i)(?:^|[;&|(\s])(del|erase)\s`),
	regexp.MustCompile(`(?i)(?:^|[;&|(\s])rmdir\s`),
	regexp.MustCompile(`(?i)(?:^|[;&|(\s])rd\s`),
	regexp.MustCompile(`(?i)\bremove-item\b`),
}

// IsHardBlocked reports whether the command matches the unconditional
// dangerous-pattern blocklist (rm -rf, del /f, format, diskpart, ...).
func IsHardBlocked(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, p := range dangerousPatterns {
		if p.MatchString(lower) {
			return true
		}
	}
	return false
}

// IsDestructive reports whether the command destroys data in a
// recoverable way (plain rm/del/rmdir/Remove-Item, ...). It is only
// meaningful when IsHardBlocked is false.
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
	// dirMu guards workingDir: the working_directory tool may move it while
	// calls are in flight.
	dirMu      sync.Mutex
	workingDir string

	// Shell pool (Windows): long-lived cmd.exe processes per session, so exec
	// calls pay no process cold start and keep cwd/env state across calls. See
	// persistent_shell.go for the protocol and shell_pool.go for who may hold a
	// shell. psMu guards the lazy pool and the feature-off flags.
	// The dsh tool surface pins this OFF (one fresh pwsh per call, as its
	// description promises); the engine stays in the tree for a future switch.
	psMu          sync.Mutex
	pool          *shellPool
	psFailures    int
	noPersistentS bool

	// jobs is this session's background-work registry: the long commands exec
	// cannot finish inside their wait are registered here rather than killed,
	// and the job_* tools read the same registry. Built with the tool, which is
	// built once per session.
	jobs *job.Registry
}

func NewExecTool(timeout int, workingDir string) *ExecTool {
	if timeout <= 0 {
		timeout = 60
	}
	t := &ExecTool{timeout: timeout, workingDir: workingDir, jobs: job.New()}
	// dsh parity: every call is a fresh shell process (its description says
	// so), so the persistent cmd.exe engine is never entered. The A/B escape
	// hatch MINICODE_NO_PERSISTENT_SHELL stays as the historical knob.
	t.noPersistentS = true
	return t
}

// Jobs returns the registry this tool hands its long commands to. The session
// takes it once, at wiring time, to install the ledger-and-notification hook and
// to give the job_* tools the same view of the same work.
func (t *ExecTool) Jobs() *job.Registry { return t.jobs }

// Name/Description/Parameters: dsh's verbatim "pwsh" surface. The engine
// matches the promise: on Windows every call runs a fresh `pwsh -Command`
// process (the persistent cmd.exe engine stays in the tree but is no longer
// entered — dsh's contract is one-shot semantics with `workdir` per call).
// The historical command/working_dir/timeout/input/run_in_background keys stay
// accepted alongside dsh's command/workdir/timeoutMs/run_in_background.
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

// SetWorkingDir moves the default directory for calls that carry no workdir
// (this is what the working_directory tool drives). Running shells and jobs
// keep their own directories.
func (t *ExecTool) SetWorkingDir(dir string) {
	t.dirMu.Lock()
	t.workingDir = dir
	t.dirMu.Unlock()
}

// WorkingDir reports the default directory calls without an explicit workdir
// run in. The working_directory tool reads it back when a per-task tool
// builder re-attaches to this session's long-lived exec tool, so the reported
// directory is the moved one, not the one the session started with.
func (t *ExecTool) WorkingDir() string {
	t.dirMu.Lock()
	defer t.dirMu.Unlock()
	return t.workingDir
}

func (t *ExecTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	return t.run(ctx, params, nil)
}

// ExecuteStreamed runs the command and forwards its output as it arrives. The
// command, the timeout, the process-tree cleanup and the returned result are
// identical to Execute's; only the reporting differs, which is what keeps the
// model's view of the tool unchanged.
func (t *ExecTool) ExecuteStreamed(ctx context.Context, params map[string]interface{}, sink func(string)) (*ToolResult, error) {
	return t.run(ctx, params, sink)
}

// run executes one shell command. When sink is non-nil the child's stdout and
// stderr are additionally forwarded to it in UI-sized pieces (outputFeeder), so
// the interface can show progress while the command is still running; the final
// result is still built from the complete buffered output.
func (t *ExecTool) run(ctx context.Context, params map[string]interface{}, sink func(string)) (*ToolResult, error) {
	spec, bad, err := t.execSpec(params)
	if err != nil {
		return nil, err
	}
	if bad != nil {
		return bad, nil
	}
	command, cwd, cwdExplicit, timeout := spec.command, spec.cwd, spec.cwdParam, spec.timeout

	// Persistent shell first (Windows): the same command over a long-lived
	// cmd.exe, no cold start, shell state carried over. Commands that must
	// not run that way (stdin `input`, batch for-loops, unencodable text, a
	// repeatedly failing shell) fall through to the one-shot path below.
	// cwdExplicit is the EXPLICIT working_dir only — never the tool default:
	// one-shot pins every call to workingDir, but the persistent pool must
	// let the model's `cd` stand, or its whole point (carried-over state)
	// would be undone by the default directory on every next call.
	if t.persistentWanted(params, command) {
		if res := t.runOnShell(ctx, command, cwdExplicit, timeout, sink); !res.fallback {
			return NewTextResult(res.text), nil
		}
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// dsh parity: one fresh PowerShell per call. `pwsh` (PowerShell 7) is
		// what the tool description promises; machines that only carry the
		// built-in shell fall back to powershell.exe.
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
		// A non-existent working_dir makes cmd.Start fail with a cryptic
		// "failed to start command" error that does not name the cause.
		// Check it up front and tell the model exactly what is wrong.
		if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
			return NewTextResult(fmt.Sprintf("Error: working_dir %q does not exist or is not a directory. Create it first (e.g. mkdir) or omit working_dir to run in the default directory.", cwd)), nil
		}
		cmd.Dir = cwd
	}

	var stdout, stderr bytes.Buffer
	if sink != nil {
		// Tee the child's output: the buffers still collect everything for the
		// result (and its truncation), while the feeders hand the UI a coalesced
		// live view. Nothing here changes what the command itself sees.
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

	// Optional stdin: when the caller supplies `input`, feed that text to the
	// command's standard input. This is the reliable way to pipe data into a
	// command — shell redirection ("echo x | cmd") is fragile on cmd.exe
	// (quoting, special characters, multi-line data). When `input` is empty
	// the command's stdin is left at its default and behavior is unchanged.
	// Go copies the reader into the process and closes the pipe; a command
	// that never reads stdin simply sees EOF, so this cannot hang.
	if in, _ := params["input"].(string); in != "" {
		cmd.Stdin = strings.NewReader(in)
	}

	// WaitDelay bounds how long Wait will keep copying I/O after the process
	// exits: if an orphaned grandchild keeps the pipes open, we stop waiting
	// after this instead of hanging until it dies.
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		return NewTextResult("Error: failed to start command: " + err.Error()), nil
	}
	// attachProcessTree (Windows): kill the whole process tree on cancel or
	// completion, so orphaned children can neither survive nor hold the
	// output pipes open. The returned close func is idempotent.
	detachTree := attachProcessTree(cmd)
	if ctx.Err() == nil {
		go func() { <-ctx.Done(); detachTree() }()
	}
	defer detachTree()

	err = cmd.Wait()
	exitCode, signal := exitOutcome(err)
	if ctx.Err() == context.DeadlineExceeded {
		// The sync path has no job to hand over to (only ExecuteStreamed fell
		// back here), so the timeout is reported dsh-style: the output captured
		// so far, then the timed-out marker.
		text := buildExecResult(stdout.Bytes(), stderr.Bytes(), exitCode, signal,
			fmt.Sprintf("[timed out after %dms]", timeout*1000))
		return NewTextResult(text), nil
	}
	return NewTextResult(buildExecResult(stdout.Bytes(), stderr.Bytes(), exitCode, signal)), nil
}

// exitOutcome maps a cmd.Wait error to dsh's exit vocabulary: the exit code,
// and — only where the platform reports signals — the signal name. Windows
// force-kills settle as exit 1 without a signal, exactly what dsh teaches the
// model to expect.
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
			return 1, "" // Windows: killed settles as a bare exit 1
		}
		return code, ""
	}
	return 1, ""
}

// buildExecResult renders the model-visible text of an exec call exactly in
// dsh's shape: the (tail-truncated) stdout, a `[stderr]` section, then
// conditional markers — extra markers first ([timed out after …],
// [stopped: …]), then `[killed by signal: …]` / `[exit code: N]` (non-zero
// only), each on its own line; an empty body renders as `(no output)`. Both
// the one-shot and the persistent path run through it so one format reaches
// the model regardless of which engine executed the command.
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

// streamCapBytes is dsh-shell's per-stream retained budget: what a stream
// holds beyond it is dropped from the HEAD (the tail is what matters — build
// verdicts and error summaries sit there) and named in a spill notice
// pointing at the complete text.
const streamCapBytes = 64000

// renderStream decodes one captured stream to UTF-8 (cmd-era console tools
// emit the ANSI code page; normalizing keeps both the LLM and the session
// history readable) and applies the dsh tail truncation with its
// `[output truncated; full output: <path>]` notice.
func renderStream(full []byte, tag string) string {
	text := util.DecodeToUTF8(full)
	if len(full) <= streamCapBytes {
		return text
	}
	cut := len(text) - streamCapBytes
	if cut < 0 {
		cut = 0
	}
	for cut < len(text) && (text[cut]&0xC0) == 0x80 { // step off a UTF-8 tail
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

// outputFeeder turns one child stream into UI-sized chunks.
//
// A command can write thousands of lines, and forwarding every write as its own
// event would flood the subscriber queue (and the browser) for nothing: the
// final result carries all of the output anyway. So bytes accumulate until
// either a chunk is worth sending (feedChunkBytes) or the reader has been quiet
// long enough (feedChunkWait), and then exactly one chunk goes out.
//
// The sink runs on this goroutine and must never block; the hub's fan-out is
// non-blocking by design. A feeder with a slow sink delays only its own stream,
// never the child process.
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

// Write implements io.Writer; os/exec copies a non-*os.File destination on its
// own goroutine, so this runs concurrently with the child process.
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

// close stops the ticker and pushes whatever is still buffered, so the last
// lines of a command always reach the UI instead of dying in the buffer.
func (f *outputFeeder) close() {
	f.once.Do(func() {
		close(f.stop)
		f.flush()
	})
}
