package web

import (
	"testing"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
)


func TestMessageViewsLabelsInjectedNotices(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "跑一下这 26 题"},
		{Role: "assistant", Content: "好", ToolCalls: []provider.ToolCall{{ID: "call_1", Type: "function"}}},
		{Role: "tool", Content: "ok", ToolCallID: "call_1", Name: "exec"},
		{Role: "user", Content: "<subagent id=\"abc12345\" state=\"completed\">Result: done</subagent>"},
		{Role: "user", Content: session.BuildCompactRequest("").Content},
	}
	views := messageViews(msgs)
	if len(views) != len(msgs) {
		t.Fatalf("views = %d, want one per message (%d)", len(views), len(msgs))
	}
	if views[1].Notice != "" {
		t.Fatalf("the human row carries notice %q; only injected rows may", views[1].Notice)
	}
	if views[1].Role != "user" || views[1].Content != "跑一下这 26 题" {
		t.Fatalf("human row lost its text: %+v", views[1])
	}
	if views[2].Notice != "" || len(views[2].ToolCalls) != 1 {
		t.Fatalf("assistant row lost its tool calls or got mislabeled: %+v", views[2])
	}
	if views[3].Notice != "" || views[3].ToolCallID != "call_1" || views[3].Name != "exec" {
		t.Fatalf("tool row lost its pairing: %+v", views[3])
	}
	if views[4].Notice != session.NoticeSubagent {
		t.Fatalf("sub-agent settlement notice = %q, want %q", views[4].Notice, session.NoticeSubagent)
	}
	if views[5].Notice != session.NoticeCompact {
		t.Fatalf("compaction instruction notice = %q, want %q", views[5].Notice, session.NoticeCompact)
	}
	if views[0].Role != "system" || views[0].Content != "SYSTEM" {
		t.Fatalf("system row altered: %+v", views[0])
	}
}

func TestMessageViewsKeepMultimodalContent(t *testing.T) {
	m := provider.Message{Role: "user"}
	m.SetMultimodalContent([]provider.ContentPart{
		provider.NewTextPart("看这张图"),
		provider.NewImagePart("data:image/png;base64,AAA"),
	})
	v := messageViews([]provider.Message{m})[0]
	parts, ok := v.Content.([]provider.ContentPart)
	if !ok || len(parts) != 2 {
		t.Fatalf("multimodal content flattened: %#v", v.Content)
	}
	if v.Notice != "" {
		t.Fatalf("a human multimodal row must stay unlabeled, got %q", v.Notice)
	}
}

