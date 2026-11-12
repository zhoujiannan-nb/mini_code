package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
)

// TestQueueDuringRunningTaskNoCorruption guards the record/loop slice
// aliasing fix. The turn hook used to store the loop's LIVE working slice
// straight into s.record.Messages (s.record.Messages = messages). While a
// task ran, the loop's unlocked appends and QueueUserMessage's append could
// then hit the same backing slot: queued messages got clobbered by the next
// tool result, and the json.Marshal inside store.Update could read a slot
// mid-write and persist torn garbage. A later restore (restart / next task)
// would replay a prefix that was never actually requested — cold prefix
// cache. The record must hold a COPY; the two sides may never share an array.
//
// The test drives a REAL Session.Prompt (real turn hook, real store): the
// fake model answers its first turn with an exec tool call whose command
// blocks ~5s; while the tool runs, group-style messages are queued and the
// view is snapshotted. The final persisted history must be exactly the
// expected sequence — each queued message once, tool result intact, final
// answer present — and the DB row must round-trip byte-equivalent content.
func TestQueueDuringRunningTaskNoCorruption(t *testing.T) {
	block := "sleep 5"
	if runtime.GOOS == "windows" {
		block = "ping -n 6 127.0.0.1 >nul"
	}

	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// First turn: text plus an exec tool call that blocks ~5s.
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"running the check"}}]}`)
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_exec","type":"function","function":{"name":"exec","arguments":"{\"command\":\"` + block + `\"}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			// Second turn: final answer.
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"all done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: "vllm", BaseURL: srv.URL, APIKey: "test", ModelName: "test-model",
		MaxTokens: 100, ContextWindow: 4096,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	defer client.Close()

	mgr, err := NewSessionManager(client, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	defer mgr.Close()

	sess, err := mgr.CreateSessionRecord("alias", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	// This test pins the EXACT message sequence, so the context-usage notices
	// (threshold-dependent extra user messages) are turned off: they are
	// orthogonal to the aliasing bug under test.
	sess.SetContextRemindersEnabled(false)

	type promptResult struct {
		content string
		err     error
	}
	resCh := make(chan promptResult, 1)
	go func() {
		c, _, err := sess.Prompt(context.Background(), "do the thing", nil, 5)
		resCh <- promptResult{c, err}
	}()

	// Wait until the pre-tool persist has landed (assistant reply with the
	// tool call is in the view) — the blocking command is running now.
	deadline := time.Now().Add(15 * time.Second)
	preToolSeen := false
	for time.Now().Before(deadline) && !preToolSeen {
		for _, m := range sess.SnapshotMessages() {
			if m.Role == "assistant" && m.Content == "running the check" && len(m.ToolCalls) == 1 {
				preToolSeen = true
			}
		}
		if !preToolSeen {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !preToolSeen {
		t.Fatal("timed out waiting for the assistant tool-call reply to be persisted")
	}

	// Group-style messages arrive while the task is running.
	sess.QueueUserMessage("queued one")
	sess.QueueUserMessage("queued two")

	// While the window is open the view must stay well-formed: every message
	// is one of the expected shapes. A torn read of a slot mid-write would
	// yield garbage strings here.
	expected := map[string]bool{
		"do the thing":      true,
		"running the check": true,
		"queued one":        true,
		"queued two":        true,
		"all done":          true,
	}
	until := time.Now().Add(6 * time.Second)
	for time.Now().Before(until) {
		for _, m := range sess.SnapshotMessages() {
			switch m.Role {
			case "system":
				if m.Content == "" {
					t.Fatalf("empty system message: %+v", m)
				}
			case "user", "assistant":
				// A runtime-injected notice (a job or sub-agent settlement, a
				// context reminder) is a well-formed message by design, so it is
				// not what this test looks for: a torn read of a slot mid-write,
				// which shows up as garbage text, not as an extra notice.
				if m.Role == "user" && IsInjectedNotice(m) {
					continue
				}
				if !expected[m.Content] {
					t.Fatalf("corrupted %s message content %q", m.Role, m.Content)
				}
			case "tool":
				if m.Content == "" {
					t.Fatalf("empty tool message: %+v", m)
				}
			default:
				t.Fatalf("unknown role %q", m.Role)
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("Prompt: %v", r.err)
		}
		if r.content != "all done" {
			t.Fatalf("final content = %q, want %q", r.content, "all done")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("timed out waiting for the task to finish")
	}

	// Final persisted history: exactly the expected sequence, queued
	// messages each exactly once, in queue order, after the tool result.
	final := sess.SnapshotMessages()
	wantRoles := []string{"system", "user", "assistant", "tool", "user", "user", "assistant"}
	if len(final) != len(wantRoles) {
		t.Fatalf("final history has %d messages, want %d: %+v", len(final), len(wantRoles), final)
	}
	for i, r := range wantRoles {
		if final[i].Role != r {
			t.Fatalf("final[%d].role = %q, want %q (history: %+v)", i, final[i].Role, r, final)
		}
	}
	if final[1].Content != "do the thing" ||
		final[2].Content != "running the check" || len(final[2].ToolCalls) != 1 ||
		final[4].Content != "queued one" || final[5].Content != "queued two" ||
		final[6].Content != "all done" {
		t.Fatalf("final history mismatch: %+v", final)
	}

	// The DB row must round-trip the same content (a torn marshal would show
	// up as invalid or divergent JSON in the database).
	rec, err := sess.store.Get(sess.ID())
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if len(rec.Messages) != len(final) {
		t.Fatalf("DB history has %d messages, view has %d", len(rec.Messages), len(final))
	}
	for i := range final {
		if rec.Messages[i].Role != final[i].Role || rec.Messages[i].Content != final[i].Content {
			t.Fatalf("DB/view mismatch at %d: db=%+v view=%+v", i, rec.Messages[i], final[i])
		}
	}
}
