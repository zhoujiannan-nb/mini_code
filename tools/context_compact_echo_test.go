package tools

import (
	"context"
	"strings"
	"testing"
)

// TestCompactContextEchoIsBounded: the tool result travels back into the
// context, so it must not echo a model-authored focus instruction in full — a
// few-thousand-character focus would double that cost on the very next call.
// The pending instruction handed to the loop stays verbatim.
func TestCompactContextEchoIsBounded(t *testing.T) {
	st := NewContextState()
	tool := NewCompactContextTool(st)
	focus := strings.Repeat("保留结论指针与关键数字,", 400) // ~5600 runes, far past the echo cap

	res, err := tool.Execute(context.Background(), map[string]interface{}{"instruction": focus})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if r := []rune(res.Text); len(r) > 420 {
		t.Errorf("tool result is %d runes; the %d-rune focus must be echoed short", len(r), len([]rune(focus)))
	}
	if !strings.Contains(res.Text, "…") {
		t.Errorf("a truncated echo should mark the cut: %q", res.Text[:120])
	}
	if !strings.Contains(res.Text, "Compaction scheduled") {
		t.Errorf("the result must still confirm the schedule: %q", res.Text[:120])
	}
	pending, instr := st.TakePendingCompaction()
	if !pending {
		t.Fatal("the compaction request was not queued for the loop")
	}
	if instr != focus {
		t.Errorf("the loop must receive the focus verbatim (%d runes), got %d runes", len([]rune(focus)), len([]rune(instr)))
	}
}

// TestCompactContextNoInstruction is the bare call: short result, no focus echo.
func TestCompactContextNoInstruction(t *testing.T) {
	st := NewContextState()
	res, err := NewCompactContextTool(st).Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, "Compaction scheduled") || strings.Contains(res.Text, "focusing on") {
		t.Errorf("unexpected result: %q", res.Text)
	}
}
