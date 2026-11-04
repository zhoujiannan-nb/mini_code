package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
)

// newTestWorkerEnv builds a session manager with the worker service wired over
// one temp DB (the shape main.go sets up at startup).
func newTestWorkerEnv(t *testing.T) (*SessionManager, *WorkerService) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "agent.db")
	mgr, err := NewSessionManager(nil, dbPath)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	svc := NewWorkerService(mgr.GetStore(), mgr)
	mgr.SetWorkers(svc)
	t.Cleanup(func() { mgr.Close() })
	return mgr, svc
}

// buildPrompt renders a session's system prompt through the same path a real
// task uses (preparePromptTools + BuildSystemPrompt), so the prefix-cache
// assertions below are about the bytes the provider would actually see.
func buildPrompt(t *testing.T, s *Session) string {
	t.Helper()
	cfg, pb, _, extra, err := s.preparePromptTools()
	if err != nil {
		t.Fatalf("preparePromptTools: %v", err)
	}
	s.mu.Lock()
	wd := s.record.WorkDir
	s.mu.Unlock()
	_ = cfg
	return pb.BuildSystemPrompt(wd, extra)
}

// toolJSON renders the tool schemas of a session (the other half of the request
// prefix).
func toolJSON(t *testing.T, s *Session) string {
	t.Helper()
	cfg, pb, _, _, err := s.preparePromptTools()
	if err != nil {
		t.Fatalf("preparePromptTools: %v", err)
	}
	b, err := json.Marshal(pb.GetFilteredDefinitions(cfg))
	if err != nil {
		t.Fatalf("marshal definitions: %v", err)
	}
	return string(b)
}

