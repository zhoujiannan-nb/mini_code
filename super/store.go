// Package super implements the Super-Agent (lite) subsystem: the resident
// entry keeps exactly one feature — 定时任务. A schedule is "到点把一句写好的
// 话投递给绑定的会话", nothing else. The v8/v9 machinery (workers, message
// channel, queue, sources, bindings, tickets, script runner) has been retired
// wholesale; its tables are dropped on startup.
package super

import (
	"time"

	"github.com/user/mini_code/util"
)

// Store holds the lite super_* tables on the shared SQLite connection.
type Store struct {
	db *util.Database
}

func NewStore(db *util.Database) *Store {
	s := &Store{db: db}
	s.init()
	return s
}

// legacyTables are the retired subsystems; their rows are worthless now.
var legacyTables = []string{
	"super_worker", "super_worker_session", "super_message", "super_queue",
	"super_process", "super_source", "super_ticket", "super_binding",
}

func (s *Store) init() {
	for _, t := range legacyTables {
		_, _ = s.db.Execute("DROP TABLE IF EXISTS " + t)
	}
	// The old schedule table keyed on script file names — new shape is
	// id-keyed with a bound session; recreate from scratch (data retired).
	_, _ = s.db.Execute("DROP TABLE IF EXISTS super_schedule")
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS super_schedule (
		id TEXT PRIMARY KEY,            -- sch-xxxx
		name TEXT NOT NULL DEFAULT '',
		text TEXT NOT NULL,             -- 到点投递的那一句话
		session TEXT NOT NULL DEFAULT '', -- 绑定会话;'' = super 会话本身
		every_sec INTEGER NOT NULL DEFAULT 3600,
		enabled INTEGER NOT NULL DEFAULT 1,
		last_run TEXT NOT NULL DEFAULT '',
		created_at TEXT,
		updated_at TEXT
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

// --- schedule ---

type Schedule struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Text      string `json:"text"`
	Session   string `json:"session"`
	EverySec  int    `json:"every_sec"`
	Enabled   bool   `json:"enabled"`
	LastRun   string `json:"last_run"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const schCols = "id, name, text, session, every_sec, enabled, last_run, created_at, updated_at"

func schFromRow(r map[string]interface{}) *Schedule {
	return &Schedule{
		ID: asStr(r["id"]), Name: asStr(r["name"]), Text: asStr(r["text"]),
		Session: asStr(r["session"]), EverySec: asInt(r["every_sec"]),
		Enabled: asInt(r["enabled"]) == 1, LastRun: asStr(r["last_run"]),
		CreatedAt: asStr(r["created_at"]), UpdatedAt: asStr(r["updated_at"]),
	}
}

func (s *Store) CreateSchedule(sc *Schedule) error {
	now := nowStr()
	if sc.CreatedAt == "" {
		sc.CreatedAt = now
	}
	sc.UpdatedAt = now
	_, err := s.db.Execute(
		`INSERT INTO super_schedule(id,name,text,session,every_sec,enabled,last_run,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		sc.ID, sc.Name, sc.Text, sc.Session, sc.EverySec, boolInt(sc.Enabled), sc.LastRun, sc.CreatedAt, sc.UpdatedAt)
	return err
}

func (s *Store) GetSchedule(id string) (*Schedule, error) {
	row, err := s.db.FetchOne("SELECT "+schCols+" FROM super_schedule WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return schFromRow(row), nil
}

func (s *Store) ListSchedules() ([]*Schedule, error) {
	rows, err := s.db.FetchAll("SELECT " + schCols + " FROM super_schedule ORDER BY created_at ASC")
	if err != nil {
		return nil, err
	}
	out := make([]*Schedule, 0, len(rows))
	for _, r := range rows {
		out = append(out, schFromRow(r))
	}
	return out, nil
}

func (s *Store) ScheduleSetEnabled(id string, on bool) error {
	_, err := s.db.Execute("UPDATE super_schedule SET enabled = ?, updated_at = ? WHERE id = ?", boolInt(on), nowStr(), id)
	return err
}

func (s *Store) ScheduleSetResult(id, runAt string) error {
	_, err := s.db.Execute("UPDATE super_schedule SET last_run = ?, updated_at = ? WHERE id = ?", runAt, nowStr(), id)
	return err
}

func (s *Store) DeleteSchedule(id string) error {
	_, err := s.db.Execute("DELETE FROM super_schedule WHERE id = ?", id)
	return err
}

// Due reports whether an enabled schedule's period has elapsed. A schedule
// that never fired is due immediately (first tick after boot delivers it).
func (sc *Schedule) Due(now time.Time) bool {
	if !sc.Enabled {
		return false
	}
	if sc.LastRun == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, sc.LastRun)
	if err != nil {
		return true
	}
	return now.Sub(last) >= time.Duration(sc.EverySec)*time.Second
}

// --- state ---

func (s *Store) StateGet(key string) string {
	row, err := s.db.FetchOne("SELECT value FROM super_state WHERE key = ?", key)
	if err != nil || row == nil {
		return ""
	}
	return asStr(row["value"])
}

func (s *Store) StateSet(key, value string) error {
	_, err := s.db.Execute(
		`INSERT INTO super_state(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
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
		`INSERT INTO super_audit(ts,actor,action,target,detail,refs) VALUES(?,?,?,?,?,?)`,
		time.Now().Format("2006-01-02 15:04:05"), actor, action, target, detail, refs)
	return err
}

func (s *Store) AuditRecent(n int) ([]*AuditRow, error) {
	rows, err := s.db.FetchAll(
		"SELECT id, ts, actor, action, target, detail, refs FROM super_audit ORDER BY id DESC LIMIT ?", n)
	if err != nil {
		return nil, err
	}
	out := make([]*AuditRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, &AuditRow{
			ID: asInt64(r["id"]), Ts: asStr(r["ts"]), Actor: asStr(r["actor"]),
			Action: asStr(r["action"]), Target: asStr(r["target"]),
			Detail: asStr(r["detail"]), Refs: asStr(r["refs"]),
		})
	}
	return out, nil
}

// --- session picker (shared sessions table, read-only) ---

type SessPick struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// ListBindableSessions lists ordinary top-level sessions for the 定时任务
// binding dropdown. The super family itself is excluded; the rows belong to
// the session store and are never written from here.
func (s *Store) ListBindableSessions() ([]*SessPick, error) {
	rows, err := s.db.FetchAll(
		`SELECT session_id, COALESCE(title,'') AS title, COALESCE(status,'') AS status
		 FROM sessions
		 WHERE COALESCE(agent_role,'') != 'super' AND COALESCE(parent_id,'') = ''
		 ORDER BY updated_at DESC LIMIT 400`)
	if err != nil {
		return nil, err
	}
	out := make([]*SessPick, 0, len(rows))
	for _, r := range rows {
		out = append(out, &SessPick{ID: asStr(r["session_id"]), Title: asStr(r["title"]), Status: asStr(r["status"])})
	}
	return out, nil
}

// --- small helpers ---

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nowStr() string { return time.Now().Format(time.RFC3339) }

func asStr(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return ""
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

func asInt64(v interface{}) int64 {
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
