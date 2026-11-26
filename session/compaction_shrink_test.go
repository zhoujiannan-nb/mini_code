package session

import (
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
)

func TestShrinkOversizedTailPrunesBeforeKeptWindow(t *testing.T) {
	bigOut := strings.Repeat("tool output line ", 3000)
	bigArgs := `{"file_path":"a.txt","content":"` + strings.Repeat("x", 6000) + `"}`
	out := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: SummaryBlock("## Goal\nx")},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "w1", Type: "function", Function: provider.FuncCall{Name: "write", Arguments: bigArgs}},
		}},
		{Role: "tool", Content: bigOut, ToolCallID: "e1", Name: "exec"},
		{Role: "assistant", Content: "recent one"},
		{Role: "assistant", Content: "recent two"},
		{Role: "assistant", Content: "recent three"},
	}
	est := func(ms []provider.Message) int {
		n := 0
		for _, m := range ms {
			n += len([]rune(m.GetText()))
			for _, tc := range m.ToolCalls {
				n += len([]rune(tc.Function.Arguments))
			}
		}
		return n
	}
	before := est(out)
	got := shrinkOversizedTail(out, before/2, est)
	if !strings.Contains(got[3].GetText(), "middle pruned") {
		t.Fatalf("oversized tool result before the kept window should be pruned: %q", firstN(got[3].GetText(), 80))
	}
	if !strings.Contains(got[2].ToolCalls[0].Function.Arguments, "aged out") {
		t.Fatalf("oversized write args before the kept window should be aged out: %q", firstN(got[2].ToolCalls[0].Function.Arguments, 80))
	}
	for i := 4; i < 7; i++ {
		if got[i].GetText() != out[i].GetText() {
			t.Fatalf("kept tail message %d must stay intact", i)
		}
	}
	if got[0].GetText() != "SYSTEM" || !strings.Contains(got[1].GetText(), "<previous-summary>") {
		t.Fatal("system and summary must stay intact")
	}
	if est(got) >= before {
		t.Fatalf("shrinking should reduce tokens: %d -> %d", before, est(got))
	}
}

func TestRebuildContextShrinksWhenOverBudget(t *testing.T) {
	bigOut := strings.Repeat("tool output line ", 3000)
	bigSummary := strings.Repeat("summary sentence ", 2000)
	in := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "work"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "e1", Type: "function", Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"run"}`}},
		}},
		{Role: "tool", Content: bigOut, ToolCallID: "e1", Name: "exec"},
		{Role: "assistant", Content: "r1"},
		{Role: "assistant", Content: "r2"},
		{Role: "assistant", Content: "r3"},
		BuildCompactRequest(""),
	}
	cheap := func(ms []provider.Message) int { return len(ms) }
	out, ok := RebuildContextEst(in, bigSummary, 4, 5, cheap)
	if !ok {
		t.Fatal("rebuild refused")
	}
	pruned := false
	for _, m := range out {
		if m.Role == "tool" && strings.Contains(m.GetText(), "middle pruned") {
			pruned = true
		}
	}
	if !pruned {
		t.Fatal("an oversized tool result before the kept window should be pruned when the rebuild is over budget")
	}
}
