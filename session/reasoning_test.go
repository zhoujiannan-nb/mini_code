package session

import (
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

func TestReasoningOverrideReachesRequest(t *testing.T) {
	type capturedBody struct {
		Thinking *struct {
			Type string `json:"type"`
		} `json:"thinking"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	var mu sync.Mutex
	var bodies []capturedBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var cb capturedBody
		_ = json.Unmarshal(b, &cb)
		mu.Lock()
		bodies = append(bodies, cb)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEChunk(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: "deepseek", BaseURL: srv.URL, APIKey: "k", ModelName: "ds",
		MaxTokens: 100, ContextWindow: 4096,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	mgr, err := NewSessionManager(client, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	defer mgr.Close()

	sess, err := mgr.CreateSessionRecord("reasoning", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	if err := mgr.SetReasoning(sess.ID(), "enabled", "max"); err != nil {
		t.Fatalf("SetReasoning: %v", err)
	}
	if _, _, err := sess.Prompt(context.Background(), "hi", nil, 1); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	mu.Lock()
	if len(bodies) != 1 {
		mu.Unlock()
		t.Fatalf("expected 1 request, got %d", len(bodies))
	}
	first := bodies[0]
	mu.Unlock()
	if first.Thinking == nil || first.Thinking.Type != "enabled" {
		t.Fatalf("thinking override not applied: %+v", first.Thinking)
	}
	if first.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort = %q, want max", first.ReasoningEffort)
	}

	if err := mgr.SetReasoning(sess.ID(), "", ""); err != nil {
		t.Fatalf("clear SetReasoning: %v", err)
	}
	if th, ef := sess.ReasoningOverride(); th != "" || ef != "" {
		t.Fatalf("ReasoningOverride after clear = (%q,%q), want empty", th, ef)
	}
	if _, _, err := sess.Prompt(context.Background(), "again", nil, 1); err != nil {
		t.Fatalf("Prompt after clear: %v", err)
	}
	mu.Lock()
	if len(bodies) != 2 {
		mu.Unlock()
		t.Fatalf("expected 2 requests, got %d", len(bodies))
	}
	second := bodies[1]
	mu.Unlock()
	if second.ReasoningEffort != "" {
		t.Fatalf("cleared override still sends reasoning_effort %q", second.ReasoningEffort)
	}
	if second.Thinking == nil || second.Thinking.Type != "enabled" {
		t.Fatalf("deepseek default should keep thinking enabled, got %+v", second.Thinking)
	}
}
