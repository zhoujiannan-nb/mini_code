package session

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/user/mini_code/util"
)

// GroupService manages group chats: membership (join/leave/rename), the
// append-only group log, and message delivery to member sessions.
//
// A group is a public channel plus tools: every member session gets the
// group_chat tool (registered per-task from its metadata) and shares the
// group background. Messages reach member sessions as user messages —
// queued for the next agent-loop turn when the member is running, otherwise
// dispatched as a normal task via the SessionManager deliver hook (which the
// runtime wires to the message hub).
type GroupService struct {
	store *GroupStore
	mgr   *SessionManager
	mu    sync.Mutex // serializes group mutations (members JSON read-modify-write)
}

func NewGroupService(store *GroupStore, mgr *SessionManager) *GroupService {
	return &GroupService{store: store, mgr: mgr}
}

const (
	maxGroupTitleLen = 60
	maxMemberNameLen = 20
	maxGroupMsgLen   = 20000

	// avatarCount is the number of built-in group avatars shipped in
	// web/ui/avatars/aNNN.svg. Groups never exceed this many members, so a
	// fresh join can usually get a group-unique avatar.
	avatarCount = 100
)

// avatarID formats an avatar number (1..avatarCount) into its id ("a001"...).
func avatarID(n int) string { return fmt.Sprintf("a%03d", n) }

// ValidAvatarID reports whether id refers to a built-in avatar.
func ValidAvatarID(id string) bool {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "a"))
	return err == nil && n >= 1 && n <= avatarCount
}

// randomAvatarID picks a random avatar id, preferring one not already used by
// another group member (groups have at most avatarCount members).
func randomAvatarID(used map[string]bool) string {
	roll := func() string { return avatarID(1 + int(util.RandomHex(1)[0])%avatarCount) }
	if len(used) < avatarCount {
		for i := 0; i < avatarCount*2; i++ {
			id := roll()
			if !used[id] {
				return id
			}
		}
	}
	return roll() // all avatars taken: allow a duplicate
}

// Create creates a group with the given title/background and returns it.
func (g *GroupService) Create(title, background string) (*GroupRecord, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("群名称不能为空")
	}
	if len([]rune(title)) > maxGroupTitleLen {
		return nil, fmt.Errorf("群名称过长(最多 %d 字)", maxGroupTitleLen)
	}
	rec := &GroupRecord{
		GroupID:    "grp_" + util.RandomHex(8),
		Title:      title,
		Background: strings.TrimSpace(background),
		Members:    []GroupMember{},
	}
	if err := g.store.CreateGroup(rec); err != nil {
		return nil, err
	}
	slog.Info("group created", "group", rec.GroupID, "title", title)
	return rec, nil
}

func (g *GroupService) Get(groupID string) (*GroupRecord, error) {
	return g.store.GetGroup(groupID)
}

func (g *GroupService) List() ([]GroupRecord, error) {
	return g.store.ListGroups()
}

// Log returns the recent group channel entries (ascending).
func (g *GroupService) Log(groupID string, limit int) ([]GroupMessage, error) {
	return g.store.QueryLog(groupID, limit)
}

// SetBackground updates the shared group background.
func (g *GroupService) SetBackground(groupID, background string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, err := g.store.GetGroup(groupID)
	if err != nil {
		return err
	}
	if len([]rune(background)) > maxGroupMsgLen {
		return fmt.Errorf("群背景过长(最多 %d 字)", maxGroupMsgLen)
	}
	rec.Background = strings.TrimSpace(background)
	return g.store.UpdateGroup(rec)
}

// Rename changes the group's title.
func (g *GroupService) Rename(groupID, title string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, err := g.store.GetGroup(groupID)
	if err != nil {
		return err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("群名称不能为空")
	}
	if len([]rune(title)) > maxGroupTitleLen {
		return fmt.Errorf("群名称过长(最多 %d 字)", maxGroupTitleLen)
	}
	rec.Title = title
	return g.store.UpdateGroup(rec)
}

