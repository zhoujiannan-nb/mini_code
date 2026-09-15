package session

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/user/mini_code/util"
)

// GroupMember is one member of a group chat.
type GroupMember struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"` // in-group name, unique within the group
	Avatar    string `json:"avatar,omitempty"`
}

// GroupRecord is a group chat: a shared background + a roster of member
// sessions. The append-only message log lives in group_messages.
type GroupRecord struct {
	GroupID    string        `json:"group_id"`
	Title      string        `json:"title"`
	Background string        `json:"background"` // shared context for every member
	Members    []GroupMember `json:"members"`
	CreatedAt  string        `json:"created_at"`
	UpdatedAt  string        `json:"updated_at"`
}

// GroupMessage is one append-only log entry of the group channel. Sender is
// a display name; SenderSession is the sending session ("" for user/system).
// Target: a member name, "group" (whole-group), "all" (user broadcast) or
// "" (system note).
type GroupMessage struct {
	ID            int64  `json:"id"`
	GroupID       string `json:"group_id"`
	Sender        string `json:"sender"`
	SenderSession string `json:"sender_session"`
	Target        string `json:"target"`
	Content       string `json:"content"`
	CreatedAt     string `json:"created_at"`
}

// GroupStore persists groups and the append-only group message log. It opens
// the same shared SQLite file as the session store; WAL + busy-timeout make
// the two connections safe to use concurrently.
type GroupStore struct {
	db *util.Database
}

// NewGroupStore opens the SQLite file at dbPath (same file as the session
// store in production) and ensures the group tables exist.
func NewGroupStore(dbPath string) (*GroupStore, error) {
	db, err := util.NewDatabase(dbPath)
	if err != nil {
		return nil, err
	}
	s := &GroupStore{db: db}
	s.initTables()
	return s, nil
}

// NewDefaultGroupStore opens the default shared database and ensures the
// group tables exist.
func NewDefaultGroupStore() (*GroupStore, error) {
	dbPath, err := util.GetDefaultDBPath()
	if err != nil {
		return nil, err
	}
	return NewGroupStore(dbPath)
}

func (s *GroupStore) initTables() {
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS groups (
		group_id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		background TEXT NOT NULL DEFAULT '',
		members TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS group_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id TEXT NOT NULL,
		sender TEXT NOT NULL,
		sender_session TEXT NOT NULL DEFAULT '',
		target TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_group_messages_group ON group_messages(group_id, id)`)
}

func (s *GroupStore) CreateGroup(rec *GroupRecord) error {
	now := time.Now().Format(time.RFC3339)
	rec.CreatedAt, rec.UpdatedAt = now, now
	if rec.Members == nil {
		rec.Members = []GroupMember{}
	}
	members, _ := json.Marshal(rec.Members)
	_, err := s.db.Execute(
		"INSERT INTO groups (group_id, title, background, members, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		rec.GroupID, rec.Title, rec.Background, string(members), rec.CreatedAt, rec.UpdatedAt)
	return err
}

func (s *GroupStore) GetGroup(groupID string) (*GroupRecord, error) {
	row, err := s.db.FetchOne(
		"SELECT group_id, title, background, members, created_at, updated_at FROM groups WHERE group_id = ?",
		groupID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("群聊不存在: %s", groupID)
	}
	return groupFromRow(row), nil
}

func (s *GroupStore) ListGroups() ([]GroupRecord, error) {
	rows, err := s.db.FetchAll(
		"SELECT group_id, title, background, members, created_at, updated_at FROM groups ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	out := make([]GroupRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, *groupFromRow(r))
	}
	return out, nil
}

// UpdateGroup persists title/background/members for an existing group.
func (s *GroupStore) UpdateGroup(rec *GroupRecord) error {
	if rec.Members == nil {
		rec.Members = []GroupMember{}
	}
	rec.UpdatedAt = time.Now().Format(time.RFC3339)
	members, _ := json.Marshal(rec.Members)
	_, err := s.db.Execute(
		"UPDATE groups SET title = ?, background = ?, members = ?, updated_at = ? WHERE group_id = ?",
		rec.Title, rec.Background, string(members), rec.UpdatedAt, rec.GroupID)
	return err
}

// DeleteGroup removes the group and its whole message log.
func (s *GroupStore) DeleteGroup(groupID string) error {
	if _, err := s.db.Execute("DELETE FROM group_messages WHERE group_id = ?", groupID); err != nil {
		return err
	}
	_, err := s.db.Execute("DELETE FROM groups WHERE group_id = ?", groupID)
	return err
}

// ClearMessages wipes the group's message log (the group record and its
// members stay untouched). Returns the number of deleted log entries.
func (s *GroupStore) ClearMessages(groupID string) (int, error) {
	res, err := s.db.Execute("DELETE FROM group_messages WHERE group_id = ?", groupID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// AppendLog appends one entry to the group channel (append-only).
func (s *GroupStore) AppendLog(m GroupMessage) error {
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().Format(time.RFC3339)
	}
	_, err := s.db.Execute(
		"INSERT INTO group_messages (group_id, sender, sender_session, target, content, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		m.GroupID, m.Sender, m.SenderSession, m.Target, m.Content, m.CreatedAt)
	return err
}

// QueryLog returns the most recent `limit` log entries in ascending id order.
func (s *GroupStore) QueryLog(groupID string, limit int) ([]GroupMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.FetchAll(
		"SELECT id, group_id, sender, sender_session, target, content, created_at FROM ("+
			"SELECT * FROM group_messages WHERE group_id = ? ORDER BY id DESC LIMIT ?) "+
			"ORDER BY id ASC",
		groupID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]GroupMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, GroupMessage{
			ID:            int64(asInt(r["id"])),
			GroupID:       asString(r["group_id"]),
			Sender:        asString(r["sender"]),
			SenderSession: asString(r["sender_session"]),
			Target:        asString(r["target"]),
			Content:       asString(r["content"]),
			CreatedAt:     asString(r["created_at"]),
		})
	}
	return out, nil
}

func (s *GroupStore) Close() error {
	return s.db.Close()
}

func groupFromRow(row map[string]interface{}) *GroupRecord {
	rec := &GroupRecord{
		GroupID:    asString(row["group_id"]),
		Title:      asString(row["title"]),
		Background: asString(row["background"]),
		CreatedAt:  asString(row["created_at"]),
		UpdatedAt:  asString(row["updated_at"]),
	}
	if m, ok := row["members"].(string); ok {
		_ = json.Unmarshal([]byte(m), &rec.Members)
	}
	if rec.Members == nil {
		rec.Members = []GroupMember{}
	}
	return rec
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}
