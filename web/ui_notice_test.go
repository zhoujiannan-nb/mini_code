package web

import (
	"testing"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
)

// The transcript labels runtime-injected user turns (session.NoticeKind) so the
// page can draw them as system lines instead of putting words in the user's
// mouth — a 公约 broadcast, a context-usage reminder, a sub-agent settlement or a
// continue-guard steer all arrive with role:"user", and the model must keep
// reading exactly the same bytes.

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
	// The system prompt still rides along unchanged (it is the model's prompt,
	// and the UI filter drops it by role, not by label).
	if views[0].Role != "system" || views[0].Content != "SYSTEM" {
		t.Fatalf("system row altered: %+v", views[0])
	}
}

// TestMessageViewsKeepMultimodalContent: a message with image parts must still
// reach the browser as a content-part array (the composer renders screenshots
// from it), not as a flattened string.
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

// The front-end half of this rule ("an injected user-role row must be drawn as
// a runtime notice, never as the user's own message") used to be asserted here
// by grepping addNotice/addCard out of the old renderer. The UI is a Preact app
// now, so the assertion lives where the behaviour does — two jsdom tests, one
// per path:
//
//	web/ui/test/render.test.mjs → "七类运行时注入的 user 轮渲染成 notice,不冒充用户"
//	web/ui/test/render.test.mjs → "后端已标 notice 的轮优先级高于 marker 探测"
//
// and the classifier itself is covered by test/messages.test.mjs. The Go half
// (the label the API hands the UI in messageViews) stays tested above.
