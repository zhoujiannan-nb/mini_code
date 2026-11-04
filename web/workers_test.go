package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/user/mini_code/approval"
	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
)

// newTestWorkerBackend wires the worker service over the standard fake backend
// (the shape main.go builds at startup) and serves the whole API.
func newTestWorkerBackend(t *testing.T) (*fakeBackend, *httptest.Server) {
	t.Helper()
	be := newTestBackend(t)
	be.mgr.SetWorkers(session.NewWorkerService(be.mgr.GetStore(), be.mgr))
	srv := httptest.NewServer(NewServer(be))
	t.Cleanup(srv.Close)
	return be, srv
}

// workerReq is getJSON/postJSON with the two things they cannot do: a non-GET
// method and a non-200 status the test wants to assert on.
func workerReq(t *testing.T, method, url, body string, out interface{}) int {
	t.Helper()
	code, raw := doJSON(t, method, url, body)
	if out != nil {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, url, raw, err)
		}
	}
	return code
}

func mustOK(t *testing.T, label string, code int, body string) {
	t.Helper()
	if code < 200 || code > 299 {
		t.Fatalf("%s: status %d, body %s", label, code, body)
	}
}

func TestWorkersAPIEndToEnd(t *testing.T) {
	be, srv := newTestWorkerBackend(t)
	base := t.TempDir()

	var created struct {
		WorkerID string `json:"worker_id"`
		Name     string `json:"name"`
		WorkDir  string `json:"work_dir"`
		NotePath string `json:"note_path"`
	}
	code, body := doJSON(t, "POST", srv.URL+"/api/workers", `{"name":"gpu-bench","summary":"显存与吞吐基准","base_dir":"`+jsonEscape(base)+`"}`)
	mustOK(t, "POST /api/workers", code, body)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode create: %v (%s)", err, body)
	}
	if created.WorkerID == "" || filepath.Dir(filepath.Clean(created.WorkDir)) != filepath.Clean(base) {
		t.Fatalf("unexpected create result: %+v", created)
	}
	if b, err := os.ReadFile(created.NotePath); err != nil || !strings.Contains(string(b), "gpu-bench") {
		t.Fatalf("convention file was not seeded: %q %v", string(b), err)
	}

	sess, err := be.mgr.CreateSessionRecord("worker api", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()

	var before []SessionSummary
	workerReq(t, "GET", srv.URL+"/api/sessions", "", &before)
	if len(before) != 1 || before[0].WorkerID != "" {
		t.Fatalf("a fresh session should be unassigned, got %+v", before)
	}

	// Move it in: the bucket list must follow, and the session takes the
	// worker's shared directory.
	code, body = doJSON(t, "POST", srv.URL+"/api/sessions/"+id+"/worker", `{"worker_id":"`+created.WorkerID+`"}`)
	mustOK(t, "move into worker", code, body)

	var inBucket []SessionSummary
	workerReq(t, "GET", srv.URL+"/api/sessions?worker="+created.WorkerID, "", &inBucket)
	if len(inBucket) != 1 || inBucket[0].WorkDir != filepath.Clean(created.WorkDir) {
		t.Fatalf("session did not move into the workspace: %+v", inBucket)
	}
	var unassigned []SessionSummary
	workerReq(t, "GET", srv.URL+"/api/sessions?worker=", "", &unassigned)
	if len(unassigned) != 0 {
		t.Fatalf("the session is still listed as unassigned: %+v", unassigned)
	}

	var list struct {
		Workers []struct {
			WorkerID string `json:"worker_id"`
			Sessions int    `json:"sessions"`
			Builtin  bool   `json:"builtin"`
		} `json:"workers"`
		Unassigned int `json:"unassigned"`
	}
	workerReq(t, "GET", srv.URL+"/api/workers", "", &list)
	if len(list.Workers) != 1 || list.Workers[0].Sessions != 1 || list.Unassigned != 0 {
		t.Fatalf("worker list wrong: %+v", list)
	}

	// Convention file through the API (the human path: no approval involved).
	var note struct {
		Content string `json:"content"`
		Path    string `json:"path"`
	}
	workerReq(t, "GET", srv.URL+"/api/workers/"+created.WorkerID+"/note", "", &note)
	if filepath.Clean(note.Path) != filepath.Clean(created.NotePath) || !strings.Contains(note.Content, "公约") {
		t.Fatalf("unexpected note payload: %+v", note)
	}
	code, body = doJSON(t, "POST", srv.URL+"/api/workers/"+created.WorkerID+"/note", `{"content":"统一用 bench.py 跑,结果写 results/"}`)
	mustOK(t, "POST note", code, body)
	workerReq(t, "GET", srv.URL+"/api/workers/"+created.WorkerID+"/note", "", &note)
	if !strings.Contains(note.Content, "results/") {
		t.Fatalf("note write did not persist: %q", note.Content)
	}

	// A live session only picks the new text up when it explicitly asks.
	var refreshed struct {
		Changed bool `json:"changed"`
	}
	code, body = doJSON(t, "POST", srv.URL+"/api/sessions/"+id+"/worker/refresh", "")
	mustOK(t, "POST worker/refresh", code, body)
	if err := json.Unmarshal([]byte(body), &refreshed); err != nil {
		t.Fatalf("decode refresh: %v (%s)", err, body)
	}
	if !refreshed.Changed {
		t.Fatal("refresh reported no change after a note write")
	}

	// Summary edit (what other sessions read when they decide to join).
	code, body = doJSON(t, "POST", srv.URL+"/api/workers/"+created.WorkerID+"/summary", `{"summary":"换成带宽测试"}`)
	mustOK(t, "POST summary", code, body)

	// Deleting the worker takes its sessions with it and keeps the workspace.
	var del struct {
		DeletedSessions int `json:"deleted_sessions"`
	}
	code, body = doJSON(t, "DELETE", srv.URL+"/api/workers/"+created.WorkerID, "")
	mustOK(t, "DELETE worker", code, body)
	if err := json.Unmarshal([]byte(body), &del); err != nil {
		t.Fatalf("decode delete: %v (%s)", err, body)
	}
	if del.DeletedSessions != 1 {
		t.Fatalf("deleted %d sessions, want 1", del.DeletedSessions)
	}
	if rec, err := be.mgr.GetStore().Get(id); err != nil || rec != nil {
		t.Fatalf("the session survived the worker delete: %+v %v", rec, err)
	}
	if fi, err := os.Stat(created.WorkDir); err != nil || !fi.IsDir() {
		t.Fatalf("the workspace directory must survive the delete: %v", err)
	}
}

