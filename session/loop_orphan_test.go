package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
)


func orphanCall(id, name string) provider.ToolCall {
	return provider.ToolCall{ID: id, Type: "function",
		Function: provider.FuncCall{Name: name, Arguments: `{}`}}
}

func unanswerable(msgs []provider.Message) []string {
	var bad []string
	for i, m := range msgs {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		got := 0
		for j := i + 1; j < len(msgs) && got < len(m.ToolCalls); j++ {
			if msgs[j].Role != "tool" {
				break
			}
			got++
		}
		if got != len(m.ToolCalls) {
			bad = append(bad, msgs[i].ToolCalls[0].ID)
		}
	}
	return bad
}

func TestRepairOrphanToolCalls(t *testing.T) {
	clean := []provider.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{orphanCall("a", "exec")}},
		{Role: "tool", ToolCallID: "a", Content: "done"},
		{Role: "assistant", Content: "all done"},
	}
	if got := repairOrphanToolCalls(clean); len(got) != len(clean) {
		t.Fatalf("a complete history was rewritten: %d messages", len(got))
	}

	history := []provider.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", Content: "let me look", ToolCalls: []provider.ToolCall{
			orphanCall("a", "exec"), orphanCall("b", "read_file")}},
		{Role: "tool", ToolCallID: "a", Content: "done"},
	}
	got := repairOrphanToolCalls(history)
	if len(got) != len(history)+1 {
		t.Fatalf("expected 1 inserted result, got %d messages", len(got)-len(history))
	}
	if got[0].Content != "go" || got[1].ToolCalls[0].ID != "a" || got[2].ToolCallID != "a" {
		t.Fatal("the messages before the repair were modified")
	}
	if got[3].Role != "tool" || got[3].ToolCallID != "b" {
		t.Fatalf("repair inserted the wrong message: %+v", got[3])
	}
	if bad := unanswerable(got); bad != nil {
		t.Fatalf("still unanswerable after repair: %v", bad)
	}

	parallel := []provider.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			orphanCall("x", "exec"), orphanCall("y", "exec"), orphanCall("z", "exec")}},
	}
	got = repairOrphanToolCalls(parallel)
	if len(got) != 5 {
		t.Fatalf("expected 3 inserted results, got %d", len(got)-2)
	}
	for i, id := range []string{"x", "y", "z"} {
		if got[2+i].Role != "tool" || got[2+i].ToolCallID != id {
			t.Fatalf("result %d = %+v, want tool/%s", i, got[2+i], id)
		}
	}
}

func TestOrphanHistorySurvivesReload(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEChunk(w, `{"choices":[{"delta":{"content":"ok","finish_reason":"stop"}}]}`)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: "vllm", BaseURL: srv.URL, APIKey: "k", ModelName: "probe",
		MaxTokens: 512, ContextWindow: 32768,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	defer client.Close()

	db := filepath.Join(t.TempDir(), "agent.db")
	mgr, err := NewSessionManager(client, db)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	sess, err := mgr.CreateSessionRecord("orphan reload", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	sid := sess.ID()
	sess.mu.Lock()
	sess.record.Messages = []provider.Message{
		{Role: "user", Content: "跑一个长命令"},
		{Role: "assistant", Content: "我看一下", ToolCalls: []provider.ToolCall{orphanCall("call_dead", "exec")}},
	}
	sess.store.Update(sess.record)
	sess.mu.Unlock()
	mgr.Close()

	mgr2, err := NewSessionManager(client, db)
	if err != nil {
		t.Fatalf("reopen manager: %v", err)
	}
	defer mgr2.Close()
	sess2, err := mgr2.Get(sid)
	if err != nil || sess2 == nil {
		t.Fatalf("reload session: %v", err)
	}
	if _, _, err := sess2.Prompt(context.Background(), "继续", nil, 2); err != nil {
		t.Fatalf("prompt after the simulated crash: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatal("no request was sent")
	}
	var req struct {
		Messages []provider.Message `json:"messages"`
	}
	if err := json.Unmarshal(bodies[0], &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if bad := unanswerable(req.Messages); bad != nil {
		t.Errorf("the request carries unanswered tool calls %v: every server rejects this", bad)
	}
	if !bytes.Contains(bodies[0], []byte("call_dead")) {
		t.Fatal("the repaired call disappeared from the request entirely")
	}
	var sys, user bool
	for _, m := range req.Messages {
		switch {
		case m.Role == "system":
			sys = true
		case m.Role == "user" && m.Content == "跑一个长命令":
			user = true
		}
	}
	if !sys || !user {
		t.Fatal("the cached prefix (system + original user message) was not preserved")
	}
}
