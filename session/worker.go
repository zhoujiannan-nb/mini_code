package session

// Worker (工作大类) service.
//
// A worker is one line of work: a name, a short summary other sessions read
// when they decide where they belong, one shared workspace directory and one
// shared convention file (worker.md). Root sessions attach to at most one
// worker; attaching moves the session into the shared directory and snapshots
// the convention file into the session record.
//
// # Prefix-cache contract (read before changing anything here)
//
// Nothing worker-related goes into the system prompt. The system message is
// rendered once and then frozen into the stored history (loop.Run only builds a
// new one when the stored history has none), so dynamic content put there would
// either never reach a live session or force a full prefix recompute.
//
// Instead the worker's identity, shared workspace and convention text travel as
// the **worker brief**: a plain user message with a stable marker, appended to
// the tail of the conversation (see WorkerBrief / AgentLoop.applyWorkerBrief).
// That shape is chosen for cache reasons:
//
//   - appending to the tail is an exact extension of the previous request, so
//     the provider's KV prefix cache keeps hitting;
//   - the brief is rendered ONLY from the snapshot stored in the session record
//     — the live worker row and the convention file on disk are never read while
//     building a prompt. Editing the file, renaming the worker or moving a
//     sibling session therefore cannot disturb a running session;
//   - a refresh (note_refresh / the sidebar button) does not rewrite anything: it
//     appends a NEWER brief and the newest one wins. Nothing already cached is
//     rewritten;
//   - the built-in buckets carry no directory and no convention, and a session
//     without a snapshot gets no brief at all, so the archived→worker migration in
//     worker_store.go changes nothing the model sees.

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
	"github.com/user/mini_code/util"
)

const (
	// WorkerNoteFileName is the shared convention file inside a worker's
	// directory. ASCII on purpose: agents reach for it through cmd.exe too,
	// where a CJK filename comes out garbled.
	WorkerNoteFileName = "worker.md"

	// MaxNoteChars caps the convention file. It is injected into every task of
	// every session of that worker, so a long file is paid for over and over.
	MaxNoteChars = 6000
	// MaxWorkerNameRunes caps the worker name (it also names the directory).
	MaxWorkerNameRunes = 40
	// MaxWorkerSummaryRunes caps the summary other sessions read.
	MaxWorkerSummaryRunes = 400
)

// WorkerService owns the worker rows, their directories and the membership of
// sessions. One instance per process, wired into the SessionManager.
type WorkerService struct {
	store *SessionStore
	mgr   *SessionManager
	mu    sync.Mutex // serializes create/delete (directory + row must not race)
}

func NewWorkerService(store *SessionStore, mgr *SessionManager) *WorkerService {
	return &WorkerService{store: store, mgr: mgr}
}

// --- reads ---

// List returns every bucket including the built-in ones (the UI switcher needs
// them; the model-facing listing uses Joinable).
func (w *WorkerService) List() ([]WorkerRecord, error) { return w.store.ListWorkers() }

// Get returns one worker (nil, nil when it does not exist).
func (w *WorkerService) Get(workerID string) (*WorkerRecord, error) { return w.store.GetWorker(workerID) }

