package tools

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// This file adds targeted failure diagnostics for the exec tool. When a
// command FAILS, the model currently sees only the raw shell/interpreter
// error. A 27B-class model often reacts to cryptic errors by retrying a
// slightly different spelling of the same command (burning turns) instead of
// changing strategy. Recognizing the specific failure signature and naming
// the concrete fix ("program X is not on PATH", "the exception is on the
// last traceback line", "fix file.go:12:5") lets the model recover in one
// step.
//
// All diagnostics are advisory text appended to the tool result on the
// failure path only; the success path is byte-for-byte unchanged and no
// diagnostic ever alters what gets executed.

// failureDiagnosis inspects the combined output of a failed command and
// returns a short, targeted recovery hint when it recognizes a known failure
// signature. It returns "" when nothing applies.
func failureDiagnosis(command, output string) string {
	var hints []string
	if runtime.GOOS == "windows" {
		if h := windowsErrorDiagnosis(command, output); h != "" {
			hints = append(hints, h)
		}
	}
	if h := pythonTracebackHint(output); h != "" {
		hints = append(hints, h)
	}
	if h := goBuildHint(command, output); h != "" {
		hints = append(hints, h)
	}
	if h := nodeErrorHint(output); h != "" {
		hints = append(hints, h)
	}
	return strings.Join(hints, "\n")
}

// --- Windows cmd.exe error signatures ---

var (
	// "'X' is not recognized as an internal or external command,
	// operable program or batch file." (English) / "'X' 不是内部或外部命令，
	// 也不是可运行的程序 或批处理文件。" (Chinese locale) — the program is
	// not on PATH. Only the first-line prefix is matched: in captured
	// output the Chinese variant may wrap to a second line.
	reNotRecognized = regexp.MustCompile(`(?im)^\s*'([^']+)' (?:is not recognized as an internal or external command|不是内部或外部命令)`)
	reCannotFindPath = regexp.MustCompile(`(?i)the system cannot find the path specified|系统找不到指定的路径`)
	reCannotFindFile = regexp.MustCompile(`(?i)the system cannot find the file specified|系统找不到指定的文件`)
	reAccessDenied   = regexp.MustCompile(`(?im)^\s*(?:access is denied|拒绝访问)`)
	reInvalidSyntax  = regexp.MustCompile(`(?im)^\s*(?:invalid syntax|语法错误)`)
	reBadLabelSyntax = regexp.MustCompile(`(?i)the filename, directory name, or volume label syntax is incorrect|文件名、目录名或卷标语法不正确`)
	reBatchNotFound  = regexp.MustCompile(`(?i)the batch file cannot be found|找不到批处理文件`)
)

// windowsErrorDiagnosis returns a hint for a recognized cmd.exe failure
// signature. It returns "" when nothing applies.
func windowsErrorDiagnosis(command, output string) string {
	var hints []string

	if m := reNotRecognized.FindStringSubmatch(output); m != nil {
		prog := m[1]
		// If the program is a known POSIX name, the existing
		// windowsCommandHint already explains it ("ls -> dir"); adding a
		// PATH hint on top would be noise.
		if _, isPosix := unixToWindows[strings.ToLower(prog)]; !isPosix {
			avail := availableTools()
			msg := "Diagnosis: program " + quoteStr(prog) + " is not on PATH (cmd error 9009). Check with `where " + prog + "`."
			if avail != "" {
				msg += " Common tools available on this machine: " + avail + "."
			} else {
				msg += " It is probably not installed; use a tool that is available."
			}
			hints = append(hints, msg)
		}
	}
	if reCannotFindPath.MatchString(output) {
		hints = append(hints, "Diagnosis: a path in the command does not exist. Verify the path spelling and the current directory (run `cd` to see where you are); create the directory first if it should exist.")
	}
	if reCannotFindFile.MatchString(output) {
		hints = append(hints, "Diagnosis: a file in the command does not exist. List the directory (`dir`) to see the actual file names, and check the path/extension.")
	}
	if reAccessDenied.MatchString(output) {
		hints = append(hints, "Diagnosis: access denied — the file may be open in another process or protected. Wait/retry, or check the path and permissions.")
	}
	if reInvalidSyntax.MatchString(output) {
		hints = append(hints, "Diagnosis: cmd.exe syntax error. Check quoting of arguments containing special characters (& | < > ^ %), and chain commands with && (not ;).")
	}
	if reBadLabelSyntax.MatchString(output) {
		hints = append(hints, "Diagnosis: bad path syntax. Quote paths that contain spaces, and use backslashes or forward slashes consistently.")
	}
	if reBatchNotFound.MatchString(output) {
		hints = append(hints, "Diagnosis: the batch file (.bat/.cmd) was not found at that path. Check the file name and working directory.")
	}
	return strings.Join(hints, "\n")
}

// quoteStr renders s with double quotes (avoids an import just for this).
func quoteStr(s string) string { return `"` + s + `"` }

// --- available-tools detection ---

var (
	availableToolsOnce sync.Once
	availableToolsList string
)

// availableTools returns the common interpreters/tools that are actually on
// PATH on this machine, detected once (lazily) via exec.LookPath. The list
// lets the model pick a working interpreter instead of guessing.
func availableTools() string {
	availableToolsOnce.Do(func() {
		var found []string
		for _, name := range []string{"python", "python3", "go", "node", "git", "curl", "tar", "powershell", "pwsh", "java", "ruby", "perl"} {
			if _, err := exec.LookPath(name); err == nil {
				found = append(found, name)
			}
		}
		availableToolsList = strings.Join(found, ", ")
	})
	return availableToolsList
}

// --- Python traceback diagnosis ---

const pythonTracebackMarker = "Traceback (most recent call last):"

