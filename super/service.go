package super

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/tools"
	"github.com/user/mini_code/util"
)

// Config is the human-owned boundary of the super system. It lives in
// ~/.mini_code/super/config.json — deliberately NOT inside AppConfig, so the
// super panel can read/write just this file without shipping model keys
// around, and the existing settings modal never touches it.
type Config struct {
	WorkerCap       int    `json:"worker_cap"`         // live super workers ceiling (1..16)
	DefaultModel    string `json:"default_model"`      // worker model; "" = server default
	WSRoot          string `json:"ws_root"`            // root for super-<id> workspaces
	BatchSize       int    `json:"batch_size"`         // buffered channel batch N
	FlushTimeoutMin int    `json:"flush_timeout_min"`  // safety flush for a stale buffer (human-set, default 60)
}

func defaultConfig() Config {
	root := filepath.Join(os.TempDir(), "mini_code-work")
	if home, err := os.UserHomeDir(); err == nil {
		root = filepath.Join(home, "mini_code-work")
	}
	return Config{WorkerCap: 3, WSRoot: root, BatchSize: 5, FlushTimeoutMin: 60}
}

func (c *Config) normalize() {
	d := defaultConfig()
	if c.WorkerCap < 1 {
		c.WorkerCap = d.WorkerCap
	}
	if c.WorkerCap > 16 {
		c.WorkerCap = 16
	}
	if c.BatchSize < 1 {
		c.BatchSize = d.BatchSize
	}
	if c.BatchSize > 20 {
		c.BatchSize = 20
	}
	if c.FlushTimeoutMin < 1 {
		c.FlushTimeoutMin = d.FlushTimeoutMin
	}
	if strings.TrimSpace(c.WSRoot) == "" {
		c.WSRoot = d.WSRoot
	}
}

// ExecRec is one script execution record (in-memory ring for the panel's
// history popover; the full output always lives in outbox/).
type ExecRec struct {
	Ts   string `json:"ts"`
	Exit int    `json:"exit"`
	Ms   int64  `json:"ms"`
	Tail string `json:"tail"`
}

// Service is the super subsystem: channel, workers, queue, runner, audit.
// It implements session.SuperRegistrar (the tool-registration hook the
// session package defines so it never has to import this package).
type Service struct {
	store *Store
	mgr   *session.SessionManager
	hub   *channels.Hub

	dir       string // ~/.mini_code/super
	scripts   string
	outbox    string
	cfgPath   string

	mu        sync.Mutex
	cfg       Config
	superID   string
	startedAt time.Time

	pickupMu sync.Mutex
	runMu    sync.Mutex
	running  map[string]bool // schedule file -> running now

	histMu sync.Mutex
	hist   map[string][]ExecRec

	ctx context.Context
}

func NewService(db *util.Database, mgr *session.SessionManager, hub *channels.Hub) *Service {
	dir, err := config.GetConfigDir()
	base := filepath.Join(os.TempDir(), "mini_code_super")
	if err == nil {
		base = filepath.Join(dir, "super")
	}
	s := &Service{
		store:   NewStore(db),
		mgr:     mgr,
		hub:     hub,
		dir:     base,
		scripts: filepath.Join(base, "scripts"),
		outbox:  filepath.Join(base, "outbox"),
		cfgPath: filepath.Join(base, "config.json"),
		running: map[string]bool{},
		hist:    map[string][]ExecRec{},
		ctx:     context.Background(),
	}
	for _, d := range []string{s.dir, s.scripts, s.outbox} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			slog.Warn("super: cannot create dir", "dir", d, "err", err)
		}
	}
	s.cfg = s.loadConfig()
	return s
}

// --- config ---

func (s *Service) loadConfig() Config {
	c := defaultConfig()
	b, err := os.ReadFile(s.cfgPath)
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	c.normalize()
	return c
}

