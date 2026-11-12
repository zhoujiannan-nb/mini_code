package session

import (
	"testing"

	"github.com/user/mini_code/provider"
)

// The transcript must be able to tell the human's words apart from context the
// runtime injected as a user turn (see notices.go). Each case is built the way
// the injector builds it, so a change of wording that drops the marker fails
// here instead of silently painting a system message as the user.
func TestNoticeKind(t *testing.T) {
	cases := []struct {
		name string
		msg  provider.Message
		want string
	}{
		{"human", provider.Message{Role: "user", Content: "跑一下这 26 题"}, ""},
		{"human question", provider.Message{Role: "user", Content: "要不要现在开跑?"}, ""},
		{"worker note", BuildWorkerNotice("公约更新", noteMarker("w-1", 2)), NoticeWorker},
		{"worker join", BuildWorkerNotice("这条会话属于 worker「x」", workerNoticeJoin), NoticeWorker},
		{"context 80", usageNotice(80, 1000, 2000, 1800), NoticeContext},
		{"context 85", usageNotice(85, 1000, 2000, 1800), NoticeContext},
		{"subagent", provider.Message{Role: "user", Content: "<subagent id=\"abc12345\" state=\"completed\">Result: done</subagent>"}, NoticeSubagent},
		{"steer", provider.Message{Role: "user", Content: legacySteerText}, NoticeSteer},
		{"retry", provider.Message{Role: "user", Content: emptyRetryNotice}, NoticeRetry},
		{"summary", provider.Message{Role: "user", Content: SummaryBlock("## Goal\n…")}, NoticeSummary},
		{"assistant", provider.Message{Role: "assistant", Content: legacySteerText}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, ok := NoticeKind(c.msg)
			if c.want == "" {
				if ok {
					t.Fatalf("classified %q as %q; a human message must stay unclassified", c.msg.GetText(), kind)
				}
				return
			}
			if !ok || kind != c.want {
				t.Fatalf("kind = %q, %v; want %q", kind, ok, c.want)
			}
		})
	}
}

// TestCompactRequestIsNotice: the synthesized compaction instruction rides in as
// a user turn too, so it must carry a label.
func TestCompactRequestIsNotice(t *testing.T) {
	m := BuildCompactRequest("")
	if !IsCompactRequest(m) {
		t.Fatal("BuildCompactRequest is not recognized as a compaction request")
	}
	kind, ok := NoticeKind(m)
	if !ok || kind != NoticeCompact {
		t.Fatalf("kind = %q, %v; want %q", kind, ok, NoticeCompact)
	}
}