// Joinable returns the workers a session may join: everything except the
// built-in buckets (已归档 is a shelf, not a line of work).
func (w *WorkerService) Joinable() ([]WorkerRecord, error) {
	all, err := w.store.ListWorkers()
	if err != nil {
		return nil, err
	}
	out := make([]WorkerRecord, 0, len(all))
	for _, rec := range all {
		if rec.Builtin {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// --- create / update / delete ---

// Create builds a new worker: a shared directory under baseDir (the calling
// session's own directory in the normal flow), a fresh convention file, and the
// row. copyFrom optionally seeds the directory from an existing folder or file.
func (w *WorkerService) Create(name, summary, baseDir, copyFrom string) (*WorkerRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("worker 名称不能为空")
	}
	if len([]rune(name)) > MaxWorkerNameRunes {
		return nil, fmt.Errorf("worker 名称过长(最多 %d 字)", MaxWorkerNameRunes)
	}
	if err := validateWorkerName(name); err != nil {
		return nil, err
	}
	summary = strings.TrimSpace(summary)
	if len([]rune(summary)) > MaxWorkerSummaryRunes {
		return nil, fmt.Errorf("worker 摘要过长(最多 %d 字)", MaxWorkerSummaryRunes)
	}
	if existing, err := w.store.GetWorkerByName(name); err == nil && existing != nil {
		return nil, fmt.Errorf("已经有一个同名 worker:%s(id %s)。要加入它请用 join;要换个名字再来。", name, existing.WorkerID)
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, fmt.Errorf("当前会话没有工作目录,无法创建 worker 的共享目录")
	}
	info, err := os.Stat(baseDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("基础目录不存在或不是目录: %s", baseDir)
	}

	dir := uniqueWorkerDir(baseDir, "worker-"+workerSlug(name))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建工作目录失败: %w", err)
	}
	if copyFrom != "" {
		if msg, err := seedFrom(dir, copyFrom); err != nil {
			return nil, fmt.Errorf("复制起点目录失败: %w", err)
		} else if msg != "" {
			slog.Info("worker workspace seeded", "dir", dir, "from", msg)
		}
	}
	rec := &WorkerRecord{
		Name:    name,
		Summary: summary,
		WorkDir: dir,
	}
	rec.NotePath = filepath.Join(dir, WorkerNoteFileName)
	if err := writeNoteIfMissing(rec.NotePath, workerNoteTemplate(name, summary)); err != nil {
		return nil, fmt.Errorf("创建公约文件失败: %w", err)
	}
	if err := w.store.CreateWorker(rec); err != nil {
		return nil, err
	}
	slog.Info("worker created", "worker", rec.WorkerID, "name", name, "dir", dir)
	return rec, nil
}

// validateWorkerName keeps a worker name a single-line label. The name is shown
// in the sidebar, listed to the model, and written into the system prompt block
// of every session that joins — and the agent itself can mint names through the
// workers tool, so path separators, newlines and control characters have no
// business being there.
func validateWorkerName(name string) error {
	if strings.ContainsAny(name, "/\\:*?\"<>|") {
		return fmt.Errorf("worker 名称不能包含路径分隔符或保留字符:/ \\ : * ? \" < > |")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("worker 名称不能包含换行或控制字符")
		}
	}
	if name == "." || name == ".." {
		return fmt.Errorf("worker 名称不能是 . 或 ..")
	}
	return nil
}

// UpdateSummary rewrites the summary other sessions read when they decide to
// join. It never touches the directory (so no prompt of an existing session can
// shift), and the name is left alone — renaming a worker would make its
// sessions' snapshots disagree with the row, which is a trade not worth it.
func (w *WorkerService) UpdateSummary(workerID, summary string) error {
	summary = strings.TrimSpace(summary)
	if len([]rune(summary)) > MaxWorkerSummaryRunes {
		return fmt.Errorf("worker 摘要过长(最多 %d 字)", MaxWorkerSummaryRunes)
	}
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("worker 不存在: %s", workerID)
	}
	rec.Summary = summary
	return w.store.UpdateWorker(rec)
}

// Delete removes a worker and every session in it (sub-agent children included,
// through SessionManager.Delete). The workspace directory stays on disk — files
// are the user's, deleting them has to stay a deliberate act.
func (w *WorkerService) Delete(workerID string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return 0, err
	}
	if rec == nil {
		return 0, fmt.Errorf("worker 不存在: %s", workerID)
	}
	ids, err := w.store.ListWorkerSessions(workerID)
	if err != nil {
		return 0, err
	}
	for _, sid := range ids {
		if err := w.mgr.Delete(sid); err != nil {
			return len(ids), fmt.Errorf("删除会话 %s 失败: %w", sid, err)
		}
	}
	if err := w.store.DeleteWorker(workerID); err != nil {
		return len(ids), err
	}
	slog.Info("worker deleted", "worker", workerID, "name", rec.Name, "sessions", len(ids), "dir_kept", rec.WorkDir)
	return len(ids), nil
}

// --- membership ---

// sessionOf resolves a session for membership calls. SessionManager.Get returns
// (nil, nil) for an unknown id, so every membership path must go through here —
// otherwise a stale id in the sidebar (deleted in another window) turns into a
// nil-pointer panic instead of an error message.
func (w *WorkerService) sessionOf(sessionID string) (*Session, error) {
	sess, err := w.mgr.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, fmt.Errorf("会话不存在: %s", sessionID)
	}
	return sess, nil
}

