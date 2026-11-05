package session

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
)

// titleInstructionNeedle identifies the auxiliary title request among the calls a
// fake model server sees. Keep it in sync with titleInstruction.
const titleInstructionNeedle = "Name the conversation above"

type capturedCall struct {
	messages  []interface{}
	tools     string // serialized tools field
	titleCall bool
}

// titleProbe records every model request and answers: the request that ends with
// the title instruction gets a decorated title (the decoration must be stripped on
// the way in), everything else gets a plain one-line answer.
type titleProbe struct {
	mu     sync.Mutex
	calls  []capturedCall
	notice string // reply for the title request
}

func (p *titleProbe) handle(rw http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	msgs, _ := body["messages"].([]interface{})
	toolsJSON, _ := json.Marshal(body["tools"])
	isTitle := false
	if len(msgs) > 0 {
		if m, ok := msgs[len(msgs)-1].(map[string]interface{}); ok {
			if s, _ := m["content"].(string); strings.Contains(s, titleInstructionNeedle) {
				isTitle = true
			}
		}
	}
	p.mu.Lock()
	p.calls = append(p.calls, capturedCall{messages: msgs, tools: string(toolsJSON), titleCall: isTitle})
	p.mu.Unlock()

	reply := "好的"
	if isTitle {
		reply = p.notice
		if reply == "" {
			reply = "Title: **配置 nginx 的 gzip**\n后面这一行会被丢掉"
		}
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	payload, _ := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{
			{"delta": map[string]string{"content": reply}, "finish_reason": "stop"},
		},
	})
	fmt.Fprintf(rw, "data: %s\n\n", payload)
	rw.Write([]byte("data: [DONE]\n\n"))
}

func (p *titleProbe) snapshot() []capturedCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]capturedCall(nil), p.calls...)
}

func (p *titleProbe) titleCalls() int {
	n := 0
	for _, c := range p.snapshot() {
		if c.titleCall {
			n++
		}
	}
	return n
}

// lastTaskCall is the last ordinary (non-title) request: the one whose prefix the
// title request is supposed to reuse.
func (p *titleProbe) lastTaskCall() (capturedCall, bool) {
	calls := p.snapshot()
	for i := len(calls) - 1; i >= 0; i-- {
		if !calls[i].titleCall {
			return calls[i], true
		}
	}
	return capturedCall{}, false
}

func newTitleEnv(t *testing.T, probe *titleProbe) *SessionManager {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(probe.handle))
	t.Cleanup(ts.Close)
	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: "vllm", BaseURL: ts.URL, APIKey: "test",
		ModelName: "test-model", MaxTokens: 200, ContextWindow: 4096,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	mgr, err := NewSessionManager(client, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	return mgr
}

