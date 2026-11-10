package session

import (
	"path/filepath"
	"sync"
	"testing"
)

func newEventTestStore(t *testing.T) *SessionStore {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestEventAppendAndList(t *testing.T) {
	store := newEventTestStore(t)
	const sid = "sess-1"

	seq1, err := store.AppendEvent(sid, EventTaskStart, 0, map[string]any{"goal": "hello"})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if seq1 != 1 {
		t.Fatalf("first seq = %d, want 1", seq1)
	}
	if _, err := store.AppendEvent(sid, EventTurnStart, 1, nil); err != nil {
		t.Fatalf("AppendEvent(nil payload): %v", err)
	}
	if _, err := store.AppendEvent(sid, EventToolResult, 1, map[string]any{"call_id": "c1", "name": "exec", "ok": true, "result": "done"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	// A second session has its own seq space.
	if s2, err := store.AppendEvent("other", EventTaskStart, 0, nil); err != nil || s2 != 1 {
		t.Fatalf("other session seq = %d, err = %v; want 1, nil", s2, err)
	}

	evs, err := store.ListEvents(sid, 0, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("len(evs) = %d, want 3", len(evs))
	}
	for i, ev := range evs {
		if want := int64(i + 1); ev.Seq != want {
			t.Fatalf("evs[%d].Seq = %d, want %d", i, ev.Seq, want)
		}
	}
	if evs[0].Type != EventTaskStart || evs[0].Payload["goal"] != "hello" {
		t.Fatalf("event 0 = %+v, want task/start with goal", evs[0])
	}
	if evs[1].Type != EventTurnStart || evs[1].Turn != 1 {
		t.Fatalf("event 1 = %+v, want turn/start turn 1", evs[1])
	}
	if evs[2].Payload["call_id"] != "c1" || evs[2].Payload["ok"] != true {
		t.Fatalf("event 2 payload = %+v, want call_id/ok", evs[2].Payload)
	}

	// after-cursor paging
	tail, err := store.ListEvents(sid, 2, 0)
	if err != nil || len(tail) != 1 || tail[0].Seq != 3 {
		t.Fatalf("ListEvents(after=2) = %+v, %v; want one event seq 3", tail, err)
	}
	if last, err := store.LastEventSeq(sid); err != nil || last != 3 {
		t.Fatalf("LastEventSeq = %d, %v; want 3", last, err)
	}
}

func TestTailRunEvents(t *testing.T) {
	store := newEventTestStore(t)
	const sid = "sess-tail"

	// run 1
	for _, e := range []struct {
		typ string
	}{
		{EventTaskStart}, {EventTurnStart}, {EventTaskEnd},
		// run 2 (still open)
		{EventTaskStart}, {EventTurnStart}, {EventToolCall},
	} {
		if _, err := store.AppendEvent(sid, e.typ, 0, nil); err != nil {
			t.Fatalf("AppendEvent(%s): %v", e.typ, err)
		}
	}

	tail, err := store.TailRunEvents(sid)
	if err != nil {
		t.Fatalf("TailRunEvents: %v", err)
	}
	if len(tail) != 3 {
		t.Fatalf("len(tail) = %d, want 3 (events after the last task/end)", len(tail))
	}
	if tail[0].Type != EventTaskStart || tail[2].Type != EventToolCall {
		t.Fatalf("tail = %+v, want the open run's events", tail)
	}

	// A session that never ended a task returns the whole ledger.
	const sid2 = "sess-first"
	if _, err := store.AppendEvent(sid2, EventTaskStart, 0, nil); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	all, err := store.TailRunEvents(sid2)
	if err != nil || len(all) != 1 {
		t.Fatalf("TailRunEvents(first run) = %+v, %v; want 1 event", all, err)
	}
}

// TestEventConcurrentAppend mirrors the real writer pattern: parallel tool
// goroutines of one session appending results at the same time. Seqs must
// stay unique and contiguous.
func TestEventConcurrentAppend(t *testing.T) {
	store := newEventTestStore(t)
	const sid = "sess-race"
	const writers, perWriter = 8, 10

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := store.AppendEvent(sid, EventToolResult, 0, map[string]any{"i": i}); err != nil {
					t.Errorf("AppendEvent: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	evs, err := store.ListEvents(sid, 0, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != writers*perWriter {
		t.Fatalf("len(evs) = %d, want %d", len(evs), writers*perWriter)
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("gap at index %d: seq = %d", i, ev.Seq)
		}
	}
}