func (s *Service) saveConfigLocked() {
	b, err := json.MarshalIndent(&s.cfg, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(s.cfgPath, b, 0o644); err != nil {
		slog.Warn("super: config not saved", "err", err)
	}
}

func (s *Service) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// SetConfig applies a human edit from the panel: every changed field is
// audited (actor=human), the file is rewritten, and the queue gets a chance
// to move (a raised cap frees slots immediately).
func (s *Service) SetConfig(next Config) Config {
	old := s.Config()
	next.normalize()
	diff := []struct {
		name       string
		changed    bool
		to         string
	}{
		{"super.worker_cap", old.WorkerCap != next.WorkerCap, fmt.Sprint(next.WorkerCap)},
		{"super.default_model", old.DefaultModel != next.DefaultModel, orNone(next.DefaultModel)},
		{"super.ws_root", old.WSRoot != next.WSRoot, next.WSRoot},
		{"super.batch_size", old.BatchSize != next.BatchSize, fmt.Sprint(next.BatchSize)},
		{"super.flush_timeout_min", old.FlushTimeoutMin != next.FlushTimeoutMin, fmt.Sprint(next.FlushTimeoutMin)},
	}
	s.mu.Lock()
	s.cfg = next
	s.saveConfigLocked()
	s.mu.Unlock()
	for _, d := range diff {
		if d.changed {
			s.audit("human", "human.config", d.name, "→ "+d.to, "")
		}
	}
	if old.WorkerCap != next.WorkerCap {
		go s.PickUpQueue()
	}
	return next
}

func orNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(默认)"
	}
	return v
}

// --- lifecycle ---

// Start wires the hub subscription (worker reports → channel) and the runner
// ticker (schedules, batch-timeout flush, queue pickup). Safe to call once.
func (s *Service) Start(ctx context.Context) {
	s.ctx = ctx
	s.audit("system", "super.boot", "runner", "super 子系统已随 minicode 常驻启动", "")
	ch := s.hub.Subscribe("super-service")
	go s.eventLoop(ctx, ch)
	go s.runnerLoop(ctx)
	go s.PickUpQueue()
}

func (s *Service) eventLoop(ctx context.Context, ch <-chan channels.Message) {
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			if m.Kind != channels.KindAssistant && m.Kind != channels.KindError {
				continue
			}
			s.handleWorkerTerm(m)
		}
	}
}

// handleWorkerTerm turns the terminal message of a bound worker session into
// an immediate channel report for the super agent.
func (s *Service) handleWorkerTerm(m channels.Message) {
	sid := m.SessionID
	if sid == "" || sid == s.SuperSessionID() {
		return
	}
	b, err := s.store.GetSessionBinding(sid)
	if err != nil || b == nil || b.Status != "active" {
		return
	}
	w, _ := s.store.GetWorker(b.SwID)
	name := b.SwID
	if w != nil {
		name = w.Name
	}
	content := truncate(strings.TrimSpace(m.Content), 2000)
	if content == "" {
		return
	}
	kind := "完成"
	if m.Kind == channels.KindError {
		kind = "执行失败"
	}
	text := fmt.Sprintf("会话 %s（worker %s）%s：\n%s", sid, name, kind, content)
	if _, err := s.Publish("imm", "worker_report:"+b.SwID, text, "runner"); err != nil {
		slog.Warn("super: worker report not published", "session", sid, "err", err)
	}
}

// --- super session ---

// SuperSessionID returns the cached id of the resident super session ("" =
// not ensured yet).
func (s *Service) SuperSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.superID
}