// Join attaches a session to a worker: the session moves into the worker's
// shared directory and gets a fresh convention snapshot. Returns the worker.
func (w *WorkerService) Join(sessionID, workerID string) (*WorkerRecord, error) {
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("worker 不存在: %s", workerID)
	}
	if rec.Builtin {
		return nil, fmt.Errorf("%s 是内置分桶,不能加入", rec.Name)
	}
	sess, err := w.sessionOf(sessionID)
	if err != nil {
		return nil, err
	}
	if err := w.attach(sess, rec); err != nil {
		return nil, err
	}
	slog.Info("session joined worker", "session", sessionID, "worker", rec.WorkerID, "dir", rec.WorkDir)
	return rec, nil
}

// attach writes the membership + snapshot for one session and one worker row.
//
// A built-in bucket (已归档) is deliberately attached with an EMPTY name,
// directory and convention: it is a shelf, not a line of work, and keeping those
// fields empty is what makes an archived session's system prompt identical to
// what it was before the worker feature existed (see workerPromptSection).
func (w *WorkerService) attach(sess *Session, rec *WorkerRecord) error {
	if sess == nil {
		return fmt.Errorf("会话不存在")
	}
	if rec == nil {
		return fmt.Errorf("worker 不存在")
	}
	name, dir, notePath, note := rec.Name, rec.WorkDir, rec.NotePath, ""
	if rec.Builtin {
		name, dir, notePath = "", "", ""
	} else {
		var err error
		if note, err = readWorkerNote(rec.NotePath); err != nil {
			// A missing/unreadable convention is not a reason to refuse the
			// move: the block simply says it is empty.
			note = ""
		}
	}
	sess.SetWorkerMembership(rec.WorkerID, name, dir, notePath, note)
	return nil
}

// Leave detaches a session. The working directory stays where it is (the files
// the session wrote stay put; the next task simply stops carrying the worker
// block).
func (w *WorkerService) Leave(sessionID string) error {
	sess, err := w.sessionOf(sessionID)
	if err != nil {
		return err
	}
	sess.SetWorkerMembership(WorkerUnassignedID, "", "", "", "")
	slog.Info("session left worker", "session", sessionID)
	return nil
}

// ensureNoteSnapshot gives a session that has a worker but no snapshot yet its
// first one, and only once. It exists for rows that predate a snapshot (a
// session created before the worker existed, a hand-edited database): without
// it the first task would render the block from live data and the second one
// from a snapshot, i.e. two different prefixes for one session.
//
// Built-in buckets (已归档) are skipped on purpose: they carry no directory and
// no convention, so those sessions keep the exact prompt they had before the
// worker feature existed.
func (s *Session) ensureNoteSnapshot() {
	s.mu.Lock()
	id := s.record.WorkerID
	_, ok := s.record.Metadata[workerNoteMetaKey]
	mgr := s.manager
	s.mu.Unlock()
	if id == "" || ok || mgr == nil {
		return
	}
	w := mgr.Workers()
	if w == nil {
		return
	}
	w.snapshotInto(id, s)
}

// snapshotInto reads the worker row and its convention file once and writes the
// result into the session record. Everything after this point reads the record.
func (w *WorkerService) snapshotInto(workerID string, sess *Session) {
	rec, err := w.store.GetWorker(workerID)
	if err != nil || rec == nil || rec.Builtin {
		return
	}
	note, _ := readWorkerNote(rec.NotePath)
	sess.SetWorkerMembership(rec.WorkerID, rec.Name, rec.WorkDir, rec.NotePath, note)
}

// Move is the session-centric entry point the UI uses ("移动到 Worker").
// bucket == "" (or "*") detaches. Unlike the model-facing join, it accepts the
// built-in buckets too — that is how the old "归档" action keeps working.
func (w *WorkerService) Move(sessionID, bucket string) error {
	if bucket == WorkerUnassignedID || bucket == WorkerAllID {
		return w.Leave(sessionID)
	}
	if bucket == WorkerArchivedID {
		// The bucket is created by the migration only when legacy archived rows
		// exist; the legacy archive API can be the first thing that needs it.
		if err := w.store.ensureArchivedBucket(); err != nil {
			return err
		}
	}
	rec, err := w.store.GetWorker(bucket)
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("worker 不存在: %s", bucket)
	}
	sess, err := w.sessionOf(sessionID)
	if err != nil {
		return err
	}
	if err := w.attach(sess, rec); err != nil {
		return err
	}
	slog.Info("session moved", "session", sessionID, "worker", rec.WorkerID, "name", rec.Name)
	return nil
}

// MoveToWorker is the manager-level entry point the web layer uses. It fails
// loudly when the worker service is not wired, instead of silently doing
// nothing on a click.
func (m *SessionManager) MoveToWorker(sessionID, bucket string) error {
	w := m.Workers()
	if w == nil {
		return fmt.Errorf("worker 服务未启用")
	}
	return w.Move(sessionID, bucket)
}