// Join adds a session to a group. name may be empty (a random "哈基-N号" is
// assigned). The member receives one orientation user message; from its next
// task on the group_chat tool is auto-registered.
func (g *GroupService) Join(groupID, sessionID, name string) (*GroupMember, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, err := g.store.GetGroup(groupID)
	if err != nil {
		return nil, err
	}
	s, err := g.mgr.Get(sessionID)
	if err != nil || s == nil {
		return nil, fmt.Errorf("会话不存在: %s", sessionID)
	}
	if gid, _ := s.Record().Metadata["group_id"].(string); gid != "" {
		return nil, fmt.Errorf("该会话已在群聊 %q 中,请先移出", gid)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = randomMemberName()
	}
	if len([]rune(name)) > maxMemberNameLen {
		return nil, fmt.Errorf("群内名称过长(最多 %d 字)", maxMemberNameLen)
	}
	for _, m := range rec.Members {
		if m.Name == name {
			return nil, fmt.Errorf("群内名称 %q 已存在", name)
		}
	}
	used := make(map[string]bool, len(rec.Members))
	for _, m := range rec.Members {
		used[m.Avatar] = true
	}
	rec.Members = append(rec.Members, GroupMember{SessionID: sessionID, Name: name,
		Avatar: randomAvatarID(used)})
	if err := g.store.UpdateGroup(rec); err != nil {
		return nil, err
	}
	member := rec.Members[len(rec.Members)-1]
	s.SetGroupMembership(groupID, name)
	g.store.AppendLog(GroupMessage{GroupID: groupID, Sender: "system", Content: name + " 加入了群聊"})
	bg := rec.Background
	if bg == "" {
		bg = "(未设置)"
	}
	orientation := fmt.Sprintf(
		"你已加入群聊 %q,你在群里的名称是 %q。\n群背景:\n%s\n"+
			"使用 group_chat 工具查看群情况(info=群信息, members=成员, history=群聊历史)和发消息: "+
			"target 传 'group' 的消息只在群聊中展示(用户可见,不通知其他成员);"+
			"要让某个成员收到并处理,用 target=该成员群内名称(或在正文中写 @该成员名)。"+
			"重要结论、需要用户知道的信息,建议主动用 group_chat 发到群里。",
		rec.Title, name, bg)
	g.deliver(sessionID, orientation)
	slog.Info("group: session joined", "group", groupID, "session", sessionID, "name", name)
	return &member, nil
}

// Leave removes a session from a group (its history is untouched).
func (g *GroupService) Leave(groupID, sessionID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, err := g.store.GetGroup(groupID)
	if err != nil {
		return err
	}
	name := ""
	found := false
	kept := rec.Members[:0]
	for _, m := range rec.Members {
		if m.SessionID == sessionID {
			name = m.Name
			found = true
			continue
		}
		kept = append(kept, m)
	}
	if !found {
		return fmt.Errorf("该会话不在这个群里")
	}
	rec.Members = kept
	if err := g.store.UpdateGroup(rec); err != nil {
		return err
	}
	if s, err := g.mgr.Get(sessionID); err == nil && s != nil {
		s.SetGroupMembership("", "")
	}
	g.store.AppendLog(GroupMessage{GroupID: groupID, Sender: "system", Content: name + " 退出了群聊"})
	return nil
}

// RenameMember changes a member's in-group name (and the group_name recorded
// in the member session's metadata).
func (g *GroupService) RenameMember(groupID, sessionID, newName string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, err := g.store.GetGroup(groupID)
	if err != nil {
		return err
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("群内名称不能为空")
	}
	if len([]rune(newName)) > maxMemberNameLen {
		return fmt.Errorf("群内名称过长(最多 %d 字)", maxMemberNameLen)
	}
	oldName := ""
	found := false
	for i := range rec.Members {
		if rec.Members[i].SessionID != sessionID {
			continue
		}
		if rec.Members[i].Name == newName {
			return nil
		}
		for _, m := range rec.Members {
			if m.SessionID != sessionID && m.Name == newName {
				return fmt.Errorf("群内名称 %q 已存在", newName)
			}
		}
		oldName = rec.Members[i].Name
		rec.Members[i].Name = newName
		found = true
		break
	}
	if !found {
		return fmt.Errorf("该会话不在这个群里")
	}
	if err := g.store.UpdateGroup(rec); err != nil {
		return err
	}
	if s, err := g.mgr.Get(sessionID); err == nil && s != nil {
		s.SetGroupMembership(groupID, newName)
	}
	g.store.AppendLog(GroupMessage{GroupID: groupID, Sender: "system",
		Content: fmt.Sprintf("%s 改名为 %s", oldName, newName)})
	return nil
}

