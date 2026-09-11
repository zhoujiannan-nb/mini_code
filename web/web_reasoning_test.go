package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSetReasoningAPI verifies the per-session thinking-depth endpoint end to
// end: POST /api/sessions/{id}/reasoning stores the override, the session
// detail reflects it, invalid values are rejected with 400, clearing resets
// it, and an unknown session returns 404.
func TestSetReasoningAPI(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("reasoning-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()

	// no override by default (omitempty → key absent)
	var detail map[string]interface{}
	if err := getJSON(api.URL+"/api/sessions/"+id, &detail); err != nil {
		t.Fatalf("GET session detail: %v", err)
	}
	sessObj, _ := detail["session"].(map[string]interface{})
	if sessObj["reasoning_effort"] != nil || sessObj["thinking"] != nil {
		t.Fatalf("expected no override by default, got %+v", sessObj)
	}

	// set an override
	var resp map[string]string
	if err := postJSON(api.URL+"/api/sessions/"+id+"/reasoning",
		map[string]string{"thinking": "enabled", "reasoning_effort": "high"}, &resp); err != nil {
		t.Fatalf("POST reasoning: %v", err)
	}
	if resp["reasoning_effort"] != "high" || resp["thinking"] != "enabled" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	// the session detail reflects it
	if err := getJSON(api.URL+"/api/sessions/"+id, &detail); err != nil {
		t.Fatalf("GET session detail after set: %v", err)
	}
	sessObj, _ = detail["session"].(map[string]interface{})
	if sessObj["reasoning_effort"] != "high" || sessObj["thinking"] != "enabled" {
		t.Fatalf("detail did not reflect override: %+v", sessObj)
	}

	// invalid effort → 400
	r, err := http.Post(api.URL+"/api/sessions/"+id+"/reasoning", "application/json",
		strings.NewReader(`{"reasoning_effort":"bogus"}`))
	if err != nil {
		t.Fatalf("POST invalid effort: %v", err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid effort, got %d", r.StatusCode)
	}

	// invalid thinking → 400
	r, err = http.Post(api.URL+"/api/sessions/"+id+"/reasoning", "application/json",
		strings.NewReader(`{"thinking":"maybe"}`))
	if err != nil {
		t.Fatalf("POST invalid thinking: %v", err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid thinking, got %d", r.StatusCode)
	}

	// clearing the override drops the metadata keys again
	var clearResp map[string]string
	if err := postJSON(api.URL+"/api/sessions/"+id+"/reasoning",
		map[string]string{"thinking": "", "reasoning_effort": ""}, &clearResp); err != nil {
		t.Fatalf("POST clear reasoning: %v", err)
	}
	if err := getJSON(api.URL+"/api/sessions/"+id, &detail); err != nil {
		t.Fatalf("GET session detail after clear: %v", err)
	}
	sessObj, _ = detail["session"].(map[string]interface{})
	if sessObj["reasoning_effort"] != nil || sessObj["thinking"] != nil {
		t.Fatalf("override not cleared: %+v", sessObj)
	}

	// unknown session → 404
	r, err = http.Post(api.URL+"/api/sessions/nope/reasoning", "application/json",
		strings.NewReader(`{"reasoning_effort":"low"}`))
	if err != nil {
		t.Fatalf("POST unknown session: %v", err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", r.StatusCode)
	}
}
