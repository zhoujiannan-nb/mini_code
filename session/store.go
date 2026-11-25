package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/util"
)

func NewDefaultSessionStore() (*SessionStore, error) {
	db, err := util.NewDefaultDatabase()
	if err != nil {
		return nil, err
	}
	s := &SessionStore{db: db, spillDir: filepath.Join(filepath.Dir(db.Path()), "spill")}
	s.initDB()
	s.startJanitor()
	return s, nil
}

type SessionRecord struct {
	SessionID  string                 `json:"session_id"`
	ParentID   string                 `json:"parent_id"`
	Title      string                 `json:"title"`
	AgentRole  string                 `json:"agent_role"`
	WorkDir    string                 `json:"work_dir"`
	Status     string                 `json:"status"`
	Mode       string                 `json:"mode,omitempty"`
	ModelKey   string                 `json:"model_key,omitempty"`
	ChannelKey string                 `json:"channel_key,omitempty"`
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
	evLocks [64]sync.Mutex
	spillDir string
	janitorStop chan struct{}
	janitorOnce sync.Once
}

const janitorInterval = 6 * time.Hour

const eventRetention = 90 * 24 * time.Hour

const orphanGrace = 24 * time.Hour

func (s *SessionStore) startJanitor() {
	s.janitorStop = make(chan struct{})
	go s.runJanitor()
	go func() {
		t := time.NewTicker(janitorInterval)
		defer t.Stop()
		for {
			select {
			case <-s.janitorStop:
				return
			case <-t.C:
				s.runJanitor()
			}
		}
	}()
}

func (s *SessionStore) runJanitor() {
	if n, err := s.PruneEvents(time.Now().Add(-eventRetention)); err != nil {
		slog.Warn("ledger retention pass failed", "err", err)
	} else if n > 0 {
		slog.Info("ledger retention pass removed events", "rows", n, "retention_days", int(eventRetention.Hours()/24))
	}
	ev, msg, err := s.PruneOrphans(time.Now().Add(-orphanGrace))
	if err != nil {
		slog.Warn("orphan sweep failed", "err", err)
	} else if ev+msg > 0 {
		slog.Info("orphan sweep removed rows of deleted sessions", "events", ev, "messages", msg)
		if _, err := s.db.Execute("PRAGMA incremental_vacuum(16384)"); err != nil {
			slog.Warn("incremental vacuum failed", "err", err)
		}
	}
	cleanupSpillDir(s.spillDir)
}

func (s *SessionStore) stopJanitor() {
	s.janitorOnce.Do(func() {
		if s.janitorStop != nil {
			close(s.janitorStop)
		}
	})
}

func NewSessionStore(dbPath string) (*SessionStore, error) {
	db, err := util.NewDatabase(dbPath)
	if err != nil {
		return nil, err
	}
	s := &SessionStore{db: db, spillDir: filepath.Join(filepath.Dir(dbPath), "spill")}
	s.initDB()
	s.startJanitor()
	return s, nil
}

func (s *SessionStore) evLock(sessionID string) *sync.Mutex {
	var h uint32 = 2166136261
	for i := 0; i < len(sessionID); i++ {
		h = (h ^ uint32(sessionID[i])) * 16777619
	}
	return &s.evLocks[h%uint32(len(s.evLocks))]
}

func (s *SessionStore) SpillDir() string { return s.spillDir }

func (s *SessionStore) DB() *util.Database { return s.db }

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
	s.migrateMessageCount()
	s.initMessages()
	s.initEvents()
	s.initWorkers()
}

