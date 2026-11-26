package session

import (
	"testing"

	"github.com/user/mini_code/provider"
)

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
