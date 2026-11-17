package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestExecInputSchema(t *testing.T) {
	tt := NewExecTool(0, "")
	params := tt.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("Parameters() missing 'properties' object")
	}
	for _, want := range []string{"description", "command", "workdir", "timeoutMs", "run_in_background"} {
		if _, ok := props[want]; !ok {
			t.Errorf("Parameters() does not declare the dsh parameter %q", want)
		}
	}
	if _, ok := props["input"]; ok {
		t.Errorf("Parameters() must match the dsh pwsh schema (no 'input' property)")
	}
}

func stdinEchoCommand() string {
	if runtime.GOOS == "windows" {
		return "[Console]::In.ReadToEnd()"
	}
	return "cat"
}

func TestExecInputFeedsStdin(t *testing.T) {
	tt := NewExecTool(30, "")
	payload := "hello_stdin_roundtrip_123"
	res, err := tt.Execute(context.Background(), map[string]interface{}{
		"command": stdinEchoCommand(),
		"input":   payload,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res == nil || res.Text == "" {
		t.Fatalf("Execute returned empty result")
	}
	if !strings.Contains(res.Text, payload) {
		t.Errorf("expected output to contain %q, got:\n%s", payload, res.Text)
	}
}

func TestExecInputMultiLine(t *testing.T) {
	tt := NewExecTool(30, "")
	payload := "line_one_alpha\nline_two_beta\nline_three_gamma"
	res, err := tt.Execute(context.Background(), map[string]interface{}{
		"command": stdinEchoCommand(),
		"input":   payload,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res == nil {
		t.Fatalf("Execute returned nil result")
	}
	for _, want := range []string{"line_one_alpha", "line_two_beta", "line_three_gamma"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, res.Text)
		}
	}
}

func TestExecInputOmittedKeepsDefault(t *testing.T) {
	tt := NewExecTool(10, "")
	res, err := tt.Execute(context.Background(), map[string]interface{}{
		"command": stdinEchoCommand(),
	})
	if err != nil {
		t.Fatalf("Execute returned error (did stdin hang?): %v", err)
	}
	if res == nil {
		t.Fatalf("Execute returned nil result")
	}
	if strings.Contains(res.Text, "hello_stdin_roundtrip_123") {
		t.Errorf("unexpected payload in output when input omitted:\n%s", res.Text)
	}
}
