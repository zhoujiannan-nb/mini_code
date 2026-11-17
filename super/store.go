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
	// --- v9 collection layer: Source → binding (thread) → ticket ---
	// A Source is one采集渠道 (kind = adapter in the registry; only
	// git-watch ships this round). An event from a bound source goes straight
	// into the bound session ("thread"); tickets ride the thread and carry
	// the state machine. The binding IS the subscription: closing it starts
	// the 3-day grace, after which the source is unbound again.
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_source (
		id TEXT PRIMARY KEY,          -- src-xxxx
		kind TEXT NOT NULL,           -- registry key; git-watch is the only enabled kind
		name TEXT NOT NULL,
		config_json TEXT NOT NULL DEFAULT '{}',
		cursor TEXT NOT NULL DEFAULT '',      -- kind-owned (git: last seen sha)
		enabled INTEGER NOT NULL DEFAULT 1,
		poll_sec INTEGER NOT NULL DEFAULT 300,
		bind_mode TEXT NOT NULL DEFAULT 'auto', -- auto=自动派发首绑 | assign=指定已有会话
		bind_session TEXT NOT NULL DEFAULT '',  -- assign: the declared session
		ttl TEXT NOT NULL DEFAULT '3d',         -- 退订宽限: 3d | forever
		last_run TEXT NOT NULL DEFAULT '',
		last_err TEXT NOT NULL DEFAULT '',
		err_count INTEGER NOT NULL DEFAULT 0,
		created_at TEXT,
		updated_at TEXT
	)`)
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_ticket (
		id TEXT PRIMARY KEY,          -- tk-xxxxxx
		src_id TEXT NOT NULL DEFAULT '',      -- "" = 人工工单 (router/人类直接开单)
		title TEXT NOT NULL DEFAULT '',
		note TEXT NOT NULL DEFAULT '',        -- ≤30字转述 (router 备注/人类备注)
		text TEXT NOT NULL DEFAULT '',        -- 原话,一字不改
		state TEXT NOT NULL DEFAULT 'queued', -- queued | running | ready | closed
		session TEXT NOT NULL DEFAULT '',     -- 落地的 thread 会话
		actor TEXT NOT NULL DEFAULT 'human',  -- human | super | auto | runner
		created_at TEXT,
		updated_at TEXT,
		closed_at TEXT NOT NULL DEFAULT '',
		reopened INTEGER NOT NULL DEFAULT 0
	)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_ticket_state ON super_ticket(state)`)
	s.db.InitIndex(`CREATE INDEX IF NOT EXISTS idx_ticket_src ON super_ticket(src_id)`)
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_binding (
		src_id TEXT PRIMARY KEY,      -- one thread per source
		sess TEXT NOT NULL,
		binder TEXT NOT NULL DEFAULT 'auto',  -- auto | human | router
		state TEXT NOT NULL DEFAULT 'active', -- active | unsubscribed
		forever INTEGER NOT NULL DEFAULT 0,   -- 永久宽限(人类显式选择)
		unsubscribed_at TEXT NOT NULL DEFAULT '',
		created_at TEXT,
		updated_at TEXT
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

// --- source (v9 采集层) ---

type Source struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	ConfigJSON   string `json:"config_json"`
	Cursor       string `json:"cursor"`
	Enabled      bool   `json:"enabled"`
	PollSec      int    `json:"poll_sec"`
	BindMode     string `json:"bind_mode"`
	BindSession  string `json:"bind_session"`
	TTL          string `json:"ttl"`
	LastRun      string `json:"last_run"`
	LastErr      string `json:"last_err"`
	ErrCount     int    `json:"err_count"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

const srcCols = "id, kind, name, config_json, cursor, enabled, poll_sec, bind_mode, bind_session, ttl, last_run, last_err, err_count, created_at, updated_at"

func srcFromRow(row map[string]interface{}) *Source {
	return &Source{
		ID: asStr(row["id"]), Kind: asStr(row["kind"]), Name: asStr(row["name"]),
		ConfigJSON: asStr(row["config_json"]), Cursor: asStr(row["cursor"]),
		Enabled: asNum(row["enabled"]) != 0, PollSec: int(asNum(row["poll_sec"])),
		BindMode: asStr(row["bind_mode"]), BindSession: asStr(row["bind_session"]), TTL: asStr(row["ttl"]),
		LastRun: asStr(row["last_run"]), LastErr: asStr(row["last_err"]), ErrCount: int(asNum(row["err_count"])),
		CreatedAt: asStr(row["created_at"]), UpdatedAt: asStr(row["updated_at"]),
	}
}

func (s *Store) CreateSource(src *Source) error {
	if src.ID == "" {
		src.ID = "src-" + util.RandomHex(4)
	}
	now := nowStr()
	src.CreatedAt, src.UpdatedAt = now, now
	if src.TTL == "" {
		src.TTL = "3d"
	}
	if src.BindMode == "" {
		src.BindMode = "auto"
	}
	_, err := s.db.Execute(
		`INSERT INTO super_source (id, kind, name, config_json, cursor, enabled, poll_sec, bind_mode, bind_session, ttl, last_run, last_err, err_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		src.ID, src.Kind, src.Name, src.ConfigJSON, src.Cursor, boolInt(src.Enabled), src.PollSec,
		src.BindMode, src.BindSession, src.TTL, src.LastRun, src.LastErr, src.ErrCount, src.CreatedAt, src.UpdatedAt)
	return err
}

