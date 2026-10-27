package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/user/mini_code/tools"
)

// maxToolOutput bounds what a plugin tool may feed back to the model. The exec
// tool uses a similar cap; without one, a chatty plugin could blow the context
// window in a single tool result.
const maxToolOutput = 32 * 1024

// Tool adapts one ToolSpec to the tools.Tool interface, so a plugin tool is
// indistinguishable from a built-in one to the agent loop and the registry.
//
// The parameter object the model produced is delivered to the child process as
// JSON on stdin. That keeps the command line in the manifest static (no shell
// quoting, no injection through an argument) and lets either an argv-style
// program or a small script wrapper read the call.
type Tool struct {
	spec ToolSpec
	dir  string // the plugin directory, used as the working directory
}

// ToolList returns every tool contributed by the plugin. (The manifest field is
// named Tools, so the accessor needs a different name.)
func (p Plugin) ToolList() []tools.Tool {
	if len(p.Tools) == 0 {
		return nil
	}
	out := make([]tools.Tool, 0, len(p.Tools))
	for _, s := range p.Tools {
		out = append(out, &Tool{spec: s, dir: p.Dir})
	}
	return out
}

// PluginTools returns the tools of every plugin, in plugin-id order followed by
// the order inside each manifest. The caller appends them AFTER the core tools:
// the core tool list is the head of the cached prompt prefix and must not move.
func PluginTools(plugins []Plugin) []tools.Tool {
	var out []tools.Tool
	for _, p := range plugins {
		out = append(out, p.ToolList()...)
	}
	return out
}

func (t *Tool) Name() string { return t.spec.Name }

func (t *Tool) Description() string { return t.spec.Description }

func (t *Tool) Parameters() map[string]interface{} {
	if t.spec.Parameters != nil {
		return t.spec.Parameters
	}
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}, "required": []string{}}
}

func (t *Tool) IsHidden() bool { return false }

// Execute runs the plugin command with the call's parameters on stdin.
func (t *Tool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	timeout := time.Duration(t.spec.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, t.spec.Command, t.spec.Args...)
	cmd.Dir = t.dir
	hideWindow(cmd)
	// Force a UTF-8 child environment. On Windows a Python (or Node, or Ruby)
	// child inherits the console code page (GBK/CP936 on a Chinese system) for
	// its stdout even though the parent streams the bytes as UTF-8 — the child
	// then dies with "UnicodeEncodeError: 'gbk' codec can't encode". Setting the
	// standard knobs costs nothing on other platforms (they are simply ignored
	// by programs that do not read them, and LANG/LC_ALL are honoured on Unix).
	cmd.Env = append(os.Environ(),
		"PYTHONIOENCODING=utf-8",
		"PYTHONUTF8=1",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	)

	payload, err := json.Marshal(params)
	if err != nil {
		return tools.NewTextResult(fmt.Sprintf("Error: cannot encode tool parameters: %v\n\n[Analyze the error above and try a different approach.]", err)), nil
	}
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	out := sanitizeUTF8(strings.TrimRight(stdout.String(), "\r\n"))

	if runCtx.Err() == context.DeadlineExceeded {
		return tools.NewTextResult(fmt.Sprintf("Error: plugin tool %q timed out after %s.\n\n[Analyze the error above and try a different approach.]", t.spec.Name, timeout)), nil
	}
	if ctx.Err() != nil {
		return tools.NewTextResult(fmt.Sprintf("Error: plugin tool %q was canceled.", t.spec.Name)), nil
	}
	if runErr != nil {
		msg := sanitizeUTF8(strings.TrimSpace(stderr.String()))
		if msg == "" {
			msg = runErr.Error()
		}
		// The child's stdout often carries a structured error (the web_search
		// driver prints {"ok":false,"error":...} and exits 1), so keep both.
		if out != "" {
			msg = out + "\n" + msg
		}
		return tools.NewTextResult(fmt.Sprintf("Error executing %s: %s\n\n[Analyze the error above and try a different approach.]",
			t.spec.Name, truncate(msg, maxToolOutput))), nil
	}
	if out == "" {
		out = "(no output)"
	}
	return tools.NewTextResult(truncate(out, maxToolOutput)), nil
}

// truncate cuts s to at most n bytes without splitting a UTF-8 rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // walk back off a continuation byte
		cut--
	}
	return s[:cut] + "\n…(truncated)"
}

// sanitizeUTF8 replaces invalid UTF-8 bytes so a plugin's output can always be
// put into a JSON request. A child that ignores the UTF-8 environment (or a
// binary program writing to stdout) would otherwise make the whole request
// marshal fail, which surfaces as an opaque "sending failed" in the UI.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}
