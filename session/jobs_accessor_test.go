package session

import (
	"context"
	"testing"
	"time"

	"github.com/user/mini_code/job"
)

func TestSessionJobsAccessor(t *testing.T) {
	mgr := newTestManager(t)
	dir := t.TempDir()
	sess, err := mgr.CreateSessionRecord("jobs", "build", dir, nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}

	if reg := sess.Jobs(); reg != nil {
		t.Fatal("Jobs() before any exec tool must be nil")
	}

	if mgr.Peek(sess.ID()) != sess {
		t.Error("Peek did not return the loaded session")
	}
	if mgr.Peek("does-not-exist") != nil {
		t.Error("Peek invented an unknown session")
	}

	if ex := sess.sharedExec(dir); ex == nil {
		t.Fatal("sharedExec returned nil")
	}
	reg := sess.Jobs()
	if reg == nil {
		t.Fatal("Jobs() is nil after the exec tool was built")
	}

	j := reg.Start(sess.ID(), "exec", "sleep 999", true)
	if got := reg.Running(sess.ID()); len(got) != 1 || got[0] != j.ID {
		t.Fatalf("Running = %v, want [%s]", got, j.ID)
	}

	j.SetCancel(func() job.Status { return job.StatusKilled })
	if !j.Cancel("从界面停止") {
		t.Fatal("Cancel on a running job returned false")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !j.Wait(ctx, 2*time.Second) {
		t.Fatal("job did not settle after Cancel")
	}
	if st := j.Status(); st != job.StatusKilled {
		t.Errorf("status after kill = %s, want killed", st)
	}
	if got := reg.Running(sess.ID()); len(got) != 0 {
		t.Errorf("Running after kill = %v, want empty", got)
	}
}
