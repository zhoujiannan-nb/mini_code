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
		{"steer", provider.Message{Role: "user", Content: continueSteerText}, NoticeSteer},
		{"retry", provider.Message{Role: "user", Content: emptyRetryNotice}, NoticeRetry},
		{"summary", provider.Message{Role: "user", Content: SummaryBlock("## Goal\n…")}, NoticeSummary},
		{"assistant", provider.Message{Role: "assistant", Content: continueSteerText}, ""},
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

// TestAwaitingUserInput: the guard may not steer a reply that handed the
// decision back — that false positive is what nagged the session twice.
func TestAwaitingUserInput(t *testing.T) {
	handoff := []string{
		"给我这四个答案,我就开跑。要哪个?",
		"四个待定项列在上面的表格里了,给我答案我就开跑。",
		"三个待定还没你答复,我不擅自开装 codex、不起跑。",
		"要我把它改成默认关掉吗?",
		"要不要我现在动手改这两处?",
		"等你确认后我再继续。",
		"Which of the two should I pin — the old version or the shim?",
		"I will run the batch once you confirm. Let me know.",
		"需要我继续吗?请拍板。",
	}
	for _, s := range handoff {
		if !awaitingUserInput(s) {
			t.Errorf("not seen as a hand-off: %q", s)
		}
	}
	// A narration-only stop must still reach the judge.
	unfinished := []string{
		"Let me grep the night scripts to confirm what is A/B-ed.",
		"Okay, I will write the script now and run it.",
		"我先把这个函数的调用点全找出来,然后改判分器。",
	}
	for _, s := range unfinished {
		if awaitingUserInput(s) {
			t.Errorf("a plain narration must not be treated as a hand-off: %q", s)
		}
	}
}