// RelatedSessionIDs returns the resident super session plus every session
// ever spawned for a super worker. The normal session list hides all of them:
// this family is reached through the Super-Agent panel, not the sidebar.
func (s *Service) RelatedSessionIDs() map[string]bool {
	out := map[string]bool{}
	if id := s.SuperSessionID(); id != "" {
		out[id] = true
	}
	ids, err := s.store.AllWorkerSessionIDs()
	if err != nil {
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// EnsureSuper finds or creates the one resident super session (agent_role
// "super", root, own workspace under ws_root) and pins its title so the
// automatic titler never renames it.
func (s *Service) EnsureSuper() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.superID != "" {
		if sess, err := s.mgr.Get(s.superID); err == nil && sess != nil {
			return s.superID, nil
		}
		s.superID = ""
	}
	// adopt a previous one (persisted state, then a DB scan for pre-state rows)
	if id := s.store.StateGet("super_session_id"); id != "" {
		if sess, err := s.mgr.Get(id); err == nil && sess != nil && sess.AgentRole() == "super" {
			s.superID = id
			return id, nil
		}
	}
	if row, err := s.store.db.FetchOne(
		"SELECT session_id FROM sessions WHERE agent_role = 'super' AND COALESCE(parent_id,'') = '' ORDER BY created_at ASC LIMIT 1"); err == nil && row != nil {
		id := asStr(row["session_id"])
		if sess, err := s.mgr.Get(id); err == nil && sess != nil {
			s.superID = id
			_ = s.store.StateSet("super_session_id", id)
			return id, nil
		}
	}
	// create
	dir := filepath.Join(s.cfg.WSRoot, "super-agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create super workspace %s: %w", dir, err)
	}
	sess, err := s.mgr.CreateSessionRecord("Super-Agent", "super", dir, nil)
	if err != nil {
		return "", fmt.Errorf("create super session: %w", err)
	}
	// Pin the title (TitleSrc=user): the auto titler must not rename the fixture.
	if err := s.mgr.RenameSession(sess.ID(), "Super-Agent"); err != nil {
		slog.Warn("super: title not pinned", "session", sess.ID(), "err", err)
	}
	s.superID = sess.ID()
	_ = s.store.StateSet("super_session_id", sess.ID())
	s.audit("system", "super.session", sess.ID(), "super 会话已创建（agent_role=super，常驻分发）", sess.ID())
	return sess.ID(), nil
}

// inject delivers one text to the super session (queued if it is mid-task).
func (s *Service) inject(text string) {
	id, err := s.EnsureSuper()
	if err != nil {
		slog.Warn("super: no super session to inject into", "err", err)
		return
	}
	if err := s.mgr.InjectMessage(id, text); err != nil {
		slog.Warn("super: inject failed", "session", id, "err", err)
	}
}

// --- audit ---

func (s *Service) audit(actor, action, target, detail, refs string) {
	if err := s.store.AuditInsert(actor, action, target, detail, refs); err != nil {
		slog.Warn("super: audit row failed", "action", action, "err", err)
	}
}

// --- channel ---

// Publish puts one message on the channel. mode=imm is delivered to the
// super session immediately; mode=buf waits in the buffer pool until the
// batch is full (batch_size) or the human flushes; a safety flush covers a
// stale buffer after flush_timeout_min. actor records who produced it.
func (s *Service) Publish(mode, src, payload, actor string) (*Message, error) {
	payload = truncate(strings.TrimSpace(payload), 4000)
	if payload == "" {
		return nil, fmt.Errorf("消息内容为空")
	}
	m := &Message{Mode: mode, Src: src, Payload: payload, Status: "pending"}
	if mode == "imm" {
		m.Status = "delivered"
		m.DeliveredAt = nowStr()
		if err := s.store.InsertMessage(m); err != nil {
			return nil, err
		}
		s.audit(orNone2(actor, "human"), "channel.deliver", "immediate", "source="+src, "")
		wrapped := fmt.Sprintf("【super通道·立即】来源=%s\n%s\n（系统注入的通道消息，请按 Router 职责处理。）", src, payload)
		go s.inject(wrapped)
		return m, nil
	}
	if err := s.store.InsertMessage(m); err != nil {
		return nil, err
	}
	pending, _ := s.store.PendingBuffered()
	s.audit(orNone2(actor, "runner"), "channel.enqueue", "buffered",
		fmt.Sprintf("source=%s · %d/%d", src, len(pending), s.Config().BatchSize), "")
	if len(pending) >= s.Config().BatchSize {
		go func() {
			if _, _, err := s.Flush("auto"); err != nil {
				slog.Debug("super: auto flush raced", "err", err)
			}
		}()
	}
	return m, nil
}

