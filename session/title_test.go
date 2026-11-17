package session

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
)

const titleInstructionNeedle = "Name the request below"

type capturedCall struct {
	messages  []interface{}
	tools     string
	thinking  string
	kwargs    string
	titleCall bool
}

type titleProbe struct {
	mu     sync.Mutex
	calls  []capturedCall
	notice string
	finish string
	holdTask chan bool
	held     sync.Once
}

func (p *titleProbe) handle(rw http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	msgs, _ := body["messages"].([]interface{})
	toolsJSON, _ := json.Marshal(body["tools"])
	thinkingJSON, _ := json.Marshal(body["thinking"])
	kwargsJSON, _ := json.Marshal(body["chat_template_kwargs"])
	isTitle := false
	if len(msgs) > 0 {
		if m, ok := msgs[len(msgs)-1].(map[string]interface{}); ok {
			if s, _ := m["content"].(string); strings.Contains(s, titleInstructionNeedle) {
				isTitle = true
			}
		}
	}
	p.mu.Lock()
	p.calls = append(p.calls, capturedCall{messages: msgs, tools: string(toolsJSON), thinking: string(thinkingJSON), kwargs: string(kwargsJSON), titleCall: isTitle})
	p.mu.Unlock()

	reply := "好的"
	if isTitle {
		if p.holdTask != nil {
			p.held.Do(func() { close(p.holdTask) })
		}
		reply = p.notice
		if reply == "" {
			reply = "Title: **配置 nginx 的 gzip**\n后面这一行会被丢掉"
		}
	} else if p.holdTask != nil {
		select {
		case <-p.holdTask:
		case <-time.After(5 * time.Second):
		}
	}
	finish := "stop"
	if isTitle && p.finish != "" {
		finish = p.finish
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	payload, _ := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{
			{"delta": map[string]string{"content": reply}, "finish_reason": finish},
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
	return newTitleEnvProvider(t, probe, "vllm")
}

func newTitleEnvProvider(t *testing.T, probe *titleProbe, providerType string) *SessionManager {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(probe.handle))
	t.Cleanup(ts.Close)
	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: providerType, BaseURL: ts.URL, APIKey: "test",
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

func TestSessionTitleGeneratedAfterFirstTask(t *testing.T) {
	probe := &titleProbe{}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	goal := "帮我给 nginx 打开 gzip 支持"
	s := mustSession(t, mgr, goal)
	titlePrompt(t, s, goal)

	if got := awaitTitle(t, mgr, s.ID(), "配置 nginx 的 gzip", 5*time.Second); got != "配置 nginx 的 gzip" {
		t.Fatalf("the title was not generated (still %q)", got)
	}
	if n := probe.titleCalls(); n != 1 {
		t.Fatalf("%d title requests, want exactly 1", n)
	}
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
	if len(titleCall.messages) != 1 {
		t.Fatalf("the title request must be a single message, got %d", len(titleCall.messages))
	}
	one, _ := titleCall.messages[0].(map[string]interface{})
	if role, _ := one["role"].(string); role != "user" {
		t.Fatalf("the title request must be one user message, got role %v", one["role"])
	}
	if body, _ := one["content"].(string); !strings.Contains(body, goal) {
		t.Errorf("the title request does not quote the first message:\n%s", body)
	}
	if titleCall.tools != "null" {
		t.Errorf("the title request carried a tool list: %s (task request carries %s)", titleCall.tools, task.tools)
	}
	if len(task.tools) < 20 {
		t.Errorf("fixture sanity: the task request should carry tools to contrast with, got %q", task.tools)
	}
	rec, err := mgr.GetStore().Get(s.ID())
	if err != nil || rec == nil {
		t.Fatalf("read session: %v", err)
	}
	for _, m := range rec.Messages {
		if strings.Contains(m.GetText(), titleInstructionNeedle) {
			t.Fatalf("the title instruction leaked into the stored conversation: %q", m.GetText())
		}
	}
	titlePrompt(t, s, "再加一个开关")
	time.Sleep(200 * time.Millisecond)
	if n := probe.titleCalls(); n != 1 {
		t.Fatalf("%d title requests after a second task, want 1 (no retries)", n)
	}
	if rec, _ := mgr.GetStore().Get(s.ID()); rec.Title != "配置 nginx 的 gzip" {
		t.Fatalf("the generated title was overwritten: %q", rec.Title)
	}
}

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

func TestTitleFailureKeepsPlaceholder(t *testing.T) {
	probe := &titleProbe{notice: "\n\n"}
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

func TestLegacyRenamedTitleIsPinned(t *testing.T) {
	probe := &titleProbe{}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	s := mustSession(t, mgr, "nginx 排错")
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
		{"\"Fix the list.\"", "Fix the list"},
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

func TestTitleRequestTurnsThinkingOff(t *testing.T) {
	probe := &titleProbe{}
	mgr := newTitleEnvProvider(t, probe, "deepseek")
	mgr.SetTitlePolicy(true, "")
	goal := "帮我看看这个 nginx 的报错"
	s := mustSession(t, mgr, goal)
	titlePrompt(t, s, goal)
	if got := awaitTitle(t, mgr, s.ID(), "配置 nginx 的 gzip", 5*time.Second); got != "配置 nginx 的 gzip" {
		t.Fatalf("no title generated on a DeepSeek-shaped provider (still %q)", got)
	}

	var titleCall, taskCall capturedCall
	for _, c := range probe.snapshot() {
		if c.titleCall {
			titleCall = c
		} else {
			taskCall = c
		}
	}
	if titleCall.thinking != `{"type":"disabled"}` {
		t.Fatalf("the naming call carried thinking %s, want {\"type\":\"disabled\"}", titleCall.thinking)
	}
	if taskCall.thinking != `{"type":"enabled"}` {
		t.Errorf("the task request should keep the model's own thinking default, got %s", taskCall.thinking)
	}
}

func TestTitleRequestQuotesOnlyTheFirstMessage(t *testing.T) {
	probe := &titleProbe{}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	full := strings.Repeat("很长的报错日志 ", 120)
	placeholder := string([]rune(full)[:FallbackTitleRunes]) + "…"
	s := mustSession(t, mgr, placeholder)
	if _, _, err := s.Prompt(context.Background(), full, nil, 3); err != nil {
		t.Fatalf("Prompt(%q…): %v", full[:60], err)
	}
	if got := awaitTitle(t, mgr, s.ID(), "配置 nginx 的 gzip", 5*time.Second); got != "配置 nginx 的 gzip" {
		t.Fatalf("no title generated (still %q)", got)
	}
	var titleCall capturedCall
	for _, c := range probe.snapshot() {
		if c.titleCall {
			titleCall = c
		}
	}
	one, _ := titleCall.messages[0].(map[string]interface{})
	body, _ := one["content"].(string)
	const marker = "Request:\n"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("the request does not hold the quoted ask:\n%.120s", body)
	}
	quoted := body[i+len(marker):]
	if n := len([]rune(quoted)); n > titleInputRunes+1 {
		t.Fatalf("the quoted request is %d runes, over the cap of %d", n, titleInputRunes)
	}
	if head := string([]rune(full)[:30]); !strings.Contains(quoted, head) {
		t.Errorf("the naming request does not quote the start of the first message:\n%.120s", quoted)
	}
}

func TestTitleQuoteCaps(t *testing.T) {
	long := strings.Repeat("很长的报错日志 ", 300)
	q := titleQuote(long)
	if n := len([]rune(q)); n != titleInputRunes+1 {
		t.Errorf("titleQuote of a 2400-rune ask = %d runes, want %d", n, titleInputRunes+1)
	}
	if !strings.HasSuffix(q, "…") {
		t.Errorf("a capped quote should end with the ellipsis, got %q", q[len(q)-20:])
	}
	if got := titleQuote("修一下 worker 列表"); got != "修一下 worker 列表" {
		t.Errorf("a short ask was altered: %q", got)
	}
}

func TestTitleRequestTurnsThinkingOffOnVLLM(t *testing.T) {
	probe := &titleProbe{}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	goal := "帮我把 worker 列表排个序"
	s := mustSession(t, mgr, goal)
	titlePrompt(t, s, goal)
	if got := awaitTitle(t, mgr, s.ID(), "配置 nginx 的 gzip", 5*time.Second); got != "配置 nginx 的 gzip" {
		t.Fatalf("no title generated on the vLLM path (still %q)", got)
	}

	var titleCall, taskCall capturedCall
	for _, c := range probe.snapshot() {
		if c.titleCall {
			titleCall = c
		} else {
			taskCall = c
		}
	}
	if titleCall.kwargs != `{"enable_thinking":false}` {
		t.Errorf("the naming call carried chat_template_kwargs %s, want {\"enable_thinking\":false}", titleCall.kwargs)
	}
	if taskCall.kwargs != "null" {
		t.Errorf("the task request should carry no chat_template_kwargs, got %s", taskCall.kwargs)
	}
}

func TestTitleSurvivesATruncatedReply(t *testing.T) {
	probe := &titleProbe{finish: "length"}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	goal := "帮我把 nginx 的 gzip 打开"
	s := mustSession(t, mgr, goal)
	titlePrompt(t, s, goal)
	if got := awaitTitle(t, mgr, s.ID(), "配置 nginx 的 gzip", 5*time.Second); got != "配置 nginx 的 gzip" {
		t.Fatalf("the title in a truncated reply was thrown away: %q", got)
	}
}

func TestTitleDoesNotWaitForTheTask(t *testing.T) {
	release := make(chan bool)
	probe := &titleProbe{holdTask: release}
	mgr := newTitleEnv(t, probe)
	mgr.SetTitlePolicy(true, "")
	goal := "先看看这个报错再决定怎么改"
	s := mustSession(t, mgr, goal)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, err := s.Prompt(context.Background(), goal, nil, 1); err != nil {
			t.Errorf("Prompt(%q): %v", goal, err)
		}
	}()

	if got := awaitTitle(t, mgr, s.ID(), "配置 nginx 的 gzip", 5*time.Second); got != "配置 nginx 的 gzip" {
		t.Fatalf("no title while the task was still unanswered: %q", got)
	}
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("the task never finished after the naming call")
	}
}
