package session


import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/user/mini_code/util"
)

const WorkerUnassignedID = ""

const WorkerAllID = "*"

const WorkerArchivedID = "w-archived"

const WorkerArchivedName = "已归档"

const (
	WorkerOtherID   = "w-other"
	WorkerOtherName = "其他"
)

func IsReservedWorker(workerID string) bool { return workerID == WorkerOtherID }

type WorkerRecord struct {
	WorkerID string `json:"worker_id"`
	Name     string `json:"name"`
	Summary string `json:"summary"`
	WorkDir string `json:"work_dir"`
	Note    string `json:"note"`
	NoteVer int    `json:"note_ver"`
	Builtin bool   `json:"builtin"`
	NotePath  string `json:"-"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Sessions int `json:"sessions"`
}

func (s *SessionStore) initWorkers() {
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS workers (
		worker_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		summary TEXT NOT NULL DEFAULT '',
		work_dir TEXT NOT NULL DEFAULT '',
		note TEXT NOT NULL DEFAULT '',
		note_ver INTEGER NOT NULL DEFAULT 0,
		builtin INTEGER NOT NULL DEFAULT 0,
		created_at TEXT,
		updated_at TEXT
	)`)
	s.migrateWorkerID()
	s.migrateWorkerNote()
	s.migrateArchivedToWorker()
	s.ensureOtherWorker()
	s.refileRootlessSessions()
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_sessions_worker ON sessions(worker_id)`)
}

func (s *SessionStore) refileRootlessSessions() {
	res, err := s.db.Execute(
		"UPDATE sessions SET worker_id = ? WHERE COALESCE(worker_id, '') = '' AND COALESCE(parent_id, '') = ''",
		WorkerOtherID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("re-filed unattached sessions under 其他", "count", n)
	}
}

func (s *SessionStore) ensureOtherWorker() {
	if existing, _ := s.db.FetchOne("SELECT worker_id FROM workers WHERE worker_id = ?", WorkerOtherID); existing != nil {
		return
	}
	now := time.Now().Format(time.RFC3339)
	if _, err := s.db.Execute(
		"INSERT INTO workers (worker_id, name, summary, work_dir, note, note_ver, builtin, created_at, updated_at) VALUES (?, ?, '', '', '', 0, 0, ?, ?)",
		WorkerOtherID, WorkerOtherName, now, now); err == nil {
		slog.Info("seeded the fixed 其他 worker", "worker", WorkerOtherID)
	}
}

func (s *SessionStore) countWorkers() (int, error) {
	row, err := s.db.FetchOne("SELECT COUNT(*) AS n FROM workers WHERE COALESCE(builtin, 0) = 0")
	if err != nil {
		return 0, err
	}
	if row == nil {
		return 0, nil
	}
	return asInt(row["n"]), nil
}

const workerCols = "worker_id, name, summary, work_dir, note, note_ver, builtin, created_at, updated_at"

func (s *SessionStore) migrateWorkerNote() {
	rows, err := s.db.FetchAll("PRAGMA table_info(workers)")
	if err != nil {
		return
	}
	have := map[string]bool{}
	for _, row := range rows {
		have[fmt.Sprint(row["name"])] = true
	}
	if !have["note"] {
		s.db.Execute("ALTER TABLE workers ADD COLUMN note TEXT NOT NULL DEFAULT ''")
	}
	if !have["note_ver"] {
		s.db.Execute("ALTER TABLE workers ADD COLUMN note_ver INTEGER NOT NULL DEFAULT 0")
	}
	if !have["note_path"] {
		return
	}
	legacy, err := s.db.FetchAll("SELECT worker_id, note, note_path FROM workers WHERE COALESCE(note_path, '') <> '' AND COALESCE(note, '') = ''")
	if err != nil {
		return
	}
	for _, row := range legacy {
		note, err := readWorkerNote(asString(row["note_path"]))
		if err != nil || strings.TrimSpace(note) == "" {
			continue
		}
		s.db.Execute("UPDATE workers SET note = ?, note_ver = 1 WHERE worker_id = ?", note, asString(row["worker_id"]))
	}
}

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
			"INSERT INTO workers (worker_id, name, summary, work_dir, note, note_ver, builtin, created_at, updated_at) VALUES (?, ?, '', '', '', 0, 1, ?, ?)",
			WorkerArchivedID, WorkerArchivedName, now, now)
	}
	s.db.Execute("UPDATE sessions SET worker_id = ? WHERE "+where, WorkerArchivedID)
}

func (s *SessionStore) ensureArchivedBucket() error {
	if existing, _ := s.db.FetchOne("SELECT worker_id FROM workers WHERE worker_id = ?", WorkerArchivedID); existing != nil {
		return nil
	}
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Execute(
		"INSERT INTO workers (worker_id, name, summary, work_dir, note, note_ver, builtin, created_at, updated_at) VALUES (?, ?, '', '', '', 0, 1, ?, ?)",
		WorkerArchivedID, WorkerArchivedName, now, now)
	return err
}


func (s *SessionStore) CreateWorker(rec *WorkerRecord) error {
	if rec.WorkerID == "" {
		rec.WorkerID = "w-" + util.RandomHex(8)
	}
	now := time.Now().Format(time.RFC3339)
	rec.CreatedAt, rec.UpdatedAt = now, now
	_, err := s.db.Execute(
		`INSERT INTO workers (worker_id, name, summary, work_dir, note, note_ver, builtin, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.WorkerID, rec.Name, rec.Summary, rec.WorkDir, rec.Note, rec.NoteVer, boolInt(rec.Builtin), rec.CreatedAt, rec.UpdatedAt)
	return err
}

