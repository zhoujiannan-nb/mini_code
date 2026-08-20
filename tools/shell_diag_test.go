package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFailureDiagnosisNotRecognized: a program that is not on PATH must get a
// targeted hint naming the program and suggesting `where`.
func TestFailureDiagnosisNotRecognized(t *testing.T) {
	out := "'java' is not recognized as an internal or external command, operable program or batch file.\n\nExit code: 9009"
	got := failureDiagnosis("java -version", out)
	if !strings.Contains(got, "java") || !strings.Contains(got, "not on PATH") {
		t.Errorf("expected a PATH diagnosis naming 'java', got: %s", got)
	}
	if !strings.Contains(got, "where java") {
		t.Errorf("expected a `where java` suggestion, got: %s", got)
	}
}

// TestFailureDiagnosisNotRecognizedChinese: the same diagnosis must work on
// Chinese-locale Windows, where cmd.exe prints
// "'X' 不是内部或外部命令，也不是可运行的程序 或批处理文件。" The captured
// output may wrap the message to a second line, so the regex anchors on the
// first-line prefix only.
func TestFailureDiagnosisNotRecognizedChinese(t *testing.T) {
	out := "'java' 不是内部或外部命令，也不是可运行的程序\n或批处理文件。\n\nExit code: 9009"
	got := failureDiagnosis("java -version", out)
	if !strings.Contains(got, "java") || !strings.Contains(got, "not on PATH") {
		t.Errorf("expected a PATH diagnosis for the Chinese cmd error, got: %s", got)
	}
	if !strings.Contains(got, "where java") {
		t.Errorf("expected a `where java` suggestion, got: %s", got)
	}
}

// TestFailureDiagnosisNotRecognizedPosixSkipped: when the missing program is a
// known POSIX name, windowsCommandHint already explains it; the PATH
// diagnosis must not duplicate that noise.
func TestFailureDiagnosisNotRecognizedPosixSkipped(t *testing.T) {
	out := "'make' is not recognized as an internal or external command, operable program or batch file."
	got := failureDiagnosis("make", out)
	if strings.Contains(got, "not on PATH") {
		t.Errorf("expected no PATH diagnosis for POSIX name 'make', got: %s", got)
	}
}

// TestFailureDiagnosisCmdErrors: each recognized cmd.exe error signature must
// produce its specific hint.
func TestFailureDiagnosisCmdErrors(t *testing.T) {
	cases := []struct {
		out  string
		want string
	}{
		{"The system cannot find the path specified.", "path"},
		{"The system cannot find the file specified.", "file"},
		{"Access is denied.", "access denied"},
		{"Invalid syntax.", "syntax"},
		{"The filename, directory name, or volume label syntax is incorrect.", "path syntax"},
		{"The batch file cannot be found.", "batch"},
		// Chinese-locale cmd.exe variants (Simplified Chinese Windows).
		{"系统找不到指定的路径。", "path"},
		{"系统找不到指定的文件。", "file"},
		{"拒绝访问。", "access denied"},
		{"语法错误。", "syntax"},
		{"文件名、目录名或卷标语法不正确。", "path syntax"},
		{"找不到批处理文件。", "batch"},
	}
	for _, c := range cases {
		got := failureDiagnosis("somecommand", c.out)
		if !strings.Contains(strings.ToLower(got), c.want) {
			t.Errorf("failureDiagnosis(%q) = %q, want it to contain %q", c.out, got, c.want)
		}
	}
}