func orNone2(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Flush sends the buffered pool to the super session as one batch. reason:
// auto | manual | timeout. Returns the batch id and the message count.
func (s *Service) Flush(reason string) (string, int, error) {
	pending, err := s.store.PendingBuffered()
	if err != nil {
		return "", 0, err
	}
	if len(pending) == 0 {
		return "", 0, fmt.Errorf("缓冲池是空的")
	}
	batchID := "b-" + util.RandomHex(4)
	ids := make([]string, 0, len(pending))
	var sb strings.Builder
	n := len(pending)
	cause := map[string]string{"auto": "攒满 " + fmt.Sprint(s.Config().BatchSize) + " 条", "manual": "人类提前冲刷", "timeout": "超时兜底（flush_timeout_min）"}[reason]
	if cause == "" {
		cause = reason
	}
	fmt.Fprintf(&sb, "【super通道·批次 %s · %d 条 · %s】\n", batchID, n, cause)
	for i, m := range pending {
		fmt.Fprintf(&sb, "[%d] (%s) %s\n", i+1, m.Src, m.Payload)
		ids = append(ids, m.ID)
	}
	sb.WriteString("（系统注入的批次消息，请分类处理：并入现有 worker、新建 worker、或排队，并回复结论。）")
	if err := s.store.BatchMessages(ids, batchID); err != nil {
		return "", 0, err
	}
	actor := "runner"
	if reason == "manual" {
		actor = "human"
	}
	s.audit(actor, "channel.batch", batchID, fmt.Sprintf("%d 条 → 注入 super（%s）", n, cause), batchID)
	go s.inject(sb.String())
	return batchID, n, nil
}

// checkFlushTimeout fires the human-configured safety flush when the oldest
// buffered message has waited longer than flush_timeout_min.
func (s *Service) checkFlushTimeout() {
	pending, err := s.store.PendingBuffered()
	if err != nil || len(pending) == 0 {
		return
	}
	oldest, err := time.Parse(time.RFC3339, pending[0].CreatedAt)
	if err != nil {
		return
	}
	s.mu.Lock()
	limit := time.Duration(s.cfg.FlushTimeoutMin) * time.Minute
	s.mu.Unlock()
	if time.Since(oldest) >= limit {
		if _, _, err := s.Flush("timeout"); err == nil {
			slog.Info("super: timeout flush", "age_min", int(time.Since(oldest).Minutes()))
		}
	}
}

// --- workers ---

// CreateWorker makes one execution body under the cap. The workspace
// ws_root\super-<id> is created with it. Every call is audited (actor=super
// when the agent decides, human never creates workers through the API).
func (s *Service) CreateWorker(name, note, model string) (*Worker, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("worker 需要名字")
	}
	cfg := s.Config()
	live, _ := s.store.CountLiveWorkers()
	if live >= cfg.WorkerCap {
		return nil, fmt.Errorf("worker 上限已满（%d/%d）：先退役一个，或用 super_queue 把任务排队并告知人类可调上限", live, cfg.WorkerCap)
	}
	if exist, _ := s.store.GetWorkerByName(name); exist != nil {
		return nil, fmt.Errorf("已存在同名活跃 worker: %s（直接给它下发任务即可）", name)
	}
	w := &Worker{
		ID: "sw-" + util.RandomHex(4), Name: name,
		Model: strings.TrimSpace(model), Note: strings.TrimSpace(note), NoteVer: 1,
	}
	if w.Model == "" {
		w.Model = cfg.DefaultModel
	}
	w.WorkDir = filepath.Join(cfg.WSRoot, "super-"+w.ID)
	if err := os.MkdirAll(w.WorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create worker workspace: %w", err)
	}
	if err := s.store.CreateWorker(w); err != nil {
		return nil, err
	}
	n, _ := s.store.CountLiveWorkers()
	s.audit("super", "super_worker.create", w.ID, fmt.Sprintf("name=%s · 名额 %d/%d", w.Name, n, cfg.WorkerCap), w.ID)
	return w, nil
}

