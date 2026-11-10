package session

import (
	"encoding/json"
	"fmt"
	"sync"
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
	Mode       string                 `json:"mode,omitempty"` // "full" (default) | "cautious"
	// ModelKey references a configured model by name; empty = the default
	// model. Sub-agent sessions snapshot their parent's key at dispatch.
	ModelKey   string                 `json:"model_key,omitempty"`
	ChannelKey string                 `json:"channel_key,omitempty"`
	// WorkerID is the worker (工作大类) this session belongs to; "" = unassigned.
	// Assigning a worker gives the session its shared workspace and injects the
	// worker block (directory + convention file snapshot) into the system prompt.
	WorkerID string                 `json:"worker_id,omitempty"`
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
	// evMu serializes ledger appends within this process so seq stays
	// contiguous even when parallel tool goroutines emit events.
	evMu sync.Mutex
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
		mode TEXT DEFAULT '',
		model_key TEXT DEFAULT '',
		channel_key TEXT DEFAULT '',
		worker_id TEXT DEFAULT '',
		messages TEXT DEFAULT '[]',
		metadata TEXT DEFAULT '{}',
		created_at TEXT,
		updated_at TEXT
	)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_id)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_sessions_channel_key ON sessions(channel_key)`)
	s.migrateChannelKey()
	s.migrateMode()
	s.migrateModelKey()
	s.initEvents()
	s.initWorkers()
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

// migrateMode adds the mode column (permission mode: "" = full, "cautious")
// to databases created before the column existed.
func (s *SessionStore) migrateMode() {
	rows, err := s.db.FetchAll("PRAGMA table_info(sessions)")
	if err != nil {
		return
	}
	for _, row := range rows {
		if fmt.Sprint(row["name"]) == "mode" {
			return
		}
	}
	s.db.Execute("ALTER TABLE sessions ADD COLUMN mode TEXT DEFAULT ''")
}

// migrateModelKey adds the model_key column to databases created before
// the column existed.
func (s *SessionStore) migrateModelKey() {
	rows, err := s.db.FetchAll("PRAGMA table_info(sessions)")
	if err != nil {
		return
	}
	for _, row := range rows {
		if fmt.Sprint(row["name"]) == "model_key" {
			return
		}
	}
	s.db.Execute("ALTER TABLE sessions ADD COLUMN model_key TEXT DEFAULT ''")
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
	Mode         string
	ModelKey     string
	ChannelKey   string
	WorkerID     string
	CreatedAt    string
	UpdatedAt    string
	MessageCount int
}

// sessionListQuery is the shared lightweight list query for root sessions.
// bucket selects one worker's sessions: WorkerAllID is "everything visible"
// (unassigned plus every non-builtin worker), WorkerUnassignedID ("") is the
// un-assigned bucket, anything else is that exact worker — the built-in 已归档
// bucket is therefore only reachable by name and never shows up in "all".
// The message count comes from json_array_length, so conversation payloads are
// never loaded into a list response — the endpoint stays cheap enough for
// frequent polling.
func sessionListQuery(bucket string) string {
	predicate := "COALESCE(worker_id, '') NOT IN (SELECT worker_id FROM workers WHERE builtin = 1)"
	switch bucket {
	case WorkerUnassignedID:
		predicate = "COALESCE(worker_id, '') = ''"
	case WorkerAllID:
	default:
		predicate = "COALESCE(worker_id, '') = ?"
	}
	return `SELECT session_id, parent_id, title, agent_role, work_dir, status, mode, model_key, channel_key,
	        COALESCE(worker_id, '') AS worker_id,
	        created_at, updated_at,
	        COALESCE(json_array_length(messages), 0) AS message_count
	 FROM sessions
	 WHERE (parent_id IS NULL OR parent_id = '') AND ` + predicate +
		` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
}

