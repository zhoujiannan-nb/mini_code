package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
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
	return "Execute a shell command and return stdout/stderr plus exit code."
}
func (t *ExecTool) IsHidden() bool { return false }
func (t *ExecTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command":     map[string]interface{}{"type": "string", "description": "Shell command to execute"},
			"working_dir": map[string]interface{}{"type": "string", "description": "Working directory"},
			"timeout":     map[string]interface{}{"type": "integer", "description": "Timeout in seconds (default 60, max 600)", "minimum": 1, "maximum": 600},
		},
		"required": []string{"command"},
	}
}

func (t *ExecTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
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

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	var parts []string
	if stdout.Len() > 0 {
		parts = append(parts, stdout.String())
	}
	if stderr.Len() > 0 {
		s := strings.TrimSpace(stderr.String())
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

	result := strings.Join(parts, "\n")
	if result == "" {
		result = "(no output)"
	}

	maxOutput := 10000
	if len(result) > maxOutput {
		half := maxOutput / 2
		result = result[:half] + fmt.Sprintf("\n\n... (%d chars truncated) ...\n\n", len(result)-maxOutput) + result[len(result)-half:]
	}
	return NewTextResult(result), nil
}

func (t *ExecTool) guardCommand(command string) string {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, pattern := range dangerousPatterns {
		if pattern.MatchString(lower) {
			return "Error: Command blocked — dangerous pattern detected"
		}
	}
	return ""
}