// UpdateWorkerConfig is the human per-worker config面: role_note (versioned)
// and model override. note_ver+1 makes the next send carry the fresh note.
func (s *Service) UpdateWorkerConfig(id, note, model, actor string) (*Worker, error) {
	w, err := s.store.GetWorker(id)
	if err != nil || w == nil {
		return nil, fmt.Errorf("worker 不存在: %s", id)
	}
	newNote := w.Note
	newVer := w.NoteVer
	if note != "" && note != w.Note {
		newNote = note
		newVer = w.NoteVer + 1
	}
	newModel := strings.TrimSpace(model)
	if newModel == "" {
		newModel = w.Model
	}
	if err := s.store.UpdateWorkerCore(id, newNote, newModel, newVer); err != nil {
		return nil, err
	}
	act := "super_worker.update"
	if orNone2(actor, "human") == "human" {
		act = "human.config.worker"
	}
	s.audit(orNone2(actor, "human"), act, id,
		fmt.Sprintf("role_note %s · model=%s", map[bool]string{true: "更新, note_ver→" + fmt.Sprint(newVer), false: "未变"}[newVer != w.NoteVer], orNone(newModel)), id)
	w.Note, w.NoteVer, w.Model = newNote, newVer, newModel
	return w, nil
}

// DeleteWorker retires a worker. Sessions are NOT deleted: they stay bound
// (status preserved) so the audit trail and the conversations remain
// reviewable; the workspace folder stays on disk too.
func (s *Service) DeleteWorker(id, actor string) error {
	w, err := s.store.GetWorker(id)
	if err != nil || w == nil {
		return fmt.Errorf("worker 不存在: %s", id)
	}
	if w.Status == "retired" {
		return fmt.Errorf("worker 已退役: %s", id)
	}
	if err := s.store.SetWorkerStatus(id, "retired"); err != nil {
		return err
	}
	s.audit(orNone2(actor, "super"), "super_worker.delete", id,
		fmt.Sprintf("name=%s 退役（会话归档保留可查）", w.Name), id)
	go s.PickUpQueue()
	return nil
}

// --- worker sessions ---

// SpawnSession creates a bound conversation under the worker's workspace and
// delivers the task. The role_note (公约) is prefixed into the first task so
// the convention travels with the message, not the (cached) system prompt.
func (s *Service) SpawnSession(swID, task, title string) (string, error) {
	w, err := s.store.GetWorker(swID)
	if err != nil || w == nil {
		return "", fmt.Errorf("worker 不存在: %s", swID)
	}
	if w.Status != "active" {
		return "", fmt.Errorf("worker %s 已退役，请新建一个", swID)
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return "", fmt.Errorf("task 不能为空")
	}
	if title == "" {
		title = truncate(task, 40)
	}
	_ = os.MkdirAll(w.WorkDir, 0o755)
	// bound worker sessions are ROOT sessions (parentID nil): they show up in
	// the sidebar like any other chat — the audit story wants them visible,
	// not hidden as sub-agents of an agent that did not run them.
	sess, err := s.mgr.CreateSessionRecord(title, "build", w.WorkDir, nil)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	if w.Model != "" {
		if err := s.mgr.SetModel(sess.ID(), w.Model); err != nil {
			slog.Debug("super: worker model not applied", "worker", swID, "model", w.Model, "err", err)
		}
	}
	if err := s.store.BindSession(&WorkerSession{ID: sess.ID(), SwID: swID, Title: title, NoteVer: w.NoteVer}); err != nil {
		slog.Warn("super: session bind not recorded", "session", sess.ID(), "err", err)
	}
	text := task
	if w.Note != "" {
		text = fmt.Sprintf("<super_worker_note name=%q ver=%d>\n%s\n</super_worker_note>\n\n---\n\n%s", w.Name, w.NoteVer, w.Note, task)
	}
	if err := s.mgr.DeliverToSession(sess.ID(), text); err != nil {
		return sess.ID(), fmt.Errorf("会话已建但任务投递失败: %w", err)
	}
	s.audit("super", "super_session.spawn", sess.ID(), fmt.Sprintf("@ %s(%s) · %s", w.Name, swID, truncate(title, 60)), sess.ID()+","+swID)
	return sess.ID(), nil
}

