package session

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/util"
)

// NewDefaultSessionStore creates a new session store using the default database path (~/.mini_code/agent.db)
func NewDefaultSessionStore() (*SessionStore, error) {
	db, err := util.NewDefaultDatabase()
	if err != nil {
		return nil, err
	}
	s := &SessionStore{db: db}
	s.initDB()
	return s, nil
}

type SessionRecord struct {
	SessionID  string                 `json:"session_id"`
	ParentID   string                 `json:"parent_id"`
	Title      string                 `json:"title"`
	AgentRole  string                 `json:"agent_role"`
	WorkDir    string                 `json:"work_dir"`
	Status     string                 `json:"status"`
	ChannelKey string                 `json:"channel_key,omitempty"`
	Messages   []provider.Message     `json:"messages"`
	Metadata   map[string]interface{} `json:"metadata"`
	CreatedAt  string                 `json:"created_at"`
	UpdatedAt  string                 `json:"updated_at"`
}

func NewSessionRecord(title, agentRole, workDir, parentID string) *SessionRecord {
	now := time.Now().Format(time.RFC3339)
	return &SessionRecord{
		SessionID: util.RandomHex(12),
		ParentID:  parentID,
		Title:     title,
		AgentRole: agentRole,
		WorkDir:   workDir,
		Status:    "created",
		Messages:  []provider.Message{},
		Metadata:  map[string]interface{}{},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

type SessionStore struct {
	db *util.Database
}

func NewSessionStore(dbPath string) (*SessionStore, error) {
	db, err := util.NewDatabase(dbPath)
	if err != nil {
		return nil, err
	}
	s := &SessionStore{db: db}
	s.initDB()
	return s, nil
}

func (s *SessionStore) initDB() {
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS sessions (
		session_id TEXT PRIMARY KEY,
		parent_id TEXT,
		title TEXT DEFAULT '',
		agent_role TEXT DEFAULT 'build',
		work_dir TEXT DEFAULT '',
		status TEXT DEFAULT 'created',
		channel_key TEXT DEFAULT '',
		messages TEXT DEFAULT '[]',
		metadata TEXT DEFAULT '{}',
		created_at TEXT,
		updated_at TEXT
	)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_id)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_sessions_channel_key ON sessions(channel_key)`)
	s.migrateChannelKey()
}

// migrateChannelKey adds the channel_key column to databases created before
// the column existed. SQLite has no "ADD COLUMN IF NOT EXISTS", so we check
// the table schema first.
func (s *SessionStore) migrateChannelKey() {
	rows, err := s.db.FetchAll("PRAGMA table_info(sessions)")
	if err != nil {
		return
	}
	for _, row := range rows {
		if fmt.Sprint(row["name"]) == "channel_key" {
			return
		}
	}
	s.db.Execute("ALTER TABLE sessions ADD COLUMN channel_key TEXT DEFAULT ''")
}

// SessionSummary is a lightweight session row for list views: metadata plus
// the message count, without the (potentially large) messages payload. It
// covers root sessions only — sub-agent sessions (parent_id set) are
// internal and stay out of channel listings.
type SessionSummary struct {
	SessionID    string
	ParentID     string
	Title        string
	AgentRole    string
	WorkDir      string
	Status       string
	ChannelKey   string
	CreatedAt    string
	UpdatedAt    string
	MessageCount int
}

// ListSessionSummaries returns root-session summaries, most recently updated
// first. The message count comes from json_array_length, so conversation
// payloads are never loaded into a list response — the endpoint stays cheap
// enough for frequent polling.
func (s *SessionStore) ListSessionSummaries(limit, offset int) ([]SessionSummary, error) {
	rows, err := s.db.FetchAll(
		`SELECT session_id, parent_id, title, agent_role, work_dir, status, channel_key,
		        created_at, updated_at,
		        COALESCE(json_array_length(messages), 0) AS message_count
		 FROM sessions
		 WHERE parent_id IS NULL OR parent_id = ''
		 ORDER BY updated_at DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, SessionSummary{
			SessionID:    fmt.Sprint(row["session_id"]),
			ParentID:     fmt.Sprint(row["parent_id"]),
			Title:        fmt.Sprint(row["title"]),
			AgentRole:    fmt.Sprint(row["agent_role"]),
			WorkDir:      fmt.Sprint(row["work_dir"]),
			Status:       fmt.Sprint(row["status"]),
			ChannelKey:   fmt.Sprint(row["channel_key"]),
			CreatedAt:    fmt.Sprint(row["created_at"]),
			UpdatedAt:    fmt.Sprint(row["updated_at"]),
			MessageCount: asInt(row["message_count"]),
		})
	}
	return out, nil
}

