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

const maxToolOutput = 32 * 1024

type Tool struct {
	spec ToolSpec
	dir  string
}

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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n…(truncated)"
}

func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}
