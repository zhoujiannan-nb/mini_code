package web

import (
	"strings"
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

// TestUITranscriptRendersNotices: the front-end must branch on the label. It
// used to call addUser() for every role:"user" row, which is how an injected
// broadcast ended up drawn as the user's own message.
func TestUITranscriptRendersNotices(t *testing.T) {
	src := uiSource(t)
	for _, want := range []string{
		"if (msg.notice) { addNotice(msg.notice, txt); return; }", // the branch, ahead of addUser
		"function addNotice(kind, text)",
		"NOTICE_LABELS",
		"addCard(\"notice\"",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("the UI no longer renders injected notices (missing %q)", want)
		}
	}
	// The notice card must not be drawn with the user's avatar or "你" tag.
	i := strings.Index(src, "function addNotice")
	if i < 0 {
		t.Fatal("addNotice missing")
	}
	block := src[i : i+900]
	for _, bad := range []string{"userAvatarBox", "m-tag user"} {
		if strings.Contains(block, bad) {
			t.Errorf("addNotice draws %q; an injected line must not impersonate the user", bad)
		}
	}
}