// ListBucketSummaries returns one worker bucket's root sessions, most recently
// updated first. See sessionListQuery for the bucket semantics.
func (s *SessionStore) ListBucketSummaries(bucket string, limit, offset int) ([]SessionSummary, error) {
	var rows []map[string]interface{}
	var err error
	switch bucket {
	case WorkerUnassignedID, WorkerAllID:
		rows, err = s.db.FetchAll(sessionListQuery(bucket), limit, offset)
	default:
		rows, err = s.db.FetchAll(sessionListQuery(bucket), bucket, limit, offset)
	}
	if err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, summaryFromRow(row))
	}
	return out, nil
}

// ListSessionSummaries returns the default conversation list: every visible
// root session (unassigned + non-builtin workers), most recently updated first.
func (s *SessionStore) ListSessionSummaries(limit, offset int) ([]SessionSummary, error) {
	return s.ListBucketSummaries(WorkerAllID, limit, offset)
}


// ListChildren returns the sub-agent sessions (parent_id set) of one parent,
// oldest first. Used by the UI to render background sub-agents under their
// parent session.
func (s *SessionStore) ListChildren(parentID string) ([]SessionSummary, error) {
	rows, err := s.db.FetchAll(
		`SELECT session_id, parent_id, title, agent_role, work_dir, status, mode, model_key, channel_key,
		        created_at, updated_at,
		        COALESCE(json_array_length(messages), 0) AS message_count
		 FROM sessions
		 WHERE parent_id = ?
		 ORDER BY created_at ASC`,
		parentID,
	)
	if err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, summaryFromRow(row))
	}
	return out, nil
}

// ChildIDsByParent returns, in a single query, the sub-agent sessions of
// every parent (parent_id -> child session IDs). The list API uses it to
// aggregate sub-agent "running" state onto parent sessions: a sub-agent runs
// in its own child session, so when the parent's own turn has finished but
// background sub-agents are still alive, the parent must read as running.
func (s *SessionStore) ChildIDsByParent() (map[string][]string, error) {
	rows, err := s.db.FetchAll(
		`SELECT parent_id, session_id FROM sessions
		 WHERE parent_id IS NOT NULL AND parent_id != ''`,
	)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string)
	for _, row := range rows {
		pid := fmt.Sprint(row["parent_id"])
		if pid == "" || pid == "<nil>" {
			continue
		}
		out[pid] = append(out[pid], fmt.Sprint(row["session_id"]))
	}
	return out, nil
}

