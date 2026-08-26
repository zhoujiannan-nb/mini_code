package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/user/mini_code/approval"
	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
)

// newTestBackend builds a fakeBackend with a mock model server (answers any
// chat request with a trivial SSE reply) plus a running hub.
func newTestBackend(t *testing.T) *fakeBackend {
	t.Helper()
	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(modelSrv.Close)

	client, err := provider.NewModelClient(config.ModelConfig{
		Provider:  "vllm",
		BaseURL:   modelSrv.URL,
		APIKey:    "test",
		ModelName: "test-model",
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	mgr, err := session.NewSessionManager(client, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })

	hub := channels.NewHub(mgr, "build", t.TempDir(), 5)
	hub.Attach(mgr)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)

	ap := approval.NewService(hub, mgr)
	return &fakeBackend{hub: hub, mgr: mgr, ap: ap}
}

// TestSetModeAPI verifies the session permission-mode endpoint end to end:
// POST /api/sessions/{id}/mode switches the mode, GET /api/sessions reflects
// it, and an invalid mode is rejected.
func TestSetModeAPI(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("mode-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()

	// default mode is full
	var list []SessionSummary
	if err := getJSON(api.URL+"/api/sessions", &list); err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	if len(list) != 1 || list[0].Mode != "full" {
		t.Fatalf("expected one session in full mode, got %+v", list)
	}

	// switch to cautious
	var modeResp map[string]string
	if err := postJSON(api.URL+"/api/sessions/"+id+"/mode", map[string]string{"mode": "cautious"}, &modeResp); err != nil {
		t.Fatalf("POST mode: %v", err)
	}
	if modeResp["mode"] != "cautious" {
		t.Fatalf("expected cautious, got %q", modeResp["mode"])
	}
	if err := getJSON(api.URL+"/api/sessions", &list); err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	if list[0].Mode != "cautious" {
		t.Fatalf("expected list mode cautious, got %q", list[0].Mode)
	}

	// unknown mode falls back to full (NormalizeMode)
	if err := postJSON(api.URL+"/api/sessions/"+id+"/mode", map[string]string{"mode": "bogus"}, &modeResp); err != nil {
		t.Fatalf("POST bogus mode: %v", err)
	}
	if modeResp["mode"] != "full" {
		t.Fatalf("expected bogus to normalize to full, got %q", modeResp["mode"])
	}

	// unknown session → 404
	resp, err := http.Post(api.URL+"/api/sessions/nope/mode", "application/json",
		strings.NewReader(`{"mode":"cautious"}`))
	if err != nil {
		t.Fatalf("POST mode unknown session: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// TestApprovalsAPI verifies the approval request lifecycle through the HTTP
// API: a pending request appears in GET /api/approvals, a decision resolves
// the blocked RequestApproval call, and a double decision returns 409.
func TestApprovalsAPI(t *testing.T) {
	be := newTestBackend(t)
	be.ap.SetTimeout(30 * time.Second)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("approval-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()

	decisionCh := make(chan session.ApprovalDecision, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		d := be.ap.RequestApproval(ctx, session.ApprovalRequest{
			SessionID:  id,
			SessionTitle: "approval-test",
			ToolName:   "delete_file",
			ArgsJSON:   `{"path":"C:/temp/x.txt"}`,
		})
		decisionCh <- d
	}()

	// wait until the request is pending
	var pend []approval.PendingInfo
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := getJSON(api.URL+"/api/approvals", &pend); err != nil {
			t.Fatalf("GET /api/approvals: %v", err)
		}
		if len(pend) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("approval never became pending (got %d)", len(pend))
		}
		time.Sleep(50 * time.Millisecond)
	}
	reqID := pend[0].ID
	if pend[0].ToolName != "delete_file" || pend[0].SessionID != id {
		t.Fatalf("unexpected pending payload: %+v", pend[0])
	}

	// approve via the API
	var resolved map[string]interface{}
	if err := postJSON(api.URL+"/api/approvals/"+reqID+"/decision",
		map[string]string{"decision": "approve"}, &resolved); err != nil {
		t.Fatalf("POST decision: %v", err)
	}
	if resolved["approved"] != true {
		t.Fatalf("expected approved=true, got %+v", resolved)
	}
	select {
	case d := <-decisionCh:
		if !d.Approved {
			t.Fatalf("expected approved decision, got %+v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RequestApproval did not return after decision")
	}

	// the request is gone now
	if err := getJSON(api.URL+"/api/approvals", &pend); err != nil {
		t.Fatalf("GET /api/approvals after resolve: %v", err)
	}
	if len(pend) != 0 {
		t.Fatalf("expected no pending after resolve, got %d", len(pend))
	}

	// a second decision on the same id → 409
	resp, err := http.Post(api.URL+"/api/approvals/"+reqID+"/decision", "application/json",
		strings.NewReader(`{"decision":"deny"}`))
	if err != nil {
		t.Fatalf("POST second decision: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on double decision, got %d", resp.StatusCode)
	}
}

// TestChildrenAPI verifies GET /api/sessions/{id}/children: a root session
// starts empty, a sub-agent session (parent_id set) shows up, and the root
// list endpoint keeps hiding it.
func TestChildrenAPI(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	root, err := be.mgr.CreateSessionRecord("parent", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	parentID := root.ID()
	child, err := be.mgr.CreateSessionRecord("child task", "work", t.TempDir(), &parentID)
	if err != nil {
		t.Fatalf("CreateSessionRecord child: %v", err)
	}
	_ = child

	var kids []SessionSummary
	if err := getJSON(api.URL+"/api/sessions/"+parentID+"/children", &kids); err != nil {
		t.Fatalf("GET children: %v", err)
	}
	if len(kids) != 1 || kids[0].SessionID != child.ID() || kids[0].ParentID != parentID {
		t.Fatalf("unexpected children: %+v", kids)
	}

	// the root list must not include the child
	var roots []SessionSummary
	if err := getJSON(api.URL+"/api/sessions", &roots); err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	if len(roots) != 1 || roots[0].SessionID != parentID {
		t.Fatalf("root list should contain only the parent: %+v", roots)
	}

	// unknown parent → empty list, not an error
	if err := getJSON(api.URL+"/api/sessions/unknown/children", &kids); err != nil {
		t.Fatalf("GET children unknown: %v", err)
	}
	if len(kids) != 0 {
		t.Fatalf("expected no children for unknown parent, got %d", len(kids))
	}
}

func getJSON(url string, out interface{}) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: %d %s", url, resp.StatusCode, string(b))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func postJSON(url string, body, out interface{}) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := http.Post(url, "application/json", strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s: %d %s", url, resp.StatusCode, string(rb))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