func (s *Store) GetSource(id string) (*Source, error) {
	row, err := s.db.FetchOne("SELECT "+srcCols+" FROM super_source WHERE id = ?", id)
	if err != nil || row == nil {
		return nil, err
	}
	return srcFromRow(row), nil
}

func (s *Store) ListSources() ([]*Source, error) {
	rows, err := s.db.FetchAll("SELECT " + srcCols + " FROM super_source ORDER BY created_at ASC")
	if err != nil {
		return nil, err
	}
	out := make([]*Source, 0, len(rows))
	for _, r := range rows {
		out = append(out, srcFromRow(r))
	}
	return out, nil
}

func (s *Store) SourceSetEnabled(id string, on bool) error {
	_, err := s.db.Execute("UPDATE super_source SET enabled = ?, updated_at = ? WHERE id = ?", boolInt(on), nowStr(), id)
	return err
}

// SourceSetConfig replaces a source's config_json(面板改「处理要求」用)。
func (s *Store) SourceSetConfig(id, configJSON string) error {
	_, err := s.db.Execute("UPDATE super_source SET config_json = ?, updated_at = ? WHERE id = ?", configJSON, nowStr(), id)
	return err
}

// SourceSetResult records one poll outcome: run timestamp, and either the
// advanced cursor (ok) or the error line (fail counter moves on its own).
func (s *Store) SourceSetResult(id, runAt, cursor, failErr string) error {
	if failErr == "" {
		_, err := s.db.Execute(
			`UPDATE super_source SET last_run = ?, cursor = CASE WHEN ? != '' THEN ? ELSE cursor END,
			   last_err = '', err_count = 0, updated_at = ? WHERE id = ?`,
			runAt, cursor, cursor, nowStr(), id)
		return err
	}
	_, err := s.db.Execute(
		`UPDATE super_source SET last_run = ?, last_err = ?, err_count = err_count + 1, updated_at = ? WHERE id = ?`,
		runAt, truncate(failErr, 300), nowStr(), id)
	return err
}

func (s *Store) DeleteSource(id string) error {
	if _, err := s.db.Execute("DELETE FROM super_source WHERE id = ?", id); err != nil {
		return err
	}
	_, err := s.db.Execute("DELETE FROM super_binding WHERE src_id = ?", id)
	return err
}

// --- ticket (工单) ---