func summaryFromRow(row map[string]interface{}) SessionSummary {
	return SessionSummary{
		SessionID:    fmt.Sprint(row["session_id"]),
		ParentID:     fmt.Sprint(row["parent_id"]),
		Title:        fmt.Sprint(row["title"]),
		AgentRole:    fmt.Sprint(row["agent_role"]),
		WorkDir:      fmt.Sprint(row["work_dir"]),
		Status:       fmt.Sprint(row["status"]),
		Mode:         fmt.Sprint(row["mode"]),
		ModelKey:     fmt.Sprint(row["model_key"]),
		ChannelKey:   fmt.Sprint(row["channel_key"]),
		WorkerID:     asString(row["worker_id"]),
		CreatedAt:    fmt.Sprint(row["created_at"]),
		UpdatedAt:    fmt.Sprint(row["updated_at"]),
		MessageCount: asInt(row["message_count"]),
	}
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
		`INSERT INTO sessions (session_id, parent_id, title, agent_role, work_dir, status, mode, model_key, channel_key, worker_id, messages, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.SessionID, record.ParentID, record.Title, record.AgentRole, record.WorkDir,
		record.Status, record.Mode, record.ModelKey, record.ChannelKey, record.WorkerID, string(msgsJSON), string(metaJSON), record.CreatedAt, record.UpdatedAt,
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
		`UPDATE sessions SET parent_id=?, title=?, agent_role=?, work_dir=?, status=?, mode=?, model_key=?, channel_key=?, worker_id=?, messages=?, metadata=?, updated_at=?
		 WHERE session_id=?`,
		record.ParentID, record.Title, record.AgentRole, record.WorkDir, record.Status, record.Mode, record.ModelKey, record.ChannelKey, record.WorkerID,
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

// SetMode updates the permission mode of a session ("full" / "cautious").
func (s *SessionStore) SetMode(sessionID, mode string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Execute(
		"UPDATE sessions SET mode=?, updated_at=? WHERE session_id=?",
		mode, now, sessionID,
	)
	return err
}

// SetTitle renames a session. It is a targeted update (not the whole-row
// Update) so a rename can neither clobber the agent loop's concurrent
// per-turn message persistence nor be clobbered by it. It deliberately does
// not bump updated_at: a rename is a label change, not conversation
// activity, and the list order (updated_at DESC) should stay stable.
func (s *SessionStore) SetTitle(sessionID, title string) error {
	_, err := s.db.Execute(
		"UPDATE sessions SET title=? WHERE session_id=?",
		title, sessionID,
	)
	return err
}

// SetTitleSrc records where a session's title came from (see session/title.go:
// "" = the truncation fallback, "tried", "model", "user"). Only the metadata
// column is written, so — like SetTitle — this can neither clobber the agent
// loop's concurrent per-turn message persistence nor be clobbered by it, and it
// does not bump updated_at (a title is a label, not conversation activity).
func (s *SessionStore) SetTitleSrc(sessionID, src string) error {
	rec, err := s.Get(sessionID)
	if err != nil || rec == nil {
		return err
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]interface{}{}
	}
	rec.Metadata[titleSrcMetaKey] = src
	metaJSON, _ := json.Marshal(rec.Metadata)
	_, err = s.db.Execute("UPDATE sessions SET metadata=? WHERE session_id=?", string(metaJSON), sessionID)
	return err
}

// SetModelKey switches the session's model (config model name; empty =
// default model). Like SetMode it is a targeted update and bumps
// updated_at, so the session jumps to the top of the list on switch.
func (s *SessionStore) SetModelKey(sessionID, key string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Execute(
		"UPDATE sessions SET model_key=?, updated_at=? WHERE session_id=?",
		key, now, sessionID,
	)
	return err
}

func (s *SessionStore) Delete(sessionID string) error {
	// 连带删掉被删会话的事件账本:append-only 的流水留着只会是永远查不到的
	// 垃圾行(会话都删了,没人再按 id 去查它)。子会话整排删,账本也整排删。
	kids, _ := s.db.FetchAll("SELECT session_id FROM sessions WHERE parent_id = ?", sessionID)
	s.db.Execute("DELETE FROM sessions WHERE parent_id = ?", sessionID)
	_, err := s.db.Execute("DELETE FROM sessions WHERE session_id = ?", sessionID)
	s.db.Execute("DELETE FROM session_event WHERE session_id = ?", sessionID)
	for _, k := range kids {
		s.db.Execute("DELETE FROM session_event WHERE session_id = ?", fmt.Sprint(k["session_id"]))
	}
	return err
}

func (s *SessionStore) rowToRecord(row map[string]interface{}) (*SessionRecord, error) {
	r := &SessionRecord{
		SessionID:  fmt.Sprint(row["session_id"]),
		ParentID:   fmt.Sprint(row["parent_id"]),
		Title:      fmt.Sprint(row["title"]),
		AgentRole:  fmt.Sprint(row["agent_role"]),
		WorkDir:    fmt.Sprint(row["work_dir"]),
		Status:     fmt.Sprint(row["status"]),
		Mode:       fmt.Sprint(row["mode"]),
		ModelKey:   fmt.Sprint(row["model_key"]),
		ChannelKey:  fmt.Sprint(row["channel_key"]),
		WorkerID:    asString(row["worker_id"]),
		CreatedAt:   fmt.Sprint(row["created_at"]),
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