// SendToSession appends an instruction to a bound session. If the worker's
// role_note changed since the last delivery, the fresh note rides along.
func (s *Service) SendToSession(sessID, text string) error {
	b, err := s.store.GetSessionBinding(sessID)
	if err != nil || b == nil {
		return fmt.Errorf("该会话不是 super worker 的绑定会话: %s", sessID)
	}
	if b.Status != "active" {
		return fmt.Errorf("会话 %s 已关闭", sessID)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("message 不能为空")
	}
	if w, _ := s.store.GetWorker(b.SwID); w != nil && w.Note != "" && w.NoteVer > b.NoteVer {
		text = fmt.Sprintf("<super_worker_note name=%q ver=%d>\n%s\n</super_worker_note>\n\n---\n\n%s", w.Name, w.NoteVer, w.Note, text)
		_ = s.store.SetSessionNoteVer(sessID, w.NoteVer)
	}
	if err := s.mgr.DeliverToSession(sessID, text); err != nil {
		return err
	}
	s.audit("super", "super_session.send", sessID, "@ "+b.SwID+" · "+truncate(text, 80), sessID+","+b.SwID)
	return nil
}

func (s *Service) CloseSession(sessID string) error {
	b, err := s.store.GetSessionBinding(sessID)
	if err != nil || b == nil {
		return fmt.Errorf("该会话不是 super worker 的绑定会话: %s", sessID)
	}
	s.hub.CancelTask(sessID)
	if err := s.store.SetSessionStatus(sessID, "closed"); err != nil {
		return err
	}
	s.audit("super", "super_session.close", sessID, "@ "+b.SwID, sessID+","+b.SwID)
	return nil
}

// --- queue ---

// QueueTask parks a task while the worker cap is full. Nothing is dropped:
// a freed slot (retire or a raised cap) picks it up automatically.
func (s *Service) QueueTask(task, workerName string) (*QueueRow, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return nil, fmt.Errorf("task 不能为空")
	}
	q := &QueueRow{Task: task, WorkerName: strings.TrimSpace(workerName)}
	if err := s.store.QueueAdd(q); err != nil {
		return nil, err
	}
	s.audit("super", "queue.add", q.ID, truncate(task, 100), q.ID)
	go s.PickUpQueue()
	return q, nil
}

// PickUpQueue drains the queue while slots allow. Serialized so a burst of
// retires cannot race two spawns into one slot.
func (s *Service) PickUpQueue() {
	s.pickupMu.Lock()
	defer s.pickupMu.Unlock()
	for {
		cfg := s.Config()
		live, err := s.store.CountLiveWorkers()
		if err != nil || live >= cfg.WorkerCap {
			return
		}
		pend, err := s.store.QueuePending()
		if err != nil || len(pend) == 0 {
			return
		}
		q := pend[0]
		name := q.WorkerName
		if name == "" {
			name = "task-" + q.ID[2:]
		}
		w, err := s.CreateWorker(name, "", "")
		if err != nil {
			return // cap raced or duplicate name: retry on the next trigger
		}
		sid, err := s.SpawnSession(w.ID, q.Task, "")
		if err != nil {
			s.audit("super", "queue.pickup.fail", q.ID, truncate(err.Error(), 150), q.ID+","+w.ID)
			return
		}
		_ = s.store.QueueMarkSpawned(q.ID, sid)
		s.audit("super", "queue.pickup", q.ID, "名额出现 → 建 "+w.Name+" 并下发", q.ID+","+w.ID+","+sid)
		s.inject(fmt.Sprintf("【super通道·系统】排队任务 %s 已自动捞起：新建 worker「%s」(%s)，会话 %s。", q.ID, w.Name, w.ID, sid))
	}
}