func (s *SessionStore) initMessages() {
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS session_message (
		session_id TEXT NOT NULL,
		seq        INTEGER NOT NULL,
		payload    TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (session_id, seq)
	)`)
}

func (s *SessionStore) migrateMessageCount() {
	rows, err := s.db.FetchAll("PRAGMA table_info(sessions)")
	if err != nil {
		return
	}
	for _, row := range rows {
		if fmt.Sprint(row["name"]) == "message_count" {
			return
		}
	}
	s.db.Execute("ALTER TABLE sessions ADD COLUMN message_count INTEGER DEFAULT 0")
}

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
	TotalPrompt     int
	TotalCompletion int
}

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
	        CASE WHEN COALESCE(message_count, 0) > 0 THEN message_count
	             ELSE COALESCE(json_array_length(messages), 0) END AS message_count,
	        COALESCE(json_extract(metadata, '$.usage_total.prompt'), 0) AS total_prompt,
	        COALESCE(json_extract(metadata, '$.usage_total.completion'), 0) AS total_completion
	 FROM sessions
	 WHERE (parent_id IS NULL OR parent_id = '') AND ` + predicate +
		` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
}

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

func (s *SessionStore) ListSessionSummaries(limit, offset int) ([]SessionSummary, error) {
	return s.ListBucketSummaries(WorkerAllID, limit, offset)
}


func (s *SessionStore) ListChildren(parentID string) ([]SessionSummary, error) {
	rows, err := s.db.FetchAll(
		`SELECT session_id, parent_id, title, agent_role, work_dir, status, mode, model_key, channel_key,
		        created_at, updated_at,
		        CASE WHEN COALESCE(message_count, 0) > 0 THEN message_count
	             ELSE COALESCE(json_array_length(messages), 0) END AS message_count,
		        COALESCE(json_extract(metadata, '$.usage_total.prompt'), 0) AS total_prompt,
		        COALESCE(json_extract(metadata, '$.usage_total.completion'), 0) AS total_completion
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

type ChildAgentRow struct {
	SessionID   string
	Name        string
	Description string
	Goal        string
	Role        string
	Status      string
	CreatedAt   string
}

func (s *SessionStore) ListChildAgents(parentID string) ([]ChildAgentRow, error) {
	rows, err := s.db.FetchAll(
		`SELECT session_id, title, agent_role, status, metadata, created_at
		 FROM sessions
		 WHERE parent_id = ?
		 ORDER BY created_at ASC`,
		parentID,
	)
	if err != nil {
		return nil, err
	}
	out := make([]ChildAgentRow, 0, len(rows))
	for _, row := range rows {
		r := ChildAgentRow{
			SessionID: fmt.Sprint(row["session_id"]),
			Role:      fmt.Sprint(row["agent_role"]),
			Status:    fmt.Sprint(row["status"]),
			CreatedAt: asString(row["created_at"]),
		}
		meta := map[string]interface{}{}
		if raw, ok := row["metadata"].(string); ok && raw != "" {
			_ = json.Unmarshal([]byte(raw), &meta)
		}
		r.Name = metadataString(&SessionRecord{Metadata: meta}, "agent_name")
		r.Description = metadataString(&SessionRecord{Metadata: meta}, "agent_description")
		r.Goal = metadataString(&SessionRecord{Metadata: meta}, "agent_goal")
		if r.Name == "" {
			r.Name = r.SessionID
		}
		out = append(out, r)
	}
	return out, nil
}

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
		CreatedAt:       fmt.Sprint(row["created_at"]),
		UpdatedAt:       fmt.Sprint(row["updated_at"]),
		MessageCount:    asInt(row["message_count"]),
		TotalPrompt:     asInt(row["total_prompt"]),
		TotalCompletion: asInt(row["total_completion"]),
	}
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

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
	rec, err := s.rowToRecord(row)
	if err != nil || rec == nil {
		return rec, err
	}
	if msgs, err := s.loadMessages(sessionID); err != nil {
		return nil, err
	} else if len(msgs) > 0 {
		rec.Messages = msgs
	}
	return rec, nil
}

func (s *SessionStore) loadMessages(sessionID string) ([]provider.Message, error) {
	rows, err := s.db.FetchAll(
		"SELECT payload FROM session_message WHERE session_id = ? ORDER BY seq ASC", sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]provider.Message, 0, len(rows))
	for _, row := range rows {
		raw, _ := row["payload"].(string)
		var m provider.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			slog.Warn("skipping unreadable message row", "session", sessionID, "err", err)
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *SessionStore) replaceMessagesTx(tx *sql.Tx, sessionID string, msgs []provider.Message) error {
	if _, err := tx.Exec("DELETE FROM session_message WHERE session_id = ?", sessionID); err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339Nano)
	for i, m := range msgs {
		payload, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("marshal message %d: %w", i, err)
		}
		if _, err := tx.Exec(
			"INSERT INTO session_message(session_id, seq, payload, created_at) VALUES (?, ?, ?, ?)",
			sessionID, i+1, string(payload), now); err != nil {
			return err
		}
	}
	_, err := tx.Exec("UPDATE sessions SET message_count = ? WHERE session_id = ?", len(msgs), sessionID)
	return err
}

