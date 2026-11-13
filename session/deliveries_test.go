package session

import "testing"

// TestDeliverStaleRunningStatus verifies that a persisted "running" status
// with no live loop behind it (stale after a restart/crash) does not swallow
// deliveries: with the live task check wired, an idle-in-reality session is
// dispatched normally, while a genuinely live one still queues.
func TestDeliverStaleRunningStatus(t *testing.T) {
	mgr, rec := newDeliveryEnv(t)
	s := mustSession(t, mgr, "stale")
	s.Record().Status = "running" // stale row: nothing actually running

	mgr.SetActiveTaskCheck(func(string) bool { return false })
	if err := mgr.DeliverToSession(s.ID(), "stale 投递"); err != nil {
		t.Fatalf("DeliverToSession: %v", err)
	}
	if got := rec.forSession(s.ID()); len(got) != 1 || got[0] != "stale 投递" {
		t.Fatalf("stale-running session must be dispatched, got %q", got)
	}
	if got := s.DrainUserMessages(); len(got) != 0 {
		t.Fatalf("stale-running session must not queue, drained %q", got)
	}

	mgr.SetActiveTaskCheck(func(string) bool { return true })
	if err := mgr.DeliverToSession(s.ID(), "live 投递"); err != nil {
		t.Fatalf("DeliverToSession: %v", err)
	}
	if got := s.DrainUserMessages(); len(got) != 1 || got[0] != "live 投递" {
		t.Fatalf("live session must queue, drained = %q", got)
	}
}

// TestQueueUserMessagePersistsImmediately: a message queued while the session
// is running must be visible in persisted history at queue time, so a web
// refresh (which re-renders only SnapshotMessages) does not lose it.
func TestQueueUserMessagePersistsImmediately(t *testing.T) {
	mgr, _ := newDeliveryEnv(t)
	s := mustSession(t, mgr, "worker")
	mgr.SetActiveTaskCheck(func(string) bool { return true }) // simulate a live task

	if err := mgr.DeliverToSession(s.ID(), "排队消息"); err != nil {
		t.Fatalf("DeliverToSession: %v", err)
	}
	msgs := s.SnapshotMessages()
	if len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Content != "排队消息" {
		t.Fatalf("queued message not persisted at queue time: %+v", msgs)
	}
	// Still in the queue for the loop's next-turn drain (exactly one copy).
	if got := s.DrainUserMessages(); len(got) != 1 || got[0] != "排队消息" {
		t.Fatalf("drained = %q", got)
	}
	// A second queued message must also persist without duplication.
	if err := mgr.DeliverToSession(s.ID(), "第二条"); err != nil {
		t.Fatalf("DeliverToSession: %v", err)
	}
	msgs = s.SnapshotMessages()
	if len(msgs) != 2 || msgs[1].Content != "第二条" {
		t.Fatalf("history = %+v, want the queued message appended once", msgs)
	}
	if got := s.DrainUserMessages(); len(got) != 1 || got[0] != "第二条" {
		t.Fatalf("drained = %q", got)
	}
}
