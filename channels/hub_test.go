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

func TestHubToolEventsCarrySessionKey(t *testing.T) {
	env := newTestEnv(t, stepTwoTurns)
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

func TestHubTaskStartedEvent(t *testing.T) {
	release := make(chan struct{}, 1)
	env := newTestEnv(t, func(n int64, _ []byte) (string, *mockToolCall) {
		switch n {
		case 1:
			return "created", nil
		case 2:
			<-release
			return "first done", nil
		default:
			return "second done", nil
		}
	})
	out := env.hub.Subscribe("task-started-check")
	defer env.hub.Unsubscribe("task-started-check")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, id, err := env.hub.RunOnce(ctx, NewUserMessage("test", "bootstrap"))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	first := NewUserMessage("test", "first")
	first.SessionID = id
	env.hub.Submit(first)

	deadline := time.After(10 * time.Second)
	for started := false; !started; {
		select {
		case m := <-out:
			if m.Kind == KindStatus && m.Status == StatusTaskStarted && m.TaskID == first.ID {
				if !env.hub.ActiveTask(id) {
					t.Fatal("task_started observed but ActiveTask is already false")
				}
				started = true
			}
		case <-deadline:
			t.Fatal("no task_started observed for the first task")
		}
	}

	second := NewUserMessage("test", "second")
	second.SessionID = id
	env.hub.Submit(second)

	quiet := time.After(300 * time.Millisecond)
drain:
	for {
		select {
		case m := <-out:
			if m.Kind == KindStatus && m.Status == StatusTaskStarted && m.TaskID == second.ID {
				t.Fatal("second task started before the first one finished")
			}
		case <-quiet:
			break drain
		}
	}

	select {
	case release <- struct{}{}:
	default:
	}
	sawDone := false
	deadline = time.After(15 * time.Second)
	for secondStarted := false; !secondStarted; {
		select {
		case m := <-out:
			switch {
			case m.Kind == KindStatus && m.Status == StatusTaskDone && m.TaskID == first.ID:
				sawDone = true
			case m.Kind == KindStatus && m.Status == StatusTaskStarted && m.TaskID == second.ID:
				if !sawDone {
					t.Fatal("second task_started arrived before the first task_done")
				}
				if !env.hub.ActiveTask(id) {
					t.Fatal("second task_started observed but ActiveTask is false")
				}
				secondStarted = true
			}
		case <-deadline:
			t.Fatal("no task_started for the second task after the first finished")
		}
	}

	deadline = time.After(15 * time.Second)
	for {
		select {
		case m := <-out:
			if m.Kind == KindStatus && m.Status == StatusTaskDone && m.TaskID == second.ID {
				return
			}
		case <-deadline:
			t.Fatal("second task did not finish")
		}
	}
}

func TestHubActiveTaskAndCancel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	env := newTestEnv(t, func(n int64, _ []byte) (string, *mockToolCall) {
		if n == 1 {
			<-release
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