func (s *SessionStore) AppendMessages(sessionID string, msgs []provider.Message, total int) error {
	if len(msgs) == 0 {
		return nil
	}
	return s.db.ExecTx(func(tx *sql.Tx) error {
		var maxSeq int
		if err := tx.QueryRow(
			"SELECT COALESCE(MAX(seq), 0) FROM session_message WHERE session_id = ?", sessionID,
		).Scan(&maxSeq); err != nil {
			return err
		}
		now := time.Now().Format(time.RFC3339Nano)
		stmt, err := tx.Prepare("INSERT INTO session_message(session_id, seq, payload, created_at) VALUES (?, ?, ?, ?)")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i, m := range msgs {
			payload, err := json.Marshal(m)
			if err != nil {
				return fmt.Errorf("marshal message %d: %w", i, err)
			}
			if _, err := stmt.Exec(sessionID, maxSeq+i+1, string(payload), now); err != nil {
				return err
			}
		}
		_, err = tx.Exec("UPDATE sessions SET message_count = ?, updated_at = ? WHERE session_id = ?",
			total, time.Now().Format(time.RFC3339), sessionID)
		return err
	})
}

func (s *SessionStore) Update(record *SessionRecord) error {
	record.UpdatedAt = time.Now().Format(time.RFC3339)
	msgsJSON, _ := json.Marshal(record.Messages)
	metaJSON, _ := json.Marshal(record.Metadata)
	return s.db.ExecTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE sessions SET parent_id=?, title=?, agent_role=?, work_dir=?, status=?, mode=?, model_key=?, channel_key=?, worker_id=?, messages=?, message_count=?, metadata=?, updated_at=?
			 WHERE session_id=?`,
			record.ParentID, record.Title, record.AgentRole, record.WorkDir, record.Status, record.Mode, record.ModelKey, record.ChannelKey, record.WorkerID,
			string(msgsJSON), len(record.Messages), string(metaJSON), record.UpdatedAt, record.SessionID,
		)
		if err != nil {
			return err
		}
		return s.replaceMessagesTx(tx, record.SessionID, record.Messages)
	})
}

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

func (s *SessionStore) SetChannelKey(sessionID, key string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Execute(
		"UPDATE sessions SET channel_key=?, updated_at=? WHERE session_id=?",
		key, now, sessionID,
	)
	return err
}

func (s *SessionStore) SetMode(sessionID, mode string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Execute(
		"UPDATE sessions SET mode=?, updated_at=? WHERE session_id=?",
		mode, now, sessionID,
	)
	return err
}

func (s *SessionStore) SetTitle(sessionID, title string) error {
	_, err := s.db.Execute(
		"UPDATE sessions SET title=? WHERE session_id=?",
		title, sessionID,
	)
	return err
}

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

func (s *SessionStore) SetModelKey(sessionID, key string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Execute(
		"UPDATE sessions SET model_key=?, updated_at=? WHERE session_id=?",
		key, now, sessionID,
	)
	return err
}

func (s *SessionStore) Delete(sessionID string) error {
	return s.db.ExecTx(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT session_id FROM sessions WHERE parent_id = ?", sessionID)
		if err != nil {
			return err
		}
		var kidIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				kidIDs = append(kidIDs, id)
			}
		}
		rows.Close()
		if _, err := tx.Exec("DELETE FROM sessions WHERE parent_id = ?", sessionID); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM sessions WHERE session_id = ?", sessionID); err != nil {
			return err
		}
		doomed := append([]string{sessionID}, kidIDs...)
		for _, id := range doomed {
			if _, err := tx.Exec("DELETE FROM session_event WHERE session_id = ?", id); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM session_message WHERE session_id = ?", id); err != nil {
				return err
			}
		}
		return nil
	})
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
	s.stopJanitor()
	return s.db.Close()
}
