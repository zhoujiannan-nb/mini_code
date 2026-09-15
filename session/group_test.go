package session

import (
	"strings"
	"sync"
	"testing"
)

// deliveryRecorder collects idle-session deliveries made through the manager
// deliver hook ("sessionID\ttext" entries).
type deliveryRecorder struct {
	mu sync.Mutex
	dr []string
}

func (r *deliveryRecorder) record(id, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dr = append(r.dr, id+"\t"+text)
}

func (r *deliveryRecorder) forSession(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.dr {
		if strings.SplitN(e, "\t", 2)[0] == id {
			out = append(out, strings.SplitN(e, "\t", 2)[1])
		}
	}
	return out
}

// newTestGroupEnv builds a session manager + group service over one temp DB,
// with the deliver hook recorded instead of dispatched to a hub.
func newTestGroupEnv(t *testing.T) (*SessionManager, *GroupService, *deliveryRecorder) {
	t.Helper()
	dbPath := t.TempDir() + "/agent.db"
	mgr, err := NewSessionManager(nil, dbPath)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	gs, err := NewGroupStore(dbPath)
	if err != nil {
		t.Fatalf("NewGroupStore: %v", err)
	}
	svc := NewGroupService(gs, mgr)
	rec := &deliveryRecorder{}
	mgr.SetDeliverHook(rec.record)
	mgr.SetGroups(svc)
	t.Cleanup(func() { svc.Close() })
	return mgr, svc, rec
}

