package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The session event ledger: an append-only log of everything that happened
// inside a conversation. The messages column of the sessions table stays as
// the projection the model and the history view read; this table is the
// factual record the UI reconciles against after an SSE gap and the place
// operational questions ("what was turn 9 doing at 11:03?") become a SQL
// query instead of a grep over agent.log. agent.log keeps only process
// diagnostics — things that cannot be derived from the ledger.
//
// Invariants:
//   - rows are never updated or deleted (append-only);
//   - seq is contiguous per session (1, 2, 3, ...), assigned under the
//     store's event mutex so a single process is strictly ordered; a second
//     process sharing the DB can only hit the PK guard and retry;
//   - deltas (LLM tokens, tool output chunks) are NOT events — they are
//     transport, and the committed assistant/message and tool/result rows
//     supersede them.
const (
	EventTaskStart        = "task/start"        // {goal, images, model}
	EventTaskEnd          = "task/end"          // {status: completed|failed|interrupted, turns, error?, reason?}
	EventTurnStart        = "turn/start"        // {turn}
	EventUserMessage      = "user/message"      // {text, source: input|queued|worker_notice|context_usage}
	EventAssistantMessage = "assistant/message" // {content, reasoning_chars, tool_calls?, usage?}
	EventToolCall         = "tool/call"         // {call_id, name, args}
	EventToolResult       = "tool/result"       // {call_id, name, ok, duration_ms, result | error}; a pruned replacement row repeats the call_id with "replaces" set (see compaction/prune)
	EventCompactionPrune  = "compaction/prune"  // {call_id, name, chars_before, chars_after, spill?} — shadow event pricing one tool-result prune; the following tool/result row with the same call_id supersedes the full row
	EventCompactionArgAge = "compaction/argage" // {call_id, name, chars_before, chars_after} — shadow event for aging a successful big write/edit call's arguments out of the model history (see argage.go)
	EventCompaction       = "compaction"        // {before_tokens, after_tokens, kind: auto|manual}
	EventGuardSteer       = "guard/steer"       // {steer} — legacy: emitted by the removed continue guard (loop_continue.go, deleted)

	// The background-command ledger (job/jobs.go). A command that outlives its
	// foreground wait stops being a tool result and becomes a piece of work with
	// a lifetime of its own, so its lifetime is recorded the same way every other
	// fact in this table is: registered when it starts, promoted when the wait
	// gave up on it, settled when it ended. "How long was the model waiting on
	// tools" must stay a query, not a guess — without these rows the wall-clock
	// spent inside a promoted job would be invisible, which is precisely the cost
	// this machinery exists to remove.
	EventJobRegistered = "job/registered" // {job_id, kind, label}
	EventJobPromoted   = "job/promoted"   // {job_id, kind, label, waited_ms} — the wait expired; the work continues
	EventJobSettled    = "job/settled"    // {job_id, kind, status, detail, waited_ms?, elapsed_ms, bytes, notified}
	// EventPolicyObserve records what a mutation was done to a file the session
	// had or had not looked at (see tools/policy.go).
	EventPolicyObserve = "policy/observe" // {call_id, tool, path, blind}

	// EventPersistFailed records that the conversation could not be written to
	// disk (F1). This is the one ledger row that may itself fail to be written
	// (the database is what broke), which is why the failure is also logged and
	// surfaced as a runtime notice to the model.
	EventPersistFailed = "persist/failed" // {error, failures}
)

// EventRecord is one row of the ledger as returned to readers.
type EventRecord struct {
	Seq       int64          `json:"seq"`
	Type      string         `json:"type"`
	Turn      int            `json:"turn"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"created_at"`
}

// initEvents creates the ledger table. Called from initDB.
func (s *SessionStore) initEvents() {
	s.db.InitTable(`CREATE TABLE IF NOT EXISTS session_event (
		session_id TEXT NOT NULL,
		seq        INTEGER NOT NULL,
		type       TEXT NOT NULL,
		turn       INTEGER NOT NULL DEFAULT 0,
		payload    TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL,
		PRIMARY KEY (session_id, seq)
	)`)
}

