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

// noteVer reads the version out of a note-write response.
func noteVer(t *testing.T, body string) int {
	t.Helper()
	var v struct {
		Ver int `json:"ver"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v.Ver
}

func TestWorkersAPIEndToEnd(t *testing.T) {
	be, srv := newTestWorkerBackend(t)
	base := t.TempDir()

	var created struct {
		WorkerID string `json:"worker_id"`
		Name     string `json:"name"`
		WorkDir  string `json:"work_dir"`
		NoteVer  int    `json:"note_ver"`
	}
	code, body := doJSON(t, "POST", srv.URL+"/api/workers", `{"name":"gpu-bench","summary":"显存与吞吐基准","base_dir":"`+jsonEscape(base)+`"}`)
	mustOK(t, "POST /api/workers", code, body)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode create: %v (%s)", err, body)
	}
	if created.WorkerID == "" || filepath.Dir(filepath.Clean(created.WorkDir)) != filepath.Clean(base) {
		t.Fatalf("unexpected create result: %+v", created)
	}
	if created.NoteVer != 0 {
		t.Fatalf("a new worker should start at 公约 v0, got %d", created.NoteVer)
	}

	sess, err := be.mgr.CreateSessionRecord("worker api", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()

	var before []SessionSummary
	workerReq(t, "GET", srv.URL+"/api/sessions", "", &before)
	if len(before) != 1 || before[0].WorkerID != session.WorkerOtherID {
		t.Fatalf("a fresh session should be born under 其他, got %+v", before)
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
			Active   int    `json:"active"`
			Builtin  bool   `json:"builtin"`
			Reserved bool   `json:"reserved"`
		} `json:"workers"`
		Unassigned        int `json:"unassigned"`
		UnassignedActive  int `json:"unassigned_active"`
	}
	workerReq(t, "GET", srv.URL+"/api/workers", "", &list)
	// The list always carries the fixed 其他 bucket plus the one created here.
	var mine, other int
	for _, wk := range list.Workers {
		switch {
		case wk.WorkerID == created.WorkerID:
			mine++
			// Nothing runs in this test yet, so the busy counter must be 0.
			if wk.Sessions != 1 || wk.Active != 0 || wk.Builtin || wk.Reserved {
				t.Fatalf("the created worker is reported wrong: %+v", wk)
			}
		case wk.WorkerID == "w-other":
			other++
			if wk.Sessions != 0 || wk.Builtin || !wk.Reserved {
				t.Fatalf("the fixed 其他 bucket is reported wrong: %+v", wk)
			}
		}
	}
	if mine != 1 || other != 1 || len(list.Workers) != 2 || list.Unassigned != 0 {
		t.Fatalf("worker list wrong: %+v", list)
	}

	// The 公约 through the API (the human path: no approval involved).
	var note struct {
		Content string `json:"content"`
		Ver     int    `json:"ver"`
	}
	workerReq(t, "GET", srv.URL+"/api/workers/"+created.WorkerID+"/note", "", &note)
	if note.Ver != 0 || strings.TrimSpace(note.Content) != "" {
		t.Fatalf("a fresh worker should have an empty v0 公约: %+v", note)
	}
	code, body = doJSON(t, "POST", srv.URL+"/api/workers/"+created.WorkerID+"/note", `{"content":"统一用 bench.py 跑,结果写 results/"}`)
	mustOK(t, "POST note", code, body)
	if v := noteVer(t, body); v != 1 {
		t.Fatalf("the first UI write should produce v1, got %d (%s)", v, body)
	}
	workerReq(t, "GET", srv.URL+"/api/workers/"+created.WorkerID+"/note", "", &note)
	if note.Ver != 1 || !strings.Contains(note.Content, "results/") {
		t.Fatalf("note write did not persist: %+v", note)
	}

	// A session already in this worker is told the new text on its next task —
	// no refresh call, no restart.
	live, err := be.mgr.Get(id)
	if err != nil || live == nil {
		t.Fatalf("get session: %+v %v", live, err)
	}
	if n := live.WorkerNotice(nil); !strings.Contains(n.GetText(), "results/") || !strings.Contains(n.GetText(), "v1") {
		t.Fatalf("the session was not told about the edited 公约: %q", n.GetText())
	}

	// Summary edit (what other sessions read when they decide to join).
	code, body = doJSON(t, "POST", srv.URL+"/api/workers/"+created.WorkerID+"/summary", `{"summary":"换成带宽测试"}`)
	mustOK(t, "POST summary", code, body)

	// Rename: only the label moves — the workspace stays put and each session's
	// own worker-name label follows (it is rendered into their prompts). The
	// fixed 其他 has a written-in-stone name.
	code, body = doJSON(t, "POST", srv.URL+"/api/workers/"+created.WorkerID+"/rename", `{"name":"带宽基准"}`)
	mustOK(t, "POST rename", code, body)
	if !strings.Contains(body, "带宽基准") {
		t.Fatalf("rename echoed the wrong name: %s", body)
	}
	if _, name, dir := live.WorkerSnapshot(); name != "带宽基准" || dir != filepath.Clean(created.WorkDir) {
		t.Fatalf("the session kept the old label: name=%q dir=%q", name, dir)
	}
	if fi, err := os.Stat(created.WorkDir); err != nil || !fi.IsDir() {
		t.Fatalf("renaming moved the workspace: %v", err)
	}
	code, body = doJSON(t, "POST", srv.URL+"/api/workers/w-other/rename", `{"name":"改名试试"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("renaming 其他 should be refused, got %d (%s)", code, body)
	}

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

	// Restoring means "back to the fixed 其他 bucket" — there is no unassigned
	// state anymore.
	code, body = doJSON(t, "POST", srv.URL+"/api/sessions/"+id+"/archive", `{"archived":false}`)
	mustOK(t, "POST un-archive", code, body)
	workerReq(t, "GET", srv.URL+"/api/sessions", "", &visible)
	if len(visible) != 1 || visible[0].WorkerID != session.WorkerOtherID {
		t.Fatalf("restore did not return the session to 其他: %+v", visible)
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
	for _, p := range []string{"/api/sessions/s-gone/worker", "/api/sessions/s-gone/archive"} {
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

// The two notice markers, mirrored here because they are unexported in the
// session package. These assertions are about what reaches the *model*.
const (
	noticeJoinMarker = "__MC_WORKER_JOIN__"
	noticeNoteMarker = "__MC_WORKER_NOTE__"
)

// TestWorkerNoticeReachesModel is the end-to-end check of where worker reminders
// land: as one user message at the tail of a task's conversation, once per task,
// never as a system section (the system prompt is frozen for a session's whole
// lifetime, so a message is the only place a mid-life join or an edited 公约 can
// reach the model) — and, unlike the old brief, visible in the UI transcript.
func TestWorkerNoticeReachesModel(t *testing.T) {
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

	// 1) A fresh session is born under the fixed 其他 bucket and receives no
	// notice at all — the old "pick or create a worker" reminder is retired.
	code, body := doJSON(t, "POST", srv.URL+"/api/messages", `{"content":"随便问一句"}`)
	mustOK(t, "POST /api/messages", code, body)
	if req := awaitBody("first task", "随便问一句"); strings.Contains(req, noticeJoinMarker) || strings.Contains(req, noticeNoteMarker) {
		t.Error("a fresh session should start with zero worker notices")
	}

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

	// 2) Joining through the sidebar: attached, but the worker has no 公约 yet, so
	// the next task still says nothing — and the retired join reminder stays gone.
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
	req := awaitBody("task after join", "加入之后问一句")
	if n := strings.Count(req, noticeJoinMarker); n != 0 {
		t.Errorf("the retired join reminder appeared %d times; it must never be injected", n)
	}
	if strings.Contains(req, noticeNoteMarker) {
		t.Error("a worker with an empty v0 公约 should not send a notice")
	}

	// 3) A human edits the 公约 in the sidebar. The next task is told the new text,
	// appended after the user's own message; nothing already in the history moves.
	note, _ := json.Marshal(map[string]string{"content": "新约定:跑分统一用 bench.py"})
	code, body = doJSON(t, "POST", srv.URL+"/api/workers/"+created.WorkerID+"/note", string(note))
	mustOK(t, "POST note", code, body)
	code, body = doJSON(t, "POST", srv.URL+"/api/messages", `{"session_id":"`+id+`","content":"新约定生效了吗"}`)
	mustOK(t, "POST /api/messages after the note edit", code, body)
	req = awaitBody("公约 notice", "跑分统一用 bench.py")
	if !strings.Contains(req, noticeNoteMarker+created.WorkerID+":1") {
		t.Errorf("the notice does not mark the version it carries: %s", noticeNoteMarker+created.WorkerID+":1")
	}
	if n := strings.Count(req, noticeJoinMarker); n != 0 {
		t.Errorf("join reminder count = %d, want 0 (retired)", n)
	}
	if strings.LastIndex(req, noticeNoteMarker) < strings.LastIndex(req, "新约定生效了吗") {
		t.Error("the 公约 notice was not appended after the user message")
	}

	// 4) The UI transcript shows them — they are context the user is entitled to
	// see — while the system prompt stays worker-free and byte-stable.
	awaitNotices(t, mgr, id, 1)
	var view struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tokens int `json:"tokens"`
	}
	workerReq(t, "GET", srv.URL+"/api/sessions/"+id, "", &view)
	var inTranscript int
	for _, m := range view.Messages {
		if strings.Contains(m.Content, noticeJoinMarker) || strings.Contains(m.Content, noticeNoteMarker) {
			inTranscript++
		}
	}
	if inTranscript != 1 {
		t.Fatalf("the transcript shows %d notices, want just the 公约 update", inTranscript)
	}
	if view.Tokens <= 0 {
		t.Error("the token estimate went missing with the notices")
	}

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
	sys := parsed.Messages[0].Content
	if strings.Contains(sys, noticeJoinMarker) || strings.Contains(sys, noticeNoteMarker) || strings.Contains(sys, "工作公约") {
		t.Error("the system prompt still carries worker content; it must stay byte-stable")
	}

	// 5) A second edit is a second notice; editing nothing changes nothing.
	note2, _ := json.Marshal(map[string]string{"content": "新约定二:结果写 results/"})
	code, body = doJSON(t, "POST", srv.URL+"/api/workers/"+created.WorkerID+"/note", string(note2))
	mustOK(t, "POST note 2", code, body)
	if v := noteVer(t, body); v != 2 {
		t.Fatalf("second edit produced v%d, want v2", v)
	}
	code, body = doJSON(t, "POST", srv.URL+"/api/messages", `{"session_id":"`+id+`","content":"第二条也生效了吗"}`)
	mustOK(t, "POST /api/messages after the second edit", code, body)
	req = awaitBody("v2 notice", "结果写 results/")
	if !strings.Contains(req, noticeNoteMarker+created.WorkerID+":2") {
		t.Error("the second notice does not carry v2")
	}
	// The record keeps notices: the model must see them on every later turn.
	awaitNotices(t, mgr, id, 2)
}

// awaitNotices polls until the session's record carries want notices — one is
// persisted only after its model request completes.
func awaitNotices(t *testing.T, mgr *session.SessionManager, id string, want int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		s, err := mgr.Get(id)
		if err != nil || s == nil {
			t.Fatalf("session %s: %+v %v", id, s, err)
		}
		if n := countNotices(s.SnapshotMessages()); n == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("session %s never carried exactly %d notices", id, want)
}

func countNotices(messages []provider.Message) int {
	n := 0
	for _, m := range messages {
		if session.IsWorkerNotice(m) {
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

// TestWorkersListBusyCounts pins what the sidebar switcher's status dots read:
// every bucket carries how many of its sessions run right now, and a session
// counts as busy even when only one of its sub-agents still runs — the same rule
// the session rows use, so a worker's dot and the dots of its rows can't disagree.
func TestWorkersListBusyCounts(t *testing.T) {
	be, srv := newTestWorkerBackend(t)
	base := t.TempDir()

	code, body := doJSON(t, "POST", srv.URL+"/api/workers", `{"name":"忙的那个","summary":""}`)
	mustOK(t, "create worker", code, body)
	var created struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	inWorker, err := be.mgr.CreateSessionRecord("in worker", "build", base, nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	free, err := be.mgr.CreateSessionRecord("free", "build", base, nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	pid := free.ID()
	kid, err := be.mgr.CreateSessionRecord("kid", "build", base, &pid)
	if err != nil {
		t.Fatalf("child session: %v", err)
	}
	if code, body = doJSON(t, "POST", srv.URL+"/api/sessions/"+inWorker.ID()+"/worker",
		`{"worker_id":"`+created.WorkerID+`"}`); code != http.StatusOK {
		t.Fatalf("move into worker: %d %s", code, body)
	}

	// busy reads the switcher payload: per-worker busy count + 未分配's.
	busy := func() (map[string]int, int) {
		t.Helper()
		var v struct {
			Workers []struct {
				WorkerID string `json:"worker_id"`
				Active   int    `json:"active"`
			} `json:"workers"`
			UnassignedActive int `json:"unassigned_active"`
		}
		workerReq(t, "GET", srv.URL+"/api/workers", "", &v)
		out := make(map[string]int, len(v.Workers))
		for _, wk := range v.Workers {
			out[wk.WorkerID] = wk.Active
		}
		return out, v.UnassignedActive
	}

	if got, freeN := busy(); got[created.WorkerID] != 0 || freeN != 0 {
		t.Fatalf("idle buckets reported busy: %+v / 未分配 %d", got, freeN)
	}

	// A task on the worker's own session → that bucket reads 1, 未分配 stays 0.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	be.hub.RegisterExternalTask(inWorker.ID(), cancel)
	if got, freeN := busy(); got[created.WorkerID] != 1 || freeN != 0 {
		t.Fatalf("busy worker bucket: %+v / 未分配 %d", got, freeN)
	}
	be.hub.UnregisterExternalTask(inWorker.ID())

	// A task on a sub-agent only: the sub-agent is not a root session, so the
	// number has to come from its parent's bucket — exactly like the session row.
	// The parent is a plain session, which now lives in the fixed 其他 bucket.
	_, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	be.hub.RegisterExternalTask(kid.ID(), cancel2)
	if got, freeN := busy(); got[session.WorkerOtherID] != 1 || got[created.WorkerID] != 0 || freeN != 0 {
		t.Fatalf("sub-agent task not counted for its parent's 其他 bucket: %+v / 未分配 %d", got, freeN)
	}
}

// TestWorkersDeleteRemovesSessionsIncludingARunningOne is the API half of "the
// sidebar did not refresh after I deleted the worker": the delete must return on
// its own, and every session of the worker (its sub-agents included) must be gone
// from every bucket right after — otherwise the client is left staring at rows
// that no longer exist. A session with a live task is the interesting case.
func TestWorkersDeleteRemovesSessionsIncludingARunningOne(t *testing.T) {
	be, srv := newTestWorkerBackend(t)
	base := t.TempDir()

	code, body := doJSON(t, "POST", srv.URL+"/api/workers", `{"name":"要删的区","summary":""}`)
	mustOK(t, "create worker", code, body)
	var created struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	mk := func(title string, parent *string) string {
		t.Helper()
		s, err := be.mgr.CreateSessionRecord(title, "build", base, parent)
		if err != nil {
			t.Fatalf("CreateSessionRecord(%s): %v", title, err)
		}
		return s.ID()
	}
	s1 := mk("跑着的会话", nil)
	s2 := mk("闲着的会话", nil)
	pid := s1
	kid := mk("子 agent", &pid)
	for _, sid := range []string{s1, s2} {
		if code, body = doJSON(t, "POST", srv.URL+"/api/sessions/"+sid+"/worker",
			`{"worker_id":"`+created.WorkerID+`"}`); code != http.StatusOK {
			t.Fatalf("move %s: %d %s", sid, code, body)
		}
	}
	// The worker's first session has a live task, as when you delete the worker
	// you are currently watching work.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	be.hub.RegisterExternalTask(s1, cancel)

	// A hung delete shows up as a client timeout: the UI would otherwise sit
	// there with the old rows on screen and no explanation.
	cl := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/workers/"+created.WorkerID, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("delete worker: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete worker: status %d, body %s", resp.StatusCode, raw)
	}
	var gone struct {
		DeletedSessions int `json:"deleted_sessions"`
	}
	if err := json.Unmarshal(raw, &gone); err != nil {
		t.Fatalf("decode delete reply %q: %v", raw, err)
	}
	if gone.DeletedSessions != 2 {
		t.Fatalf("deleted_sessions = %d, want the 2 root sessions of the worker", gone.DeletedSessions)
	}

	for _, q := range []string{"", "?worker=", "?worker=" + created.WorkerID} {
		var left []SessionSummary
		workerReq(t, "GET", srv.URL+"/api/sessions"+q, "", &left)
		for _, s := range left {
			if s.SessionID == s1 || s.SessionID == s2 || s.SessionID == kid {
				t.Fatalf("session %s survived the worker delete (list %q): %+v", s.SessionID, q, left)
			}
		}
	}
}