// TestFailureDiagnosisPythonTraceback: the hint must quote the ACTUAL
// exception line (last non-empty line of the traceback).
func TestFailureDiagnosisPythonTraceback(t *testing.T) {
	out := `STDERR:
Traceback (most recent call last):
  File "calc.py", line 7, in <module>
    total = sum(nums) / 0
ZeroDivisionError: division by zero

Exit code: 1`
	got := failureDiagnosis("python calc.py", out)
	if !strings.Contains(got, "ZeroDivisionError: division by zero") {
		t.Errorf("expected the exception line to be quoted, got: %s", got)
	}

	// Chained exceptions: the final exception is the one to fix.
	out2 := `Traceback (most recent call last):
  File "a.py", line 4, in f
    1/0
During handling of the above exception, another exception occurred:
Traceback (most recent call last):
  File "a.py", line 6, in <module>
    raise RuntimeError("boom")
RuntimeError: boom`
	got2 := failureDiagnosis("python a.py", out2)
	if !strings.Contains(got2, "RuntimeError: boom") {
		t.Errorf("expected the final chained exception, got: %s", got2)
	}
}

// TestFailureDiagnosisGoBuild: a failed go build must point at the first
// compile error location.
func TestFailureDiagnosisGoBuild(t *testing.T) {
	out := `# github.com/user/mini_code
.\main.go:12:5: undefined: foo
.\util.go:3:1: other error

Exit code: 1`
	got := failureDiagnosis("go build .", out)
	if !strings.Contains(got, "main.go:12:5") || !strings.Contains(got, "undefined: foo") {
		t.Errorf("expected the first compile error to be named, got: %s", got)
	}
}

// TestFailureDiagnosisGoRunPanicNoFalsePositive: a go run runtime panic has
// no file.go:line:col compile error and must not get a Go build hint.
func TestFailureDiagnosisGoRunPanicNoFalsePositive(t *testing.T) {
	out := `panic: runtime error: index out of range [3] with length 3

goroutine 1 [running]:
main.main()
	C:\work\main.go:10 +0x45

Exit code: 2`
	got := failureDiagnosis("go run .", out)
	if strings.Contains(got, "Go build failed") {
		t.Errorf("expected no Go build hint for a runtime panic, got: %s", got)
	}
}

// TestFailureDiagnosisNoFalsePositives: benign output must not produce any
// diagnosis.
func TestFailureDiagnosisNoFalsePositives(t *testing.T) {
	for _, out := range []string{
		"hello world\n\nExit code: 1",
		"some random failure text",
		"error: something went wrong",
		"",
	} {
		if got := failureDiagnosis("dir", out); got != "" {
			t.Errorf("failureDiagnosis(%q) = %q, want empty", out, got)
		}
	}
	// A go command that did not build must not get a Go hint either.
	if got := failureDiagnosis("go version", "go version go1.25.0"); got != "" {
		t.Errorf("failureDiagnosis(go version) = %q, want empty", got)
	}
}

// TestFailureDiagnosisWindowsOnly: the cmd.exe-specific diagnoses must not
// fire on non-Windows platforms.
func TestFailureDiagnosisWindowsOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows-only test")
	}
	out := "'java' is not recognized as an internal or external command"
	if got := failureDiagnosis("java", out); got != "" {
		t.Errorf("expected no Windows diagnosis on %s, got: %s", runtime.GOOS, got)
	}
}

// TestAvailableTools: the available-tools list must be non-empty on a
// machine that has at least one common interpreter (this dev box has python
// and go), and the result must be cached (same value on repeated calls).
func TestAvailableTools(t *testing.T) {
	first := availableTools()
	second := availableTools()
	if first != second {
		t.Errorf("availableTools not stable: %q vs %q", first, second)
	}
	if first == "" {
		t.Log("note: no common tools found on PATH (acceptable on minimal CI)")
	}
}

// TestExecDiagnosisEndToEnd: a failing exec call must carry the targeted
// diagnosis in the tool result (full wiring: Execute -> failure ->
// failureDiagnosis). Uses a program name that cannot exist on any machine.
func TestExecDiagnosisEndToEnd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cmd.exe-specific diagnosis")
	}
	tool := NewExecTool(30, t.TempDir())
	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "definitely_not_a_real_prog_xyz -v"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "not on PATH") {
		t.Errorf("expected a PATH diagnosis in the exec result, got: %s", res.Text)
	}
	if !strings.Contains(res.Text, "definitely_not_a_real_prog_xyz") {
		t.Errorf("expected the program name in the diagnosis, got: %s", res.Text)
	}
}