// RefreshNoteSnapshot reloads one session's convention snapshot from disk.
// This is the ONLY way a live session's prompt picks up an edited convention
// file, so it is always an explicit user action — and it does mean the next
// request of that session recomputes its prefix (the whole point).
func (w *WorkerService) RefreshNoteSnapshot(sessionID string) (changed bool, err error) {
	sess, err := w.sessionOf(sessionID)
	if err != nil {
		return false, err
	}
	sess.mu.Lock()
	workerID := sess.record.WorkerID
	prev := metadataString(sess.record, workerNoteMetaKey)
	sess.mu.Unlock()
	if workerID == "" {
		return false, fmt.Errorf("该会话还没有归属 worker")
	}
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return false, err
	}
	if rec == nil {
		return false, fmt.Errorf("worker 不存在: %s", workerID)
	}
	note, err := readWorkerNote(rec.NotePath)
	if err != nil {
		return false, err
	}
	sess.SetWorkerMembership(rec.WorkerID, rec.Name, rec.WorkDir, rec.NotePath, note)
	return prev != note, nil
}

// --- convention file (公约) ---

// ReadNote returns the convention file of a worker as it is on disk.
func (w *WorkerService) ReadNote(workerID string) (string, error) {
	rec, err := w.store.GetWorker(workerID)
	if err != nil || rec == nil {
		return "", fmt.Errorf("worker 不存在: %s", workerID)
	}
	return readWorkerNote(rec.NotePath)
}

