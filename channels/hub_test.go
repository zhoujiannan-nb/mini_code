package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
)

// mockLLM is a scripted OpenAI-compatible streaming (SSE) chat endpoint.
type mockLLM struct {
	calls atomic.Int64
	step  func(n int64, reqBody []byte) (content string, toolCall *mockToolCall)
}

type mockToolCall struct {
	Name string
	Args string
}

func (m *mockLLM) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		n := m.calls.Add(1)
		var content string
		var tc *mockToolCall
		if m.step != nil {
			content, tc = m.step(n, body)
		}
		var chunk map[string]interface{}
		if tc != nil {
			chunk = map[string]interface{}{
				"choices": []map[string]interface{}{{
					"delta": map[string]interface{}{
						"tool_calls": []map[string]interface{}{{
							"index":    0,
							"id":       fmt.Sprintf("call_%d", n),
							"type":     "function",
							"function": map[string]string{"name": tc.Name, "arguments": tc.Args},
						}},
					},
					"finish_reason": "tool_calls",
				}},
			}
		} else {
			chunk = map[string]interface{}{
				"choices": []map[string]interface{}{{
					"delta":         map[string]interface{}{"content": content},
					"finish_reason": "stop",
				}},
			}
		}
		b, _ := json.Marshal(chunk)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	})
}

// testEnv wires a real session manager (temp DB) + hub + mock LLM.
type testEnv struct {
	hub *Hub
	mgr *session.SessionManager
}

func newTestEnv(t *testing.T, step func(n int64, reqBody []byte) (string, *mockToolCall)) *testEnv {
	t.Helper()
	srv := httptest.NewServer((&mockLLM{step: step}).handler())
	t.Cleanup(srv.Close)

	cfg := config.ModelConfig{
		Provider:      "vllm",
		BaseURL:       srv.URL,
		APIKey:        "test",
		ModelName:     "mock",
		MaxTokens:     2048,
		ContextWindow: 16384,
	}
	client, err := provider.NewModelClient(cfg)
	if err != nil {
		t.Fatalf("model client: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	mgr, err := session.NewSessionManager(client, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })

	hub := NewHub(mgr, "build", t.TempDir(), 0)
	hub.Attach(mgr)

	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)
	t.Cleanup(cancel)

	return &testEnv{hub: hub, mgr: mgr}
}

// stepTwoTurns: turn 1 calls list_dir, turn 2 answers with text.
func stepTwoTurns(n int64, _ []byte) (string, *mockToolCall) {
	if n == 1 {
		return "", &mockToolCall{Name: "list_dir", Args: "{}"}
	}
	return "all done", nil
}

func TestHubRunOnceCreatesSession(t *testing.T) {
	env := newTestEnv(t, stepTwoTurns)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	content, sessionID, err := env.hub.RunOnce(ctx, NewUserMessage("test", "hello world"))
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if content != "all done" {
		t.Fatalf("content = %q, want %q", content, "all done")
	}
	if sessionID == "" {
		t.Fatal("expected a session to be created")
	}

	// The session must be persisted with the full conversation
	// (system prompt + user + assistant/tool turns + final reply).
	sess, err := env.mgr.Get(sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	msgs := sess.Record().Messages
	if len(msgs) != 5 {
		t.Fatalf("expected 5 persisted messages, got %d: %+v", len(msgs), msgs)
	}
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,assistant" {
		t.Fatalf("unexpected roles: %v", roles)
	}
}

func TestHubSessionKeyReuse(t *testing.T) {
	env := newTestEnv(t, stepTwoTurns)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, id1, err := env.hub.RunOnce(ctx, withKey(NewUserMessage("test", "first"), "conv-1"))
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	_, id2, err := env.hub.RunOnce(ctx, withKey(NewUserMessage("test", "second"), "conv-1"))
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	_, id3, err := env.hub.RunOnce(ctx, withKey(NewUserMessage("test", "other"), "conv-2"))
	if err != nil {
		t.Fatalf("run 3: %v", err)
	}
	_, id4, err := env.hub.RunOnce(ctx, NewUserMessage("test", "no key"))
	if err != nil {
		t.Fatalf("run 4: %v", err)
	}

	if id1 != id2 {
		t.Errorf("same conversation key should reuse session: %s != %s", id1, id2)
	}
	if id1 == id3 || id3 == id4 || id1 == id4 {
		t.Errorf("different conversations should get different sessions: %s %s %s", id1, id3, id4)
	}

	// The reused session must contain both user messages.
	sess, _ := env.mgr.Get(id1)
	userMsgs := 0
	for _, m := range sess.Record().Messages {
		if m.Role == "user" {
			userMsgs++
		}
	}
	if userMsgs != 2 {
		t.Errorf("expected 2 user messages in reused session, got %d", userMsgs)
	}
}

func TestHubExplicitSessionID(t *testing.T) {
	env := newTestEnv(t, stepTwoTurns)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, id1, err := env.hub.RunOnce(ctx, NewUserMessage("test", "one"))
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	m := NewUserMessage("test", "two")
	m.SessionID = id1
	_, id2, err := env.hub.RunOnce(ctx, m)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("explicit session_id should continue the session: %s != %s", id1, id2)
	}

	sess, _ := env.mgr.Get(id1)
	userMsgs := 0
	for _, m := range sess.Record().Messages {
		if m.Role == "user" {
			userMsgs++
		}
	}
	if userMsgs != 2 {
		t.Errorf("expected 2 user messages, got %d", userMsgs)
	}
}