// asInt converts a SQLite scalar (int64 via database/sql) to int, tolerating
// the float64 encoding some drivers use for INTEGER columns.
func asInt(v interface{}) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func (s *SessionStore) Create(record *SessionRecord) error {
	now := time.Now().Format(time.RFC3339)
	record.CreatedAt = now
	record.UpdatedAt = now
	msgsJSON, _ := json.Marshal(record.Messages)
	metaJSON, _ := json.Marshal(record.Metadata)
	_, err := s.db.Execute(
		`INSERT INTO sessions (session_id, parent_id, title, agent_role, work_dir, status, channel_key, messages, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.SessionID, record.ParentID, record.Title, record.AgentRole, record.WorkDir,
		record.Status, record.ChannelKey, string(msgsJSON), string(metaJSON), record.CreatedAt, record.UpdatedAt,
	)
	return err
}

func (s *SessionStore) Get(sessionID string) (*SessionRecord, error) {
	row, err := s.db.FetchOne("SELECT * FROM sessions WHERE session_id = ?", sessionID)
	if err != nil || row == nil {
		return nil, err
	}
	return s.rowToRecord(row)
}

func (s *SessionStore) Update(record *SessionRecord) error {
	record.UpdatedAt = time.Now().Format(time.RFC3339)
	msgsJSON, _ := json.Marshal(record.Messages)
	metaJSON, _ := json.Marshal(record.Metadata)
	_, err := s.db.Execute(
		`UPDATE sessions SET parent_id=?, title=?, agent_role=?, work_dir=?, status=?, channel_key=?, messages=?, metadata=?, updated_at=?
		 WHERE session_id=?`,
		record.ParentID, record.Title, record.AgentRole, record.WorkDir, record.Status, record.ChannelKey,
		string(msgsJSON), string(metaJSON), record.UpdatedAt, record.SessionID,
	)
	return err
}

// GetByChannelKey returns the most recently updated session bound to the
// given channel key, or nil if none is bound.
func (s *SessionStore) GetByChannelKey(key string) (*SessionRecord, error) {
	if key == "" {
		return nil, nil
	}
	row, err := s.db.FetchOne("SELECT * FROM sessions WHERE channel_key = ? ORDER BY updated_at DESC LIMIT 1", key)
	if err != nil || row == nil {
		return nil, err
	}
	return s.rowToRecord(row)
}

// SetChannelKey binds a channel key to a session.
func (s *SessionStore) SetChannelKey(sessionID, key string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Execute(
		"UPDATE sessions SET channel_key=?, updated_at=? WHERE session_id=?",
		key, now, sessionID,
	)
	return err
}

func (s *SessionStore) Delete(sessionID string) error {
	s.db.Execute("DELETE FROM sessions WHERE parent_id = ?", sessionID)
	_, err := s.db.Execute("DELETE FROM sessions WHERE session_id = ?", sessionID)
	return err
}

func (s *SessionStore) ListSessions(parentID, status string, limit, offset int) ([]*SessionRecord, error) {
	query := "SELECT * FROM sessions WHERE 1=1"
	var params []interface{}
	if parentID != "" {
		query += " AND parent_id = ?"
		params = append(params, parentID)
	}
	if status != "" {
		query += " AND status = ?"
		params = append(params, status)
	}
	query += " ORDER BY updated_at DESC LIMIT ? OFFSET ?"
	params = append(params, limit, offset)

	rows, err := s.db.FetchAll(query, params...)
	if err != nil {
		return nil, err
	}
	var records []*SessionRecord
	for _, row := range rows {
		r, err := s.rowToRecord(row)
		if err != nil {
			continue
		}
		records = append(records, r)
	}
	return records, nil
}

func (s *SessionStore) rowToRecord(row map[string]interface{}) (*SessionRecord, error) {
	r := &SessionRecord{
		SessionID:  fmt.Sprint(row["session_id"]),
		ParentID:   fmt.Sprint(row["parent_id"]),
		Title:      fmt.Sprint(row["title"]),
		AgentRole:  fmt.Sprint(row["agent_role"]),
		WorkDir:    fmt.Sprint(row["work_dir"]),
		Status:     fmt.Sprint(row["status"]),
		ChannelKey: fmt.Sprint(row["channel_key"]),
		CreatedAt:  fmt.Sprint(row["created_at"]),
		UpdatedAt:  fmt.Sprint(row["updated_at"]),
	}
	if msgsStr, ok := row["messages"].(string); ok && msgsStr != "" {
		json.Unmarshal([]byte(msgsStr), &r.Messages)
	}
	if metaStr, ok := row["metadata"].(string); ok && metaStr != "" {
		json.Unmarshal([]byte(metaStr), &r.Metadata)
	}
	if r.Messages == nil {
		r.Messages = []provider.Message{}
	}
	if r.Metadata == nil {
		r.Metadata = map[string]interface{}{}
	}
	return r, nil
}

func (s *SessionStore) Close() error {
	return s.db.Close()
}