// WriteNote rewrites the convention file. The runtime gate makes an agent write
// need an explicit UI approval in every permission mode (session/gate.go); a
// human editing through the sidebar is already the authorization, so this
// method itself does not ask. Existing sessions keep their snapshots — call
// RefreshNoteSnapshot for the sessions that should see it now.
func (w *WorkerService) WriteNote(workerID, content string) error {
	rec, err := w.store.GetWorker(workerID)
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("worker 不存在: %s", workerID)
	}
	if rec.NotePath == "" {
		return fmt.Errorf("%s 没有公约文件(它没有工作目录)", rec.Name)
	}
	content = strings.TrimSpace(content)
	if len([]rune(content)) > MaxNoteChars {
		return fmt.Errorf("公约太长(当前 %d 字,最多 %d 字):它每个任务都会注入一次,请只留结论和约定", len([]rune(content)), MaxNoteChars)
	}
	if err := os.MkdirAll(filepath.Dir(rec.NotePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(rec.NotePath, []byte(content+"\n"), 0644)
}

// --- worker brief (a special user message, never the system prompt) ---

const (
	// workerBriefMarkerPrefix marks a brief so later turns (and compaction) can
	// find it by scanning the conversation. Same trick as the usage notices.
	workerBriefMarkerPrefix = "__MC_WORKER_BRIEF__"
	// workerBriefRole is deliberately "user": it is the role the app may add to a
	// conversation; "system" would be a second system message.
	workerBriefRole = "user"
)

// workerBriefSignature identifies one brief: the worker id plus a hash of its
// body. Two briefs with the same signature carry the same text, which is how the
// loop decides to append nothing at all (zero new bytes) when nothing changed.
func workerBriefSignature(workerID, body string) string {
	var h uint32 = 2166136261 // FNV-1a over a few KB of text: fingerprint, not crypto
	for _, r := range body {
		h ^= uint32(r)
		h *= 16777619
	}
	return fmt.Sprintf("%s%s:%08x", workerBriefMarkerPrefix, workerID, h)
}

// BuildWorkerBrief wraps a rendered body into the message that joins the
// conversation. Exported for tests.
func BuildWorkerBrief(workerID, body string) provider.Message {
	return provider.Message{Role: workerBriefRole, Content: body + "\n\n" + workerBriefSignature(workerID, body)}
}

// IsWorkerBrief reports whether m is a worker brief of any worker, any version.
func IsWorkerBrief(m provider.Message) bool {
	return m.Role == workerBriefRole && strings.Contains(m.GetText(), workerBriefMarkerPrefix)
}

// workerBriefSignatureOf pulls the marker out of a brief ("" when m is not one).
func workerBriefSignatureOf(m provider.Message) string {
	t := m.GetText()
	i := strings.Index(t, workerBriefMarkerPrefix)
	if i < 0 {
		return ""
	}
	t = t[i:]
	if j := strings.IndexAny(t, " \n\r\t"); j >= 0 {
		return t[:j]
	}
	return t
}

// lastWorkerBrief returns the newest brief in a context (and its signature).
// The newest one is the current state; older briefs are simply history.
func lastWorkerBrief(messages []provider.Message) (provider.Message, string) {
	var last provider.Message
	sig := ""
	for _, m := range messages {
		if IsWorkerBrief(m) {
			last, sig = m, workerBriefSignatureOf(m)
		}
	}
	return last, sig
}

// WorkerBrief renders the brief this session should carry right now — from its own
// snapshot only, never from the worker row or the 公约 file on disk. The zero
// message means "nothing to say": the sub-agent of an unattached session, or a
// session in a built-in bucket, which stays exactly as plain as it was.
func (s *Session) WorkerBrief() provider.Message {
	s.mu.Lock()
	id := s.record.WorkerID
	name := metadataString(s.record, workerNameMetaKey)
	dir := metadataString(s.record, workerDirMetaKey)
	note := metadataString(s.record, workerNoteMetaKey)
	isRoot := s.record.ParentID == ""
	s.mu.Unlock()

	if id == "" {
		if !isRoot {
			return provider.Message{}
		}
		return BuildWorkerBrief("none", workerAssignmentGuidance)
	}
	if name == "" {
		// Migrated/archived bucket, or a row without a snapshot: say nothing.
		return provider.Message{}
	}

	var b strings.Builder
	b.WriteString("<worker-brief>\n")
	b.WriteString("This session runs under the worker \"")
	b.WriteString(name)
	b.WriteString("\" (")
	b.WriteString(id)
	b.WriteString(") — one line of work whose sessions share a workspace and one convention file.\n\n")
	if dir != "" {
		b.WriteString("Shared workspace: ")
		b.WriteString(dir)
		b.WriteString("\nThat is this session's working directory: put the deliverables of this line of work there, and look there first for what earlier sessions of this worker produced.\n\n")
	}
	b.WriteString("公约 — the convention every session of this worker must follow:\n")
	b.WriteString("<convention>\n")
	if strings.TrimSpace(note) == "" {
		b.WriteString("(空的:还没有人写下规矩。做完整件事后,用 workers(action=note_write) 把值得留给同事的约定补进去——这一步需要用户批准。)\n")
	} else {
		b.WriteString(strings.TrimRight(note, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("</convention>\n\n")
	b.WriteString("This note is background the app puts in, not a request from the user — do not answer it. ")
	b.WriteString("If a newer worker brief shows up further down this conversation, the newest one is the current state. ")
	b.WriteString("To pull an edited 公约 into this session use workers(action=note_refresh) or the sidebar's ↻: that appends a newer brief and changes nothing already in the conversation.")
	b.WriteString("\n</worker-brief>")
	return BuildWorkerBrief(id, b.String())
}

// workerAssignmentGuidance is the brief an unattached root session carries until it
// decides. It stays a few lines and is a constant, so it is appended exactly once
// per session and afterwards sits in the cached history for free. The full how-to
// lives in the workers tool description.
const workerAssignmentGuidance = `<worker-brief>
This session belongs to no worker (工作大类) yet. A worker is one line of work: a
shared workspace plus a shared convention file every session of it must follow.

On your first turn, call workers(action="list") and decide:
- join an existing worker (action="join") if this continues that line of work;
- create one (action="create", auto-joins) if this is a new line of work;
- stay unattached for a one-off question that needs no workspace.
Joining switches this session's working directory to the worker's workspace, so
decide early — ideally now.

This note is background the app puts in, not a request from the user — do not answer it.
</worker-brief>`

// --- tool backend (bound to one session, see tools.WorkerDispatcher) ---

// WorkerToolAPI is the workers-tool backend for one session. Like the group
// tool, membership is read live on every call, so a join made mid-task takes
// effect for the next call.
type WorkerToolAPI struct {
	svc       *WorkerService
	sessionID string
}

// ToolAPIFor returns the workers-tool backend bound to a session.
func (w *WorkerService) ToolAPIFor(sessionID string) *WorkerToolAPI {
	return &WorkerToolAPI{svc: w, sessionID: sessionID}
}

func (a *WorkerToolAPI) own() (*Session, bool) {
	sess, err := a.svc.mgr.Get(a.sessionID)
	if err != nil || sess == nil {
		return nil, false
	}
	return sess, true
}

// ownWorker resolves the caller's worker row (nil when unattached/unknown).
func (a *WorkerToolAPI) ownWorker() *WorkerRecord {
	sess, ok := a.own()
	if !ok {
		return nil
	}
	sess.mu.Lock()
	id := sess.record.WorkerID
	sess.mu.Unlock()
	if id == "" {
		return nil
	}
	rec, err := a.svc.store.GetWorker(id)
	if err != nil {
		return nil
	}
	return rec
}

func (a *WorkerToolAPI) WorkerInfo() string {
	sess, ok := a.own()
	if !ok {
		return "会话不存在,无法确认 worker 归属。"
	}
	sess.mu.Lock()
	id, dir := sess.record.WorkerID, sess.record.WorkDir
	name := metadataString(sess.record, workerNameMetaKey)
	notePath := metadataString(sess.record, workerNotePathMetaKey)
	sess.mu.Unlock()
	if id == "" {
		return "本会话尚未归属任何 worker。用 action=list 看看已有的 worker,或 action=create 新建一个,或保持现状(一次性小问题不需要 worker)。"
	}
	rec, _ := a.svc.store.GetWorker(id)
	var b strings.Builder
	fmt.Fprintf(&b, "本会话属于 worker「%s」(id %s)。\n", firstNonEmpty(name, rec2Name(rec)), id)
	fmt.Fprintf(&b, "共享工作目录:%s\n", dir)
	if notePath != "" {
		fmt.Fprintf(&b, "公约文件:%s(本会话看到的是加入时的快照,用 note_read 看磁盘最新版)\n", notePath)
	}
	if rec != nil && rec.Summary != "" {
		fmt.Fprintf(&b, "摘要:%s\n", rec.Summary)
	}
	if rec != nil {
		fmt.Fprintf(&b, "同 worker 会话数:%d(用 history 看它们在做什么)\n", rec.Sessions)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (a *WorkerToolAPI) List() string {
	recs, err := a.svc.Joinable()
	if err != nil {
		return "读取 worker 列表失败: " + err.Error()
	}
	mine := ""
	if w := a.ownWorker(); w != nil {
		mine = w.WorkerID
	}
	if len(recs) == 0 {
		return "还没有任何 worker。如果这次工作需要一个长期共享目录(会持续多轮、多个会话),用 action=create 新建;一次性小问题可以不用 worker。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 个 worker(★=本会话当前所在):\n", len(recs))
	for _, rec := range recs {
		mark := "-"
		if rec.WorkerID == mine {
			mark = "★"
		}
		fmt.Fprintf(&b, "%s [%s] %s — %d 个会话\n", mark, rec.WorkerID, rec.Name, rec.Sessions)
		if rec.Summary != "" {
			fmt.Fprintf(&b, "    摘要:%s\n", rec.Summary)
		}
		fmt.Fprintf(&b, "    工作目录:%s\n", rec.WorkDir)
	}
	b.WriteString("join 用 worker_id;新建用 create。")
	return b.String()
}

func (a *WorkerToolAPI) Create(name, summary, copyFrom string) string {
	base := ""
	if sess, ok := a.own(); ok {
		sess.mu.Lock()
		base = sess.record.WorkDir
		sess.mu.Unlock()
	}
	rec, err := a.svc.Create(name, summary, base, copyFrom)
	if err != nil {
		return "创建 worker 失败: " + err.Error()
	}
	if _, err := a.svc.Join(a.sessionID, rec.WorkerID); err != nil {
		return fmt.Sprintf("worker「%s」已建好(id %s,目录 %s),但本会话加入失败:%s", rec.Name, rec.WorkerID, rec.WorkDir, err)
	}
	return fmt.Sprintf(
		"已创建并加入 worker「%s」(id %s)。\n共享工作目录:%s\n公约文件:%s\n"+
			"注意:本回合已创建的工具仍指向旧工作目录,接下来的文件操作请继续使用绝对路径;从下一个任务开始,工作目录自动变为上面的共享目录。\n"+
			"建议:用 note_write 把这次工作的目标和约定写进公约(会弹用户确认)。",
		rec.Name, rec.WorkerID, rec.WorkDir, rec.NotePath)
}

func (a *WorkerToolAPI) Join(workerID string) string {
	rec, err := a.svc.Join(a.sessionID, workerID)
	if err != nil {
		return "加入 worker 失败: " + err.Error()
	}
	note, _ := readWorkerNote(rec.NotePath)
	tail := ""
	if strings.TrimSpace(note) == "" {
		tail = "\n该 worker 的公约还是空的,建议用 note_write 写几条简短约定(会弹用户确认)。"
	}
	return fmt.Sprintf(
		"已加入 worker「%s」(id %s)。\n共享工作目录:%s\n公约文件:%s\n"+
			"注意:本回合已创建的工具仍指向旧工作目录,接下来的文件操作请继续使用绝对路径;从下一个任务开始,工作目录自动变为上面的共享目录。"+
			"用 history 可以看同 worker 的其他会话在做什么。"+tail,
		rec.Name, rec.WorkerID, rec.WorkDir, rec.NotePath)
}

func (a *WorkerToolAPI) Leave() string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话本来就没有归属 worker。"
	}
	if err := a.svc.Leave(a.sessionID); err != nil {
		return "退出 worker 失败: " + err.Error()
	}
	return fmt.Sprintf("已退出 worker「%s」。工作目录保持在 %s 不变(如需换目录请说明)。下一个任务起不再注入该 worker 的目录与公约。",
		rec.Name, a.workDirOf())
}

func (a *WorkerToolAPI) Update(summary string) string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话还没有归属 worker,改写不了摘要。"
	}
	if err := a.svc.UpdateSummary(rec.WorkerID, summary); err != nil {
		return "改写摘要失败: " + err.Error()
	}
	return "已更新 worker「" + rec.Name + "」的摘要,别的会话下次 list 就能看到。"
}

func (a *WorkerToolAPI) History(limit int) string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话还没有归属 worker。"
	}
	sums, err := a.svc.store.ListBucketSummaries(rec.WorkerID, limit, 0)
	if err != nil {
		return "读取兄弟会话失败: " + err.Error()
	}
	var b strings.Builder
	n := 0
	for _, sum := range sums {
		if sum.SessionID == a.sessionID {
			continue
		}
		n++
		fmt.Fprintf(&b, "- [%s] %s — %s,%d 条,%s\n", sum.SessionID, firstNonEmpty(sum.Title, "未命名会话"),
			sum.Status, sum.MessageCount, relTimeShort(sum.UpdatedAt))
		if sum.WorkDir != "" && sum.WorkDir != rec.WorkDir {
			fmt.Fprintf(&b, "    (工作目录:%s)\n", sum.WorkDir)
		}
	}
	if n == 0 {
		return "同 worker 目前只有本会话,没有别的会话。"
	}
	return fmt.Sprintf("worker「%s」的其他会话(共 %d 个,按更新时间):\n%s用 messages(session_id) 读某个会话的具体内容。",
		rec.Name, n, b.String())
}

