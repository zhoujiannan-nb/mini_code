package session

// Worker (工作大类) persistence.
//
// A worker is a named line of work with one shared directory and one shared
// convention file. Root sessions are attached to at most one worker through
// sessions.worker_id; attaching a session moves it into the worker's directory
// and snapshots the convention file into the session (see worker.go for the
// snapshot rules and the prefix-cache reasoning).
//
// The rows live in the SAME SQLite file/connection as the sessions: deleting a
// worker has to delete its sessions, and keeping both in one connection is what
// makes that cascade free of cross-connection races.

import (
	"fmt"
	"time"

	"github.com/user/mini_code/util"
)

// WorkerUnassignedID is the virtual bucket of sessions that belong to no worker
// (sessions.worker_id = ""). It is never a real row.
const WorkerUnassignedID = ""

// WorkerAllID is the virtual "everything" bucket of the UI switcher: every
// session, assigned or not — except the built-in buckets (see
// WorkerArchivedID), which stay reachable only through their own entry.
const WorkerAllID = "*"

// WorkerArchivedID is the built-in bucket that replaced the old "archived"
// metadata flag. Sessions move into it on the first start after the upgrade, so
// nothing that was hidden before suddenly shows up in the list, and nothing is
// unreachable either. Builtin workers are never offered to the model (the
// workers tool hides them) and carry no directory.
const WorkerArchivedID = "w-archived"

// WorkerArchivedName is the display name of the built-in archived bucket.
const WorkerArchivedName = "已归档"

// WorkerRecord is one worker: what it is about, where its shared workspace
// lives, and where its convention file is.
type WorkerRecord struct {
	WorkerID string `json:"worker_id"`
	Name     string `json:"name"`
	// Summary is the short "what this worker is for" blurb the model reads when
	// it decides which worker to join. Kept short on purpose (see the tool).
	Summary   string `json:"summary"`
	WorkDir   string `json:"work_dir"`
	NotePath  string `json:"note_path"`
	Builtin   bool   `json:"builtin"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// Sessions is the number of root sessions in this bucket; filled by the
	// list queries only (never persisted).
	Sessions int `json:"sessions"`
}

// initWorkers creates the workers table, adds sessions.worker_id and moves the
// legacy archived flag into the built-in bucket. Called from SessionStore.initDB.
func (s *SessionStore) initWorkers() {
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS workers (
		worker_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		summary TEXT NOT NULL DEFAULT '',
		work_dir TEXT NOT NULL DEFAULT '',
		note_path TEXT NOT NULL DEFAULT '',
		builtin INTEGER NOT NULL DEFAULT 0,
		created_at TEXT,
		updated_at TEXT
	)`)
	s.migrateWorkerID()
	s.migrateArchivedToWorker()
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_sessions_worker ON sessions(worker_id)`)
}

// migrateWorkerID adds sessions.worker_id to databases created before the
// column existed (same PRAGMA dance as migrateMode).
func (s *SessionStore) migrateWorkerID() {
	rows, err := s.db.FetchAll("PRAGMA table_info(sessions)")
	if err != nil {
		return
	}
	for _, row := range rows {
		if fmt.Sprint(row["name"]) == "worker_id" {
			return
		}
	}
	s.db.Execute("ALTER TABLE sessions ADD COLUMN worker_id TEXT DEFAULT ''")
}

// migrateArchivedToWorker replaces the old metadata.archived flag with the
// built-in 已归档 bucket, once. Idempotent: after the move the sessions carry a
// worker_id, so the WHERE clause stops matching them (and un-archiving a session
// afterwards never re-moves it).
func (s *SessionStore) migrateArchivedToWorker() {
	where := "COALESCE(json_extract(metadata, '$.archived'), 0) = 1 AND COALESCE(worker_id, '') = ''"
	row, err := s.db.FetchOne("SELECT COUNT(*) AS n FROM sessions WHERE " + where)
	if err != nil {
		return
	}
	if row == nil || asInt(row["n"]) == 0 {
		return
	}
	if existing, _ := s.db.FetchOne("SELECT worker_id FROM workers WHERE worker_id = ?", WorkerArchivedID); existing == nil {
		now := time.Now().Format(time.RFC3339)
		s.db.Execute(
			"INSERT INTO workers (worker_id, name, summary, work_dir, note_path, builtin, created_at, updated_at) VALUES (?, ?, '', '', '', 1, ?, ?)",
			WorkerArchivedID, WorkerArchivedName, now, now)
	}
	s.db.Execute("UPDATE sessions SET worker_id = ? WHERE "+where, WorkerArchivedID)
}

// ensureArchivedBucket creates the built-in 已归档 row when it is needed but
// does not exist yet — the case for a database that never had an archived
// session and now receives the legacy "archive" API call. Idempotent.
func (s *SessionStore) ensureArchivedBucket() error {
	if existing, _ := s.db.FetchOne("SELECT worker_id FROM workers WHERE worker_id = ?", WorkerArchivedID); existing != nil {
		return nil
	}
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Execute(
		"INSERT INTO workers (worker_id, name, summary, work_dir, note_path, builtin, created_at, updated_at) VALUES (?, ?, '', '', '', 1, ?, ?)",
		WorkerArchivedID, WorkerArchivedName, now, now)
	return err
}

// --- worker CRUD ---

func (s *SessionStore) CreateWorker(rec *WorkerRecord) error {
	if rec.WorkerID == "" {
		rec.WorkerID = "w-" + util.RandomHex(8)
	}
	now := time.Now().Format(time.RFC3339)
	rec.CreatedAt, rec.UpdatedAt = now, now
	_, err := s.db.Execute(
		`INSERT INTO workers (worker_id, name, summary, work_dir, note_path, builtin, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.WorkerID, rec.Name, rec.Summary, rec.WorkDir, rec.NotePath, boolInt(rec.Builtin), rec.CreatedAt, rec.UpdatedAt)
	return err
}