// --- overview (one round trip for every panel tab) ---

type Overview struct {
	SuperSessionID    string            `json:"super_session_id"`
	Config            Config            `json:"config"`
	Workers           []*Worker         `json:"workers"`
	Queue             []*QueueRow       `json:"queue"`
	Buf               []*Message        `json:"buf"`
	Imm               []*Message        `json:"imm"`
	BufCount          int               `json:"buf_count"`
	Schedules         []*Schedule       `json:"schedules"`
	Hist              map[string][]ExecRec `json:"hist"`
	Procs             []*Process        `json:"procs"`
	Audits            []*AuditRow       `json:"audits"`
	RunnerStartedAt   string            `json:"runner_started_at"`
	ScriptsDir        string            `json:"scripts_dir"`
	OutboxDir         string            `json:"outbox_dir"`
}

func (s *Service) Overview() *Overview {
	ov := &Overview{
		SuperSessionID:  s.SuperSessionID(),
		Config:          s.Config(),
		Workers:         []*Worker{},
		Queue:           []*QueueRow{},
		Buf:             []*Message{},
		Imm:             []*Message{},
		Schedules:       []*Schedule{},
		Hist:            s.HistSnapshot(),
		Procs:           []*Process{},
		Audits:          []*AuditRow{},
		RunnerStartedAt: s.startedAt.Format(time.RFC3339),
		ScriptsDir:      s.scripts,
		OutboxDir:       s.outbox,
	}
	if ws, err := s.store.ListWorkers(); err == nil {
		for _, w := range ws {
			if ss, err := s.store.ListBoundSessions(w.ID); err == nil {
				for _, x := range ss {
					x.Active = s.hub.ActiveTask(x.ID)
					if x.Active {
						w.Live = true
					}
				}
				w.Sessions = ss
			}
			ov.Workers = append(ov.Workers, w)
		}
	}
	if q, err := s.store.QueuePending(); err == nil {
		ov.Queue = q
	}
	if p, err := s.store.PendingBuffered(); err == nil {
		ov.Buf = p
		ov.BufCount = len(p)
	}
	if im, err := s.store.RecentMessages("imm", 20); err == nil {
		ov.Imm = im
	}
	if sc, err := s.store.ListSchedules(); err == nil {
		ov.Schedules = sc
	}
	if pr, err := s.store.ProcRecent(40); err == nil {
		ov.Procs = append(ov.Procs, pr...)
	}
	// live bound sessions appear as session processes
	for _, w := range ov.Workers {
		for _, sess := range w.Sessions {
			if sess.Active {
				ov.Procs = append(ov.Procs, &Process{
					ID: sess.ID, Kind: "session", Name: truncate(sess.Title, 60), Status: "run",
					ExitCode: -999, Ref: w.ID,
				})
			}
		}
	}
	if a, err := s.store.AuditRecent(120, "", ""); err == nil {
		ov.Audits = a
	}
	return ov
}

func (s *Service) HistSnapshot() map[string][]ExecRec {
	s.histMu.Lock()
	defer s.histMu.Unlock()
	out := make(map[string][]ExecRec, len(s.hist))
	for k, v := range s.hist {
		out[k] = append([]ExecRec(nil), v...)
	}
	return out
}

// --- registration hook (implements session.SuperRegistrar) ---

