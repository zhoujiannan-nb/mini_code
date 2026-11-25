package session

import (
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
)

func todoCallHistory() []provider.Message {
	return []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "任务:改样式并部署"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "t1", Type: "function", Function: provider.FuncCall{Name: "todo_write", Arguments: `{"todos":[{"content":"改 base.css","status":"completed"},{"content":"跑测试","status":"in_progress"},{"content":"部署","status":"pending"}]}`}},
		}},
		{Role: "tool", Content: "Updated todo list: 1 pending, 1 in progress, 1 completed.", ToolCallID: "t1", Name: "todo_write"},
		{Role: "assistant", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "assistant", Content: "c"},
		{Role: "assistant", Content: "d"},
		BuildCompactRequest(""),
	}
}

func TestRebuildContextReinjectsLatestTodoWrite(t *testing.T) {
	out, ok := RebuildContext(todoCallHistory(), "## Goal\n改样式并部署", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("rebuild refused")
	}
	if out[0].Role != "system" {
		t.Fatalf("head should be system, got %q", out[0].Role)
	}
	if !strings.Contains(out[1].GetText(), "<previous-summary>") {
		t.Fatalf("second should be the summary block: %q", firstN(out[1].GetText(), 60))
	}
	inj := out[2]
	if inj.Role != "assistant" || len(inj.ToolCalls) != 1 || inj.ToolCalls[0].Function.Name != "todo_write" {
		t.Fatalf("third should be the reinjected todo_write call: %+v", inj)
	}
	for _, want := range []string{"改 base.css", "跑测试", "部署"} {
		if !strings.Contains(inj.ToolCalls[0].Function.Arguments, want) {
			t.Fatalf("reinjected call should carry full list, missing %q: %s", want, inj.ToolCalls[0].Function.Arguments)
		}
	}
	if out[3].Role != "tool" || out[3].ToolCallID != "t1" {
		t.Fatalf("fourth should be the todo_write result paired with the call: %+v", out[3])
	}
	count := 0
	for _, m := range out {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == "todo_write" {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("exactly one todo_write call expected, got %d", count)
	}
}

func TestRebuildContextNoDuplicateTodoWrite(t *testing.T) {
	in := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "work"},
		{Role: "assistant", Content: "前面"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "t9", Type: "function", Function: provider.FuncCall{Name: "todo_write", Arguments: `{"todos":[{"content":"收尾","status":"in_progress"}]}`}},
		}},
		{Role: "tool", Content: "Updated todo list: 0 pending, 1 in progress, 0 completed.", ToolCallID: "t9", Name: "todo_write"},
		BuildCompactRequest(""),
	}
	out, ok := RebuildContext(in, "summary", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("rebuild refused")
	}
	count := 0
	for _, m := range out {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == "todo_write" {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("todo_write already in the kept tail must not be duplicated, got %d", count)
	}
}