type Ticket struct {
	ID        string `json:"id"`
	SrcID     string `json:"src_id"`
	Title     string `json:"title"`
	Note      string `json:"note"`
	Text      string `json:"text"`
	State     string `json:"state"`
	Session   string `json:"session"`
	Actor     string `json:"actor"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ClosedAt  string `json:"closed_at"`
	Reopened  int    `json:"reopened"`
}

const ticketCols = "id, src_id, title, note, text, state, session, actor, created_at, updated_at, closed_at, reopened"

func ticketFromRow(row map[string]interface{}) *Ticket {
	return &Ticket{
		ID: asStr(row["id"]), SrcID: asStr(row["src_id"]), Title: asStr(row["title"]),
		Note: asStr(row["note"]), Text: asStr(row["text"]), State: asStr(row["state"]),
		Session: asStr(row["session"]), Actor: asStr(row["actor"]),
		CreatedAt: asStr(row["created_at"]), UpdatedAt: asStr(row["updated_at"]),
		ClosedAt: asStr(row["closed_at"]), Reopened: int(asNum(row["reopened"])),
	}
}

func (s *Store) CreateTicket(t *Ticket) error {
	if t.ID == "" {
		t.ID = "tk-" + util.RandomHex(6)
	}
	now := nowStr()
	t.CreatedAt, t.UpdatedAt = now, now
	if t.State == "" {
		t.State = "queued"
	}
	if t.Actor == "" {
		t.Actor = "human"
	}
	_, err := s.db.Execute(
		`INSERT INTO super_ticket (id, src_id, title, note, text, state, session, actor, created_at, updated_at, closed_at, reopened)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0)`,
		t.ID, t.SrcID, t.Title, t.Note, t.Text, t.State, t.Session, t.Actor, t.CreatedAt, t.UpdatedAt)
	return err
}

func (s *Store) GetTicket(id string) (*Ticket, error) {
	row, err := s.db.FetchOne("SELECT "+ticketCols+" FROM super_ticket WHERE id = ?", id)
	if err != nil || row == nil {
		return nil, err
	}
	return ticketFromRow(row), nil
}

// TicketSetState moves one ticket; session="" keeps the current landing.
// Entering closed stamps closed_at; leaving closed counts one reopen.
func (s *Store) TicketSetState(id, state, session string) error {
	if state == "closed" {
		_, err := s.db.Execute("UPDATE super_ticket SET state = 'closed', closed_at = ?, updated_at = ? WHERE id = ?",
			nowStr(), nowStr(), id)
		return err
	}
	if _, err := s.db.Execute("UPDATE super_ticket SET reopened = reopened + 1 WHERE id = ? AND state = 'closed'", id); err != nil {
		return err
	}
	q := "UPDATE super_ticket SET state = ?, updated_at = ?"
	args := []interface{}{state, nowStr()}
	if session != "" {
		q += ", session = ?"
		args = append(args, session)
	}
	q += " WHERE id = ?"
	args = append(args, id)
	_, err := s.db.Execute(q, args...)
	return err
}

func (s *Store) TicketCountState(states ...string) (int, error) {
	q := "SELECT COUNT(*) AS n FROM super_ticket WHERE state IN ("
	args := make([]interface{}, 0, len(states))
	for i, st := range states {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, st)
	}
	q += ")"
	row, err := s.db.FetchOne(q, args...)
	if err != nil || row == nil {
		return 0, err
	}
	return int(asNum(row["n"])), nil
}

// TicketQueuedFIFO lists queued tickets oldest first (出队按 created_at,
// 工单本身携带 src_id,不会找不回源)。
func (s *Store) TicketQueuedFIFO(limit int) ([]*Ticket, error) {
	rows, err := s.db.FetchAll(
		"SELECT "+ticketCols+" FROM super_ticket WHERE state = 'queued' ORDER BY rowid ASC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	out := make([]*Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, ticketFromRow(r))
	}
	return out, nil
}

func (s *Store) TicketsBySrc(srcID string) ([]*Ticket, error) {
	rows, err := s.db.FetchAll(
		"SELECT "+ticketCols+" FROM super_ticket WHERE src_id = ? ORDER BY rowid DESC", srcID)
	if err != nil {
		return nil, err
	}
	out := make([]*Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, ticketFromRow(r))
	}
	return out, nil
}

// TicketActiveBySrc returns the one live (queued|running) ticket of a source, if any.
func (s *Store) TicketActiveBySrc(srcID string) (*Ticket, error) {
	row, err := s.db.FetchOne(
		"SELECT " + ticketCols + " FROM super_ticket WHERE src_id = ? AND state IN ('queued','running') ORDER BY rowid ASC LIMIT 1", srcID)
	if err != nil || row == nil {
		return nil, err
	}
	return ticketFromRow(row), nil
}

func (s *Store) TicketActiveBySession(sessID string) (*Ticket, error) {
	row, err := s.db.FetchOne(
		"SELECT " + ticketCols + " FROM super_ticket WHERE session = ? AND state = 'running' ORDER BY rowid ASC LIMIT 1", sessID)
	if err != nil || row == nil {
		return nil, err
	}
	return ticketFromRow(row), nil
}

// ListTickets powers the panel tab: newest first, optionally only the live ones first.
func (s *Store) ListTickets(limit int) ([]*Ticket, error) {
	if limit <= 0 || limit > 500 {
		limit = 80
	}
	rows, err := s.db.FetchAll(
		"SELECT " + ticketCols + " FROM super_ticket ORDER BY CASE state WHEN 'running' THEN 0 WHEN 'queued' THEN 1 WHEN 'ready' THEN 2 ELSE 3 END, rowid DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	out := make([]*Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, ticketFromRow(r))
	}
	return out, nil
}

// --- binding (订阅即绑定:thread 地址 = 会话) ---

type Binding struct {
	SrcID          string `json:"src_id"`
	Sess           string `json:"sess"`
	Binder         string `json:"binder"`
	State          string `json:"state"`
	Forever        bool   `json:"forever"`
	UnsubscribedAt string `json:"unsubscribed_at"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