// SetMemberAvatar changes a member's in-group avatar. avatar must be one of
// the built-in avatars (a001..a100, see web/ui/avatars). Display-only: it is
// not written to the group log.
func (g *GroupService) SetMemberAvatar(groupID, sessionID, avatar string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, err := g.store.GetGroup(groupID)
	if err != nil {
		return err
	}
	if !ValidAvatarID(avatar) {
		return fmt.Errorf("无效头像: %s", avatar)
	}
	for i := range rec.Members {
		if rec.Members[i].SessionID != sessionID {
			continue
		}
		if rec.Members[i].Avatar == avatar {
			return nil
		}
		rec.Members[i].Avatar = avatar
		if err := g.store.UpdateGroup(rec); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("该会话不在这个群里")
}

// DeleteGroup removes a group and its log; member sessions keep their own
// history (they just lose the group background/tool). Refuses while any
// member task is running.
func (g *GroupService) DeleteGroup(groupID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, err := g.store.GetGroup(groupID)
	if err != nil {
		return err
	}
	for _, m := range rec.Members {
		if s, err := g.mgr.Get(m.SessionID); err == nil && s != nil && g.mgr.TaskRunning(m.SessionID) {
			return fmt.Errorf("成员 %s 有任务在运行,请先停止再删群", m.Name)
		}
	}
	for _, m := range rec.Members {
		if s, err := g.mgr.Get(m.SessionID); err == nil && s != nil {
			s.SetGroupMembership("", "")
		}
	}
	return g.store.DeleteGroup(groupID)
}

// ClearMessages is the "delete group records" action from group settings:
// the group's message log is wiped, a system note marks the clear point, and
// the group itself (title/background/members) is kept. Running member tasks
// are unaffected — they simply see a fresh channel afterwards. Member private
// session histories are not touched (each agent keeps what it saw). Returns
// the number of deleted entries.
func (g *GroupService) ClearMessages(groupID string) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := g.store.GetGroup(groupID); err != nil {
		return 0, err
	}
	n, err := g.store.ClearMessages(groupID)
	if err != nil {
		return 0, err
	}
	g.store.AppendLog(GroupMessage{GroupID: groupID, Sender: "system",
		Content: "群记录已清空(成员私聊会话历史不受影响)"})
	return n, nil
}

// mentionedMembers returns the members @-mentioned in content: an occurrence
// of "@" directly followed by a member's in-group name, anywhere in the text.
// Order follows the member list; each member appears at most once.
func mentionedMembers(content string, members []GroupMember) []GroupMember {
	var out []GroupMember
	for _, m := range members {
		if strings.Contains(content, "@"+m.Name) {
			out = append(out, m)
		}
	}
	return out
}

// UserMessage is the web composer in a group view: content is written to the
// group channel and delivered to members with a source prefix.
// Delivery rules:
//   - target is a member's in-group name -> only that member;
//   - target is ""/"group"/"all"/"全群" and the content @mentions one or more
//     members -> only the mentioned members;
//   - otherwise -> broadcast to every member.
// Returns the number of members notified.
func (g *GroupService) UserMessage(groupID, content, target string) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, err := g.store.GetGroup(groupID)
	if err != nil {
		return 0, err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return 0, fmt.Errorf("消息不能为空")
	}
	if len([]rune(content)) > maxGroupMsgLen {
		return 0, fmt.Errorf("消息过长(最多 %d 字)", maxGroupMsgLen)
	}
	target = strings.TrimSpace(target)
	if target == "" || target == "group" || target == "all" || target == "全群" {
		// 正文 @成员名(任意位置):只投递被 @ 的成员;无 @ 才全群广播。
		if ms := mentionedMembers(content, rec.Members); len(ms) > 0 {
			names := make([]string, 0, len(ms))
			for _, m := range ms {
				names = append(names, m.Name)
			}
			g.store.AppendLog(GroupMessage{GroupID: groupID, Sender: "user", Target: strings.Join(names, "/"), Content: content})
			n := 0
			for _, m := range ms {
				g.deliver(m.SessionID, fmt.Sprintf("【群聊·用户@%s】%s", m.Name, content))
				n++
			}
			return n, nil
		}
		g.store.AppendLog(GroupMessage{GroupID: groupID, Sender: "user", Target: "all", Content: content})
		n := 0
		for _, m := range rec.Members {
			g.deliver(m.SessionID, fmt.Sprintf("【群聊·用户】%s", content))
			n++
		}
		return n, nil
	}
	var member *GroupMember
	for i := range rec.Members {
		if rec.Members[i].Name == target {
			member = &rec.Members[i]
			break
		}
	}
	if member == nil {
		return 0, fmt.Errorf("群内成员 %q 不存在", target)
	}
	g.store.AppendLog(GroupMessage{GroupID: groupID, Sender: "user", Target: target, Content: content})
	g.deliver(member.SessionID, fmt.Sprintf("【群聊·用户@%s】%s", target, content))
	return 1, nil
}