func (a *WorkerToolAPI) Messages(sessionID string, chars int) string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话还没有归属 worker,不能读别的会话。"
	}
	// Only siblings: a worker's sessions may read each other, nothing else.
	other, err := a.svc.store.Get(sessionID)
	if err != nil || other == nil {
		return "找不到会话: " + sessionID
	}
	if other.WorkerID != rec.WorkerID {
		return "只能读取同一个 worker 里其他会话的历史。"
	}
	msgs := a.svc.mgr.CopyMessages(sessionID)
	if len(msgs) == 0 {
		return "会话「" + firstNonEmpty(other.Title, sessionID) + "」还没有消息。"
	}
	return renderConversation(firstNonEmpty(other.Title, sessionID), msgs, chars)
}

func (a *WorkerToolAPI) NoteRead() string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话还没有归属 worker,没有公约可读。"
	}
	note, err := readWorkerNote(rec.NotePath)
	if err != nil {
		return "读取公约失败: " + err.Error()
	}
	return fmt.Sprintf("公约(%s)\n%s", rec.NotePath, firstNonEmpty(strings.TrimSpace(note), "(空的)"))
}

func (a *WorkerToolAPI) NoteWrite(content string) string {
	rec := a.ownWorker()
	if rec == nil {
		return "本会话还没有归属 worker,写不了公约(先 join 或 create)。"
	}
	if err := a.svc.WriteNote(rec.WorkerID, content); err != nil {
		return "写公约失败: " + err.Error()
	}
	return fmt.Sprintf("已写入公约:%s\n本会话仍使用加入时的快照;要让本会话立即改用新版,再调 action=note_refresh(它会给本会话追加一条最新简报)。同 worker 的其他会话在下次刷新或新建会话时才会看到新版。", rec.NotePath)
}

