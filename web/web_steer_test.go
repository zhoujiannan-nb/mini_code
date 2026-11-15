package web

// 插话(steering):会话正在跑任务时,纯文本消息注入到运行中的循环,
// 而不是排队等任务结束。测试用一台"卡住"的 mock 模型把任务按住,
// 期间 POST /api/messages 必须秒回 {"mode":"steer"}(走注入而不是挤会话锁),
// 消息以写前置形式立刻进历史,模型放开后照常完成。

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
	// 第一次"模型对话"调用卡在 release 上(任务被按住),之后的调用立刻回 done。
	// 只数 chat/completions:NewModelClient 可能先发 models 探测请求,别让它偷走闸口。
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

	// 第一条消息 → 任务开始,模型卡住不放。
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

	// 运行中补一句:必须秒回 mode=steer(挤会话锁的旧路径要等模型放开才有空处理)。
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

	// 写前置持久化:不等任务结束,历史里已经能读到这句。
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

	// 放开模型,任务收尾;插话不另起任务:原任务一句、插话一句,各自恰好一条
	// (worker 公约等运行时注入也是 user 行,所以按文本点名计数,不数总数)。
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
			return // 恰好各一条:注入没有重复持久化
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