// memberSend is the group_chat tool's send action: log entry + delivery.
// senderName/senderSession identify the agent sending; it is never
// delivered back to itself.
func (g *GroupService) memberSend(groupID, senderName, senderSession, target, content string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, err := g.store.GetGroup(groupID)
	if err != nil {
		return "群聊已不存在(可能被删除)。"
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "content 不能为空。"
	}
	if r := []rune(content); len(r) > maxGroupMsgLen {
		content = string(r[:maxGroupMsgLen])
	}
	target = strings.TrimSpace(target)
	if target == senderName {
		return "那是你自己;通知他人请发给 'group'(仅群内展示)或其他成员的群内名称。"
	}
	if target == "" || target == "group" || target == "all" {
		// 群发消息:默认仅群内展示,不投递任何其他成员;
		// 正文 @成员名 时只投递被 @ 的成员(不含发送者自己)。
		ms := mentionedMembers(content, rec.Members)
		names := make([]string, 0, len(ms))
		for _, m := range ms {
			if m.SessionID != senderSession {
				names = append(names, m.Name)
			}
		}
		logTarget := "group"
		if len(names) > 0 {
			logTarget = strings.Join(names, "/")
		}
		g.store.AppendLog(GroupMessage{GroupID: groupID, Sender: senderName, SenderSession: senderSession, Target: logTarget, Content: content})
		n := 0
		for _, m := range ms {
			if m.SessionID == senderSession {
				continue
			}
			g.deliver(m.SessionID, fmt.Sprintf("【群聊·%s】%s", senderName, content))
			n++
		}
		if n == 0 {
			return "已发到群里(仅群内展示,不通知其他成员)。"
		}
		return fmt.Sprintf("已发到群里,并通知了被 @ 的成员:%s。", strings.Join(names, "、"))
	}
	var member *GroupMember
	for i := range rec.Members {
		if rec.Members[i].Name == target {
			member = &rec.Members[i]
			break
		}
	}
	if member == nil {
		names := make([]string, 0, len(rec.Members))
		for _, m := range rec.Members {
			names = append(names, m.Name)
		}
		return fmt.Sprintf("群内成员 %q 不存在。当前群内名称: %s", target, strings.Join(names, " / "))
	}
	if member.SessionID == senderSession {
		return "那是你自己;通知他人请发给 'group'(全群)或其他成员的群内名称。"
	}
	g.store.AppendLog(GroupMessage{GroupID: groupID, Sender: senderName, SenderSession: senderSession, Target: target, Content: content})
	g.deliver(member.SessionID, fmt.Sprintf("【群聊·%s】%s", senderName, content))
	state := ""
	if g.mgr.TaskRunning(member.SessionID) {
		state = " (对方正在运行,消息将在其下一轮收到)"
	}
	return fmt.Sprintf("已发给 %s。%s", target, state)
}

// deliver hands a user message to a member session: queued for the next
// agent-loop turn when the session is running, otherwise dispatched as a
// normal task via the manager's deliver hook (message hub).
func (g *GroupService) deliver(sessionID, text string) {
	if err := g.mgr.DeliverToSession(sessionID, text); err != nil {
		slog.Warn("group: delivery failed", "session", sessionID, "err", err)
	}
}

// Close releases the database handle.
func (g *GroupService) Close() error { return g.store.Close() }

// randomMemberName returns a random in-group name like "哈基-123号".
func randomMemberName() string {
	b := util.RandomHex(2)
	return fmt.Sprintf("哈基-%d号", int(b[0])%1000)
}