func (a *WorkerToolAPI) NoteRefresh() string {
	changed, err := a.svc.RefreshNoteSnapshot(a.sessionID)
	if err != nil {
		return "刷新公约快照失败: " + err.Error()
	}
	if !changed {
		return "本会话的公约快照已经是磁盘上的最新版,没有变化。"
	}
	return "已把最新公约加载进本会话(下一轮起带上一条新简报,已有的对话内容不变)。"
}

func (a *WorkerToolAPI) workDirOf() string {
	sess, ok := a.own()
	if !ok {
		return ""
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.record.WorkDir
}

// renderConversation flattens a sibling session's conversation into plain text,
// keeping the most recent messages that fit into the character budget (the tail
// is what tells you where that session stopped).
func renderConversation(title string, msgs []provider.Message, chars int) string {
	type item struct{ role, text string }
	var items []item
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue // tool calls/results are noise for cross-session context
		}
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		if len([]rune(text)) > 800 {
			text = string([]rune(text)[:800]) + "…"
		}
		items = append(items, item{role: m.Role, text: text})
	}
	if len(items) == 0 {
		return "会话「" + title + "」没有可读的文本消息。"
	}
	// Walk backwards so the newest messages win the budget.
	used := 0
	start := len(items)
	for i := len(items) - 1; i >= 0; i-- {
		cost := len([]rune(items[i].text)) + 12
		if used+cost > chars {
			break
		}
		used += cost
		start = i
	}
	var b strings.Builder
	fmt.Fprintf(&b, "会话「%s」最近的对话(从第 %d/%d 条起,共 %d 条消息):\n", title, start+1, len(items), len(msgs))
	for _, it := range items[start:] {
		fmt.Fprintf(&b, "[%s] %s\n", it.role, it.text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- helpers ---

// workerSlug turns a worker name into something safe (and printable in a cmd
// window) to name the shared directory. Non-ASCII names fall back to a short
// random suffix: the row still carries the real name, and the UI shows that.
func workerSlug(name string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(name) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_'
		if ok {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-.")
	if slug == "" {
		return util.RandomHex(4)
	}
	if len(slug) > 24 {
		slug = slug[:24]
	}
	return strings.Trim(slug, "-.")
}

// uniqueWorkerDir picks a directory name that does not exist yet under base.
func uniqueWorkerDir(base, name string) string {
	p := filepath.Join(base, name)
	for i := 2; pathExists(p); i++ {
		p = filepath.Join(base, fmt.Sprintf("%s-%d", name, i))
	}
	return p
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// seedFrom copies an existing file or directory into the new workspace.
func seedFrom(dir, copyFrom string) (string, error) {
	src, err := filepath.Abs(strings.TrimSpace(copyFrom))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("copy_from 不存在: %s", src)
	}
	if src == filepath.Clean(dir) || strings.HasPrefix(strings.ToLower(src+string(os.PathSeparator)), strings.ToLower(dir+string(os.PathSeparator))) {
		return "", fmt.Errorf("copy_from 不能是新工作目录本身或它里面的东西: %s", src)
	}
	if info.IsDir() {
		if err := tools.CopyTree(src, dir); err != nil {
			return "", err
		}
		return src, nil
	}
	return src, tools.CopyTree(src, filepath.Join(dir, filepath.Base(src)))
}

func workerNoteTemplate(name, summary string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(name)
	b.WriteString(" · 工作公约\n\n")
	b.WriteString("> 本文件是这个 worker 下所有会话共享的公约:每个会话提问时都会以一条独立的背景消息看到它。\n")
	b.WriteString("> 请只写结论与约定,越短越好(上限 6000 字)。改动会影响这里的全部会话。\n\n")
	b.WriteString("## 目标\n")
	if summary != "" {
		b.WriteString(summary)
		b.WriteString("\n")
	} else {
		b.WriteString("(这个 worker 在做什么,一句话说清)\n")
	}
	b.WriteString("\n## 约定\n")
	b.WriteString("- (目录/命名/交付物/验收标准)\n")
	b.WriteString("\n## 现状\n")
	b.WriteString("- (最近进展、下一步、已知坑)\n")
	return b.String()
}

func writeNoteIfMissing(path, content string) error {
	if pathExists(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func readWorkerNote(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func rec2Name(rec *WorkerRecord) string {
	if rec == nil {
		return ""
	}
	return rec.Name
}

// relTimeShort is the tool-facing form of a timestamp: the absolute time, cut
// to minutes (the model has no use for the sidebar's "3 分钟前").
func relTimeShort(ts string) string {
	if len(ts) >= 16 {
		return ts[:16]
	}
	return ts
}
