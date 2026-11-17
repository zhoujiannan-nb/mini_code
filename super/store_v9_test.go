package super

// v9 采集层存储层测试:Source/Ticket/Binding 三表的状态机、FIFO 序、
// 宽限期回收——这些是决策路由全部踩在其上的地基。

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/user/mini_code/util"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := util.NewDatabase(filepath.Join(t.TempDir(), "super_test.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() }) // Windows: 不关掉句柄 TempDir 删不掉
	return NewStore(db)
}

func TestSourceRoundtrip(t *testing.T) {
	s := newTestStore(t)
	src := &Source{Kind: "git-watch", Name: "上游", ConfigJSON: `{"path":"D:\\x"}`, Enabled: true, PollSec: 300}
	if err := s.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	if src.ID[:4] != "src-" || src.TTL != "3d" || src.BindMode != "auto" {
		t.Fatalf("defaults not applied: %+v", src)
	}
	// 首轮:基线静默推进游标
	if err := s.SourceSetResult(src.ID, nowStr(), "aaa1111122223333", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSource(src.ID)
	if got.Cursor != "aaa1111122223333" || got.ErrCount != 0 {
		t.Fatalf("cursor not advanced: %+v", got)
	}
	// 失败:错误计数累加,游标不动
	if err := s.SourceSetResult(src.ID, nowStr(), "", "ls-remote timeout"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSource(src.ID)
	if got.Cursor != "aaa1111122223333" || got.ErrCount != 1 || got.LastErr == "" {
		t.Fatalf("failure not recorded: %+v", got)
	}
	// 成功清零
	_ = s.SourceSetResult(src.ID, nowStr(), "", "")
	got, _ = s.GetSource(src.ID)
	if got.ErrCount != 0 {
		t.Fatal("err_count not cleared")
	}
	if err := s.SourceSetEnabled(src.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSource(src.ID)
	if got.Enabled {
		t.Fatal("toggle off failed")
	}
	ls, _ := s.ListSources()
	if len(ls) != 1 {
		t.Fatalf("list = %d", len(ls))
	}
}

func TestTicketStateMachine(t *testing.T) {
	s := newTestStore(t)
	a := &Ticket{SrcID: "src-a", Text: "事件甲", State: "queued"}
	b := &Ticket{Text: "人工乙", State: "queued", Actor: "super"}
	if err := s.CreateTicket(a); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTicket(b); err != nil {
		t.Fatal(err)
	}
	// FIFO:创建序即出队序,排队工单携带 src_id(出队找得回源)
	q, err := s.TicketQueuedFIFO(8)
	if err != nil || len(q) != 2 || q[0].ID != a.ID || q[0].SrcID != "src-a" {
		t.Fatalf("fifo = %+v err=%v", q, err)
	}
	if n, _ := s.TicketCountState("queued"); n != 2 {
		t.Fatalf("queued count = %d", n)
	}
	_ = s.TicketSetState(a.ID, "running", "sess-1")
	if n, _ := s.TicketCountState("running"); n != 1 {
		t.Fatalf("running count = %d", n)
	}
	act, _ := s.TicketActiveBySrc("src-a")
	if act == nil || act.ID != a.ID {
		t.Fatal("active by src failed")
	}
	bySess, _ := s.TicketActiveBySession("sess-1")
	if bySess == nil || bySess.ID != a.ID {
		t.Fatal("active by session failed")
	}
	_ = s.TicketSetState(a.ID, "ready", "")
	_ = s.TicketSetState(a.ID, "closed", "")
	got, _ := s.GetTicket(a.ID)
	if got.State != "closed" || got.ClosedAt == "" {
		t.Fatalf("closed stamp missing: %+v", got)
	}
	// resume:从 closed 回到 running 记一次 reopen
	_ = s.TicketSetState(a.ID, "running", "sess-1")
	got, _ = s.GetTicket(a.ID)
	if got.Reopened != 1 {
		t.Fatalf("reopen not counted: %+v", got)
	}
}

func TestBindingLifecycle(t *testing.T) {
	s := newTestStore(t)
	if err := s.BindSource(&Binding{SrcID: "src-a", Sess: "sess-1", Binder: "auto"}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.GetBinding("src-a")
	if b == nil || b.State != "active" {
		t.Fatalf("bind failed: %+v", b)
	}
	// 退订 → 宽限期内未过期:不回收
	if err := s.BindingUnsubscribe("src-a", false); err != nil {
		t.Fatal(err)
	}
	b, _ = s.GetBinding("src-a")
	if b.State != "unsubscribed" || b.UnsubscribedAt == "" {
		t.Fatalf("unsubscribe failed: %+v", b)
	}
	future := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	s.db.Execute("UPDATE super_binding SET unsubscribed_at = ? WHERE src_id = ?", future, "src-a")
	if dropped, _ := s.BindingDropExpired(time.Now().Format(time.RFC3339)); len(dropped) != 0 {
		t.Fatalf("grace period not honored: %+v", dropped)
	}
	// 宽限已过 → 回收
	s.db.Execute("UPDATE super_binding SET unsubscribed_at = ? WHERE src_id = ?",
		time.Now().Add(-73*time.Hour).Format(time.RFC3339), "src-a")
	dropped, err := s.BindingDropExpired(time.Now().Add(-72 * time.Hour).Format(time.RFC3339))
	if err != nil || len(dropped) != 1 || dropped[0] != "src-a" {
		t.Fatalf("expiry drop failed: %+v err=%v", dropped, err)
	}
	// 永久宽限:过期也不回收
	_ = s.BindSource(&Binding{SrcID: "src-b", Sess: "sess-2", Binder: "human"})
	_ = s.BindingUnsubscribe("src-b", true)
	s.db.Execute("UPDATE super_binding SET unsubscribed_at = ? WHERE src_id = ?",
		time.Now().Add(-365*24*time.Hour).Format(time.RFC3339), "src-b")
	if dropped, _ := s.BindingDropExpired(time.Now().Add(-72 * time.Hour).Format(time.RFC3339)); len(dropped) != 0 {
		t.Fatalf("forever binding was dropped: %+v", dropped)
	}
	// 复订(宽限内事件再达)
	_ = s.BindSource(&Binding{SrcID: "src-c", Sess: "sess-3", Binder: "auto"})
	_ = s.BindingUnsubscribe("src-c", false)
	if err := s.BindingReactivate("src-c"); err != nil {
		t.Fatal(err)
	}
	b, _ = s.GetBinding("src-c")
	if b.State != "active" || b.UnsubscribedAt != "" {
		t.Fatalf("reactivate failed: %+v", b)
	}
	// 删源连坐删绑定
	_ = s.CreateSource(&Source{ID: "src-c", Kind: "git-watch", Name: "c"})
	_ = s.DeleteSource("src-c")
	if b, _ := s.GetBinding("src-c"); b != nil {
		t.Fatal("binding survived source delete")
	}
}

func TestGitWatchCfgGuards(t *testing.T) {
	k := &gitWatchKind{}
	// path 必填 / 目录必须存在
	if _, err := k.Probe(nil, map[string]interface{}{}); err == nil {
		t.Fatal("empty path must fail probe")
	}
	if _, err := k.Probe(nil, map[string]interface{}{"path": "D:\\definitely-not-here-9x7"}); err == nil {
		t.Fatal("missing dir must fail probe")
	}
	if k.Enabled() != true || k.Name() != "git-watch" {
		t.Fatal("registry descriptor wrong")
	}
	if _, err := GetKind("timer"); err == nil {
		t.Fatal("预留 kind 不应可解析")
	}
	if ds := KindDescriptors(); len(ds) != 1 || ds[0]["name"] != "git-watch" {
		t.Fatalf("descriptors = %+v", ds)
	}
	if short("0123456789abcdef") != "01234567" || short("abc") != "abc" {
		t.Fatal("short() broken")
	}
}