// awaitTitle polls the stored title until it becomes want (or the wait runs out,
// in which case it reports what it saw). The request is fired in the background on
// purpose, so a test can only wait for the outcome.
func awaitTitle(t *testing.T, mgr *SessionManager, sessionID, want string, wait time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		rec, err := mgr.GetStore().Get(sessionID)
		if err != nil || rec == nil {
			t.Fatalf("read session: %v", err)
		}
		if rec.Title == want || time.Now().After(deadline) {
			return rec.Title
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func titlePrompt(t *testing.T, s *Session, goal string) {
	t.Helper()
	if _, _, err := s.Prompt(context.Background(), goal, nil, 1); err != nil {
		t.Fatalf("Prompt(%q): %v", goal, err)
	}
}

// TestSessionTitleGeneratedAfterFirstTask is the whole mechanism: the placeholder
// title is replaced by the model's, the auxiliary request reuses the conversation
// prefix (that is the caching the design buys), the instruction is never
// persisted, and it happens exactly once per session.
func TestSessionTitleGeneratedAfterFirstTask(t *testing.T) {
	probe := &titleProbe{}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	// Production shape: the hub names a new session after its first message, so
	// the title here must equal the goal or the guard would read it as a rename.
	goal := "帮我给 nginx 打开 gzip 支持"
	s := mustSession(t, mgr, goal)
	titlePrompt(t, s, goal)

	if got := awaitTitle(t, mgr, s.ID(), "配置 nginx 的 gzip", 5*time.Second); got != "配置 nginx 的 gzip" {
		t.Fatalf("the title was not generated (still %q)", got)
	}
	if n := probe.titleCalls(); n != 1 {
		t.Fatalf("%d title requests, want exactly 1", n)
	}
	// The prefix: everything the task sent, then the assistant reply, then the
	// instruction. Anything else and the provider would recompute the context.
	task, ok := probe.lastTaskCall()
	if !ok {
		t.Fatal("the fake model never saw a task request")
	}
	var titleCall capturedCall
	for _, c := range probe.snapshot() {
		if c.titleCall {
			titleCall = c
		}
	}
	if len(titleCall.messages) <= len(task.messages) {
		t.Fatalf("the title request must extend the task request: %d vs %d messages",
			len(titleCall.messages), len(task.messages))
	}
	for i, m := range task.messages {
		if !reflect.DeepEqual(m, titleCall.messages[i]) {
			t.Fatalf("message %d differs, so the cached prefix was broken:\ntask  %+v\ntitle %+v", i, m, titleCall.messages[i])
		}
	}
	if titleCall.tools != task.tools {
		t.Fatalf("the tool list changed, which also breaks the prefix:\ntask  %s\ntitle %s", task.tools, titleCall.tools)
	}
	// Nothing of the auxiliary turn is persisted into the session.
	rec, err := mgr.GetStore().Get(s.ID())
	if err != nil || rec == nil {
		t.Fatalf("read session: %v", err)
	}
	for _, m := range rec.Messages {
		if strings.Contains(m.GetText(), titleInstructionNeedle) {
			t.Fatalf("the title instruction leaked into the stored conversation: %q", m.GetText())
		}
	}
	// A second task must not spend another request.
	titlePrompt(t, s, "再加一个开关")
	time.Sleep(200 * time.Millisecond)
	if n := probe.titleCalls(); n != 1 {
		t.Fatalf("%d title requests after a second task, want 1 (no retries)", n)
	}
	if rec, _ := mgr.GetStore().Get(s.ID()); rec.Title != "配置 nginx 的 gzip" {
		t.Fatalf("the generated title was overwritten: %q", rec.Title)
	}
}

// TestRenamedSessionIsPinned: a human-chosen title wins forever — no request is
// even made for it.
func TestRenamedSessionIsPinned(t *testing.T) {
	probe := &titleProbe{}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	s := mustSession(t, mgr, "先随便起的")
	if err := mgr.RenameSession(s.ID(), "我自己起的名字"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	titlePrompt(t, s, "帮我看看这个报错")
	time.Sleep(300 * time.Millisecond)
	if n := probe.titleCalls(); n != 0 {
		t.Fatalf("a renamed session still asked for a title (%d calls)", n)
	}
	rec, _ := mgr.GetStore().Get(s.ID())
	if rec.Title != "我自己起的名字" {
		t.Fatalf("the rename was overwritten: %q", rec.Title)
	}
	if got := metadataString(rec, titleSrcMetaKey); got != TitleSrcUser {
		t.Fatalf("title_src = %q, want %q", got, TitleSrcUser)
	}
}

// TestTitleOffUntilWired: the policy is off unless the runtime sets it, so tests
// (and any future wiring gap) never fire the extra request.
func TestTitleOffUntilWired(t *testing.T) {
	probe := &titleProbe{}
	mgr := newTitleEnv(t, probe)
	s := mustSession(t, mgr, "帮我把日志级别调成 warn")
	titlePrompt(t, s, "帮我把日志级别调成 warn")
	time.Sleep(200 * time.Millisecond)
	if n := probe.titleCalls(); n != 0 {
		t.Fatalf("%d title requests with the policy off", n)
	}
	rec, _ := mgr.GetStore().Get(s.ID())
	if rec.Title != "帮我把日志级别调成 warn" {
		t.Fatalf("the placeholder title changed: %q", rec.Title)
	}
}

// TestSubagentSessionNotTitled: a sub-agent session is named by its goal, and
// titling it would be a request nobody reads the result of.
func TestSubagentSessionNotTitled(t *testing.T) {
	probe := &titleProbe{}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	parent := mustSession(t, mgr, "主会话")
	parentID := parent.ID()
	child, err := mgr.CreateSessionRecord("子任务:数一下文件", "build", t.TempDir(), &parentID)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	titlePrompt(t, child, "数一下文件")
	time.Sleep(200 * time.Millisecond)
	if n := probe.titleCalls(); n != 0 {
		t.Fatalf("a sub-agent session asked for a title (%d calls)", n)
	}
}

// TestTitleReplyCapped: a rambling reply is cut to the cap, not rejected — a
// slightly too long title is still better than the first sentence of the user.
func TestTitleReplyCapped(t *testing.T) {
	probe := &titleProbe{notice: "关于 nginx gzip 配置的若干说明与追问,这里没有可直接使用的标题"}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	s := mustSession(t, mgr, "帮我写一个 bash 脚本")
	titlePrompt(t, s, "帮我写一个 bash 脚本")
	want := sanitizeTitle("关于 nginx gzip 配置的若干说明与追问,这里没有可直接使用的标题")
	if got := awaitTitle(t, mgr, s.ID(), want, 5*time.Second); got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
	if n := len([]rune(probe.notice)); n <= GeneratedTitleRunes {
		t.Fatalf("this test needs a reply longer than the cap, got %d runes", n)
	}
}

// TestTitleFailureKeepsPlaceholder: a reply that is not a title leaves the
// placeholder alone, and the one attempt stays spent — no retry on later tasks.
func TestTitleFailureKeepsPlaceholder(t *testing.T) {
	probe := &titleProbe{notice: "\n\n"} // the model answered nothing usable
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	s := mustSession(t, mgr, "帮我看看这个报错")
	titlePrompt(t, s, "帮我看看这个报错")
	time.Sleep(300 * time.Millisecond)
	rec, _ := mgr.GetStore().Get(s.ID())
	if rec.Title != "帮我看看这个报错" {
		t.Fatalf("a failed title request changed the title: %q", rec.Title)
	}
	if got := metadataString(rec, titleSrcMetaKey); got != TitleSrcTried {
		t.Fatalf("title_src = %q, want %q (the attempt is spent, never retried)", got, TitleSrcTried)
	}
	titlePrompt(t, s, "再看看第二处")
	time.Sleep(200 * time.Millisecond)
	if n := probe.titleCalls(); n != 1 {
		t.Fatalf("%d title requests, want 1 (failures are not retried)", n)
	}
}

// TestLegacyRenamedTitleIsPinned: sessions renamed before title_src existed carry
// no marker, so the shape of the title is the only evidence. A title that is not a
// truncation of the first message was typed by someone and gets pinned.
func TestLegacyRenamedTitleIsPinned(t *testing.T) {
	probe := &titleProbe{}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	s := mustSession(t, mgr, "nginx 排错") // a hand-made title, years old in DB terms
	titlePrompt(t, s, "帮我把这个报错的栈贴给你看看")
	time.Sleep(300 * time.Millisecond)
	if n := probe.titleCalls(); n != 0 {
		t.Fatalf("a hand-made title was still taken apart for a title (%d calls)", n)
	}
	rec, _ := mgr.GetStore().Get(s.ID())
	if rec.Title != "nginx 排错" {
		t.Fatalf("the hand-made title was overwritten: %q", rec.Title)
	}
	if got := metadataString(rec, titleSrcMetaKey); got != TitleSrcUser {
		t.Fatalf("title_src = %q, want %q (pinned so this is decided once)", got, TitleSrcUser)
	}
}

func TestLooksLikePlaceholder(t *testing.T) {
	long := strings.Repeat("长", 50)
	longMsg := []provider.Message{{Role: "system", Content: "s"}, {Role: "user", Content: long}}
	if !looksLikePlaceholder(string([]rune(long)[:FallbackTitleRunes])+"…", longMsg) {
		t.Fatal("a cut-off placeholder was not recognised")
	}
	if !looksLikePlaceholder("短消息", []provider.Message{{Role: "user", Content: "短消息"}}) {
		t.Fatal("a short placeholder was not recognised")
	}
	if looksLikePlaceholder("nginx 排错", longMsg) {
		t.Fatal("a hand-made title looked like a placeholder")
	}
	if looksLikePlaceholder("x", nil) {
		t.Fatal("a session without messages has nothing to compare against")
	}
	// A compaction instruction is injected, not typed by the user.
	notices := []provider.Message{
		BuildCompactRequest(""),
		{Role: "user", Content: "真实第一句"},
	}
	if !looksLikePlaceholder("真实第一句", notices) {
		t.Fatal("an injected compaction instruction must be skipped")
	}
}

func TestSanitizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Title: **配置 nginx 的 gzip**\n后面这一行会被丢掉", "配置 nginx 的 gzip"},
		{"标题：修一下 worker 列表", "修一下 worker 列表"},
		{"「修一下 worker 列表」", "修一下 worker 列表"},
		{"\"Fix the worker list.\"", "Fix the worker list"},
		{"   \n\n   真实一行   ", "真实一行"},
		{"修复\u200b渲染\u200e问题", "修复渲染问题"},
		{"带\x00控制\x1b字符", "带控制字符"},
		{"**", ""},
		{"", ""},
		{"只有一行\n还有第二行", "只有一行"},
		{"结尾带句号。", "结尾带句号"},
		{"_id 这种名字别乱切", "_id 这种名字别乱切"},
	}
	for _, c := range cases {
		if got := sanitizeTitle(c.in); got != c.want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := sanitizeTitle("配置 gzip 后带宽占用降了一半,顺手把缓存也打开了"); len([]rune(got)) != GeneratedTitleRunes {
		t.Errorf("long title not capped at %d runes: %q", GeneratedTitleRunes, got)
	}
	if got := sanitizeTitle(strings.Repeat("长", 60)); len([]rune(got)) != GeneratedTitleRunes {
		t.Errorf("cjk title not capped: %d runes", len([]rune(got)))
	}
}