// CreateTaskSchedule registers a natural-language periodic task: the runner
// re-delivers `task` onto the message channel every `every` period (buffered
// by default — the router decides who executes when it arrives). No script,
// no code: the schedule is only the reminder; the work still goes to workers.
func (s *Service) CreateTaskSchedule(name, task, every, mode string) (*Schedule, error) {
	name = sanitizeTaskName(name)
	if name == "" {
		return nil, fmt.Errorf("name 必填（英文短名，如 repo-pull-check）")
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return nil, fmt.Errorf("task 必填：到点发给通道的一句话任务（给未来的你自己）")
	}
	everySec, err := parseEvery(every)
	if err != nil {
		return nil, err
	}
	if mode != "imm" {
		mode = "buf"
	}
	file := "task-" + name + "-" + util.RandomHex(3)
	sc := &Schedule{File: file, Kind: "task", EverySec: everySec, Mode: mode, Enabled: true, Note: task}
	if err := s.store.UpsertSchedule(sc); err != nil {
		return nil, err
	}
	s.audit("super", "schedule.task.create", file,
		fmt.Sprintf("每 %ds · %s 通道 · %s", everySec, mode, truncate(task, 120)), file)
	return sc, nil
}

// DeleteTaskSchedule removes a task-type schedule. Script-type schedules are
// owned by the scripts folder (a human deletes the file) — super may not.
func (s *Service) DeleteTaskSchedule(file string) error {
	sc, err := s.store.GetSchedule(file)
	if err != nil || sc == nil {
		return fmt.Errorf("计划不存在: %s", file)
	}
	if sc.Kind != "task" {
		return fmt.Errorf("%s 是脚本计划，由人类在 scripts 目录管理，你不能删", file)
	}
	if err := s.store.DeleteSchedule(file); err != nil {
		return err
	}
	s.audit("super", "schedule.task.delete", file, truncate(sc.Note, 120), file)
	return nil
}

// sanitizeTaskName keeps schedule keys filesystem-safe and predictable.
func sanitizeTaskName(name string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

var everyUnit = map[string]int{"s": 1, "m": 60, "h": 3600, "d": 86400}

// parseEvery accepts "90m" / "3h" / "2h30m" / "1d" / bare seconds and
// clamps into [60s, 30d]. Empty means the 1h default.
func parseEvery(v string) (int, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return 3600, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 60 {
			n = 60
		}
		if n > 30*86400 {
			return 0, fmt.Errorf("every 超过 30 天上限: %s", v)
		}
		return n, nil
	}
	total, seen, num := 0, false, ""
	flush := func() error {
		if num == "" {
			return nil
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			return fmt.Errorf("every 格式不对: %s（例 3h / 90m / 2h30m）", v)
		}
		total += n
		num = ""
		return nil
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
		case r == 's' || r == 'm' || r == 'h' || r == 'd':
			if num == "" {
				return 0, fmt.Errorf("every 格式不对: %s（单位前要带数字）", v)
			}
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, fmt.Errorf("every 格式不对: %s", v)
			}
			total += n * everyUnit[string(r)]
			seen = true
			num = ""
		case r == ' ' || r == ',':
			if err := flush(); err != nil {
				return 0, err
			}
		default:
			return 0, fmt.Errorf("every 格式不对: %s（只认 s/m/h/d）", v)
		}
	}
	if !seen {
		return 0, fmt.Errorf("every 格式不对: %s（例 3h / 90m / 2h30m）", v)
	}
	if total < 60 {
		total = 60
	}
	if total > 30*86400 {
		return 0, fmt.Errorf("every 超过 30 天上限: %s", v)
	}
	return total, nil
}

// RegisterTools adds the super_* orchestration tools to one session's
// registry. Called per task for agent_role=="super" sessions only.
func (s *Service) RegisterTools(reg *tools.ToolRegistry, sessionID string) {
	reg.Register(newWorkersTool(s))
	reg.Register(newSessionsTool(s))
	reg.Register(newQueueTool(s))
	reg.Register(newScheduleTool(s))
	reg.Register(newAuditTool(s))
}

// --- helpers ---

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
