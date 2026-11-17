package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	EventTaskStart        = "task/start"
	EventTaskEnd          = "task/end"
	EventTurnStart        = "turn/start"
	EventUserMessage      = "user/message"
	EventAssistantMessage = "assistant/message"
	EventToolCall         = "tool/call"
	EventToolResult       = "tool/result"
	EventCompactionPrune  = "compaction/prune"
	EventCompactionArgAge = "compaction/argage"
	EventCompaction       = "compaction"

	EventJobRegistered = "job/registered"
	EventJobPromoted   = "job/promoted"
	EventJobSettled    = "job/settled"
	EventPolicyObserve = "policy/observe"

	EventPersistFailed = "persist/failed"
)

type EventRecord struct {
	Seq       int64          `json:"seq"`
	Type      string         `json:"type"`
	Turn      int            `json:"turn"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"created_at"`
}

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
		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, err
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return 0, lastErr
}

func (s *SessionStore) PruneEvents(before time.Time) (int64, error) {
	res, err := s.db.Execute("DELETE FROM session_event WHERE created_at < ?", before.Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *SessionStore) PruneOrphans(olderThan time.Time) (events, messages int64, err error) {
	cut := olderThan.Format(time.RFC3339Nano)
	res, err := s.db.Execute(`DELETE FROM session_event
		WHERE created_at < ? AND session_id NOT IN (SELECT session_id FROM sessions)`, cut)
	if err != nil {
		return 0, 0, err
	}
	events, _ = res.RowsAffected()
	res, err = s.db.Execute(`DELETE FROM session_message
		WHERE created_at < ? AND session_id NOT IN (SELECT session_id FROM sessions)`, cut)
	if err != nil {
		return events, 0, err
	}
	messages, _ = res.RowsAffected()
	return events, messages, nil
}

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
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				payload = map[string]any{"raw": raw}
			}
		}
		ev.Payload = payload
		out = append(out, ev)
	}
	return out, nil
}

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
