package web

import "testing"

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

	if code := workerReq(t, "GET", srv.URL+"/api/sessions/"+id+"/jobs/output?job=exec-00000000", "", nil); code != 404 {
		t.Errorf("output without a registry: code %d, want 404", code)
	}

	var cleared struct {
		Cleared int `json:"cleared"`
	}
	if code := workerReq(t, "POST", srv.URL+"/api/sessions/"+id+"/jobs/clear", "", &cleared); code != 200 {
		t.Errorf("clear without a registry: code %d, want 200", code)
	}
	if cleared.Cleared != 0 {
		t.Errorf("cleared = %d, want 0", cleared.Cleared)
	}

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