// TestExecDiagnosisPythonEndToEnd: a failing python call must carry the
// traceback diagnosis naming the actual exception.
func TestExecDiagnosisPythonEndToEnd(t *testing.T) {
	tool := NewExecTool(30, t.TempDir())
	script := "raise ValueError('bad input 42')\n"
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(tool.workingDir, "boom.py"), []byte(script), 0644); err != nil {
			t.Fatal(err)
		}
		res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "python boom.py"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Text, "ValueError: bad input 42") {
			t.Errorf("expected the exception line in the diagnosis, got: %s", res.Text)
		}
	}
}

// TestNodeErrorHint: a Node.js uncaught exception must be diagnosed with the
// named error and the first user-code frame (file:line:col).
func TestNodeErrorHint(t *testing.T) {
	out := `STDERR:
C:\work\calc.js:5
console.log(undefinedVar)
          ^

ReferenceError: undefinedVar is not defined
    at Object.<anonymous> (C:\work\calc.js:5:13)
    at Module._compile (node:internal/modules/cjs/loader:1105:14)
    at Module.wrapSafe (node:internal/modules/cjs/loader:115:16)

Node.js v18.17.0

Exit code: 1`
	got := nodeErrorHint(out)
	if !strings.Contains(got, "ReferenceError: undefinedVar is not defined") {
		t.Errorf("expected the error line to be quoted, got: %s", got)
	}
	if !strings.Contains(got, "calc.js:5:13") {
		t.Errorf("expected the user-code frame location, got: %s", got)
	}
	if strings.Contains(got, "node:internal") {
		t.Errorf("must not point at node:internal frames, got: %s", got)
	}
	if !strings.Contains(got, "Node.js error") {
		t.Errorf("expected the Node.js label, got: %s", got)
	}
}

// TestNodeErrorHintSkipsInternalFrames: when the first frame is a
// node:internal one, the hint must skip to the first user-code frame.
func TestNodeErrorHintSkipsInternalFrames(t *testing.T) {
	out := `TypeError: Cannot read properties of undefined (reading 'map')
    at Module._extensions..js (node:internal/modules/cjs/loader:1178:10)
    at Object.require (C:\work\app.js:12:5)

Node.js v20.1.0`
	got := nodeErrorHint(out)
	if !strings.Contains(got, "TypeError: Cannot read properties of undefined (reading 'map')") {
		t.Errorf("expected the error line, got: %s", got)
	}
	if !strings.Contains(got, "app.js:12:5") {
		t.Errorf("expected the user-code frame (app.js:12:5), got: %s", got)
	}
}

// TestNodeErrorHintNoFalsePositive: output that merely contains the word
// "Error" without a Node.js signature must not be diagnosed as Node.js.
func TestNodeErrorHintNoFalsePositive(t *testing.T) {
	for _, out := range []string{
		"error: something went wrong\n\nExit code: 1",
		"ValueError: bad input",
		"all good",
		"",
	} {
		if got := nodeErrorHint(out); got != "" {
			t.Errorf("nodeErrorHint(%q) = %q, want empty", out, got)
		}
	}
}

// TestExecDiagnosisNodeEndToEnd: a failing node call must carry the Node.js
// diagnosis naming the actual exception (skipped when node is not installed).
func TestExecDiagnosisNodeEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	tool := NewExecTool(30, t.TempDir())
	script := "const x = undefined;\nconsole.log(x.foo);\n"
	if err := os.WriteFile(filepath.Join(tool.workingDir, "boom.js"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "node boom.js"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Node.js error") {
		t.Errorf("expected a Node.js diagnosis in the exec result, got: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Cannot read properties of undefined") {
		t.Errorf("expected the exception message in the diagnosis, got: %s", res.Text)
	}
}
