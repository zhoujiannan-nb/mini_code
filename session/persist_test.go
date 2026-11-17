package session

import (
	"strings"
	"testing"
	"time"

	"github.com/user/mini_code/provider"
)

func msgCount(t *testing.T, mgr *SessionManager, sessionID string) int {
	t.Helper()
	row, err := mgr.GetStore().db.FetchOne(
		"SELECT COUNT(*) AS n FROM session_message WHERE session_id = ?", sessionID)
	if err != nil {
		t.Fatalf("count message rows: %v", err)
	}
	return asInt(row["n"])
}

func TestMessageRowsAppendAndReload(t *testing.T) {
	mgr := newTestManager(t)
	s := mustSession(t, mgr, "rows")
	if got := msgCount(t, mgr, s.ID()); got != 0 {
		t.Fatalf("new session has %d message rows, want 0", got)
	}

	s.mu.Lock()
	s.record.Messages = append(s.record.Messages,
		providerMessage("user", "hello"),
		providerMessage("assistant", "hi there"))
	s.persistTurnLocked()
	s.mu.Unlock()
	if got := msgCount(t, mgr, s.ID()); got != 2 {
		t.Fatalf("after turn 1: %d rows, want 2", got)
	}

	s.mu.Lock()
	s.record.Messages = append(s.record.Messages,
		providerMessage("user", "again"),
		providerMessage("assistant", "sure"))
	s.persistTurnLocked()
	s.mu.Unlock()
	if got := msgCount(t, mgr, s.ID()); got != 4 {
		t.Fatalf("after turn 2: %d rows, want 4", got)
	}

	mgr2, err := NewSessionManager(nil, mgr.GetStore().db.Path())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer mgr2.Close()
	rec, err := mgr2.GetStore().Get(s.ID())
	if err != nil || rec == nil {
		t.Fatalf("reload: %v", err)
	}
	if len(rec.Messages) != 4 {
		t.Fatalf("reloaded %d messages, want 4: %+v", len(rec.Messages), rec.Messages)
	}
	if rec.Messages[3].GetText() != "sure" {
		t.Fatalf("reloaded tail = %q, want %q", rec.Messages[3].GetText(), "sure")
	}
}

func TestMessageRowsFullRewriteWhenPrefixChanges(t *testing.T) {
	mgr := newTestManager(t)
	s := mustSession(t, mgr, "rewrite")
	s.mu.Lock()
	s.record.Messages = append(s.record.Messages,
		providerMessage("user", "a"),
		providerMessage("assistant", "b"))
	s.persistTurnLocked()
	s.mu.Unlock()
	if got := msgCount(t, mgr, s.ID()); got != 2 {
		t.Fatalf("setup: %d rows", got)
	}

	s.mu.Lock()
	s.record.Messages = []provider.Message{providerMessage("assistant", "[summary of the past]")}
	s.persistTurnLocked()
	s.mu.Unlock()

	if got := msgCount(t, mgr, s.ID()); got != 1 {
		t.Fatalf("after rewrite: %d rows, want 1", got)
	}
	rec, err := mgr.GetStore().Get(s.ID())
	if err != nil || rec == nil {
		t.Fatalf("reload: %v", err)
	}
	if len(rec.Messages) != 1 || !strings.Contains(rec.Messages[0].GetText(), "summary") {
		t.Fatalf("reloaded = %+v, want the single summary", rec.Messages)
	}
}

func TestEventSeqIsReturnedExactly(t *testing.T) {
	mgr := newTestManager(t)
	s := mustSession(t, mgr, "seq")
	store := mgr.GetStore()

	var seqs []int64
	for i := 0; i < 5; i++ {
		seq, err := store.AppendEvent(s.ID(), EventTurnStart, i+1, map[string]any{"turn": i + 1})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		seqs = append(seqs, seq)
	}
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("seq[%d] = %d, want %d (contiguous from 1)", i, seq, i+1)
		}
	}

	row, err := store.db.FetchOne(
		"SELECT payload FROM session_event WHERE session_id = ? AND seq = ?", s.ID(), seqs[2])
	if err != nil || row == nil {
		t.Fatalf("row for seq %d missing: %v", seqs[2], err)
	}
	if !strings.Contains(asString(row["payload"]), `"turn":3`) {
		t.Fatalf("seq %d points at the wrong row: %v", seqs[2], row["payload"])
	}
}