// AppendEvent adds one fact to the ledger and returns its seq.
//
// The seq is assigned and returned by the SAME statement (`INSERT … SELECT
// MAX(seq)+1 … RETURNING seq`), which is what fixes the cross-process race the
// earlier version had: it inserted with MAX+1 and then ran a separate
// `SELECT MAX(seq)`, so a second writer process could slip a row in between and
// the value returned to the caller belonged to somebody else's event. The
// per-session shard lock keeps one process strictly ordered; a second writer can
// still collide on the primary key, which the bounded retry below absorbs.
//
// Best-effort callers (the loop) log failures and carry on — a ledger write must
// never break a task.
func (s *SessionStore) AppendEvent(sessionID, typ string, turn int, payload any) (int64, error) {
	data := []byte("{}")
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, fmt.Errorf("marshal event payload: %w", err)
		}
		data = b
	}
	mu := s.evLock(sessionID)
	mu.Lock()
	defer mu.Unlock()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		row, err := s.db.FetchOne(
			`INSERT INTO session_event(session_id, seq, type, turn, payload, created_at)
			 SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ? FROM session_event WHERE session_id = ?
			 RETURNING seq`,
			sessionID, typ, turn, string(data), time.Now().Format(time.RFC3339Nano), sessionID)
		if err == nil && row != nil {
			return toInt64(row["seq"]), nil
		}
		if err == nil {
			err = fmt.Errorf("event insert returned no seq")
		}
		lastErr = err
		// Only a seq collision (another writer process appended first) is worth
		// retrying; anything else fails fast.
		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, err
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return 0, lastErr
}

// PruneEvents deletes ledger rows older than the cutoff and reports how many
// rows went. The ledger keeps whole tool results, so without a retention policy
// a long-lived install grows without bound (spill files had a 30-day sweep;
// the table itself had none).
func (s *SessionStore) PruneEvents(before time.Time) (int64, error) {
	res, err := s.db.Execute("DELETE FROM session_event WHERE created_at < ?", before.Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListEvents returns up to limit events with seq > after, oldest first.
// limit <= 0 uses the default (500), capped at 5000.
func (s *SessionStore) ListEvents(sessionID string, after int64, limit int) ([]EventRecord, error) {
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := s.db.FetchAll(
		"SELECT seq, type, turn, payload, created_at FROM session_event WHERE session_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?",
		sessionID, after, limit)
	if err != nil {
		return nil, err
	}
	return rowsToEvents(rows)
}

// TailRunEvents returns the events of the current (or last) run: everything
// after the most recent task/end, or the whole ledger when the session never
// ended a task. The live UI reconciles a run with this — it is exactly the
// window an SSE gap could have hidden.
func (s *SessionStore) TailRunEvents(sessionID string) ([]EventRecord, error) {
	row, err := s.db.FetchOne(
		"SELECT COALESCE(MAX(seq), 0) AS seq FROM session_event WHERE session_id = ? AND type = ?",
		sessionID, EventTaskEnd)
	if err != nil {
		return nil, err
	}
	after := int64(0)
	if row != nil {
		after = toInt64(row["seq"])
	}
	return s.ListEvents(sessionID, after, 5000)
}

// LastEventSeq is the highest seq in the ledger (0 when empty).
func (s *SessionStore) LastEventSeq(sessionID string) (int64, error) {
	row, err := s.db.FetchOne("SELECT COALESCE(MAX(seq), 0) AS seq FROM session_event WHERE session_id = ?", sessionID)
	if err != nil || row == nil {
		return 0, err
	}
	return toInt64(row["seq"]), nil
}

func rowsToEvents(rows []map[string]any) ([]EventRecord, error) {
	out := make([]EventRecord, 0, len(rows))
	for _, row := range rows {
		ev := EventRecord{
			Seq:       toInt64(row["seq"]),
			Type:      fmt.Sprint(row["type"]),
			Turn:      int(toInt64(row["turn"])),
			CreatedAt: fmt.Sprint(row["created_at"]),
		}
		payload := map[string]any{}
		if raw, ok := row["payload"].(string); ok && raw != "" {
			// A malformed payload must not hide the row: keep the raw text.
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				payload = map[string]any{"raw": raw}
			}
		}
		ev.Payload = payload
		out = append(out, ev)
	}
	return out, nil
}

// toInt64 coerces the driver's numeric column types (int64, float64,
// []byte, string) to int64.
func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case []byte:
		var i int64
		fmt.Sscan(string(n), &i)
		return i
	case string:
		var i int64
		fmt.Sscan(n, &i)
		return i
	}
	return 0
}