// rePyExcLine matches a Python exception line: an identifier at column 0
// optionally followed by ": message" (e.g. "ValueError: bad input" or bare
// "SystemExit"). Indented traceback source lines ("    raise ...") and the
// "During handling of..." separator do not match, and neither does the
// "Exit code: N" footer (the identifier must be immediately followed by a
// colon or end of line).
var rePyExcLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.]*)(:\s*.*)?$`)

// pythonTracebackHint recognizes a Python traceback in the output of a
// failed command and points the model at the exception line, which is the
// actual error to fix. Models frequently re-run the same script or re-read
// the whole file instead of fixing the named exception; naming it saves a
// turn. The exception is taken from the LAST traceback block (chained
// "During handling..." sections end with the final exception).
func pythonTracebackHint(output string) string {
	idx := strings.LastIndex(output, pythonTracebackMarker)
	if idx < 0 {
		return ""
	}
	rest := output[idx+len(pythonTracebackMarker):]
	var exc string
	for _, line := range strings.Split(rest, "\n") {
		if rePyExcLine.MatchString(line) {
			exc = strings.TrimSpace(line)
			break
		}
	}
	if exc == "" {
		return "Diagnosis: Python error — a traceback was printed; the exception is the last line of it. Read the traceback, fix the named error, then re-run the script."
	}
	if len([]rune(exc)) > 160 {
		exc = string([]rune(exc)[:160]) + "…"
	}
	return "Diagnosis: Python error — the actual exception is the last line of the traceback: " + quoteStr(exc) + ". Fix that specific error (read the referenced file at the given line), then re-run the script."
}

// --- Go build/run diagnosis ---

var (
	// A go build/run/test/vet command at the start of a segment.
	reGoBuildCmd = regexp.MustCompile(`(?i)(?:^|[|;&]\s*)go\s+(build|run|test|vet)\b`)
	// A Go compiler error location: file.go:12:5: message
	reGoErrLine = regexp.MustCompile(`([^\s:]+\.(?:go|s|S)):(\d+):(\d+):\s+(.+)`)
)

// goBuildHint recognizes a failed `go build/run/test/vet` and points the
// model at the FIRST compile error (file:line:col), which is the one to fix.
func goBuildHint(command, output string) string {
	if !reGoBuildCmd.MatchString(command) {
		return ""
	}
	m := reGoErrLine.FindStringSubmatch(output)
	if m == nil {
		return ""
	}
	return "Diagnosis: Go build failed — fix the first compile error: " + m[1] + ":" + m[2] + ":" + m[3] + " (" + truncateForHint(m[4], 120) + "), then rebuild."
}

// truncateForHint shortens s to at most n runes.
func truncateForHint(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// --- Node.js error diagnosis ---
//
// Node.js is installed on this machine and is a common target for the
// "write a script and run it" task shape, yet the exec tool had no
// diagnosis for it: a crashed node script dumped a raw stack trace and the
// model often re-ran the same script or re-read the whole file instead of
// fixing the named exception. This mirrors the Python/Go diagnostics:
// recognize the failure signature and name the concrete error and location.
//
// An uncaught Node.js exception prints, in order: the offending source line,
// a caret, the error line ("ReferenceError: x is not defined"), a stack
// trace of "at ... (file.js:line:col)" frames, and a final "Node.js vN.N.N"
// line. We anchor on the "Node.js v" line or a Node-style stack frame, then
// extract the error line and the first user-code frame (skipping
// node:internal frames, which point inside the runtime, not the user's file).

var (
	// A Node.js error line: an Error/Exception/Warning identifier at the
	// start of the line, optionally prefixed by "Uncaught" and optionally
	// followed by ": message". Stack frames ("at ...") and the "Node.js v"
	// footer do not match (they start with lowercase / "Node.js").
	reNodeErrLine = regexp.MustCompile(`^(?:Uncaught\s+)?[A-Z][A-Za-z]*(?:Error|Exception|Warning)(?:\s*:\s*.+)?$`)
	// A Node.js stack frame: "at <fn> (<file>:<line>:<col>)".
	reNodeStackFrame = regexp.MustCompile(`at .*\(([^()]+):(\d+):(\d+)\)`)
)

// nodeErrorHint recognizes a Node.js error in the output of a failed command
// and points the model at the named exception and the first user-code frame,
// which is the actual error to fix. It returns "" when the output does not
// look like a Node.js failure.
func nodeErrorHint(output string) string {
	// Require a Node.js signature before doing any extraction, so unrelated
	// output (e.g. a Python traceback that happens to contain "Error:") is
	// never mislabeled as a Node.js failure.
	hasVersion := strings.Contains(output, "Node.js v")
	hasFrame := reNodeStackFrame.MatchString(output)
	if !hasVersion && !hasFrame {
		return ""
	}

	var errMsg string
	var errFile, errLine, errCol string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if errMsg == "" {
			if reNodeErrLine.MatchString(trimmed) {
				errMsg = trimmed
			}
		}
		if errFile == "" {
			if m := reNodeStackFrame.FindStringSubmatch(trimmed); m != nil && !strings.Contains(m[1], "node:internal") {
				errFile, errLine, errCol = m[1], m[2], m[3]
			}
		}
		if errMsg != "" && errFile != "" {
			break
		}
	}
	if errMsg == "" {
		return ""
	}
	if len([]rune(errMsg)) > 160 {
		errMsg = string([]rune(errMsg)[:160]) + "…"
	}
	hint := "Diagnosis: Node.js error — the actual error is " + quoteStr(errMsg) + "."
	if errFile != "" {
		hint += fmt.Sprintf(" It originates at %s:%s:%s. ", errFile, errLine, errCol)
	}
	hint += "Read that file at the given line, fix the specific error, then re-run the script."
	return hint
}
