package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/user/mini_code/session"
)

// TestSessionEventsAPI covers GET /api/sessions/{id}/events: paging with
// after, the tail_run window (everything after the last task/end), and the
// active flag the live UI resync reads.
func TestSessionEventsAPI(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("events-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()
	store := be.mgr.GetStore()

	// run 1 (ended) + run 2 (open)
	seqs := []struct {
		typ     string
		payload map[string]any
	}{
		{session.EventTaskStart, map[string]any{"goal": "one"}},
		{session.EventToolResult, map[string]any{"call_id": "c1", "name": "exec", "ok": true, "result": "r1"}},
		{session.EventTaskEnd, map[string]any{"status": "completed", "turns": 1}},
		{session.EventTaskStart, map[string]any{"goal": "two"}},
		{session.EventToolCall, map[string]any{"call_id": "c2", "name": "exec", "args": "{}"}},
	}
	for i, e := range seqs {
		seq, err := store.AppendEvent(id, e.typ, 1, e.payload)
		if err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
		if want := int64(i + 1); seq != want {
			t.Fatalf("seq = %d, want %d", seq, want)
		}
	}

	var resp struct {
		Events   []session.EventRecord `json:"events"`
		LastSeq  int64                 `json:"last_seq"`
		Active   bool                  `json:"active"`
	}
	if err := getJSON(api.URL+"/api/sessions/"+id+"/events", &resp); err != nil {
		t.Fatalf("GET events: %v", err)
	}
	if len(resp.Events) != 5 || resp.LastSeq != 5 {
		t.Fatalf("events = %d, last_seq = %d; want 5/5", len(resp.Events), resp.LastSeq)
	}
	if resp.Active {
		t.Fatal("active = true, want false (no task running)")
	}
	if resp.Events[1].Payload["result"] != "r1" {
		t.Fatalf("payload not round-tripped: %+v", resp.Events[1].Payload)
	}

	if err := getJSON(api.URL+"/api/sessions/"+id+"/events?after=3", &resp); err != nil {
		t.Fatalf("GET events?after=3: %v", err)
	}
	if len(resp.Events) != 2 || resp.Events[0].Seq != 4 {
		t.Fatalf("after=3: %d events starting %v; want 2 starting at 4", len(resp.Events), firstSeq(resp.Events))
	}

	if err := getJSON(api.URL+"/api/sessions/"+id+"/events?tail_run=1", &resp); err != nil {
		t.Fatalf("GET events?tail_run=1: %v", err)
	}
	if len(resp.Events) != 2 || resp.Events[0].Type != session.EventTaskStart {
		t.Fatalf("tail_run: %+v; want the two open-run events", resp.Events)
	}

	// bad cursor -> 400
	r, err := http.Get(api.URL + "/api/sessions/" + id + "/events?after=x")
	if err != nil {
		t.Fatalf("GET bad after: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

func firstSeq(evs []session.EventRecord) int64 {
	if len(evs) == 0 {
		return -1
	}
	return evs[0].Seq
}
