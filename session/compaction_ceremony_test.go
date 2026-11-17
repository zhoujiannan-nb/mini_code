package session

import (
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
)

func compactCeremonyHistory(echo string) []provider.Message {
	return []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "任务:harness 对比跑分"},
		{Role: "assistant", Content: "先看 bench 目录"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "e1", Type: "function", Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"python run.py"}`}},
		}},
		{Role: "tool", Content: "六题齐备", ToolCallID: "e1", Name: "exec"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "k1", Type: "function", Function: provider.FuncCall{Name: "compact_context", Arguments: `{"instruction":"保留结论指针"}`}},
		}},
		{Role: "tool", Content: echo, ToolCallID: "k1", Name: "compact_context"},
		BuildCompactRequest("保留结论指针"),
	}
}

func TestRebuildContextDropsItsOwnCompactCall(t *testing.T) {
	echo := "Compaction scheduled, focusing on: " + strings.Repeat("保留结论指针与关键数字。", 600)
	in := compactCeremonyHistory(echo)
	out, ok := RebuildContext(in, "## Goal\nharness 对比跑分", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("rebuild refused")
	}
	for _, m := range out {
		if m.Name == compactToolName || m.ToolCallID == "k1" {
			t.Fatalf("the compact_context result survived the rebuild: %q", firstN(m.GetText(), 80))
		}
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == compactToolName {
				t.Fatal("the compact_context tool_call survived the rebuild")
			}
		}
	}
	tail := out[2:]
	if len(tail) != 3 {
		t.Fatalf("tail = %d messages, want the 3 newest real ones: %+v", len(tail), tail)
	}
	if tail[2].Role != "tool" || tail[2].Content != "六题齐备" {
		t.Errorf("the last real tool result is missing from the tail: %+v", tail[2])
	}
	if tail[1].GetText() != "" || len(tail[1].ToolCalls) != 1 || tail[1].ToolCalls[0].ID != "e1" {
		t.Errorf("the tool result lost its parent tool_calls: %+v", tail[1])
	}
	if tail[0].GetText() != "先看 bench 目录" {
		t.Errorf("the newest real assistant turn should head the tail, got %q", tail[0].GetText())
	}
}

func TestRebuildContextPrunesMixedCompactCall(t *testing.T) {
	in := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "work"},
		{Role: "assistant", Content: "顺手压一下", ToolCalls: []provider.ToolCall{
			{ID: "k1", Type: "function", Function: provider.FuncCall{Name: "compact_context", Arguments: "{}"}},
			{ID: "e1", Type: "function", Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"dir"}`}},
		}},
		{Role: "tool", Content: "scheduled", ToolCallID: "k1", Name: "compact_context"},
		{Role: "tool", Content: "dir output", ToolCallID: "e1", Name: "exec"},
		BuildCompactRequest(""),
	}
	out, ok := RebuildContext(in, "summary", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("rebuild refused")
	}
	declared := map[string]bool{}
	for _, m := range out {
		for _, tc := range m.ToolCalls {
			declared[tc.ID] = true
			if tc.Function.Name == compactToolName {
				t.Error("compact_context call survived")
			}
		}
	}
	var real *provider.Message
	for i, m := range out {
		if m.Role != "tool" {
			continue
		}
		if m.Name == compactToolName {
			t.Fatal("the compaction result survived alongside a mixed call")
		}
		if !declared[m.ToolCallID] {
			t.Fatalf("tool result %s has no declaring tool_calls in the rebuilt context", m.ToolCallID)
		}
		real = &out[i]
	}
	if real == nil || real.Content != "dir output" {
		t.Fatalf("the real tool result should survive, got %+v", real)
	}
	for _, m := range out {
		if m.GetText() == "顺手压一下" && len(m.ToolCalls) != 1 {
			t.Errorf("mixed assistant turn should keep exactly the exec call: %+v", m.ToolCalls)
		}
	}
}

func TestStripCompactionCeremonyNoOp(t *testing.T) {
	in := historyMsgs()[1:]
	got := stripCompactionCeremony(in)
	if len(got) != len(in) {
		t.Fatalf("history changed without any compact_context: %d -> %d", len(in), len(got))
	}
	for i := range in {
		if got[i].Role != in[i].Role || got[i].GetText() != in[i].GetText() || len(got[i].ToolCalls) != len(in[i].ToolCalls) {
			t.Fatalf("message %d rewritten: %+v vs %+v", i, got[i], in[i])
		}
	}
}

func firstN(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
