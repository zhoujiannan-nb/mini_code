package web

import "testing"

// TestJobsAPIEndpoints pins the UI's jobs endpoints on a session that is loaded
// but has no background work yet: the roster reads empty (never an error — the
// popover polls on open), the session list carries the jobs_running/jobs_total
// fields, and killing an unknown job is a 404 rather than a silent success.
// The live-job paths (running row, kill settling) go through the same registry
// the job_kill model tool uses, which tools/shell_jobs_test.go already covers.
func TestJobsAPIEndpoints(t *testing.T) {
	be, srv := newTestWorkerBackend(t)
	sess, err := be.mgr.CreateSessionRecord("jobs", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	id := sess.ID()

	var roster struct {
		Jobs    []map[string]interface{} `json:"jobs"`
		Running int                      `json:"running"`
	}
	if code := workerReq(t, "GET", srv.URL+"/api/sessions/"+id+"/jobs", "", &roster); code != 200 {
		t.Fatalf("GET jobs: code %d, want 200", code)
	}
	if roster.Running != 0 || len(roster.Jobs) != 0 {
		t.Errorf("empty roster = %+v, want {[] 0}", roster)
	}

	if code := workerReq(t, "POST", srv.URL+"/api/sessions/"+id+"/jobs/kill",
		`{"job_id":"exec-00000000"}`, nil); code != 404 {
		t.Errorf("kill without a registry: code %d, want 404", code)
	}

	// Clearing is idempotent: no registry is "nothing to clear", a 200 with
	// cleared=0, never a 404 — the panel's Clear button must converge even if
	// the session was torn down between the render and the click.
	var cleared struct {
		Cleared int `json:"cleared"`
	}
	if code := workerReq(t, "POST", srv.URL+"/api/sessions/"+id+"/jobs/clear", "", &cleared); code != 200 {
		t.Errorf("clear without a registry: code %d, want 200", code)
	}
	if cleared.Cleared != 0 {
		t.Errorf("cleared = %d, want 0", cleared.Cleared)
	}

	// The list summary carries the new fields (omitted while zero, decoded as 0).
	var list []SessionSummary
	if code := workerReq(t, "GET", srv.URL+"/api/sessions", "", &list); code != 200 {
		t.Fatalf("GET sessions: code %d, want 200", code)
	}
	var found bool
	for _, s := range list {
		if s.SessionID == id {
			found = true
			if s.JobsRunning != 0 || s.JobsTotal != 0 {
				t.Errorf("summary jobs = %d/%d, want 0/0", s.JobsRunning, s.JobsTotal)
			}
		}
	}
	if !found {
		t.Fatal("session missing from /api/sessions")
	}
}