const bindCols = "src_id, sess, binder, state, forever, unsubscribed_at, created_at, updated_at"

func bindFromRow(row map[string]interface{}) *Binding {
	return &Binding{
		SrcID: asStr(row["src_id"]), Sess: asStr(row["sess"]), Binder: asStr(row["binder"]),
		State: asStr(row["state"]), Forever: asNum(row["forever"]) != 0,
		UnsubscribedAt: asStr(row["unsubscribed_at"]),
		CreatedAt:      asStr(row["created_at"]), UpdatedAt: asStr(row["updated_at"]),
	}
}

func (s *Store) BindSource(b *Binding) error {
	now := nowStr()
	b.CreatedAt, b.UpdatedAt = now, now
	if b.State == "" {
		b.State = "active"
	}
	_, err := s.db.Execute(
		`INSERT INTO super_binding (src_id, sess, binder, state, forever, unsubscribed_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(src_id) DO UPDATE SET sess = excluded.sess, binder = excluded.binder,
		   state = 'active', unsubscribed_at = '', updated_at = excluded.updated_at`,
		b.SrcID, b.Sess, b.Binder, b.State, boolInt(b.Forever), b.UnsubscribedAt, b.CreatedAt, b.UpdatedAt)
	return err
}

func (s *Store) GetBinding(srcID string) (*Binding, error) {
	row, err := s.db.FetchOne("SELECT "+bindCols+" FROM super_binding WHERE src_id = ?", srcID)
	if err != nil || row == nil {
		return nil, err
	}
	return bindFromRow(row), nil
}

func (s *Store) ListBindings() ([]*Binding, error) {
	rows, err := s.db.FetchAll("SELECT " + bindCols + " FROM super_binding ORDER BY created_at ASC")
	if err != nil {
		return nil, err
	}
	out := make([]*Binding, 0, len(rows))
	for _, r := range rows {
		out = append(out, bindFromRow(r))
	}
	return out, nil
}

// BindingUnsubscribe closes a subscription; forever keeps the grace open
// indefinitely, otherwise the GC drops the row 3 days later (v9 定稿)。
func (s *Store) BindingUnsubscribe(srcID string, forever bool) error {
	_, err := s.db.Execute(
		`UPDATE super_binding SET state = 'unsubscribed', forever = ?, unsubscribed_at = ?, updated_at = ? WHERE src_id = ?`,
		boolInt(forever), nowStr(), nowStr(), srcID)
	return err
}

func (s *Store) BindingReactivate(srcID string) error {
	_, err := s.db.Execute(
		"UPDATE super_binding SET state = 'active', unsubscribed_at = '', updated_at = ? WHERE src_id = ?", nowStr(), srcID)
	return err
}

func (s *Store) BindingDropExpired(cutoff string) ([]string, error) {
	rows, err := s.db.FetchAll(
		`SELECT src_id FROM super_binding WHERE state = 'unsubscribed' AND forever = 0 AND unsubscribed_at != '' AND unsubscribed_at <= ?`, cutoff)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		id := asStr(r["src_id"])
		if _, err := s.db.Execute("DELETE FROM super_binding WHERE src_id = ?", id); err == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