// TestArchiveAPIIsTheWorkerBucket pins the replacement of the old archive flag:
// the endpoint still answers, but it now means "move into the built-in 已归档
// bucket", which the default list keeps out of sight.
func TestArchiveAPIIsTheWorkerBucket(t *testing.T) {
	be, srv := newTestWorkerBackend(t)
	sess, err := be.mgr.CreateSessionRecord("legacy", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()

	code, body := doJSON(t, "POST", srv.URL+"/api/sessions/"+id+"/archive", `{"archived":true}`)
	mustOK(t, "POST archive", code, body)

	var visible []SessionSummary
	workerReq(t, "GET", srv.URL+"/api/sessions", "", &visible)
	if len(visible) != 0 {
		t.Fatalf("an archived session must stay out of the default list: %+v", visible)
	}
	var bucket []SessionSummary
	workerReq(t, "GET", srv.URL+"/api/sessions?archived=1", "", &bucket)
	if len(bucket) != 1 || bucket[0].WorkerID != session.WorkerArchivedID {
		t.Fatalf("archive did not map to the built-in bucket: %+v", bucket)
	}

	// Restoring means "back to 未分配".
	code, body = doJSON(t, "POST", srv.URL+"/api/sessions/"+id+"/archive", `{"archived":false}`)
	mustOK(t, "POST un-archive", code, body)
	workerReq(t, "GET", srv.URL+"/api/sessions", "", &visible)
	if len(visible) != 1 || visible[0].WorkerID != "" {
		t.Fatalf("restore did not return the session to 未分配: %+v", visible)
	}
}

// TestWorkersAPIRejectsUnknownTargets keeps a stale sidebar (a worker deleted in
// another window) from silently filing a session nowhere. The session does not
// have to exist for the worker lookup to answer, so the unknown worker wins.
func TestWorkersAPIRejectsUnknownTargets(t *testing.T) {
	be, srv := newTestWorkerBackend(t)
	sess, err := be.mgr.CreateSessionRecord("s", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	if code, body := doJSON(t, "POST", srv.URL+"/api/sessions/"+sess.ID()+"/worker", `{"worker_id":"w-nope"}`); code != http.StatusNotFound {
		t.Fatalf("status = %d (%s), want 404", code, body)
	}
	// And a worker without a workspace cannot host a convention file edit.
	if code, body := doJSON(t, "POST", srv.URL+"/api/workers/nope/note", `{"content":"x"}`); code == http.StatusOK {
		t.Fatalf("note write on an unknown worker succeeded: %s", body)
	}
	// A session that no longer exists (deleted in another window) must answer 404
	// on every membership route — it used to nil-deref and drop the connection.
	for _, p := range []string{"/api/sessions/s-gone/worker", "/api/sessions/s-gone/worker/refresh", "/api/sessions/s-gone/archive"} {
		body := `{"worker_id":"w-nope"}`
		if p != "/api/sessions/s-gone/worker" {
			body = ""
		}
		if code, msg := doJSON(t, "POST", srv.URL+p, body); code != http.StatusNotFound {
			t.Errorf("%s: status = %d (%s), want 404", p, code, msg)
		}
	}
}

// TestNewSessionCarriesWorker checks the sidebar's switcher really puts a brand
// new session into the worker: the first message creates the session already
// attached, so its very first request carries the worker block (no mid-life
// prefix break).
func TestNewSessionCarriesWorker(t *testing.T) {
	_, srv := newTestWorkerBackend(t)
	base := t.TempDir()
	var created struct {
		WorkerID string `json:"worker_id"`
		WorkDir  string `json:"work_dir"`
	}
	code, body := doJSON(t, "POST", srv.URL+"/api/workers", `{"name":"web","summary":"前端","base_dir":"`+jsonEscape(base)+`"}`)
	mustOK(t, "POST /api/workers", code, body)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}

	code, body = doJSON(t, "POST", srv.URL+"/api/messages", `{"content":"开工","worker_id":"`+created.WorkerID+`"}`)
	mustOK(t, "POST /api/messages", code, body)

	// The hub creates the session asynchronously; poll the bucket list for it.
	var got []SessionSummary
	for i := 0; i < 100; i++ {
		workerReq(t, "GET", srv.URL+"/api/sessions?worker="+created.WorkerID, "", &got)
		if len(got) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("no session showed up in the worker bucket: %+v", got)
	}
	if got[0].WorkDir != filepath.Clean(created.WorkDir) {
		t.Fatalf("new session was created outside the workspace: %q want %q", got[0].WorkDir, created.WorkDir)
	}
}

// briefMarker mirrors session.workerBriefMarkerPrefix (unexported there). The
// point of these assertions is that the marker reaches the *model*, not just the
// session's own memory.
const briefMarker = "__MC_WORKER_BRIEF__"

// TestWorkerBriefReachesModel is the end-to-end check of where the worker block
// actually lands: as a refreshable user message at the tail of the conversation,
// never as a system section. The system prompt is frozen for a session's whole
// lifetime (AgentLoop.Run only prepends it when history has none), so a message
// is the only place a mid-life join or a refreshed convention can reach the model.
func TestWorkerBriefReachesModel(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer modelSrv.Close()

	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: "vllm", BaseURL: modelSrv.URL, APIKey: "test", ModelName: "test-model", MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	mgr, err := session.NewSessionManager(client, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	defer mgr.Close()
	mgr.SetWorkers(session.NewWorkerService(mgr.GetStore(), mgr))

	hub := channels.NewHub(mgr, "build", t.TempDir(), 5)
	hub.Attach(mgr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	srv := httptest.NewServer(NewServer(&fakeBackend{hub: hub, mgr: mgr, ap: approval.NewService(hub, mgr)}))
	defer srv.Close()

	// awaitBody waits for a captured model request containing want. Each POST
	// /api/messages answers with a plain "stop" so it produces exactly one request.
	awaitBody := func(what, want string) string {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			mu.Lock()
			for _, b := range bodies {
				if strings.Contains(b, want) {
					mu.Unlock()
					return b
				}
			}
			mu.Unlock()
			if time.Now().After(deadline) {
				t.Fatalf("%s: no model request contained %q", what, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// 1) A session with no worker still gets the "pick or create one" brief.
	code, body := doJSON(t, "POST", srv.URL+"/api/messages", `{"content":"随便问一句"}`)
	mustOK(t, "POST /api/messages", code, body)
	awaitBody("unassigned brief", briefMarker+"none:")

	var list []SessionSummary
	for i := 0; i < 200; i++ {
		workerReq(t, "GET", srv.URL+"/api/sessions", "", &list)
		if len(list) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(list) != 1 {
		t.Fatalf("expected the hub to create one session, got %+v", list)
	}
	id := list[0].SessionID

	// 2) Joining mid-life: the very next request must carry that worker's brief.
	base := t.TempDir()
	code, body = doJSON(t, "POST", srv.URL+"/api/workers", `{"name":"gpu","summary":"统一跑分脚本","base_dir":"`+jsonEscape(base)+`"}`)
	mustOK(t, "POST /api/workers", code, body)
	var created struct {
		WorkerID string `json:"worker_id"`
		WorkDir  string `json:"work_dir"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode create: %v (%s)", err, body)
	}
	code, body = doJSON(t, "POST", srv.URL+"/api/sessions/"+id+"/worker", `{"worker_id":"`+created.WorkerID+`"}`)
	mustOK(t, "POST join", code, body)

	code, body = doJSON(t, "POST", srv.URL+"/api/messages", `{"session_id":"`+id+`","content":"加入之后问一句"}`)
	mustOK(t, "POST /api/messages after join", code, body)
	req := awaitBody("joined brief", briefMarker+created.WorkerID+":")
	// The workspace and the seeded convention have to come along with it, or the
	// agent gets a label without the shared context that made the worker worth
	// joining in the first place.
	if want := jsonEscape(created.WorkDir); !strings.Contains(req, want) {
		t.Errorf("brief did not name the shared workspace %q", want)
	}
	if !strings.Contains(req, "统一跑分脚本") {
		t.Error("brief did not carry the convention file")
	}

	// 3) A refreshed convention appends a second brief; the first one stays. A
	// rewritten history would be a cache miss (and a lie about what was said), so
	// the only legal move is to append.
	note, _ := json.Marshal(map[string]string{"content": "# gpu\n\n新约定:跑分统一用 bench.py"})
	code, body = doJSON(t, "POST", srv.URL+"/api/workers/"+created.WorkerID+"/note", string(note))
	mustOK(t, "POST note", code, body)
	code, body = doJSON(t, "POST", srv.URL+"/api/sessions/"+id+"/worker/refresh", "")
	mustOK(t, "POST refresh", code, body)

	code, body = doJSON(t, "POST", srv.URL+"/api/messages", `{"session_id":"`+id+`","content":"新约定生效了吗"}`)
	mustOK(t, "POST /api/messages after refresh", code, body)
	req = awaitBody("refreshed brief", "跑分统一用 bench.py")

	if n := strings.Count(req, briefMarker); n < 3 {
		t.Errorf("model request carries %d briefs, want the guidance + join + refresh ones", n)
	}
	// And the newest brief sits after the user's own text: appended at the tail.
	if strings.LastIndex(req, briefMarker) < strings.LastIndex(req, "新约定生效了吗") {
		t.Error("the refreshed brief was not appended after the user message")
	}

	// The transcript the UI renders must not carry them: they are injected
	// context, and a thousand-character "user" bubble per turn would bury the
	// conversation the user actually had.
	var view struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tokens int `json:"tokens"`
	}
	workerReq(t, "GET", srv.URL+"/api/sessions/"+id, "", &view)
	for _, m := range view.Messages {
		if strings.Contains(m.Content, briefMarker) {
			t.Fatal("the transcript handed a worker brief to the UI as a chat message")
		}
	}
	// The record keeps them: the model has to see them on every later turn.
	rec, err := mgr.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if n := countBriefs(rec.SnapshotMessages()); n < 3 {
		t.Errorf("the record carries %d briefs, want the three that were injected", n)
	}
	if view.Tokens <= 0 {
		t.Error("the token estimate went missing with the briefs")
	}

	// The system prompt must stay worker-free: nothing in it may mention a worker.
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(req), &parsed); err != nil {
		t.Fatalf("decode model request: %v", err)
	}
	if len(parsed.Messages) == 0 || parsed.Messages[0].Role != "system" {
		t.Fatalf("first message should be the system prompt, got %q", parsed.Messages[0].Role)
	}
	if strings.Contains(parsed.Messages[0].Content, briefMarker) || strings.Contains(parsed.Messages[0].Content, "工作公约") {
		t.Error("the system prompt still carries worker content; it must stay byte-stable")
	}
}

func countBriefs(messages []provider.Message) int {
	n := 0
	for _, m := range messages {
		if session.IsWorkerBrief(m) {
			n++
		}
	}
	return n
}

func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b[1 : len(b)-1])
}