// TestHubToolEventsCarrySessionKey verifies that tool events carry the
// session's channel key, so channels (e.g. DingTalk progress pings) can
// attribute them to a conversation.
func TestHubToolEventsCarrySessionKey(t *testing.T) {
	env := newTestEnv(t, stepTwoTurns) // turn 1 calls list_dir
	out := env.hub.Subscribe("key-check")
	defer env.hub.Unsubscribe("key-check")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := env.hub.RunOnce(ctx, withKey(NewUserMessage("test", "go"), "conv-key-1")); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-out:
			if !ok {
				t.Fatal("stream closed")
			}
			if m.Kind == KindToolStart && m.ToolName == "list_dir" {
				if m.SessionKey != "conv-key-1" {
					t.Fatalf("tool_start SessionKey = %q, want conv-key-1", m.SessionKey)
				}
				return
			}
		case <-deadline:
			t.Fatal("no tool_start observed")
		}
	}
}

func withKey(m Message, key string) Message {
	m.SessionKey = key
	return m
}

// TestHubActiveTaskAndCancel verifies the live task registry that the web
// UI relies on after a page refresh: a running task is reported active,
// CancelTask reports it and interrupts it, and both queries come back
// empty once the task settles with a terminal status.
func TestHubActiveTaskAndCancel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	env := newTestEnv(t, func(n int64, _ []byte) (string, *mockToolCall) {
		if n == 1 {
			<-release // hold the first LLM call open so the task stays running
		}
		return "done", nil
	})

	out := env.hub.Subscribe("active-check")
	defer env.hub.Unsubscribe("active-check")

	env.hub.Submit(NewUserMessage("test", "hang"))

	var id string
	deadline := time.After(10 * time.Second)
	for id == "" {
		select {
		case m := <-out:
			if m.Kind == KindStatus && m.Status == StatusSessionCreated {
				id = m.SessionID
			}
		case <-deadline:
			t.Fatal("no session_created observed")
		}
	}

	for i := 0; i < 400 && !env.hub.ActiveTask(id); i++ {
		time.Sleep(25 * time.Millisecond)
	}
	if !env.hub.ActiveTask(id) {
		t.Fatal("ActiveTask = false while the task is running")
	}
	if !env.hub.CancelTask(id) {
		t.Fatal("CancelTask = false while the task is running")
	}

	interrupted := false
	deadline = time.After(10 * time.Second)
	for !interrupted {
		select {
		case m := <-out:
			if m.Kind == KindStatus && m.Status == StatusInterrupted && m.SessionID == id {
				interrupted = true
			}
		case <-deadline:
			t.Fatal("no interrupted status observed after cancel")
		}
	}

	if env.hub.ActiveTask(id) {
		t.Fatal("ActiveTask = true after the task was interrupted")
	}
	if env.hub.CancelTask(id) {
		t.Fatal("CancelTask = true with no running task")
	}

	sess, err := env.mgr.Get(id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Status() != "interrupted" {
		t.Fatalf("session status = %q, want interrupted", sess.Status())
	}
}
