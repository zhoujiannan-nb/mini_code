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
	timeout    int
	workingDir string
}

func NewExecTool(timeout int, workingDir string) *ExecTool {
	if timeout <= 0 {
		timeout = 60
	}
	return &ExecTool{timeout: timeout, workingDir: workingDir}
}

func (t *ExecTool) Name() string { return "exec" }
func (t *ExecTool) Description() string {
	if runtime.GOOS == "windows" {
		return "Execute a shell command and return stdout/stderr plus exit code. The shell is cmd.exe (Windows): use dir/type/findstr/copy/move/del and && to chain commands, not Unix syntax. Use the optional `input` parameter to feed text to the command's stdin (more reliable than shell redirection)."
	}
	return "Execute a shell command and return stdout/stderr plus exit code. The shell is sh (POSIX). Use the optional `input` parameter to feed text to the command's stdin (more reliable than shell redirection)."
}
func (t *ExecTool) IsHidden() bool { return false }
func (t *ExecTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command":     map[string]interface{}{"type": "string", "description": "Shell command to execute"},
			"working_dir": map[string]interface{}{"type": "string", "description": "Working directory"},
			"timeout":     map[string]interface{}{"type": "integer", "description": "Timeout in seconds (default 120, max 600)", "minimum": 1, "maximum": 600},
			"input":       map[string]interface{}{"type": "string", "description": "Optional text to feed to the command's standard input (stdin). Preferred over shell redirection for piping data into a command reliably."},
		},
		"required": []string{"command"},
	}
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
	command, _ := params["command"].(string)
	if command == "" {
		return nil, fmt.Errorf("missing command")
	}

	cwd := t.workingDir
	if wd, ok := params["working_dir"].(string); ok && wd != "" {
		cwd = wd
	}
	timeout := t.timeout
	if v, ok := params["timeout"].(float64); ok && int(v) > 0 {
		timeout = int(v)
	}
	if timeout > 600 {
		timeout = 600
	}

	if err := t.guardCommand(command); err != "" {
		return NewTextResult(err), nil
	}

	// Guard against commands that exceed the shell command-line limit. On
	// Windows, cmd.exe accepts at most 8191 characters for the whole command
	// line; a command this long fails with a cryptic "filename or extension
	// is too long" error that does not say what to do. Point the model at the
	// real fix: write the command to a script file and run that file.
	if len(command) > 8000 {
		return NewTextResult(fmt.Sprintf("Error: command is too long (%d chars); the shell command-line limit is ~8191. Write the command to a script file (.bat/.py/.sh) with the write_file tool, then run that file instead.", len(command))), nil
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
		// Pass the command line to cmd.exe verbatim (see applyRawCmdLine):
		// Go's default argument escaping plus cmd.exe's quote-stripping
		// rules mangle any command containing double quotes.
		applyRawCmdLine(cmd, command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
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

	err := cmd.Wait()
	var parts []string
	// DecodeToUTF8: on Windows cmd.exe emits its diagnostics (and many
	// console programs their output) in the ANSI code page (GBK on Chinese
	// systems); normalizing to UTF-8 keeps the text readable for both the
	// LLM and the persisted session history.
	if stdout.Len() > 0 {
		parts = append(parts, util.DecodeToUTF8(stdout.Bytes()))
	}
	if stderr.Len() > 0 {
		s := strings.TrimSpace(util.DecodeToUTF8(stderr.Bytes()))
		if s != "" {
			parts = append(parts, "STDERR:\n"+s)
		}
	}

	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return NewTextResult(fmt.Sprintf("Error: Command timed out after %ds", timeout)), nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	parts = append(parts, fmt.Sprintf("\nExit code: %d", exitCode))
	// Recovery hints: when a command fails and looks like it was written for a
	// POSIX shell, point the model at the cmd.exe equivalent. This is the top
	// failure mode on Windows (ls/cat/grep/... are not cmd commands) and the
	// hints cost nothing on the success path. The mkdir note additionally
	// fires on SUCCESS for "mkdir -p x": that form exits 0 but leaves a junk
	// "-p x" directory behind, so the model must hear about it even then.
	if exitCode != 0 {
		if runtime.GOOS == "windows" {
			if hint := windowsCommandHint(command); hint != "" {
				parts = append(parts, hint)
			}
			if note := mkdirCmdNote(command); note != "" {
				parts = append(parts, note)
			}
		}
		// Targeted failure diagnostics: recognize the specific error
		// signature (program not on PATH, missing file/path, access denied,
		// a Python traceback, a Go compile error) and name the concrete fix,
		// so the model recovers in one step instead of retrying variants of
		// the same failing command.
		if diag := failureDiagnosis(command, strings.Join(parts, "\n")); diag != "" {
			parts = append(parts, diag)
		}
	} else if runtime.GOOS == "windows" && reMkdirP.MatchString(command) {
		parts = append(parts, "Note: cmd's mkdir does not support -p — a junk directory literally named \"-p ...\" was likely created. Use plain `mkdir` (it creates parent directories automatically).")
	}

	result := strings.Join(parts, "\n")
	if result == "" {
		result = "(no output)"
	}

	// Oversized output is NOT trimmed here: the loop's dsh-style prune
	// (session/prune.go) does the head+tail cut once per result, spills the
	// full text to a file, and keeps the full row in the ledger — so the
	// model history carries a small pruned copy that can point at the full
	// output. An early cut here would defeat that (the old 10K head-only
	// byte trim lost exit codes and verdicts that sit at the tail). Only a
	// hard safety cap stays, guarding the process and the ledger against
	// runaway multi-MB streams; it still keeps the tail.
	if r := []rune(result); len(r) > hardOutputCap {
		head := string(r[:hardOutputHead])
		tail := string(r[len(r)-hardOutputTail:])
		result = head + fmt.Sprintf("\n\n... (%d chars elided: output over %d-char hard cap) ...\n\n", len(r)-hardOutputHead-hardOutputTail, hardOutputCap) + tail
	}
	return NewTextResult(result), nil
}

// Hard caps for exec output (runes). Between the prune threshold (8192) and
// the hard cap the loop's prune handles the cut with a full-text spill;
// above the cap the stream itself is elided in the middle, keeping both ends.
const (
	hardOutputCap  = 200000
	hardOutputHead = 150000
	hardOutputTail = 50000
)

// unixToWindows maps common POSIX command names to their cmd.exe
// equivalents. It is used only to build a recovery hint after a command has
// already failed, so a conservative (small, unambiguous) set is deliberate:
// a false positive merely adds a gentle hint, never changes behavior.
//
// The values are short "try this" suggestions, not strict equivalents: for
// commands with no cmd counterpart the value says so, which steers the model
// toward a script (python/powershell) instead of it guessing a wrong builtin.
var unixToWindows = map[string]string{
	// Direct renames.
	"ls":         "dir",
	"cat":        "type",
	"grep":       "findstr",
	"rm":         "del",
	"cp":         "copy",
	"mv":         "move",
	"chmod":      "icacls",
	"chown":      "icacls",
	"which":      "where",
	"whereis":    "where",
	"pwd":        "cd",
	"head":       "more",
	"tail":       "more",
	"less":       "more",
	"ps":         "tasklist",
	"kill":       "taskkill",
	"killall":    "taskkill /IM",
	"pkill":      "taskkill /FI",
	"top":        "taskmgr",
	"htop":       "taskmgr",
	"python3":    "python",
	"pip3":       "pip",
	"wget":       "curl",
	"diff":       "fc",
	"clear":      "cls",
	"ifconfig":   "ipconfig",
	"ip":         "ipconfig",
	"ss":         "netstat -ano",
	"lsof":       "netstat -ano",
	"stat":       "dir",
	"du":         "dir (size only; no -sh)",
	"lscpu":      "systeminfo",
	"uname":      "ver",
	"id":         "whoami",
	"traceroute": "tracert",
	"dig":        "nslookup",
	"whois":      "nslookup",
	"sleep":      "timeout /t",
	"touch":      "type nul > file (or the write_file tool)",
	"unzip":      "tar -xf",
	"zip":        "tar -a -cf out.zip",
	"ln":         "mklink",
	"mount":      "subst",
	"fsck":       "chkdsk",
	"env":        "set",
	"export":     "set",
	"unset":      "set VAR=",
	"source":     "call",
	"alias":      "doskey",
	"history":    "doskey /history",
	"read":       "set /p",
	"service":    "sc",
	"crontab":    "schtasks (Task Scheduler)",
	"at":         "schtasks (Task Scheduler)",
	"hexdump":    "certutil -encodehex",
	"base64":     "certutil -encode / -decode",
	"sha256sum":  "certutil -hashfile SHA256",
	"md5sum":     "certutil -hashfile MD5",
	"man":        "use --help",
	"ping":       "ping -n N (count) / -w ms (timeout)",
	"date":       "date (no +%Y format; use python for formatted dates)",
	"sort":       "sort (cmd sort flags differ; no -u)",
	"cut":        "cmd's cut differs from GNU cut (use a script)",
	"for":        "for /f (cmd for-loop syntax differs)",
	"if":         "if exist (cmd if syntax differs)",
	"printf":     "echo (limited) or a script",
	"sh":         "cmd (Windows has no sh; run .bat/.cmd or python)",
	"bash":       "cmd (Windows has no bash; run .bat/.cmd or python)",
	"zsh":        "cmd (Windows has no zsh; run .bat/.cmd or python)",
	"dash":       "cmd (Windows has no dash)",
	"ksh":        "cmd (Windows has no ksh)",
	"csh":        "cmd (Windows has no csh)",
	"fish":       "cmd (Windows has no fish)",
	"apt":        "winget (Windows package manager)",
	"apt-get":    "winget (Windows package manager)",
	"yum":        "winget (Windows package manager)",
	"dnf":        "winget (Windows package manager)",
	"pacman":     "winget (Windows package manager)",
	"brew":       "winget (Windows package manager)",
	"zypper":     "winget (Windows package manager)",
	"dpkg":       "winget (Windows package manager)",
	"rpm":        "winget (Windows package manager)",
	"snap":       "winget (Windows package manager)",
	"flatpak":    "winget (Windows package manager)",
	"sudo":       "not needed on Windows (run as-is)",
	"su":         "not needed on Windows",
	"doas":       "not needed on Windows",
	"chgrp":      "no cmd equivalent",
	"readlink":   "no cmd equivalent",
	"umount":     "no cmd equivalent",
	"groups":     "no cmd equivalent",
	"systemctl":  "no cmd equivalent (use 'sc' or services.msc)",
	"journalctl": "no cmd equivalent (use eventvwr)",
	"dmesg":      "no cmd equivalent (use eventvwr)",
	// No cmd equivalent: steer the model to a script.
	"find":     "dir /s /b (or the glob tool)",
	"sed":      "no cmd equivalent (use a python/powershell script)",
	"awk":      "no cmd equivalent (use a python/powershell script)",
	"tr":       "no cmd equivalent (use a python/powershell script)",
	"uniq":     "no cmd equivalent (use a python/powershell script)",
	"tee":      "no cmd equivalent (use a python/powershell script)",
	"xargs":    "no cmd equivalent (use a python/powershell script)",
	"realpath": "no cmd equivalent (use a python/powershell script)",
	"basename": "no cmd equivalent (use a python/powershell script)",
	"dirname":  "no cmd equivalent (use a python/powershell script)",
	"mktemp":   "no cmd equivalent (use a python/powershell script)",
	"wc":       "no cmd equivalent (use a python/powershell script)",
	"df":       "no cmd equivalent (use a python/powershell script)",
	"free":     "no cmd equivalent (use a python/powershell script)",
	"seq":      "no cmd equivalent (use a python/powershell script)",
	"shuf":     "no cmd equivalent (use a python/powershell script)",
	"yes":      "no cmd equivalent (use a python/powershell script)",
	"od":       "no cmd equivalent (use a python/powershell script)",
	"file":     "no cmd equivalent (use a python/powershell script)",
	"while":    "no cmd equivalent (use a script or for /f)",
	"case":     "no cmd equivalent (use if)",
	"time":     "no cmd equivalent (measure with a script)",
	"watch":    "no cmd equivalent",
	"nohup":    "start (run in the background)",
	"eval":     "no cmd equivalent",
	"exec":     "no cmd equivalent",
	"trap":     "no cmd equivalent",
	"local":    "no cmd equivalent",
	"return":   "no cmd equivalent",
	"function": "no cmd equivalent",
	"command":  "no cmd equivalent",
	"builtin":  "no cmd equivalent",
	"make":     "no cmd equivalent (run the compiler directly)",
	"gcc":      "no cmd equivalent (install MinGW-w64)",
	"g++":      "no cmd equivalent (install MinGW-w64)",
	"cc":       "no cmd equivalent (install MinGW-w64)",
	"ninja":    "no cmd equivalent (install ninja-build)",
	"curl":     "curl (Windows 10+ only; else use Invoke-WebRequest)",
	"tar":      "tar (Windows 10+ only)",
}

// windowsCommandHint returns a short recovery hint when the command — or the
// first token of any of its && / || / | / ; segments — is a known POSIX
// command with a cmd.exe equivalent. It returns "" when no hint applies. The
// hint is advisory text only; it never alters what gets executed.
func windowsCommandHint(command string) string {
	segments := strings.FieldsFunc(command, func(r rune) bool {
		return r == '&' || r == '|' || r == ';'
	})
	seen := make(map[string]bool)
	var hints []string
	for _, seg := range segments {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		first := strings.ToLower(fields[0])
		// Skip env-var assignments (e.g. "FOO=bar cmd") and Windows builtins
		// that are not in the map (they simply won't match).
		if strings.Contains(first, "=") {
			continue
		}
		repl, ok := unixToWindows[first]
		if !ok || seen[first] {
			continue
		}
		seen[first] = true
		hints = append(hints, fmt.Sprintf("`%s` -> `%s`", first, repl))
	}
	if len(hints) == 0 {
		return ""
	}
	return "Hint: the command looks like a Unix/POSIX command, but this shell is cmd.exe (Windows). Try: " +
		strings.Join(hints, ", ") + "."
}

// mkdirCmdNote returns an advisory note when cmd's mkdir is used with
// Unix-style arguments that misbehave on Windows. Verified behavior:
//   - "mkdir -p x"    exits 0 but creates a junk directory literally named "-p x"
//   - "mkdir a/b/c"   fails: an unquoted '/' is parsed as a switch
// cmd's mkdir wants backslashes (a\b\c), creates parent directories
// automatically, and takes one path per call. The note is advisory text only;
// it never changes what gets executed.
var (
	reMkdirP     = regexp.MustCompile(`(?i)(?:^|[|;&]\s*)mkdir\s+-p\b`)
	reMkdirSlash = regexp.MustCompile(`(?i)(?:^|[|;&]\s*)mkdir\s+.*?/`)
)

func mkdirCmdNote(command string) string {
	var notes []string
	if reMkdirP.MatchString(command) {
		notes = append(notes, "cmd's mkdir has no -p flag — it may have created a junk directory literally named \"-p ...\"; use plain `mkdir`, which creates parent directories automatically")
	}
	if reMkdirSlash.MatchString(command) {
		notes = append(notes, "an unquoted '/' is parsed as a switch and fails — use backslashes (a\\b\\c) or quote the path (\"a/b/c\")")
	}
	if len(notes) == 0 {
		return ""
	}
	return "Note: " + strings.Join(notes, "; ") + "."
}

func (t *ExecTool) guardCommand(command string) string {
	if IsHardBlocked(command) {
		return "Error: Command blocked — dangerous pattern detected"
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
