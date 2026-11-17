package session

import "testing"

func TestDeliverStaleRunningStatus(t *testing.T) {
	mgr, rec := newDeliveryEnv(t)
	s := mustSession(t, mgr, "stale")
	s.Record().Status = "running"

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

func TestQueueUserMessagePersistsImmediately(t *testing.T) {
	mgr, _ := newDeliveryEnv(t)
	s := mustSession(t, mgr, "worker")
	mgr.SetActiveTaskCheck(func(string) bool { return true })

	if err := mgr.DeliverToSession(s.ID(), "排队消息"); err != nil {
		t.Fatalf("DeliverToSession: %v", err)
	}
	msgs := s.SnapshotMessages()
	if len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Content != "排队消息" {
		t.Fatalf("queued message not persisted at queue time: %+v", msgs)
	}
	if got := s.DrainUserMessages(); len(got) != 1 || got[0] != "排队消息" {
		t.Fatalf("drained = %q", got)
	}
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