func TestWorkerCreateJoinAndCascadeDelete(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	a := mustSession(t, mgr, "会话A")
	b := mustSession(t, mgr, "会话B")

	w, err := svc.Create("gpu-bench", "跑 GPU 推理基准", a.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if filepath.Dir(w.NotePath) != w.WorkDir || filepath.Base(w.NotePath) != WorkerNoteFileName {
		t.Fatalf("note path %q is not %s inside %s", w.NotePath, WorkerNoteFileName, w.WorkDir)
	}
	if fi, err := os.Stat(w.WorkDir); err != nil || !fi.IsDir() {
		t.Fatalf("shared workspace missing: %v", err)
	}
	note, err := os.ReadFile(w.NotePath)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if !strings.Contains(string(note), "gpu-bench") {
		t.Fatalf("convention template does not mention the worker name: %q", string(note))
	}

	// A duplicate name must not create a second workspace.
	if _, err := svc.Create("gpu-bench", "", a.WorkDir(), ""); err == nil {
		t.Fatal("Create with a duplicate name should fail")
	}

	if _, err := svc.Join(a.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join a: %v", err)
	}
	if _, err := svc.Join(b.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join b: %v", err)
	}
	// Same worker = same working directory, for both sessions.
	if a.WorkDir() != w.WorkDir || b.WorkDir() != w.WorkDir {
		t.Fatalf("sessions did not share the workspace: %q %q want %q", a.WorkDir(), b.WorkDir(), w.WorkDir)
	}
	if _, _, _, noteA := a.WorkerSnapshot(); !strings.Contains(noteA, "gpu-bench") {
		t.Fatalf("session A has no convention snapshot: %q", noteA)
	}

	// Summary update is visible to other sessions through the row.
	if err := svc.UpdateSummary(w.WorkerID, "换主题:显存带宽"); err != nil {
		t.Fatalf("UpdateSummary: %v", err)
	}
	if rec, err := svc.Get(w.WorkerID); err != nil || rec.Summary != "换主题:显存带宽" {
		t.Fatalf("summary not persisted: %+v %v", rec, err)
	}

	// Leaving keeps the directory (files stay where they are).
	if err := svc.Leave(b.ID()); err != nil {
		t.Fatalf("Leave: %v", err)
	}
	if b.WorkDir() != w.WorkDir {
		t.Fatalf("Leave changed the working directory to %q", b.WorkDir())
	}
	if id, _, _, _ := b.WorkerSnapshot(); id != WorkerUnassignedID {
		t.Fatalf("Leave left worker id %q", id)
	}

	// Deleting the worker deletes its sessions and keeps the files.
	n, err := svc.Delete(w.WorkerID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n != 1 { // A is still in it; B left
		t.Fatalf("Delete removed %d sessions, want 1", n)
	}
	// The store is the authority here: SessionManager.Get reports a missing row
	// as (nil, nil), not as an error.
	if rec, err := mgr.GetStore().Get(a.ID()); err != nil || rec != nil {
		t.Fatalf("session A survived the worker delete: %+v %v", rec, err)
	}
	if rec, err := svc.Get(w.WorkerID); err != nil || rec != nil {
		t.Fatalf("worker survived: %+v %v", rec, err)
	}
	if _, err := os.Stat(w.WorkDir); err != nil {
		t.Fatalf("workspace was removed (it must be kept): %v", err)
	}
	// The session that left is untouched.
	if rec, err := mgr.GetStore().Get(b.ID()); err != nil || rec == nil {
		t.Fatalf("the detached session vanished: %+v %v", rec, err)
	}
}

// TestWorkerBriefStableAcrossNoteEdit is the prefix-cache guard for the worker
// brief: the bytes a session sends must not move when a human or a sibling agent
// edits the shared 公约, because that would recompute everything after the change
// and the user pays for it. Only an explicit refresh adds a newer brief.
func TestWorkerBriefStableAcrossNoteEdit(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	s := mustSession(t, mgr, "brief-stability")
	w, err := svc.Create("docs", "整理文档", s.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Join(s.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join: %v", err)
	}

	first := s.WorkerBrief()
	text := first.GetText()
	if !strings.Contains(text, w.WorkDir) || !strings.Contains(text, "docs") {
		t.Fatalf("brief does not carry the worker block:\n%s", text)
	}
	if !IsWorkerBrief(first) {
		t.Fatal("the brief is not recognizable as one")
	}
	if got := textOf(s.WorkerBrief()); got != text {
		t.Fatal("brief changed between two turns of the same session")
	}
	// The system prompt carries no worker content at all (it is frozen into the
	// history after the first turn, so anything dynamic there would go stale).
	if p := buildPrompt(t, s); strings.Contains(p, "worker-brief") || strings.Contains(p, w.WorkDir+"\"") {
		t.Fatal("the system prompt still carries worker content")
	}

	// A human edits the shared convention file on disk.
	if err := os.WriteFile(w.NotePath, []byte("新约定:所有产物放到 results/ 目录\n"), 0644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	if got := textOf(s.WorkerBrief()); got != text {
		t.Fatal("editing the 公约 changed a running session's brief (prefix cache broken)")
	}
	// The tool still shows the fresh text: reading and injecting are separate.
	api := svc.ToolAPIFor(s.ID())
	if !strings.Contains(api.NoteRead(), "results/") {
		t.Fatalf("note_read did not see the new file: %q", api.NoteRead())
	}

	// Only an explicit refresh produces a newer brief.
	changed, err := svc.RefreshNoteSnapshot(s.ID())
	if err != nil {
		t.Fatalf("RefreshNoteSnapshot: %v", err)
	}
	if !changed {
		t.Fatal("RefreshNoteSnapshot reported no change")
	}
	refreshed := s.WorkerBrief()
	if !strings.Contains(refreshed.GetText(), "results/") {
		t.Fatalf("refreshed brief lacks the new convention:\n%s", refreshed.GetText())
	}
	if workerBriefSignatureOf(refreshed) == workerBriefSignatureOf(first) {
		t.Fatal("the refresh produced the same signature")
	}
	if got := textOf(s.WorkerBrief()); got != refreshed.GetText() {
		t.Fatal("brief kept moving after the refresh")
	}
}

// TestApplyWorkerBriefAppendOnly pins how the brief rides the conversation: it is
// appended once, costs nothing while unchanged, and a refresh appends a newer one
// instead of rewriting history (newest brief wins).
func TestApplyWorkerBriefAppendOnly(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	s := mustSession(t, mgr, "brief-append")
	w, err := svc.Create("api", "后端接口", s.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Join(s.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join: %v", err)
	}
	loop := NewAgentLoop(nil, 4, nil, nil, nil, nil, nil, nil)
	loop.SetWorkerBrief(s.WorkerBrief)

	msgs := []provider.Message{
		{Role: "system", Content: "SYS"},
		{Role: "user", Content: "干活"},
	}
	msgs = loop.applyWorkerBrief(msgs)
	if len(msgs) != 3 || !IsWorkerBrief(msgs[2]) {
		t.Fatalf("brief was not appended to the tail: %+v", msgs)
	}
	// Unchanged worker = zero new bytes, however many turns pass.
	for i := 0; i < 3; i++ {
		msgs = append(msgs, provider.Message{Role: "assistant", Content: "ok"})
		msgs = loop.applyWorkerBrief(msgs)
	}
	if n := countWorkerBriefs(msgs); n != 1 {
		t.Fatalf("unchanged brief got appended %d times", n)
	}

	// A note_refresh appends a NEWER brief; the old bytes stay untouched, so the
	// provider's cached prefix still matches.
	if err := svc.WriteNote(w.WorkerID, "新约定:results/"); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	before := append([]provider.Message(nil), msgs...)
	if _, err := svc.RefreshNoteSnapshot(s.ID()); err != nil {
		t.Fatalf("RefreshNoteSnapshot: %v", err)
	}
	after := loop.applyWorkerBrief(msgs)
	if n := countWorkerBriefs(after); n != 2 {
		t.Fatalf("refresh should append a second brief, got %d", n)
	}
	if len(after) != len(before)+1 {
		t.Fatal("the refresh rewrote the conversation instead of appending")
	}
	for i := range before {
		if after[i].GetText() != before[i].GetText() {
			t.Fatalf("message %d changed on refresh (cache broken)", i)
		}
	}
	if _, sig := lastWorkerBrief(after); sig != workerBriefSignatureOf(s.WorkerBrief()) {
		t.Fatal("the newest brief is not the current one")
	}

	// A session with nothing to say (sub-agent of an unattached session) appends
	// nothing at all.
	child := mustSession(t, mgr, "child")
	child.record.ParentID = "p"
	loop.SetWorkerBrief(child.WorkerBrief)
	if got := loop.applyWorkerBrief(after); len(got) != len(after) {
		t.Fatal("a silent session must not append a brief")
	}
	// An unattached root session gets the guidance brief, exactly once.
	free := mustSession(t, mgr, "free")
	loop.SetWorkerBrief(free.WorkerBrief)
	got := loop.applyWorkerBrief([]provider.Message{{Role: "user", Content: "hi"}})
	if len(got) != 2 || !strings.Contains(got[1].GetText(), "workers(action=\"list\")") {
		t.Fatalf("no guidance brief for an unattached session: %+v", got)
	}
	if got2 := loop.applyWorkerBrief(got); len(got2) != len(got) {
		t.Fatal("the guidance brief got appended twice")
	}
	// Compaction rebuild drops briefs (derived data); the next turn re-appends one.
	rebuilt, ok := RebuildContext(append(got, BuildCompactRequest("")), "summary here", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("RebuildContext refused")
	}
	if countWorkerBriefs(rebuilt) != 0 {
		t.Fatal("a stale brief survived the compaction rebuild")
	}
	if got3 := loop.applyWorkerBrief(rebuilt); countWorkerBriefs(got3) != 1 {
		t.Fatal("the brief was not re-created after a rebuild")
	}
}

// textOf reads a message body (GetText has a pointer receiver).
func textOf(m provider.Message) string { return m.GetText() }

func countWorkerBriefs(messages []provider.Message) int {
	n := 0
	for _, m := range messages {
		if IsWorkerBrief(m) {
			n++
		}
	}
	return n
}

// TestWorkersToolRegisteredForEveryRootSession pins the other half of the
// prefix: a session must not gain or lose tools when it joins a worker, or the
// join would break its cache too.
func TestWorkersToolRegisteredForEveryRootSession(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	unattached := mustSession(t, mgr, "unattached")
	attached := mustSession(t, mgr, "attached")

	defsBefore := toolJSON(t, unattached)
	if !strings.Contains(defsBefore, `"workers"`) {
		t.Fatal("the workers tool is missing for an unattached session")
	}
	w, err := svc.Create("web", "前端工作", attached.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Join(attached.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if after := toolJSON(t, attached); after != defsBefore {
		t.Fatal("joining a worker changed the tool schema (prefix cache broken)")
	}
	// A sub-agent gets neither the block nor the tool.
	parentID := attached.ID()
	child, err := mgr.CreateSessionRecord("child", "work", attached.WorkDir(), &parentID)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	if strings.Contains(toolJSON(t, child), `"workers"`) {
		t.Fatal("a sub-agent session was given the workers tool")
	}
}

// TestWorkerNoteWriteAlwaysAsks pins the one always-confirmed agent action: the
// convention file is a contract with every other session of the worker, so even
// full mode (no confirmation for anything else) has to ask.
func TestWorkerNoteWriteAlwaysAsks(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	s := mustSession(t, mgr, "gate")
	w, err := svc.Create("api", "后端接口", s.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Join(s.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join: %v", err)
	}
	cfg, _, _, _, err := s.preparePromptTools()
	if err != nil {
		t.Fatalf("preparePromptTools: %v", err)
	}
	// Mode stays the default (full): every other tool call runs unchecked.
	if mgr.ModeOf(s.ID()) == ModeCautious {
		t.Fatal("this test must run in full mode")
	}
	ap := &recordingApprover{reason: "user denied"}
	mgr.SetApprover(ap)
	gate := s.gateFor(cfg)

	allow, msg := gate(context.Background(), "workers", map[string]interface{}{"action": "note_write", "content": "新约定"})
	if allow {
		t.Fatal("a convention write ran without confirmation")
	}
	if !strings.Contains(msg, "公约") {
		t.Fatalf("deny message should name the convention file: %q", msg)
	}
	if ap.called != 1 {
		t.Fatalf("approver called %d times, want 1", ap.called)
	}
	// Everything else about workers is read/low-risk: no prompt for the user.
	if allow, msg := gate(context.Background(), "workers", map[string]interface{}{"action": "list"}); !allow {
		t.Fatalf("workers(list) was denied: %q", msg)
	}
	if ap.called != 1 {
		t.Fatalf("workers(list) triggered an approval (%d calls)", ap.called)
	}
	// Approved → it runs.
	ap.approved = true
	if allow, msg := gate(context.Background(), "workers", map[string]interface{}{"action": "note_write", "content": "新约定"}); !allow {
		t.Fatalf("approved write was denied: %q", msg)
	}
}

// TestWorkerBucketsAndArchiveMigration covers the switcher's data: the unassigned
// and per-worker buckets, and that the legacy metadata.archived flag becomes the
// built-in 已归档 bucket on the next open — without resurrecting those sessions
// into the default list.
func TestWorkerBucketsAndArchiveMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent.db")
	store, err := NewSessionStore(dbPath)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	legacy := NewSessionRecord("old archived", "build", "", "")
	if err := store.Create(legacy); err != nil {
		t.Fatalf("Create legacy: %v", err)
	}
	if _, err := store.db.Execute(`UPDATE sessions SET metadata = '{"archived":true}' WHERE session_id = ?`, legacy.SessionID); err != nil {
		t.Fatalf("flag archived: %v", err)
	}
	plain := NewSessionRecord("plain", "build", "", "")
	if err := store.Create(plain); err != nil {
		t.Fatalf("Create plain: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening runs the migration.
	reopened, err := NewSessionStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	bucket, err := reopened.GetWorker(WorkerArchivedID)
	if err != nil || bucket == nil {
		t.Fatalf("built-in archived bucket was not created: %+v %v", bucket, err)
	}
	if !bucket.Builtin {
		t.Fatal("the archived bucket must be marked builtin")
	}
	rec, err := reopened.Get(legacy.SessionID)
	if err != nil || rec == nil || rec.WorkerID != WorkerArchivedID {
		t.Fatalf("legacy archived session not moved into the bucket: %+v %v", rec, err)
	}

	inBucket, err := reopened.ListBucketSummaries(WorkerArchivedID, 10, 0)
	if err != nil || len(inBucket) != 1 {
		t.Fatalf("archived bucket query returned %+v (%v)", inBucket, err)
	}
	all, err := reopened.ListSessionSummaries(10, 0)
	if err != nil || len(all) != 1 || all[0].SessionID != plain.SessionID {
		t.Fatalf("default list must show only the visible session, got %+v (%v)", all, err)
	}
	unassigned, err := reopened.ListBucketSummaries(WorkerUnassignedID, 10, 0)
	if err != nil || len(unassigned) != 1 {
		t.Fatalf("unassigned bucket returned %+v (%v)", unassigned, err)
	}
}

// TestWorkerSnapshotOnRestart checks the snapshot survives a reopen: a session
// reloaded from the database must render exactly the prompt it rendered before,
// and must not silently re-read the (possibly edited) convention file.
func TestWorkerSnapshotOnRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent.db")
	mgr, err := NewSessionManager(nil, dbPath)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	svc := NewWorkerService(mgr.GetStore(), mgr)
	mgr.SetWorkers(svc)

	s := mustSession(t, mgr, "restart")
	w, err := svc.Create("notes", "笔记", s.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Join(s.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join: %v", err)
	}
	before := buildPrompt(t, s)
	if err := os.WriteFile(w.NotePath, []byte("磁盘上已经改掉了\n"), 0644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close manager: %v", err)
	}

	mgr2, err := NewSessionManager(nil, dbPath)
	if err != nil {
		t.Fatalf("reopen manager: %v", err)
	}
	defer mgr2.Close()
	svc2 := NewWorkerService(mgr2.GetStore(), mgr2)
	mgr2.SetWorkers(svc2)

	loaded, err := mgr2.Get(s.ID())
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if after := buildPrompt(t, loaded); after != before {
		t.Fatal("a reloaded session re-read the convention file (prefix cache broken)")
	}
	if _, _, _, note := loaded.WorkerSnapshot(); strings.Contains(note, "磁盘上已经改掉了") {
		t.Fatal("snapshot was refreshed by the restart")
	}
}

// TestStaleSessionIDErrorsNotPanics covers the cross-window case: a session was
// deleted in another window but the sidebar still holds its id. Get returns
// (nil, nil) for an unknown id, so every membership entry point has to turn that
// into an error — a nil deref would kill the whole HTTP connection (found by the
// live smoke test on :7600).
func TestStaleSessionIDErrorsNotPanics(t *testing.T) {
	_, svc := newTestWorkerEnv(t)
	w, err := svc.Create("stale", "残留 id 回归", t.TempDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const gone = "s-does-not-exist"
	if _, err := svc.Join(gone, w.WorkerID); err == nil {
		t.Error("Join 对不存在的会话应返回错误")
	}
	if err := svc.Leave(gone); err == nil {
		t.Error("Leave 对不存在的会话应返回错误")
	}
	if err := svc.Move(gone, w.WorkerID); err == nil {
		t.Error("Move 对不存在的会话应返回错误")
	}
	if err := svc.Move(gone, WorkerArchivedID); err == nil {
		t.Error("Move(内置分桶) 对不存在的会话应返回错误")
	}
	if _, err := svc.RefreshNoteSnapshot(gone); err == nil {
		t.Error("RefreshNoteSnapshot 对不存在的会话应返回错误")
	}
}

// TestWorkerNameValidation: the name reaches the sidebar, the model-facing
// worker list and every joining session's system prompt, and the agent can mint
// names through the workers tool — so separators and control characters must be
// refused outright, not sanitized into the row.
func TestWorkerNameValidation(t *testing.T) {
	_, svc := newTestWorkerEnv(t)
	base := t.TempDir()
	for _, bad := range []string{"a/b", "..\\evil", "带\n换行", "尾随\t制表", ".", "..", "con:名"} {
		if _, err := svc.Create(bad, "", base, ""); err == nil {
			t.Errorf("Create(%q) 应被拒绝", bad)
		}
	}
	for _, good := range []string{"gpu-bench", "文档整理", "v1.2 复盘", "a b"} {
		if _, err := svc.Create(good, "", base, ""); err != nil {
			t.Errorf("Create(%q) 不该被拒绝: %v", good, err)
		}
	}
}
