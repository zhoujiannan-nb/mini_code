package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// TestExecInputSchema verifies the exec tool declares an `input` (stdin)
// parameter in its JSON schema and documents it in the description, so the
// model knows the capability exists.
func TestExecInputSchema(t *testing.T) {
	tt := NewExecTool(0, "")
	params := tt.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("Parameters() missing 'properties' object")
	}
	if _, ok := props["input"]; !ok {
		t.Errorf("Parameters() does not declare an 'input' parameter")
	}
	if desc := tt.Description(); !strings.Contains(desc, "stdin") {
		t.Errorf("Description() should mention stdin, got: %s", desc)
	}
}

// stdinEchoCommand returns a command that reads all of stdin and writes it
// back to stdout, so the test can observe what the child actually received.
func stdinEchoCommand() string {
	if runtime.GOOS == "windows" {
		return "more" // reads stdin and echoes it (verified: `echo x | more` -> x)
	}
	return "cat"
}

// TestExecInputFeedsStdin is the integration test for the `input` parameter:
// a command that echoes its stdin must return the fed text. This proves the
// parameter is actually wired to cmd.Stdin, not merely declared in the schema.
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

// TestExecInputMultiLine feeds multi-line data (the case shell redirection
// handles poorly) and checks every line round-trips.
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

// TestExecInputOmittedKeepsDefault: without `input`, a command that reads
// stdin must see EOF immediately (stdin defaults to NUL/devnull) and must
// not hang. Guards against the new parameter changing default behavior.
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