func (s *SessionStore) GetWorker(workerID string) (*WorkerRecord, error) {
	row, err := s.db.FetchOne(
		"SELECT worker_id, name, summary, work_dir, note_path, builtin, created_at, updated_at FROM workers WHERE worker_id = ?",
		workerID)
	if err != nil || row == nil {
		return nil, err
	}
	return workerFromRow(row), nil
}

// workerSessionCounts returns bucket -> number of root sessions. The list
// queries use it so the UI can show a per-worker session badge in one round
// trip (the buckets themselves are tiny, so one GROUP BY is enough).
func (s *SessionStore) workerSessionCounts() map[string]int {
	rows, err := s.db.FetchAll(
		`SELECT COALESCE(worker_id, '') AS wid, COUNT(*) AS n FROM sessions
		 WHERE parent_id IS NULL OR parent_id = ''
		 GROUP BY COALESCE(worker_id, '')`)
	if err != nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[asString(r["wid"])] = asInt(r["n"])
	}
	return out
}

// ListWorkers returns every worker (built-in buckets included) with its session
// count, most recently updated first.
func (s *SessionStore) ListWorkers() ([]WorkerRecord, error) {
	rows, err := s.db.FetchAll(
		"SELECT worker_id, name, summary, work_dir, note_path, builtin, created_at, updated_at FROM workers ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	counts := s.workerSessionCounts()
	out := make([]WorkerRecord, 0, len(rows))
	for _, r := range rows {
		rec := workerFromRow(r)
		rec.Sessions = counts[rec.WorkerID]
		out = append(out, *rec)
	}
	return out, nil
}

// GetWorkerByName looks a worker up by its (unique-by-convention) name.
func (s *SessionStore) GetWorkerByName(name string) (*WorkerRecord, error) {
	row, err := s.db.FetchOne(
		"SELECT worker_id, name, summary, work_dir, note_path, builtin, created_at, updated_at FROM workers WHERE name = ?",
		name)
	if err != nil || row == nil {
		return nil, err
	}
	return workerFromRow(row), nil
}

// UpdateWorker persists name/summary (and optionally the directory/convention
// path) of an existing worker.
func (s *SessionStore) UpdateWorker(rec *WorkerRecord) error {
	now := time.Now().Format(time.RFC3339)
	rec.UpdatedAt = now
	_, err := s.db.Execute(
		"UPDATE workers SET name = ?, summary = ?, work_dir = ?, note_path = ?, updated_at = ? WHERE worker_id = ?",
		rec.Name, rec.Summary, rec.WorkDir, rec.NotePath, now, rec.WorkerID)
	return err
}

// DeleteWorker removes the worker row. Its sessions are deleted by the caller
// (WorkerService.Delete, which also drops them from the manager so a running
// task gets canceled the same way a manual session delete does).
func (s *SessionStore) DeleteWorker(workerID string) error {
	_, err := s.db.Execute("DELETE FROM workers WHERE worker_id = ?", workerID)
	return err
}

// --- session ↔ worker ---

// SetWorker attaches a session to a worker (or detaches it) and, when workDir
// is non-nil, rewrites its working directory in the same statement. A targeted
// update, like SetMode: it must never clobber the agent loop's per-turn message
// write, and must never be clobbered by it.
func (s *SessionStore) SetWorker(sessionID, workerID string, workDir *string) error {
	now := time.Now().Format(time.RFC3339)
	if workDir == nil {
		_, err := s.db.Execute("UPDATE sessions SET worker_id=?, updated_at=? WHERE session_id=?", workerID, now, sessionID)
		return err
	}
	_, err := s.db.Execute("UPDATE sessions SET worker_id=?, work_dir=?, updated_at=? WHERE session_id=?", workerID, *workDir, now, sessionID)
	return err
}

// ListWorkerSessions returns the IDs of every session assigned to a worker
// (root sessions and their sub-agent children alike).
func (s *SessionStore) ListWorkerSessions(workerID string) ([]string, error) {
	rows, err := s.db.FetchAll("SELECT session_id FROM sessions WHERE COALESCE(worker_id, '') = ?", workerID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, asString(r["session_id"]))
	}
	return out, nil
}

func workerFromRow(row map[string]interface{}) *WorkerRecord {
	return &WorkerRecord{
		WorkerID:  asString(row["worker_id"]),
		Name:      asString(row["name"]),
		Summary:   asString(row["summary"]),
		WorkDir:   asString(row["work_dir"]),
		NotePath:  asString(row["note_path"]),
		Builtin:   asInt(row["builtin"]) != 0,
		CreatedAt: asString(row["created_at"]),
		UpdatedAt: asString(row["updated_at"]),
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