// ---------------------------------------------------------------------------
// group_chat tool backend (implements tools.GroupChatDispatcher)
// ---------------------------------------------------------------------------

// GroupToolAPI is the group_chat tool backend bound to one member session.
// It always reads live data, so mid-task membership changes are reflected.
type GroupToolAPI struct {
	g         *GroupService
	sessionID string
}

// ToolAPIFor returns the group_chat backend bound to a member session.
func (g *GroupService) ToolAPIFor(sessionID string) *GroupToolAPI {
	return &GroupToolAPI{g: g, sessionID: sessionID}
}

func (a *GroupToolAPI) groupRef() (rec *GroupRecord, ownName string, err error) {
	s, err := a.g.mgr.Get(a.sessionID)
	if err != nil || s == nil {
		return nil, "", fmt.Errorf("会话不存在")
	}
	meta := s.Record().Metadata
	gid, _ := meta["group_id"].(string)
	own, _ := meta["group_name"].(string)
	if gid == "" {
		return nil, "", fmt.Errorf("你已不在任何群聊中(可能已退群或群被删除)")
	}
	rec, err = a.g.store.GetGroup(gid)
	if err != nil {
		return nil, "", fmt.Errorf("群聊已不存在(可能被删除)")
	}
	return rec, own, nil
}

func (a *GroupToolAPI) GroupTitle() string {
	rec, _, err := a.groupRef()
	if err != nil {
		return "(群不存在)"
	}
	return rec.Title
}

func (a *GroupToolAPI) OwnName() string {
	_, own, err := a.groupRef()
	if err != nil {
		return "(未知)"
	}
	return own
}

func (a *GroupToolAPI) Info() string {
	rec, own, err := a.groupRef()
	if err != nil {
		return err.Error()
	}
	bg := rec.Background
	if bg == "" {
		bg = "(未设置)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "群名称: %s\n群背景:\n%s\n\n成员(%d):\n", rec.Title, bg, len(rec.Members))
	for _, m := range rec.Members {
		mark := ""
		if m.Name == own {
			mark = " (你)"
		}
		status := "空闲"
		if a.g.mgr.TaskRunning(m.SessionID) {
			status = "运行中"
		}
		fmt.Fprintf(&b, "- %s%s · %s\n", m.Name, mark, status)
	}
	return b.String()
}

func (a *GroupToolAPI) Members() string {
	rec, own, err := a.groupRef()
	if err != nil {
		return err.Error()
	}
	if len(rec.Members) == 0 {
		return "群里还没有成员。"
	}
	var b strings.Builder
	for _, m := range rec.Members {
		mark := ""
		if m.Name == own {
			mark = " (你)"
		}
		status := "离线"
		title := ""
		if s, err := a.g.mgr.Get(m.SessionID); err == nil && s != nil {
			title = s.Record().Title
			if a.g.mgr.TaskRunning(m.SessionID) {
				status = "运行中"
			} else {
				status = "空闲"
			}
		}
		fmt.Fprintf(&b, "- %s%s: %s(会话 %q)\n", m.Name, mark, status, title)
	}
	return b.String()
}

func (a *GroupToolAPI) History(limit int) string {
	rec, _, err := a.groupRef()
	if err != nil {
		return err.Error()
	}
	rows, err := a.g.store.QueryLog(rec.GroupID, limit)
	if err != nil {
		return "读取历史失败: " + err.Error()
	}
	if len(rows) == 0 {
		return "群里还没有聊天记录。"
	}
	var b strings.Builder
	for _, m := range rows {
		ts := m.CreatedAt
		if len(ts) >= 16 {
			ts = ts[5:16] // "MM-DD HH:MM"
		}
		to := ""
		if m.Target != "" && m.Target != "all" && m.Target != "group" {
			to = "→" + m.Target
		}
		fmt.Fprintf(&b, "[%s] %s%s: %s\n", ts, m.Sender, to, m.Content)
	}
	return b.String()
}

func (a *GroupToolAPI) Send(target, content string) string {
	rec, own, err := a.groupRef()
	if err != nil {
		return err.Error()
	}
	return a.g.memberSend(rec.GroupID, own, a.sessionID, target, content)
}
