package web


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

	"github.com/user/mini_code/approval"
	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
)

func TestSteerWhileRunning(t *testing.T) {
	release := make(chan struct{})
	var calls int64
	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/chat/completions") && atomic.AddInt64(&calls, 1) == 1 {
			<-release
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer modelSrv.Close()

	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: "vllm", BaseURL: modelSrv.URL, APIKey: "test",
		ModelName: "test-model", MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	mgr, err := session.NewSessionManager(client, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	defer mgr.Close()
	hub := channels.NewHub(mgr, "build", t.TempDir(), 5)
	hub.Attach(mgr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)
	be := &fakeBackend{hub: hub, mgr: mgr, ap: approval.NewService(hub, mgr)}
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := mgr.CreateSessionRecord("steer-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()

	r, err := http.Post(api.URL+"/api/messages", "application/json",
		strings.NewReader(`{"session_id":"`+id+`","content":"第一条"}`))
	if err != nil {
		t.Fatalf("POST first: %v", err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("first POST status = %d, want 202", r.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !hub.ActiveTask(id) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !hub.ActiveTask(id) {
		t.Fatal("任务没有进入运行状态,测不了插话")
	}

	start := time.Now()
	r2, err := http.Post(api.URL+"/api/messages", "application/json",
		strings.NewReader(`{"session_id":"`+id+`","content":"插话一句:改用 plan B"}`))
	if err != nil {
		t.Fatalf("POST steer: %v", err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	elapsed := time.Since(start)
	if r2.StatusCode != http.StatusAccepted {
		t.Fatalf("steer status = %d, want 202: %s", r2.StatusCode, b2)
	}
	var resp map[string]string
	if err := json.Unmarshal(b2, &resp); err != nil {
		t.Fatalf("steer body %s: %v", b2, err)
	}
	if resp["mode"] != "steer" {
		t.Fatalf("steer mode = %q, want steer(body %s)", resp["mode"], b2)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("steer 没有秒回(%s):像是排队而不是注入", elapsed)
	}

	found := false
	if s, err := mgr.Get(id); err == nil {
		for _, m := range s.SnapshotMessages() {
			if m.Role == "user" && strings.Contains(m.GetText(), "plan B") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("插话没有立即写进会话历史(刷新页面会看不见)")
	}

	close(release)
	awaitConversation(t, mgr, id, 4)
	for i := 0; i < 200; i++ {
		s, err := mgr.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
			firstN, steerN, total := 0, 0, len(s.SnapshotMessages())
		for _, m := range s.SnapshotMessages() {
			if m.Role != "user" {
				continue
			}
			if strings.Contains(m.GetText(), "plan B") {
				steerN++
			}
			if strings.Contains(m.GetText(), "第一条") {
				firstN++
			}
		}
		if steerN == 1 && firstN == 1 {
			return
		}
		if i == 199 {
			var dump []string
			for _, m := range s.SnapshotMessages() {
				t2 := m.GetText()
				if len(t2) > 40 {
					t2 = t2[:40]
				}
				dump = append(dump, m.Role+": "+t2)
			}
			t.Fatalf("first=%d steer=%d (want 1/1),共 %d 条:\n%s", firstN, steerN, total, strings.Join(dump, "\n"))
		}
		time.Sleep(25 * time.Millisecond)
	}
}
