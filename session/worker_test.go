package session

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
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
	cfg, pb, _, err := s.preparePromptTools()
	if err != nil {
		t.Fatalf("preparePromptTools: %v", err)
	}
	s.mu.Lock()
	wd := s.record.WorkDir
	s.mu.Unlock()
	_ = cfg
	return pb.BuildSystemPrompt(wd, "")
}

// toolJSON renders the tool schemas of a session (the other half of the request
// prefix).
func toolJSON(t *testing.T, s *Session) string {
	t.Helper()
	cfg, pb, _, err := s.preparePromptTools()
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
	if fi, err := os.Stat(w.WorkDir); err != nil || !fi.IsDir() {
		t.Fatalf("shared workspace missing: %v", err)
	}
	// A new worker starts with an empty, version-0 公约: nothing to broadcast yet.
	if rec, err := svc.Get(w.WorkerID); err != nil || rec == nil || rec.Note != "" || rec.NoteVer != 0 {
		t.Fatalf("a new worker must carry an empty v0 公约: %+v %v", rec, err)
	}
	if _, err := os.Stat(filepath.Join(w.WorkDir, "worker.md")); !os.IsNotExist(err) {
		t.Fatalf("the 公约 must live in the row, not in a file: %v", err)
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
	if id, name, dir := a.WorkerSnapshot(); id != w.WorkerID || name != "gpu-bench" || dir != w.WorkDir {
		t.Fatalf("session A membership = %q %q %q, want %q gpu-bench %q", id, name, dir, w.WorkerID, w.WorkDir)
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
	if id, _, _ := b.WorkerSnapshot(); id != WorkerOtherID {
		t.Fatalf("Leave re-filed worker id %q, want %q", id, WorkerOtherID)
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

// TestWorkerNoteVersionNotice pins the subscription: a session is told the 公约
// only while the version its own conversation carries is behind, and every write —
// human through the UI or agent through the tool — bumps that version.
func TestWorkerNoteVersionNotice(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	s := mustSession(t, mgr, "note-version")
	w, err := svc.Create("docs", "整理文档", s.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Join(s.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join: %v", err)
	}
	// v0/empty: nothing to say.
	if n := s.WorkerNotice(nil); n.GetText() != "" {
		t.Fatalf("an empty 公约 must not produce a notice: %q", n.GetText())
	}
	// The system prompt carries no worker content at all: it is frozen into the
	// history after the first turn, so anything dynamic there would go stale.
	if p := buildPrompt(t, s); strings.Contains(p, "worker_notice") || strings.Contains(p, "<convention>") {
		t.Fatal("the system prompt still carries worker content")
	}

	ver, err := svc.WriteNote(w.WorkerID, "所有产物放到 results/ 目录")
	if err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	if ver != 1 {
		t.Fatalf("the first write produced v%d, want 1", ver)
	}
	msgs := []provider.Message{{Role: "system", Content: "SYS"}, {Role: "user", Content: "干活"}}
	first := s.WorkerNotice(msgs)
	if !IsWorkerNotice(first) {
		t.Fatal("the notice is not recognizable as one")
	}
	if !strings.Contains(first.GetText(), "results/") || !strings.Contains(first.GetText(), noteMarker(w.WorkerID, 1)) {
		t.Fatalf("the notice lacks the 公约 or its marker:\n%s", first.GetText())
	}
	// Carrying v1 → silent.
	held := append(append([]provider.Message(nil), msgs...), first)
	if n := s.WorkerNotice(held); n.GetText() != "" {
		t.Fatalf("a session holding the current version was notified again: %q", n.GetText())
	}
	// A newer write → told once more, with the new text.
	if ver, err = svc.WriteNote(w.WorkerID, "第二条:commit 前先跑 go test"); err != nil || ver != 2 {
		t.Fatalf("WriteNote v2 = %d (%v)", ver, err)
	}
	second := s.WorkerNotice(held)
	if !strings.Contains(second.GetText(), "go test") || !strings.Contains(second.GetText(), noteMarker(w.WorkerID, 2)) {
		t.Fatalf("the v2 notice is wrong:\n%s", second.GetText())
	}
	// A join result is a tool result: it carries the same marker, so joining
	// consumes the version instead of repeating the text one task later.
	api := svc.ToolAPIFor(s.ID())
	joined := provider.Message{Role: "tool", Content: api.Join(w.WorkerID)}
	if !strings.Contains(joined.GetText(), noteMarker(w.WorkerID, 2)) {
		t.Fatalf("the join result lacks the current version marker: %q", joined.GetText())
	}
	if n := s.WorkerNotice(append(append([]provider.Message(nil), msgs...), joined)); n.GetText() != "" {
		t.Fatalf("joining should have consumed v2: %q", n.GetText())
	}
	// note_read reports the current version. Writing is not offered to the model
	// at all: svc.WriteNote is the human path (the sidebar), which still bumps.
	if got := api.NoteRead(); !strings.Contains(got, "v2") || !strings.Contains(got, "go test") {
		t.Fatalf("note_read: %q", got)
	}
	if ver, err := svc.WriteNote(w.WorkerID, "第三条:产物放 dist/"); err != nil || ver != 3 {
		t.Fatalf("WriteNote v3 = %d (%v)", ver, err)
	}
	if n := s.WorkerNotice(held); !strings.Contains(n.GetText(), "dist/") {
		t.Fatalf("the writer's own v3 should still reach the sessions holding v1: %q", n.GetText())
	}
}

// TestWorkerNoteWriteValidation: the 公约 is injected into every session of the
// worker, so it is trimmed on the way in and an oversized write is refused
// instead of silently truncated.
func TestWorkerNoteWriteValidation(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	s := mustSession(t, mgr, "note-validation")
	w, err := svc.Create("api", "后端接口", s.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ver, err := svc.WriteNote(w.WorkerID, "  带空白  \n")
	if err != nil || ver != 1 {
		t.Fatalf("WriteNote = %d (%v)", ver, err)
	}
	note, got, err := svc.ReadNote(w.WorkerID)
	if err != nil || got != 1 || note != "带空白" {
		t.Fatalf("ReadNote = %q v%d (%v)", note, got, err)
	}
	if _, err := svc.WriteNote(w.WorkerID, strings.Repeat("长", MaxNoteChars+1)); err == nil {
		t.Fatal("an oversized 公约 should be refused")
	}
	if _, _, err := svc.ReadNote("w-does-not-exist"); err == nil {
		t.Fatal("ReadNote on a missing worker should error")
	}
}

// TestApplyWorkerNoticeOncePerTask pins how the notice rides the conversation: the
// loop appends it once at the start of a task, an up-to-date session gets nothing,
// and the bytes already in the conversation never move.
func TestApplyWorkerNoticeOncePerTask(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	s := mustSession(t, mgr, "notice-append")
	w, err := svc.Create("api", "后端接口", s.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Join(s.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if _, err := svc.WriteNote(w.WorkerID, "新约定:results/"); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	loop := NewAgentLoop(nil, 4, nil, nil, nil, nil, nil, nil)
	loop.SetWorkerNotice(s.WorkerNotice)

	msgs := []provider.Message{
		{Role: "system", Content: "SYS"},
		{Role: "user", Content: "干活"},
	}
	msgs = loop.applyWorkerNotice(msgs)
	if len(msgs) != 3 || !IsWorkerNotice(msgs[2]) {
		t.Fatalf("notice was not appended to the tail: %+v", msgs)
	}
	// The next task holds this version already: zero new bytes.
	next := append(msgs, provider.Message{Role: "assistant", Content: "ok"})
	if got := loop.applyWorkerNotice(next); len(got) != len(next) {
		t.Fatalf("an up-to-date session got %d extra messages", len(got)-len(next))
	}

	// A newer 公约 appends one message; everything before it stays byte-identical,
	// so the provider's cached prefix still matches.
	before := append([]provider.Message(nil), next...)
	if _, err := svc.WriteNote(w.WorkerID, "新约定:dist/"); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	after := loop.applyWorkerNotice(next)
	if n := countWorkerNotices(after); n != 2 {
		t.Fatalf("a newer 公约 should append one notice, got %d notices", n)
	}
	if len(after) != len(before)+1 {
		t.Fatal("the update rewrote the conversation instead of appending")
	}
	for i := range before {
		if after[i].GetText() != before[i].GetText() {
			t.Fatalf("message %d changed on update (cache broken)", i)
		}
	}

	// A sub-agent has nothing to say.
	child := mustSession(t, mgr, "child")
	child.record.ParentID = "p"
	loop.SetWorkerNotice(child.WorkerNotice)
	if got := loop.applyWorkerNotice(after); len(got) != len(after) {
		t.Fatal("a silent session must not append a notice")
	}

	// A fresh root session is born under 其他 and gets no notice at all: the
	// join reminder is retired, and 其他 carries no shared convention.
	free := mustSession(t, mgr, "free")
	if id, _, _ := free.WorkerSnapshot(); id != WorkerOtherID {
		t.Fatalf("a fresh root session should be born under %q, got %q", WorkerOtherID, id)
	}
	loop.SetWorkerNotice(free.WorkerNotice)
	task1 := loop.applyWorkerNotice([]provider.Message{{Role: "user", Content: "hi"}})
	if countWorkerNotices(task1) != 0 {
		t.Fatalf("a fresh session should start notice-free: %+v", task1)
	}

	// A compaction rebuild drops notices (derived data); the next task re-delivers
	// the convention. The history rebuilt here carries none, so the owed note is
	// re-created — the held version resets with the dropped notices.
	loop.SetWorkerNotice(s.WorkerNotice)
	rebuilt, ok := RebuildContext(append(after, BuildCompactRequest("")), "summary here", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("RebuildContext refused")
	}
	if countWorkerNotices(rebuilt) != 0 {
		t.Fatal("a stale notice survived the compaction rebuild")
	}
	if got := loop.applyWorkerNotice(rebuilt); countWorkerNotices(got) != 1 {
		t.Fatal("the notice was not re-created after a rebuild")
	}
}

func countWorkerNotices(messages []provider.Message) int {
	n := 0
	for _, m := range messages {
		if IsWorkerNotice(m) {
			n++
		}
	}
	return n
}

// TestWorkersToolRegisteredForEveryRootSession pins the other half of the
// prefix: a session must not gain or lose tools when it joins a worker, or the
// join would break its cache too. The workers tool itself is no longer on the
// exposed surface (dsh parity) — the join reminder and 公约 travel as a user
// message, so the tool list must stay identical either way.
func TestWorkersToolRegisteredForEveryRootSession(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	unattached := mustSession(t, mgr, "unattached")
	attached := mustSession(t, mgr, "attached")

	defsBefore := toolJSON(t, unattached)
	if strings.Contains(defsBefore, `"workers"`) {
		t.Fatal("the workers tool must not be on the dsh tool surface")
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

// TestWorkerConventionReadOnlyForModel pins the removal: a session may read its
// worker's 公约 but has no way to write one — no NoteWrite on the dispatcher, no
// note_write in any tool schema, and no leftover approval prompt. The single
// write path is a human editing it in the sidebar (handleWorkersNoteWrite).
func TestWorkerConventionReadOnlyForModel(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	s := mustSession(t, mgr, "gate")
	w, err := svc.Create("api", "后端接口", s.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Join(s.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if _, ok := reflect.TypeOf(svc.ToolAPIFor(s.ID())).MethodByName("NoteWrite"); ok {
		t.Fatal("WorkerToolAPI still exposes a NoteWrite method")
	}
	if defs := toolJSON(t, s); strings.Contains(defs, "note_write") {
		t.Fatalf("a tool schema still advertises note_write:\n%s", defs)
	}
	if p := buildPrompt(t, s); strings.Contains(p, "note_write") {
		t.Fatalf("the system prompt still suggests note_write:\n%s", p)
	}
	// Nothing about the workers tool asks the user anything any more (mode is the
	// default full; the old always-confirm hook is gone with the write path).
	cfg, _, _, err := s.preparePromptTools()
	if err != nil {
		t.Fatalf("preparePromptTools: %v", err)
	}
	ap := &recordingApprover{reason: "user denied"}
	mgr.SetApprover(ap)
	gate := s.gateFor(cfg)
	for _, action := range []string{"list", "note_read", "note_write"} {
		if allow, msg := gate(context.Background(), "workers", map[string]interface{}{"action": action}); !allow {
			t.Fatalf("workers(%s) was denied: %q", action, msg)
		}
	}
	if ap.called != 0 {
		t.Fatalf("workers tool triggered %d approvals, want 0", ap.called)
	}
	// Even if a model asks for the old action, the row is untouched.
	tools := svc.ToolAPIFor(s.ID())
	if !strings.Contains(tools.List(), w.WorkerID) {
		t.Fatalf("list should include the worker: %q", tools.List())
	}
	if rec, _ := svc.Get(w.WorkerID); rec == nil || rec.NoteVer != 0 {
		t.Fatalf("the 公约 version should stay untouched: %+v", rec)
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
	// The plain session (empty worker_id) is re-filed under 其他 when the store
	// reopens: "no worker" is no longer a state a root session may be in, and
	// the unassigned bucket is gone with the join reminder.
	other, err := reopened.ListBucketSummaries(WorkerOtherID, 10, 0)
	if err != nil || len(other) != 1 || other[0].SessionID != plain.SessionID {
		t.Fatalf("其他 bucket should hold the re-filed session, got %+v (%v)", other, err)
	}
	unassigned, err := reopened.ListBucketSummaries(WorkerUnassignedID, 10, 0)
	if err != nil || len(unassigned) != 0 {
		t.Fatalf("unassigned bucket must be empty after the refile, got %+v (%v)", unassigned, err)
	}
}

// TestWorkerMembershipOnRestart checks a reopened session still knows its worker
// (id, name, shared directory) and that its notice behaviour follows the row's
// version — the version lives in the conversation, so a restart neither re-reads
// nor forgets anything.
func TestWorkerMembershipOnRestart(t *testing.T) {
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
	if _, err := svc.WriteNote(w.WorkerID, "第一条约定"); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	// The conversation holds v1 through the join result.
	held := []provider.Message{
		{Role: "user", Content: "干活"},
		{Role: "tool", Content: svc.ToolAPIFor(s.ID()).Join(w.WorkerID)},
	}
	if n := s.WorkerNotice(held); n.GetText() != "" {
		t.Fatalf("the join should have consumed v1: %q", n.GetText())
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
	if id, name, dir := loaded.WorkerSnapshot(); id != w.WorkerID || name != "notes" || dir != w.WorkDir {
		t.Fatalf("reloaded membership = %q %q %q, want %q notes %q", id, name, dir, w.WorkerID, w.WorkDir)
	}
	// Same version → still silent after the restart; a new write → told again.
	if n := loaded.WorkerNotice(held); n.GetText() != "" {
		t.Fatalf("a reloaded session re-sent a consumed 公约: %q", n.GetText())
	}
	if _, err := svc2.WriteNote(w.WorkerID, "第二条约定"); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	if n := loaded.WorkerNotice(held); !strings.Contains(n.GetText(), "第二条约定") {
		t.Fatalf("a reloaded session did not get the update: %q", n.GetText())
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

// TestMigrateLegacyWorkerNoteIntoRow: a database from the worker.md era must not
// silently lose its 公约 — the file's content becomes version 1 on the next open.
func TestMigrateLegacyWorkerNoteIntoRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent.db")
	store, err := NewSessionStore(dbPath)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	defer store.Close()
	dir := t.TempDir()
	notePath := filepath.Join(dir, "worker.md")
	if err := os.WriteFile(notePath, []byte("# 老 worker · 工作公约\n\n产物放 dist/\n"), 0644); err != nil {
		t.Fatalf("write legacy note: %v", err)
	}
	// Reshape the table into its pre-version shape: note_path, no note/note_ver.
	for _, q := range []string{
		`DROP TABLE workers`,
		`CREATE TABLE workers (
			worker_id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			work_dir TEXT NOT NULL DEFAULT '',
			note_path TEXT NOT NULL DEFAULT '',
			builtin INTEGER NOT NULL DEFAULT 0,
			created_at TEXT,
			updated_at TEXT)`,
	} {
		if _, err := store.db.Execute(q); err != nil {
			t.Fatalf("reshape workers table: %v", err)
		}
	}
	if _, err := store.db.Execute(
		`INSERT INTO workers (worker_id, name, summary, work_dir, note_path, builtin, created_at, updated_at)
		 VALUES ('w-old', '老 worker', '旧摘要', ?, ?, 0, '', '')`, dir, notePath); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	store.migrateWorkerNote()

	rec, err := store.GetWorker("w-old")
	if err != nil || rec == nil {
		t.Fatalf("GetWorker: %+v %v", rec, err)
	}
	if !strings.Contains(rec.Note, "产物放 dist/") {
		t.Fatalf("the legacy file was not imported: %q", rec.Note)
	}
	if rec.NoteVer != 1 {
		t.Fatalf("imported 公约 version = %d, want 1", rec.NoteVer)
	}
	// Idempotent: a second pass never bumps it again.
	store.migrateWorkerNote()
	if rec, err := store.GetWorker("w-old"); err != nil || rec == nil || rec.NoteVer != 1 {
		t.Fatalf("re-running the migration changed the version: %+v %v", rec, err)
	}
}

// TestWorkerNoticeOncePerRun is the requirement itself: one task, however many
// turns it runs, is reminded exactly once — appended before the first request, so
// it reaches the model on turn one and never repeats inside the task.
func TestWorkerNoticeOncePerRun(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	s := mustSession(t, mgr, "once-per-run")
	w, err := svc.Create("api", "后端接口", s.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Join(s.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if _, err := svc.WriteNote(w.WorkerID, "约定:跑分统一用 bench.py"); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	var requests int64
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		rw.Header().Set("Content-Type", "text/event-stream")
		if n <= 2 { // two tool turns: one task spans three model requests
			fmt.Fprintf(rw, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_%d\",\"type\":\"function\",\"function\":{\"name\":\"probetool\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n", n)
		} else {
			rw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n"))
		}
		rw.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: "vllm", BaseURL: ts.URL, APIKey: "test",
		ModelName: "test-model", MaxTokens: 100, ContextWindow: 4096,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	defer client.Close()
	reg := tools.NewToolRegistry()
	reg.Register(&probeTool{})
	loop := NewAgentLoop(client, 5, reg, nil, nil, nil, nil, nil)
	loop.compactionOn = false
	loop.SetWorkerNotice(s.WorkerNotice)

	res, err := loop.Run(context.Background(), "SYS", "干活", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := int(atomic.LoadInt64(&requests))
	if calls < 3 {
		t.Fatalf("the fake model answered %d requests; this test needs a multi-turn task", calls)
	}
	if n := countWorkerNotices(res.Messages); n != 1 {
		t.Fatalf("a %d-request task carried %d notices, want exactly 1", calls, n)
	}
	// It rides right behind the user message, before any reply of this task.
	at := -1
	for i, m := range res.Messages {
		if IsWorkerNotice(m) {
			at = i
			break
		}
	}
	if at < 1 || res.Messages[at-1].Role != "user" {
		t.Fatalf("the notice is not the message right after the user turn: %+v", res.Messages)
	}
}

// TestWorkerOtherIsFixed pins the floor and the ceiling of the worker list: a
// fresh install already carries the fixed 其他 worker (so there is always some-
// where to put a stray session), a session can land there WITHOUT giving up its
// own directory, it cannot be deleted, and it holds one of the MaxWorkers slots.
func TestWorkerOtherIsFixed(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	rec, err := svc.Get(WorkerOtherID)
	if err != nil || rec == nil {
		t.Fatalf("the 其他 worker is missing: %+v (%v)", rec, err)
	}
	// A real, joinable worker row — not a built-in bucket like 已归档 — and with
	// no shared directory of its own.
	if rec.Name != WorkerOtherName || rec.Builtin || rec.WorkDir != "" {
		t.Fatalf("the 其他 row is wrong: %+v", rec)
	}
	if !IsReservedWorker(WorkerOtherID) {
		t.Fatal("IsReservedWorker should recognise 其他")
	}
	s := mustSession(t, mgr, "流浪会话")
	own := s.WorkDir()
	joined, err := svc.Join(s.ID(), WorkerOtherID)
	if err != nil {
		t.Fatalf("joining 其他: %v", err)
	}
	if joined.WorkDir != "" {
		t.Fatalf("其他 must not carry a shared dir: %+v", joined)
	}
	if s.WorkDir() != own {
		t.Fatalf("a session in 其他 keeps its own dir, got %q want %q", s.WorkDir(), own)
	}
	// The tool text says what an empty directory means instead of printing "".
	if out := svc.ToolAPIFor(s.ID()).Join(WorkerOtherID); !strings.Contains(out, "不设共享目录") {
		t.Fatalf("join(其他) result: %q", out)
	}
	if out := svc.ToolAPIFor(s.ID()).WorkerInfo(); !strings.Contains(out, own) || strings.Contains(out, "工作目录:\n") {
		t.Fatalf("info(其他) result: %q", out)
	}
	if _, err := svc.Delete(WorkerOtherID); err == nil {
		t.Fatal("the fixed 其他 worker was deletable")
	}
	if _, err := svc.Create(WorkerOtherName, "再来一个", own, ""); err == nil {
		t.Fatal("a second worker named 其他 was created")
	}
	// The cap: 其他 plus MaxWorkers-1 created ones fills the list.
	base := mustSession(t, mgr, "base")
	// The quota is stated before any create fails, in the list header.
	if out := svc.ToolAPIFor(base.ID()).List(); !strings.Contains(out, fmt.Sprintf("worker 名额 1/%d", MaxWorkers)) {
		t.Fatalf("list with room left: %q", out)
	}
	for i := 1; i < MaxWorkers; i++ {
		if _, err := svc.Create(fmt.Sprintf("line-%d", i), "", base.WorkDir(), ""); err != nil {
			t.Fatalf("create #%d: %v", i, err)
		}
	}
	if _, err := svc.Create("one-too-many", "", base.WorkDir(), ""); err == nil {
		t.Fatalf("worker #%d should have been refused", MaxWorkers+1)
	}
	// Full list → the tool text says so instead of offering create as an option.
	if out := svc.ToolAPIFor(base.ID()).List(); !strings.Contains(out, "名额已满") ||
		!strings.Contains(out, fmt.Sprintf("worker 名额 %d/%d", MaxWorkers, MaxWorkers)) {
		t.Fatalf("list at the cap: %q", out)
	}
	if q := quotaLine(MaxWorkers); !strings.Contains(q, "已满") {
		t.Fatalf("quota line when full: %q", q)
	}
	if q := quotaLine(2); !strings.Contains(q, fmt.Sprintf("还剩 %d/%d", MaxWorkers-2, MaxWorkers)) {
		t.Fatalf("quota line with room: %q", q)
	}
}

// TestWorkerRename covers the label-only rename: the row, the labels each session
// carries (they reach the model through the notice and the tool results) and the
// refusals. The shared directory never moves — paths in files, tool results and
// history already point at it.
func TestWorkerRename(t *testing.T) {
	mgr, svc := newTestWorkerEnv(t)
	a := mustSession(t, mgr, "会话A")
	w, err := svc.Create("gpu-bench", "跑 GPU 推理基准", a.WorkDir(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Join(a.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if err := svc.Rename(w.WorkerID, "显卡推理基准"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	rec, err := svc.Get(w.WorkerID)
	if err != nil || rec == nil || rec.Name != "显卡推理基准" {
		t.Fatalf("the row kept its old name: %+v (%v)", rec, err)
	}
	if rec.WorkDir != w.WorkDir {
		t.Fatalf("renaming must not move the workspace: %q -> %q", w.WorkDir, rec.WorkDir)
	}
	if _, name, dir := a.WorkerSnapshot(); name != "显卡推理基准" || dir != w.WorkDir {
		t.Fatalf("the session label was not relabeled: name=%q dir=%q", name, dir)
	}
	// The label reaches the model through the worker notice and the tool results
	// (never the system prompt), so a rename costs no prefix cache — and the
	// model-facing text must show the new name.
	info := svc.ToolAPIFor(a.ID()).WorkerInfo()
	if !strings.Contains(info, "本会话属于 worker「显卡推理基准」") {
		t.Fatalf("the session still reports the old label: %q", info)
	}
	// A session of the same worker that nobody has loaded still gets relabeled.
	other, err := mgr.CreateSessionRecord("会话B", "build", a.WorkDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	if _, err := svc.Join(other.ID(), w.WorkerID); err != nil {
		t.Fatalf("Join(2nd): %v", err)
	}
	if err := svc.Rename(w.WorkerID, "推理基准 v2"); err != nil {
		t.Fatalf("Rename #2: %v", err)
	}
	sess, err := mgr.Get(other.ID())
	if err != nil || sess == nil {
		t.Fatalf("Get: %v", err)
	}
	if _, name, _ := sess.WorkerSnapshot(); name != "推理基准 v2" {
		t.Fatalf("the second session kept the old label: %q", name)
	}
	// Refusals: empty, too long, a name that is taken, the fixed 其他, a
	// built-in bucket, and path separators (the name also names a directory).
	if err := svc.Rename(w.WorkerID, "  "); err == nil {
		t.Fatal("an empty name was accepted")
	}
	if err := svc.Rename(w.WorkerID, strings.Repeat("长", MaxWorkerNameRunes+1)); err == nil {
		t.Fatal("an oversized name was accepted")
	}
	if _, err := svc.Create("别的线", "", a.WorkDir(), ""); err != nil {
		t.Fatalf("Create(2): %v", err)
	}
	if err := svc.Rename(w.WorkerID, "别的线"); err == nil {
		t.Fatal("a duplicate name was accepted")
	}
	if err := svc.Rename(WorkerOtherID, "改名试试"); err == nil {
		t.Fatal("the fixed 其他 worker was renamed")
	}
	if err := svc.Rename(WorkerArchivedID, "改名试试"); err == nil {
		t.Fatal("a built-in bucket was renamed")
	}
	if err := svc.Rename(w.WorkerID, "a/b"); err == nil {
		t.Fatal("a name with a path separator was accepted")
	}
	// Renaming to the same name is a no-op, not an error.
	if err := svc.Rename(w.WorkerID, "推理基准 v2"); err != nil {
		t.Fatalf("renaming to the same name should be silent: %v", err)
	}
}
