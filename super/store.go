// Package super implements the Super-Agent (Router) subsystem: a resident
// dispatch agent that never executes work itself but manages its own
// execution bodies ("super workers"), a persistent message channel
// (immediate + batched buffer), scheduled script runner, process registry
// and a full audit trail.
//
// Everything lives beside the sessions in the SAME SQLite connection
// (super_* tables): like workers-and-sessions, the cascade from a worker to
// its session rows must stay race-free. Nothing here touches the existing
// 工作大类 workers table — the two systems are deliberately separate.
package super

import (
	"fmt"
	"time"

	"github.com/user/mini_code/util"
)

// Store is the super_* table group. Created once at startup on the shared
// *util.Database the session store already owns.
type Store struct {
	db *util.Database
}

func NewStore(db *util.Database) *Store {
	s := &Store{db: db}
	s.init()
	return s
}

func (s *Store) init() {
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_worker (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		work_dir TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		note TEXT NOT NULL DEFAULT '',
		note_ver INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active',
		created_at TEXT,
		updated_at TEXT
	)`)
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_worker_session (
		id TEXT PRIMARY KEY,
		sw_id TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'active',
		note_ver INTEGER NOT NULL DEFAULT 0,
		created_at TEXT,
		updated_at TEXT
	)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_sw_sess_sw ON super_worker_session(sw_id)`)
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_message (
		id TEXT PRIMARY KEY,
		mode TEXT NOT NULL,          -- imm | buf
		src TEXT NOT NULL DEFAULT '',
		payload TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,        -- pending | delivered | batched
		batch_id TEXT NOT NULL DEFAULT '',
		created_at TEXT,
		delivered_at TEXT NOT NULL DEFAULT ''
	)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_super_msg_status ON super_message(mode, status)`)
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_queue (
		id TEXT PRIMARY KEY,
		task TEXT NOT NULL,
		worker_name TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending', -- pending | spawned
		spawned_session TEXT NOT NULL DEFAULT '',
		created_at TEXT
	)`)
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_schedule (
		file TEXT PRIMARY KEY,
		kind TEXT NOT NULL DEFAULT '',
		every_sec INTEGER NOT NULL DEFAULT 300,
		mode TEXT NOT NULL DEFAULT 'buf',       -- imm | buf
		enabled INTEGER NOT NULL DEFAULT 1,
		last_run TEXT NOT NULL DEFAULT '',
		last_exit INTEGER NOT NULL DEFAULT -999,
		note TEXT NOT NULL DEFAULT '',
		created_at TEXT,
		updated_at TEXT
	)`)
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_process (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,           -- script | session
		name TEXT NOT NULL DEFAULT '',
		pid INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'run',
		exit_code INTEGER NOT NULL DEFAULT -999,
		started_at TEXT,
		ended_at TEXT NOT NULL DEFAULT '',
		ref TEXT NOT NULL DEFAULT ''
	)`)
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts TEXT NOT NULL,
		actor TEXT NOT NULL,          -- super | human | runner | system
		action TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		refs TEXT NOT NULL DEFAULT ''
	)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_super_audit_refs ON super_audit(refs)`)
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_state (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT ''
	)`)
}

// --- small row helpers (super has its own copies: the session package's are
// unexported and importing them would add nothing but a fake dependency) ---