func TestDeleteRemovesChildrenAndLedgerAtomically(t *testing.T) {
	mgr := newTestManager(t)
	parent := mustSession(t, mgr, "parent")
	pid := parent.ID()
	child, err := mgr.CreateSessionRecord("child", "work", parent.WorkDir(), &pid)
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	store := mgr.GetStore()
	if _, err := store.AppendEvent(pid, EventTaskStart, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(child.ID(), EventTaskStart, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessages(pid, []provider.Message{providerMessage("user", "parent turn")}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessages(child.ID(), []provider.Message{providerMessage("user", "child turn")}, 1); err != nil {
		t.Fatal(err)
	}
	if msgCount(t, mgr, pid) != 1 || msgCount(t, mgr, child.ID()) != 1 {
		t.Fatal("setup: message rows missing")
	}

	if err := store.Delete(pid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, q := range []struct{ sql string; arg string }{
		{"SELECT COUNT(*) AS n FROM sessions WHERE session_id = ?", pid},
		{"SELECT COUNT(*) AS n FROM sessions WHERE session_id = ?", child.ID()},
		{"SELECT COUNT(*) AS n FROM session_event WHERE session_id = ?", pid},
		{"SELECT COUNT(*) AS n FROM session_event WHERE session_id = ?", child.ID()},
		{"SELECT COUNT(*) AS n FROM session_message WHERE session_id = ?", pid},
		{"SELECT COUNT(*) AS n FROM session_message WHERE session_id = ?", child.ID()},
	} {
		row, err := store.db.FetchOne(q.sql, q.arg)
		if err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
		if n := asInt(row["n"]); n != 0 {
			t.Fatalf("%s (%s) left %d rows, want 0", q.sql, q.arg, n)
		}
	}
}

func TestOrphanSweepRemovesRowsOfDeletedSessions(t *testing.T) {
	mgr := newTestManager(t)
	s := mustSession(t, mgr, "orphaned")
	store := mgr.GetStore()
	if _, err := store.AppendEvent(s.ID(), EventTaskStart, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessages(s.ID(), []provider.Message{providerMessage("user", "gone")}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Execute("DELETE FROM sessions WHERE session_id = ?", s.ID()); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-orphanGrace - time.Hour).Format(time.RFC3339Nano)
	for _, q := range []string{"UPDATE session_event SET created_at = ? WHERE session_id = ?",
		"UPDATE session_message SET created_at = ? WHERE session_id = ?"} {
		if _, err := store.db.Execute(q, old, s.ID()); err != nil {
			t.Fatal(err)
		}
	}
	live := mustSession(t, mgr, "live")
	if _, err := store.AppendEvent(live.ID(), EventTaskStart, 1, nil); err != nil {
		t.Fatal(err)
	}

	ev, msg, err := store.PruneOrphans(time.Now().Add(-orphanGrace))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ev != 1 || msg != 1 {
		t.Fatalf("swept events=%d messages=%d, want 1/1", ev, msg)
	}
	row, err := store.db.FetchOne("SELECT COUNT(*) AS n FROM session_event WHERE session_id = ?", live.ID())
	if err != nil {
		t.Fatal(err)
	}
	if asInt(row["n"]) != 1 {
		t.Fatalf("live session lost rows to the sweep: %+v", row)
	}

	if _, err := store.db.Execute("UPDATE session_event SET created_at = ? WHERE session_id = ?", old, live.ID()); err != nil {
		t.Fatal(err)
	}
	if ev, _, err := store.PruneOrphans(time.Now().Add(-orphanGrace)); err != nil || ev != 0 {
		t.Fatalf("backdated live rows swept: ev=%d err=%v", ev, err)
	}
}

func TestPersistFailureIsRecordedAndReported(t *testing.T) {
	mgr := newTestManager(t)
	s := mustSession(t, mgr, "failing")
	if err := mgr.GetStore().db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	s.mu.Lock()
	s.record.Messages = append(s.record.Messages, providerMessage("user", "still working?"))
	s.persistTurnLocked()
	s.mu.Unlock()

	failures, lastErr := s.PersistHealth()
	if failures == 0 {
		t.Fatal("a failed write was not recorded (F1 regression)")
	}
	if lastErr == "" {
		t.Fatal("the failure has no error text")
	}

	s.mu.Lock()
	s.record.Messages = append(s.record.Messages, providerMessage("assistant", "yes"))
	s.persistTurnLocked()
	s.mu.Unlock()

	notices := 0
	for _, m := range s.SnapshotMessages() {
		if strings.Contains(m.GetText(), "__MC_PERSIST_FAILED__") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("persist-failure notices = %d, want exactly 1", notices)
	}
	queued := 0
	for _, text := range s.DrainUserMessages() {
		if strings.Contains(text, "__MC_PERSIST_FAILED__") {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("queued persist-failure notices = %d, want 1", queued)
	}
}

func TestLedgerRetentionPrunesOldRows(t *testing.T) {
	mgr := newTestManager(t)
	s := mustSession(t, mgr, "retention")
	store := mgr.GetStore()
	if _, err := store.AppendEvent(s.ID(), EventTurnStart, 1, nil); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-eventRetention - time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.Execute("UPDATE session_event SET created_at = ? WHERE session_id = ?", old, s.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(s.ID(), EventTurnStart, 2, nil); err != nil {
		t.Fatal(err)
	}

	n, err := store.PruneEvents(time.Now().Add(-eventRetention))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
	row, err := store.db.FetchOne("SELECT COUNT(*) AS n FROM session_event WHERE session_id = ?", s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := asInt(row["n"]); got != 1 {
		t.Fatalf("rows after prune = %d, want the one recent row", got)
	}
}

func providerMessage(role, text string) provider.Message {
	return provider.Message{Role: role, Content: text}
}