func (s *SessionStore) GetWorker(workerID string) (*WorkerRecord, error) {
	row, err := s.db.FetchOne("SELECT "+workerCols+" FROM workers WHERE worker_id = ?", workerID)
	if err != nil || row == nil {
		return nil, err
	}
	return workerFromRow(row), nil
}

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

func (s *SessionStore) ListWorkers() ([]WorkerRecord, error) {
	rows, err := s.db.FetchAll("SELECT " + workerCols + " FROM workers ORDER BY updated_at DESC")
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

func (s *SessionStore) GetWorkerByName(name string) (*WorkerRecord, error) {
	row, err := s.db.FetchOne("SELECT "+workerCols+" FROM workers WHERE name = ?", name)
	if err != nil || row == nil {
		return nil, err
	}
	return workerFromRow(row), nil
}

func (s *SessionStore) UpdateWorker(rec *WorkerRecord) error {
	now := time.Now().Format(time.RFC3339)
	rec.UpdatedAt = now
	_, err := s.db.Execute(
		"UPDATE workers SET name = ?, summary = ?, work_dir = ?, updated_at = ? WHERE worker_id = ?",
		rec.Name, rec.Summary, rec.WorkDir, now, rec.WorkerID)
	return err
}

func (s *SessionStore) UpdateWorkerNote(workerID, note string, ver int) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.Execute("UPDATE workers SET note = ?, note_ver = ?, updated_at = ? WHERE worker_id = ?",
		note, ver, now, workerID)
	return err
}

func (s *SessionStore) DeleteWorker(workerID string) error {
	_, err := s.db.Execute("DELETE FROM workers WHERE worker_id = ?", workerID)
	return err
}

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

func (s *SessionStore) RelabelWorkerSession(sessionID, workerID, name string) error {
	rec, err := s.Get(sessionID)
	if err != nil || rec == nil {
		return err
	}
	if rec.WorkerID != workerID {
		return nil
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]interface{}{}
	}
	rec.Metadata[workerNameMetaKey] = name
	return s.Update(rec)
}

func workerFromRow(row map[string]interface{}) *WorkerRecord {
	return &WorkerRecord{
		WorkerID:  asString(row["worker_id"]),
		Name:      asString(row["name"]),
		Summary:   asString(row["summary"]),
		WorkDir:   asString(row["work_dir"]),
		Note:      asString(row["note"]),
		NoteVer:   asInt(row["note_ver"]),
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