func mustSession(t *testing.T, mgr *SessionManager, title string) *Session {
	t.Helper()
	s, err := mgr.CreateSessionRecord(title, "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	return s
}

// TestGroupJoinLeaveRename covers membership lifecycle: join (explicit and
// random name), duplicate-name rejection, rename, leave, delete — and the
// session metadata that drives group_chat tool registration.
func TestGroupJoinLeaveRename(t *testing.T) {
	mgr, svc, rec := newTestGroupEnv(t)
	a := mustSession(t, mgr, "coder")
	b := mustSession(t, mgr, "reviewer")

	grp, err := svc.Create("后端攻坚群", "目标: 修复支付模块 bug")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	m, err := svc.Join(grp.GroupID, a.ID(), "coder")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if m.Name != "coder" {
		t.Fatalf("member name = %q, want coder", m.Name)
	}
	// Join delivers one orientation message to the new member.
	if got := rec.forSession(a.ID()); len(got) != 1 || !strings.Contains(got[0], "你已加入群聊") {
		t.Fatalf("orientation delivery = %q", got)
	}
	// Metadata drives tool registration + shared background.
	if gid, _ := a.Record().Metadata["group_id"].(string); gid != grp.GroupID {
		t.Fatalf("metadata group_id = %q", gid)
	}
	if name, _ := a.Record().Metadata["group_name"].(string); name != "coder" {
		t.Fatalf("metadata group_name = %q", name)
	}

	// Duplicate in-group name is rejected.
	if _, err := svc.Join(grp.GroupID, b.ID(), "coder"); err == nil {
		t.Fatal("expected duplicate-name error")
	}
	// Second join with an empty name gets a random 哈基-N号.
	m2, err := svc.Join(grp.GroupID, b.ID(), "")
	if err != nil {
		t.Fatalf("Join(random): %v", err)
	}
	if !strings.HasPrefix(m2.Name, "哈基-") || !strings.HasSuffix(m2.Name, "号") {
		t.Fatalf("random name = %q", m2.Name)
	}
	// A session can belong to only one group at a time.
	if _, err := svc.Join(grp.GroupID, a.ID(), "x"); err == nil {
		t.Fatal("expected already-in-group error")
	}

	// Rename the member.
	if err := svc.RenameMember(grp.GroupID, b.ID(), "哈基二号"); err != nil {
		t.Fatalf("RenameMember: %v", err)
	}
	if name, _ := b.Record().Metadata["group_name"].(string); name != "哈基二号" {
		t.Fatalf("metadata after rename = %q", name)
	}

	// Leave clears the metadata.
	if err := svc.Leave(grp.GroupID, b.ID()); err != nil {
		t.Fatalf("Leave: %v", err)
	}
	if gid, _ := b.Record().Metadata["group_id"].(string); gid != "" {
		t.Fatalf("metadata after leave = %q", gid)
	}

	// Group rename.
	if err := svc.Rename(grp.GroupID, "新群名"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	rec2, _ := svc.Get(grp.GroupID)
	if rec2.Title != "新群名" {
		t.Fatalf("group title = %q", rec2.Title)
	}

	// Delete removes the group and clears remaining members.
	if err := svc.DeleteGroup(grp.GroupID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, err := svc.Get(grp.GroupID); err == nil {
		t.Fatal("expected error after delete")
	}
	if gid, _ := a.Record().Metadata["group_id"].(string); gid != "" {
		t.Fatalf("metadata after group delete = %q", gid)
	}
}

// TestGroupMessageFlow covers the channel: user broadcast, targeted user
// message, member sends (to member / whole group / self / unknown), the
// append-only log, and running-session queueing.
func TestGroupMessageFlow(t *testing.T) {
	mgr, svc, rec := newTestGroupEnv(t)
	a := mustSession(t, mgr, "coder")
	b := mustSession(t, mgr, "reviewer")
	grp, _ := svc.Create("群A", "背景X")
	if _, err := svc.Join(grp.GroupID, a.ID(), "coder"); err != nil {
		t.Fatalf("Join a: %v", err)
	}
	if _, err := svc.Join(grp.GroupID, b.ID(), "reviewer"); err != nil {
		t.Fatalf("Join b: %v", err)
	}
	rec.dr = nil // drop the orientation deliveries

	// User broadcast reaches every member with a source prefix.
	n, err := svc.UserMessage(grp.GroupID, "大家看下进度", "")
	if err != nil || n != 2 {
		t.Fatalf("UserMessage broadcast: n=%d err=%v", n, err)
	}
	for _, id := range []string{a.ID(), b.ID()} {
		if got := rec.forSession(id); len(got) != 1 || !strings.Contains(got[0], "【群聊·用户】大家看下进度") {
			t.Fatalf("broadcast to %s = %q", id[:8], got)
		}
	}

	// User message @member in the MIDDLE of the text reaches only that member.
	if n, err := svc.UserMessage(grp.GroupID, "帮我看下 @reviewer 这个", ""); err != nil || n != 1 {
		t.Fatalf("UserMessage @mid: n=%d err=%v", n, err)
	}
	if got := rec.forSession(b.ID()); !strings.Contains(got[len(got)-1], "【群聊·用户@reviewer】帮我看下 @reviewer 这个") {
		t.Fatalf("@mid delivery to b = %q", got)
	}
	if got := len(rec.forSession(a.ID())); got != 1 {
		t.Fatalf("@mid must not reach other members: %q", rec.forSession(a.ID()))
	}

	// User message with an explicit target (API compat) still works.
	if n, err := svc.UserMessage(grp.GroupID, "reviewer 看下这个", "reviewer"); err != nil || n != 1 {
		t.Fatalf("UserMessage targeted: n=%d err=%v", n, err)
	}
	if got := rec.forSession(b.ID()); !strings.Contains(got[len(got)-1], "【群聊·用户@reviewer】") {
		t.Fatalf("targeted delivery = %q", got)
	}

	api := svc.ToolAPIFor(a.ID())

	// Member → member.
	out := api.Send("reviewer", "接口改好了,请 review")
	if !strings.Contains(out, "已发给 reviewer") {
		t.Fatalf("Send to member: %q", out)
	}
	got := rec.forSession(b.ID())
	if !strings.Contains(got[len(got)-1], "【群聊·coder】接口改好了") {
		t.Fatalf("member delivery = %q", got)
	}

	// Member → whole group WITHOUT @: display-only, nobody is notified.
	aBefore := len(rec.forSession(a.ID()))
	bBefore := len(rec.forSession(b.ID()))
	if out := api.Send("group", "今晚 8 点同步"); !strings.Contains(out, "仅群内展示") {
		t.Fatalf("Send to group (no mention): %q", out)
	}
	if got := len(rec.forSession(a.ID())); got != aBefore {
		t.Fatalf("sender must not be re-notified: %q", rec.forSession(a.ID()))
	}
	if got := len(rec.forSession(b.ID())); got != bBefore {
		t.Fatalf("plain group send must not notify other members: %q", rec.forSession(b.ID()))
	}

	// Member → group with @member in the text: only that member is notified.
	bBefore = len(rec.forSession(b.ID()))
	if out := api.Send("group", "@reviewer 帮我看下这段"); !strings.Contains(out, "通知了被 @ 的成员") || !strings.Contains(out, "reviewer") {
		t.Fatalf("Send group with mention: %q", out)
	}
	if got := rec.forSession(b.ID()); len(got) != bBefore+1 || !strings.Contains(got[len(got)-1], "【群聊·coder】@reviewer 帮我看下这段") {
		t.Fatalf("mention delivery to b = %q", got)
	}
	if got := len(rec.forSession(a.ID())); got != aBefore {
		t.Fatalf("mention send must not reach other members: %q", rec.forSession(a.ID()))
	}

	// Member → group @user (not a group member): display-only.
	bBefore = len(rec.forSession(b.ID()))
	if out := api.Send("group", "@至高王 搞定了"); !strings.Contains(out, "仅群内展示") {
		t.Fatalf("Send group @user: %q", out)
	}
	if got := len(rec.forSession(b.ID())); got != bBefore {
		t.Fatalf("@user must not notify members: %q", rec.forSession(b.ID()))
	}

	// Member @-ing itself in a group send: nobody (incl. itself) notified.
	if out := api.Send("group", "@coder 我自言自语"); !strings.Contains(out, "仅群内展示") {
		t.Fatalf("Send group @self: %q", out)
	}

	// Member → self / unknown target.
	if out := api.Send("coder", "hi"); !strings.Contains(out, "你自己") {
		t.Fatalf("Send to self: %q", out)
	}
	if out := api.Send("ghost", "hi"); !strings.Contains(out, "不存在") {
		t.Fatalf("Send to unknown: %q", out)
	}

	// The append-only log carries user + member entries.
	log, err := svc.Log(grp.GroupID, 100)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	var sawBroadcast, sawMember, sawMentionGroup, sawUserMention bool
	for _, m := range log {
		if m.Sender == "user" && m.Target == "all" && strings.Contains(m.Content, "大家看下进度") {
			sawBroadcast = true
		}
		if m.Sender == "coder" && m.Target == "reviewer" && strings.Contains(m.Content, "接口改好了") {
			sawMember = true
		}
		if m.Sender == "coder" && m.Target == "reviewer" && strings.Contains(m.Content, "@reviewer 帮我看下这段") {
			sawMentionGroup = true
		}
		if m.Sender == "user" && m.Target == "reviewer" && strings.Contains(m.Content, "帮我看下 @reviewer 这个") {
			sawUserMention = true
		}
	}
	if !sawBroadcast || !sawMember || !sawMentionGroup || !sawUserMention {
		t.Fatalf("log missing entries: broadcast=%v member=%v mentionGroup=%v userMention=%v",
			sawBroadcast, sawMember, sawMentionGroup, sawUserMention)
	}

	// History tool renders the log.
	if h := api.History(10); !strings.Contains(h, "coder→reviewer") {
		t.Fatalf("History: %q", h)
	}

	// Running member: the message is queued for the next loop turn, not
	// dispatched as a task.
	b.Record().Status = "running"
	before := len(rec.dr)
	if err := mgr.DeliverToSession(b.ID(), "【群聊·用户】running 时的消息"); err != nil {
		t.Fatalf("DeliverToSession: %v", err)
	}
	if len(rec.dr) != before {
		t.Fatal("running session must not be dispatched")
	}
	if got := b.DrainUserMessages(); len(got) != 1 || got[0] != "【群聊·用户】running 时的消息" {
		t.Fatalf("drained = %q", got)
	}
}

// TestDeliverStaleRunningStatus verifies that a persisted "running" status
// with no live loop behind it (stale after a restart/crash) does not swallow
// deliveries: with the live task check wired, an idle-in-reality session is
// dispatched normally, while a genuinely live one still queues.
func TestDeliverStaleRunningStatus(t *testing.T) {
	mgr, _, rec := newTestGroupEnv(t)
	s := mustSession(t, mgr, "stale")
	s.Record().Status = "running" // stale row: nothing actually running

	mgr.SetActiveTaskCheck(func(string) bool { return false })
	if err := mgr.DeliverToSession(s.ID(), "stale 投递"); err != nil {
		t.Fatalf("DeliverToSession: %v", err)
	}
	if got := rec.forSession(s.ID()); len(got) != 1 || got[0] != "stale 投递" {
		t.Fatalf("stale-running session must be dispatched, got %q", got)
	}
	if got := s.DrainUserMessages(); len(got) != 0 {
		t.Fatalf("stale-running session must not queue, drained %q", got)
	}

	mgr.SetActiveTaskCheck(func(string) bool { return true })
	if err := mgr.DeliverToSession(s.ID(), "live 投递"); err != nil {
		t.Fatalf("DeliverToSession: %v", err)
	}
	if got := s.DrainUserMessages(); len(got) != 1 || got[0] != "live 投递" {
		t.Fatalf("live session must queue, drained = %q", got)
	}
}

// TestGroupToolAPIScope covers the tool API surface: info/members reflect
// live group data, and everything degrades gracefully after the group is
// deleted mid-task.
func TestGroupToolAPIScope(t *testing.T) {
	mgr, svc, _ := newTestGroupEnv(t)
	a := mustSession(t, mgr, "coder")
	grp, _ := svc.Create("群B", "背景Y")
	if _, err := svc.Join(grp.GroupID, a.ID(), "coder"); err != nil {
		t.Fatalf("Join: %v", err)
	}
	api := svc.ToolAPIFor(a.ID())

	if api.GroupTitle() != "群B" {
		t.Fatalf("GroupTitle = %q", api.GroupTitle())
	}
	if api.OwnName() != "coder" {
		t.Fatalf("OwnName = %q", api.OwnName())
	}
	if info := api.Info(); !strings.Contains(info, "背景Y") || !strings.Contains(info, "coder") {
		t.Fatalf("Info: %q", info)
	}
	if mem := api.Members(); !strings.Contains(mem, "(你)") {
		t.Fatalf("Members: %q", mem)
	}

	// After the group is deleted the tool degrades to text, not a panic.
	_ = svc.DeleteGroup(grp.GroupID)
	for name, fn := range map[string]func() string{
		"Info": api.Info, "Members": api.Members, "History": func() string { return api.History(5) },
		"Send": func() string { return api.Send("group", "x") },
	} {
		if out := fn(); !strings.Contains(out, "不存在") && !strings.Contains(out, "不在任何群聊") {
			t.Fatalf("%s after delete: %q", name, out)
		}
	}
}

// TestGroupMemberAvatar covers avatar assignment at join (group-unique while
// avatars are free) and the SetMemberAvatar validation/update path.
func TestGroupMemberAvatar(t *testing.T) {
	// ValidAvatarID boundaries.
	for id, ok := range map[string]bool{
		"a001": true, "a050": true, "a100": true,
		"a000": false, "a101": false, "b001": false, "": false, "../x": false,
	} {
		if ValidAvatarID(id) != ok {
			t.Fatalf("ValidAvatarID(%q) = %v, want %v", id, !ok, ok)
		}
	}

	mgr, svc, _ := newTestGroupEnv(t)
	grp, err := svc.Create("头像群", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	a := mustSession(t, mgr, "coder-a")
	b := mustSession(t, mgr, "coder-b")

	m1, err := svc.Join(grp.GroupID, a.ID(), "A")
	if err != nil {
		t.Fatalf("Join A: %v", err)
	}
	if !ValidAvatarID(m1.Avatar) {
		t.Fatalf("A avatar = %q, want valid", m1.Avatar)
	}
	m2, err := svc.Join(grp.GroupID, b.ID(), "B")
	if err != nil {
		t.Fatalf("Join B: %v", err)
	}
	if m2.Avatar == "" || m2.Avatar == m1.Avatar {
		t.Fatalf("B avatar = %q, want valid and different from A", m2.Avatar)
	}

	// Change A's avatar to a valid id that is not the current one.
	want := "a001"
	if m1.Avatar == want {
		want = "a002"
	}
	if err := svc.SetMemberAvatar(grp.GroupID, a.ID(), want); err != nil {
		t.Fatalf("SetMemberAvatar: %v", err)
	}
	got, err := svc.Get(grp.GroupID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	found := ""
	for _, m := range got.Members {
		if m.SessionID == a.ID() {
			found = m.Avatar
		}
	}
	if found != want {
		t.Fatalf("avatar after change = %q, want %q", found, want)
	}

	// Invalid / non-member cases are rejected.
	if err := svc.SetMemberAvatar(grp.GroupID, a.ID(), "a999"); err == nil {
		t.Fatal("SetMemberAvatar accepted invalid id")
	}
	if err := svc.SetMemberAvatar(grp.GroupID, a.ID(), "a%03d"); err == nil {
		t.Fatal("SetMemberAvatar accepted formatted id")
	}
	c := mustSession(t, mgr, "coder-c")
	if err := svc.SetMemberAvatar(grp.GroupID, c.ID(), want); err == nil {
		t.Fatal("SetMemberAvatar accepted non-member")
	}
}