func asStr(v interface{}) string {
	if v == nil {
		return ""
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}

func asNum(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func nowStr() string { return time.Now().Format(time.RFC3339) }

// --- state kv ---

func (s *Store) StateGet(key string) string {
	row, err := s.db.FetchOne("SELECT value FROM super_state WHERE key = ?", key)
	if err != nil || row == nil {
		return ""
	}
	return asStr(row["value"])
}

func (s *Store) StateSet(key, value string) error {
	_, err := s.db.Execute(
		`INSERT INTO super_state (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// --- worker ---

type Worker struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	WorkDir   string `json:"work_dir"`
	Model     string `json:"model"`
	Note      string `json:"note"`
	NoteVer   int    `json:"note_ver"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// filled by list views only:
	Sessions []*WorkerSession `json:"sessions"`
	Live     bool            `json:"live"` // any bound session has a running task right now
}

const workerCols = "id, name, work_dir, model, note, note_ver, status, created_at, updated_at"

func workerFromRow(row map[string]interface{}) *Worker {
	return &Worker{
		ID: asStr(row["id"]), Name: asStr(row["name"]), WorkDir: asStr(row["work_dir"]),
		Model: asStr(row["model"]), Note: asStr(row["note"]), NoteVer: int(asNum(row["note_ver"])),
		Status: asStr(row["status"]), CreatedAt: asStr(row["created_at"]), UpdatedAt: asStr(row["updated_at"]),
		Sessions: []*WorkerSession{},
	}
}

func (s *Store) CreateWorker(w *Worker) error {
	if w.ID == "" {
		w.ID = "sw-" + util.RandomHex(4)
	}
	now := nowStr()
	w.CreatedAt, w.UpdatedAt = now, now
	if w.Status == "" {
		w.Status = "active"
	}
	_, err := s.db.Execute(
		`INSERT INTO super_worker (id, name, work_dir, model, note, note_ver, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.Name, w.WorkDir, w.Model, w.Note, w.NoteVer, w.Status, w.CreatedAt, w.UpdatedAt)
	return err
}

func (s *Store) GetWorker(id string) (*Worker, error) {
	row, err := s.db.FetchOne("SELECT "+workerCols+" FROM super_worker WHERE id = ?", id)
	if err != nil || row == nil {
		return nil, err
	}
	return workerFromRow(row), nil
}

func (s *Store) GetWorkerByName(name string) (*Worker, error) {
	row, err := s.db.FetchOne("SELECT "+workerCols+" FROM super_worker WHERE name = ? AND status = 'active'", name)
	if err != nil || row == nil {
		return nil, err
	}
	return workerFromRow(row), nil
}

func (s *Store) ListWorkers() ([]*Worker, error) {
	rows, err := s.db.FetchAll("SELECT " + workerCols + " FROM super_worker ORDER BY created_at ASC")
	if err != nil {
		return nil, err
	}
	out := make([]*Worker, 0, len(rows))
	for _, r := range rows {
		out = append(out, workerFromRow(r))
	}
	return out, nil
}

func (s *Store) UpdateWorkerCore(id, note, model string, noteVer int) error {
	_, err := s.db.Execute("UPDATE super_worker SET note = ?, model = ?, note_ver = ?, updated_at = ? WHERE id = ?",
		note, model, noteVer, nowStr(), id)
	return err
}

func (s *Store) SetWorkerStatus(id, status string) error {
	_, err := s.db.Execute("UPDATE super_worker SET status = ?, updated_at = ? WHERE id = ?", status, nowStr(), id)
	return err
}

func (s *Store) CountLiveWorkers() (int, error) {
	row, err := s.db.FetchOne("SELECT COUNT(*) AS n FROM super_worker WHERE status = 'active'")
	if err != nil || row == nil {
		return 0, err
	}
	return int(asNum(row["n"])), nil
}

// --- worker session bindings ---

type WorkerSession struct {
	ID        string `json:"id"`
	SwID      string `json:"sw_id"`
	Title     string `json:"title"`
	Status    string `json:"status"` // active | closed
	NoteVer   int    `json:"note_ver"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Active    bool   `json:"active"` // live task right now (filled by the service, never persisted)
}

func (s *Store) BindSession(row *WorkerSession) error {
	now := nowStr()
	row.CreatedAt, row.UpdatedAt = now, now
	if row.Status == "" {
		row.Status = "active"
	}
	_, err := s.db.Execute(
		`INSERT INTO super_worker_session (id, sw_id, title, status, note_ver, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.SwID, row.Title, row.Status, row.NoteVer, row.CreatedAt, row.UpdatedAt)
	return err
}

func (s *Store) GetSessionBinding(sessionID string) (*WorkerSession, error) {
	row, err := s.db.FetchOne(
		"SELECT id, sw_id, title, status, note_ver, created_at, updated_at FROM super_worker_session WHERE id = ?", sessionID)
	if err != nil || row == nil {
		return nil, err
	}
	return &WorkerSession{
		ID: asStr(row["id"]), SwID: asStr(row["sw_id"]), Title: asStr(row["title"]),
		Status: asStr(row["status"]), NoteVer: int(asNum(row["note_ver"])),
		CreatedAt: asStr(row["created_at"]), UpdatedAt: asStr(row["updated_at"]),
	}, nil
}

// AllWorkerSessionIDs lists every session ever spawned for a super worker
// (active or closed). The normal session list hides these; they live under
// the Super-Agent panel only.
func (s *Store) AllWorkerSessionIDs() ([]string, error) {
	rows, err := s.db.FetchAll("SELECT id FROM super_worker_session")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, asStr(r["id"]))
	}
	return out, nil
}

func (s *Store) ListBoundSessions(swID string) ([]*WorkerSession, error) {
	rows, err := s.db.FetchAll(
		`SELECT id, sw_id, title, status, note_ver, created_at, updated_at FROM super_worker_session
		 WHERE sw_id = ? ORDER BY created_at ASC`, swID)
	if err != nil {
		return nil, err
	}
	out := make([]*WorkerSession, 0, len(rows))
	for _, r := range rows {
		out = append(out, &WorkerSession{
			ID: asStr(r["id"]), SwID: asStr(r["sw_id"]), Title: asStr(r["title"]),
			Status: asStr(r["status"]), NoteVer: int(asNum(r["note_ver"])),
			CreatedAt: asStr(r["created_at"]), UpdatedAt: asStr(r["updated_at"]),
		})
	}
	return out, nil
}

func (s *Store) SetSessionStatus(sessionID, status string) error {
	_, err := s.db.Execute("UPDATE super_worker_session SET status = ?, updated_at = ? WHERE id = ?", status, nowStr(), sessionID)
	return err
}

func (s *Store) SetSessionNoteVer(sessionID string, ver int) error {
	_, err := s.db.Execute("UPDATE super_worker_session SET note_ver = ?, updated_at = ? WHERE id = ?", ver, nowStr(), sessionID)
	return err
}

// --- messages ---

type Message struct {
	ID          string `json:"id"`
	Mode        string `json:"mode"` // imm | buf
	Src         string `json:"src"`
	Payload     string `json:"payload"`
	Status      string `json:"status"` // pending | delivered | batched
	BatchID     string `json:"batch_id"`
	CreatedAt   string `json:"created_at"`
	DeliveredAt string `json:"delivered_at"`
}

const msgCols = "id, mode, src, payload, status, batch_id, created_at, delivered_at"

func msgFromRow(r map[string]interface{}) *Message {
	return &Message{
		ID: asStr(r["id"]), Mode: asStr(r["mode"]), Src: asStr(r["src"]), Payload: asStr(r["payload"]),
		Status: asStr(r["status"]), BatchID: asStr(r["batch_id"]),
		CreatedAt: asStr(r["created_at"]), DeliveredAt: asStr(r["delivered_at"]),
	}
}

func (s *Store) InsertMessage(m *Message) error {
	if m.ID == "" {
		m.ID = "m-" + util.RandomHex(6)
	}
	m.CreatedAt = nowStr()
	_, err := s.db.Execute(
		`INSERT INTO super_message (id, mode, src, payload, status, batch_id, created_at, delivered_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Mode, m.Src, m.Payload, m.Status, m.BatchID, m.CreatedAt, m.DeliveredAt)
	return err
}

func (s *Store) MarkMessageDelivered(id, status string) error {
	_, err := s.db.Execute("UPDATE super_message SET status = ?, delivered_at = ? WHERE id = ?", status, nowStr(), id)
	return err
}

// PendingBuffered returns the buffered messages waiting to be batched, oldest first.
func (s *Store) PendingBuffered() ([]*Message, error) {
	rows, err := s.db.FetchAll(
		"SELECT " + msgCols + " FROM super_message WHERE mode = 'buf' AND status = 'pending' ORDER BY created_at ASC")
	if err != nil {
		// created_at has second granularity; ties keep insertion order via rowid
		return nil, err
	}
	out := make([]*Message, 0, len(rows))
	for _, r := range rows {
		out = append(out, msgFromRow(r))
	}
	return out, nil
}

func (s *Store) BatchMessages(ids []string, batchID string) error {
	for _, id := range ids {
		if _, err := s.db.Execute("UPDATE super_message SET status = 'batched', batch_id = ?, delivered_at = ? WHERE id = ?",
			batchID, nowStr(), id); err != nil {
			return err
		}
	}
	return nil
}

// RecentMessages lists the last n delivered messages of one mode (UI view).
func (s *Store) RecentMessages(mode string, n int) ([]*Message, error) {
	rows, err := s.db.FetchAll(
		"SELECT "+msgCols+" FROM super_message WHERE mode = ? AND status != 'pending' ORDER BY id DESC LIMIT ?", mode, n)
	if err != nil {
		return nil, err
	}
	out := make([]*Message, 0, len(rows))
	for _, r := range rows {
		out = append(out, msgFromRow(r))
	}
	return out, nil
}

// --- queue ---

type QueueRow struct {
	ID             string `json:"id"`
	Task           string `json:"task"`
	WorkerName     string `json:"worker_name"`
	Status         string `json:"status"`
	SpawnedSession string `json:"spawned_session"`
	CreatedAt      string `json:"created_at"`
}

func (s *Store) QueueAdd(q *QueueRow) error {
	if q.ID == "" {
		q.ID = "q-" + util.RandomHex(6)
	}
	q.CreatedAt = nowStr()
	q.Status = "pending"
	_, err := s.db.Execute(
		`INSERT INTO super_queue (id, task, worker_name, status, spawned_session, created_at)
		 VALUES (?, ?, ?, ?, '', ?)`, q.ID, q.Task, q.WorkerName, q.Status, q.CreatedAt)
	return err
}

func (s *Store) QueuePending() ([]*QueueRow, error) {
	rows, err := s.db.FetchAll(
		"SELECT id, task, worker_name, status, spawned_session, created_at FROM super_queue WHERE status = 'pending' ORDER BY rowid ASC")
	if err != nil {
		return nil, err
	}
	out := make([]*QueueRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, &QueueRow{
			ID: asStr(r["id"]), Task: asStr(r["task"]), WorkerName: asStr(r["worker_name"]),
			Status: asStr(r["status"]), SpawnedSession: asStr(r["spawned_session"]), CreatedAt: asStr(r["created_at"]),
		})
	}
	return out, nil
}

func (s *Store) QueueMarkSpawned(id, sessionID string) error {
	_, err := s.db.Execute("UPDATE super_queue SET status = 'spawned', spawned_session = ? WHERE id = ?", sessionID, id)
	return err
}

// --- schedule ---

type Schedule struct {
	File     string `json:"file"`
	Kind     string `json:"kind"`
	EverySec int    `json:"every_sec"`
	Mode     string `json:"mode"`
	Enabled  bool   `json:"enabled"`
	LastRun  string `json:"last_run"`
	LastExit int    `json:"last_exit"`
	Note     string `json:"note"`
}

func (s *Store) ListSchedules() ([]*Schedule, error) {
	rows, err := s.db.FetchAll(
		"SELECT file, kind, every_sec, mode, enabled, last_run, last_exit, note FROM super_schedule ORDER BY file ASC")
	if err != nil {
		return nil, err
	}
	out := make([]*Schedule, 0, len(rows))
	for _, r := range rows {
		out = append(out, &Schedule{
			File: asStr(r["file"]), Kind: asStr(r["kind"]), EverySec: int(asNum(r["every_sec"])),
			Mode: asStr(r["mode"]), Enabled: asNum(r["enabled"]) != 0,
			LastRun: asStr(r["last_run"]), LastExit: int(asNum(r["last_exit"])), Note: asStr(r["note"]),
		})
	}
	return out, nil
}

func (s *Store) GetSchedule(file string) (*Schedule, error) {
	rows, err := s.db.FetchAll(
		"SELECT file, kind, every_sec, mode, enabled, last_run, last_exit, note FROM super_schedule WHERE file = ?", file)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	r := rows[0]
	return &Schedule{
		File: asStr(r["file"]), Kind: asStr(r["kind"]), EverySec: int(asNum(r["every_sec"])),
		Mode: asStr(r["mode"]), Enabled: asNum(r["enabled"]) != 0,
		LastRun: asStr(r["last_run"]), LastExit: int(asNum(r["last_exit"])), Note: asStr(r["note"]),
	}, nil
}

func (s *Store) UpsertSchedule(sc *Schedule) error {
	now := nowStr()
	_, err := s.db.Execute(
		`INSERT INTO super_schedule (file, kind, every_sec, mode, enabled, last_run, last_exit, note, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(file) DO UPDATE SET kind = excluded.kind, every_sec = excluded.every_sec,
		   mode = excluded.mode, enabled = excluded.enabled, updated_at = excluded.updated_at`,
		sc.File, sc.Kind, sc.EverySec, sc.Mode, boolInt(sc.Enabled), sc.LastRun, sc.LastExit, sc.Note, now, now)
	return err
}

func (s *Store) ScheduleSetEnabled(file string, on bool) error {
	_, err := s.db.Execute("UPDATE super_schedule SET enabled = ?, updated_at = ? WHERE file = ?", boolInt(on), nowStr(), file)
	return err
}

// DeleteSchedule drops a schedule row (task-type only; guarded by the
// service layer). The runner loop never re-adds task rows — only real
// script files get rediscovered.
func (s *Store) DeleteSchedule(file string) error {
	_, err := s.db.Execute("DELETE FROM super_schedule WHERE file = ?", file)
	return err
}

func (s *Store) ScheduleSetResult(file, runAt string, exit int) error {
	_, err := s.db.Execute("UPDATE super_schedule SET last_run = ?, last_exit = ?, updated_at = ? WHERE file = ?",
		runAt, exit, nowStr(), file)
	return err
}

// --- process ---

type Process struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	PID       int    `json:"pid"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Ref       string `json:"ref"`
}

func (s *Store) ProcInsert(p *Process) error {
	if p.ID == "" {
		p.ID = "p-" + util.RandomHex(6)
	}
	p.StartedAt = nowStr()
	_, err := s.db.Execute(
		`INSERT INTO super_process (id, kind, name, pid, status, exit_code, started_at, ended_at, ref)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', ?)`,
		p.ID, p.Kind, p.Name, p.PID, p.Status, p.ExitCode, p.StartedAt, p.Ref)
	return err
}

func (s *Store) ProcEnd(id string, exit int) error {
	_, err := s.db.Execute("UPDATE super_process SET status = 'exit', exit_code = ?, ended_at = ? WHERE id = ?",
		exit, nowStr(), id)
	return err
}

func (s *Store) ProcRecent(n int) ([]*Process, error) {
	rows, err := s.db.FetchAll(
		"SELECT id, kind, name, pid, status, exit_code, started_at, ended_at, ref FROM super_process ORDER BY rowid DESC LIMIT ?", n)
	if err != nil {
		return nil, err
	}
	out := make([]*Process, 0, len(rows))
	for _, r := range rows {
		out = append(out, &Process{
			ID: asStr(r["id"]), Kind: asStr(r["kind"]), Name: asStr(r["name"]), PID: int(asNum(r["pid"])),
			Status: asStr(r["status"]), ExitCode: int(asNum(r["exit_code"])),
			StartedAt: asStr(r["started_at"]), EndedAt: asStr(r["ended_at"]), Ref: asStr(r["ref"]),
		})
	}
	return out, nil
}

// --- audit ---

type AuditRow struct {
	ID     int64  `json:"id"`
	Ts     string `json:"ts"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail"`
	Refs   string `json:"refs"`
}

func (s *Store) AuditInsert(actor, action, target, detail, refs string) error {
	_, err := s.db.Execute(
		"INSERT INTO super_audit (ts, actor, action, target, detail, refs) VALUES (?, ?, ?, ?, ?, ?)",
		time.Now().Format("2006-01-02 15:04:05"), actor, action, target, detail, refs)
	return err
}

func (s *Store) AuditRecent(limit int, refsLike, actor string) ([]*AuditRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 120
	}
	q := "SELECT id, ts, actor, action, target, detail, refs FROM super_audit"
	var conds []string
	var args []interface{}
	if refsLike != "" {
		conds = append(conds, "refs LIKE ?")
		args = append(args, "%"+refsLike+"%")
	}
	if actor != "" {
		conds = append(conds, "actor = ?")
		args = append(args, actor)
	}
	for i, c := range conds {
		if i == 0 {
			q += " WHERE " + c
		} else {
			q += " AND " + c
		}
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.FetchAll(q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]*AuditRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, &AuditRow{
			ID: asNum(r["id"]), Ts: asStr(r["ts"]), Actor: asStr(r["actor"]), Action: asStr(r["action"]),
			Target: asStr(r["target"]), Detail: asStr(r["detail"]), Refs: asStr(r["refs"]),
		})
	}
	return out, nil
}

// AuditSearch matches the query against action/target/detail/refs (chained recall).
func (s *Store) AuditSearch(query string, limit int) ([]*AuditRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	like := "%" + query + "%"
	return s.AuditRecentWhere(
		"(action LIKE ? OR target LIKE ? OR detail LIKE ? OR refs LIKE ?)",
		[]interface{}{like, like, like, like}, limit)
}

func (s *Store) AuditRecentWhere(cond string, args []interface{}, limit int) ([]*AuditRow, error) {
	q := "SELECT id, ts, actor, action, target, detail, refs FROM super_audit"
	if cond != "" {
		q += " WHERE " + cond
	}
	q += " ORDER BY id DESC LIMIT ?"
	qArgs := append(append([]interface{}{}, args...), limit)
	rows, err := s.db.FetchAll(q, qArgs...)
	if err != nil {
		return nil, err
	}
	out := make([]*AuditRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, &AuditRow{
			ID: asNum(r["id"]), Ts: asStr(r["ts"]), Actor: asStr(r["actor"]), Action: asStr(r["action"]),
			Target: asStr(r["target"]), Detail: asStr(r["detail"]), Refs: asStr(r["refs"]),
		})
	}
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
