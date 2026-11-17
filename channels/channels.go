package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/session"
	"github.com/user/mini_code/util"
)

const (
	KindUser      = "user"
	KindAssistant = "assistant"
	KindToolStart = "tool_start"
	KindToolEnd   = "tool_end"
	KindToolOutput = "tool_output"
	KindToolArgs         = "tool_args"
	KindContextUsage     = "context_usage"
	KindUsage            = "usage"
	KindStatus           = "status"
	KindError            = "error"
	KindReasoning        = "reasoning_delta"
	KindContent          = "content_delta"
	KindApprovalRequest  = "approval_request"
	KindApprovalResolved = "approval_resolved"
)

const (
	StatusSessionCreated = "session_created"
	StatusTaskStarted    = "task_started"
	StatusTaskDone       = "task_done"
	StatusInterrupted    = "interrupted"
)

const (
	ChannelCLI = "cli"
	ChannelWeb = "web"
)

type Message struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Channel    string `json:"channel"`
	TaskID     string `json:"task_id,omitempty"`
	Status     string `json:"status,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	SessionKey string `json:"session_key,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Model      string `json:"model,omitempty"`
	Thinking        string   `json:"thinking,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	WorkerID string `json:"worker_id,omitempty"`
	Content         string   `json:"content"`
	Images          []string `json:"images,omitempty"`
	Reasoning       string   `json:"reasoning,omitempty"`
	ToolName        string   `json:"tool_name,omitempty"`
	ToolArgs        string   `json:"tool_args,omitempty"`
	ToolResult      string   `json:"tool_result,omitempty"`
	Tokens          int      `json:"tokens,omitempty"`
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	CacheReported bool `json:"cache_reported,omitempty"`
	Speed     float64   `json:"speed,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (m *Message) ensureID() {
	if m.ID == "" {
		m.ID = util.RandomHex(12)
	}
}

func NewUserMessage(channel, content string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindUser, Channel: channel, Content: content, CreatedAt: time.Now()}
}

func NewAssistantMessage(taskID, sessionID, channel, content string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindAssistant, TaskID: taskID, Channel: channel, SessionID: sessionID, Content: content, CreatedAt: time.Now()}
}

func NewReasoningDeltaMessage(sessionID, sessionKey, content string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindReasoning, SessionID: sessionID, SessionKey: sessionKey, Reasoning: content, CreatedAt: time.Now()}
}

func NewContentDeltaMessage(sessionID, sessionKey, content string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindContent, SessionID: sessionID, SessionKey: sessionKey, Content: content, CreatedAt: time.Now()}
}

func NewErrorMessage(taskID, sessionID, sessionKey, channel, text string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindError, TaskID: taskID, Channel: channel, SessionID: sessionID, SessionKey: sessionKey, Content: text, CreatedAt: time.Now()}
}

func NewStatusMessage(taskID, sessionID, channel, status, sessionKey string) Message {
	content := status
	if status == StatusSessionCreated {
		content = "new session created"
	}
	return Message{ID: util.RandomHex(12), Kind: KindStatus, TaskID: taskID, Channel: channel, Status: status, SessionID: sessionID, SessionKey: sessionKey, Content: content, CreatedAt: time.Now()}
}

func NewToolStartMessage(sessionID, sessionKey, name string, params map[string]interface{}) Message {
	var args string
	if b, err := json.Marshal(params); err == nil {
		args = string(b)
	}
	return Message{ID: util.RandomHex(12), Kind: KindToolStart, SessionID: sessionID, SessionKey: sessionKey, ToolName: name, ToolArgs: args, CreatedAt: time.Now()}
}

func NewToolOutputMessage(sessionID, sessionKey, name, chunk string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindToolOutput, SessionID: sessionID, SessionKey: sessionKey, ToolName: name, Content: chunk, CreatedAt: time.Now()}
}

func NewToolArgsMessage(sessionID, sessionKey, name, argsSoFar string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindToolArgs, SessionID: sessionID, SessionKey: sessionKey, ToolName: name, Content: argsSoFar, CreatedAt: time.Now()}
}

func NewToolEndMessage(sessionID, sessionKey, name string, params map[string]interface{}, result string) Message {
	var args string
	if b, err := json.Marshal(params); err == nil {
		args = string(b)
	}
	return Message{ID: util.RandomHex(12), Kind: KindToolEnd, SessionID: sessionID, SessionKey: sessionKey, ToolName: name, ToolArgs: args, ToolResult: result, CreatedAt: time.Now()}
}

func NewContextUsageMessage(sessionID, sessionKey string, tokens int) Message {
	return Message{ID: util.RandomHex(12), Kind: KindContextUsage, SessionID: sessionID, SessionKey: sessionKey, Tokens: tokens, CreatedAt: time.Now()}
}

func NewUsageMessage(sessionID, sessionKey string, promptTokens, cachedTokens, completionTokens int, cacheReported bool, speed float64) Message {
	return Message{ID: util.RandomHex(12), Kind: KindUsage, SessionID: sessionID, SessionKey: sessionKey, PromptTokens: promptTokens, CachedTokens: cachedTokens, CompletionTokens: completionTokens, CacheReported: cacheReported, Speed: speed, CreatedAt: time.Now()}
}

type ApprovalRequestPayload struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	ParentID    string    `json:"parent_id,omitempty"`
	Title       string    `json:"title"`
	ParentTitle string    `json:"parent_title,omitempty"`
	ToolName    string    `json:"tool_name"`
	Args        string    `json:"args"`
	Deadline    time.Time `json:"deadline"`
}

func NewApprovalRequestMessage(p ApprovalRequestPayload) Message {
	b, _ := json.Marshal(p)
	return Message{ID: util.RandomHex(12), Kind: KindApprovalRequest, SessionID: p.SessionID, Content: string(b), CreatedAt: time.Now()}
}

type ApprovalResolvedPayload struct {
	ID       string `json:"id"`
	Approved bool   `json:"approved"`
	Reason   string `json:"reason"`
}

func NewApprovalResolvedMessage(p ApprovalResolvedPayload) Message {
	b, _ := json.Marshal(p)
	return Message{ID: util.RandomHex(12), Kind: KindApprovalResolved, Content: string(b), CreatedAt: time.Now()}
}

type Hub struct {
	mgr       *session.SessionManager
	agentRole string
	workDir   string
	maxTurns  int

	in chan Message

	runMu  sync.Mutex
	runCtx context.Context

	subsMu sync.RWMutex
	subs   map[string]chan Message

	lockMu sync.Mutex
	locks  map[string]*sync.Mutex

	taskMu sync.Mutex
	tasks  map[string]context.CancelFunc

	closed  bool
	closeMu sync.Mutex
}

func NewHub(mgr *session.SessionManager, agentRole, workDir string, maxTurns int) *Hub {
	if agentRole == "" {
		agentRole = "build"
	}
	return &Hub{
		mgr:       mgr,
		agentRole: agentRole,
		workDir:   workDir,
		maxTurns:  maxTurns,
		in:        make(chan Message, 256),
		runCtx:    context.Background(),
		subs:      make(map[string]chan Message),
		locks:     make(map[string]*sync.Mutex),
		tasks:     make(map[string]context.CancelFunc),
	}
}

func (h *Hub) WorkDirFor(sessionKey string) string {
	if sessionKey != "" {
		if s, err := h.mgr.GetByChannelKey(sessionKey); err == nil && s != nil {
			if wd := s.WorkDir(); wd != "" {
				return wd
			}
		}
	}
	return h.workDir
}

func (h *Hub) Attach(mgr *session.SessionManager) {
	mgr.SetSessionCallbackFactory(func(sessionID string) *session.SessionCallbacks {
		var lastReasoning, lastContent string
		return &session.SessionCallbacks{
			OnToolStart: func(name string, params map[string]interface{}) {
				h.emit(NewToolStartMessage(sessionID, h.channelKeyFor(sessionID), name, params))
			},
			OnToolEnd: func(name string, params map[string]interface{}, result string) {
				h.emit(NewToolEndMessage(sessionID, h.channelKeyFor(sessionID), name, params, result))
			},
			OnToolOutput: func(name string, chunk string) {
				h.emit(NewToolOutputMessage(sessionID, h.channelKeyFor(sessionID), name, chunk))
			},
			OnToolArgs: func(name string, argsSoFar string) {
				h.emit(NewToolArgsMessage(sessionID, h.channelKeyFor(sessionID), name, argsSoFar))
			},
			OnContextUsage: func(tokens int) {
				h.emit(NewContextUsageMessage(sessionID, h.channelKeyFor(sessionID), tokens))
			},
			OnUsage: func(promptTokens, cachedTokens, completionTokens int, cacheReported bool, speed float64) {
				if promptTokens > 0 {
					h.emit(NewUsageMessage(sessionID, h.channelKeyFor(sessionID), promptTokens, cachedTokens, completionTokens, cacheReported, speed))
				}
			},
			OnReply: func(content string, reasoning string) {
				if reasoning != "" && reasoning != lastReasoning {
					lastReasoning = reasoning
					h.emit(NewReasoningDeltaMessage(sessionID, h.channelKeyFor(sessionID), reasoning))
				}
				if content != "" && content != lastContent {
					lastContent = content
					h.emit(NewContentDeltaMessage(sessionID, h.channelKeyFor(sessionID), content))
				}
			},
		}
	})
	mgr.SetDeliverHook(func(sessionID, text string) {
		msg := NewUserMessage(ChannelWeb, text)
		msg.SessionID = sessionID
		if err := h.Submit(msg); err != nil {
			slog.Warn("deliver: hub submit failed", "session", sessionID, "err", err)
		}
	})
	mgr.SetActiveTaskCheck(func(sessionID string) bool { return h.ActiveTask(sessionID) })
}

func (h *Hub) channelKeyFor(sessionID string) string {
	seen := make(map[string]bool)
	cur := sessionID
	for cur != "" && !seen[cur] {
		seen[cur] = true
		s, err := h.mgr.Get(cur)
		if err != nil || s == nil {
			return ""
		}
		if key := s.ChannelKey(); key != "" {
			return key
		}
		cur = s.ParentID()
	}
	return ""
}

func (h *Hub) Run(ctx context.Context) {
	h.runMu.Lock()
	h.runCtx = ctx
	h.runMu.Unlock()

	for {
		select {
		case <-ctx.Done():
			h.closeSubs()
			return
		case m, ok := <-h.in:
			if !ok {
				return
			}
			m.ensureID()
			if m.CreatedAt.IsZero() {
				m.CreatedAt = time.Now()
			}
			if m.Kind != KindUser {
				h.emit(m)
				continue
			}
			go h.process(m)
		}
	}
}

const hubSubmitWait = 2 * time.Second

func (h *Hub) Submit(m Message) error {
	m.ensureID()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	select {
	case h.in <- m:
		return nil
	default:
	}
	select {
	case h.in <- m:
		return nil
	case <-time.After(hubSubmitWait):
		slog.Error("hub input queue full, message rejected", "id", m.ID, "kind", m.Kind, "waited", hubSubmitWait)
		return fmt.Errorf("the agent is busy and did not accept the message within %s; retry shortly", hubSubmitWait)
	}
}

func (h *Hub) Subscribe(name string) <-chan Message {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	if ch, ok := h.subs[name]; ok {
		return ch
	}
	ch := make(chan Message, 512)
	h.subs[name] = ch
	return ch
}

func (h *Hub) Unsubscribe(name string) {
	h.subsMu.Lock()
	ch, ok := h.subs[name]
	if ok {
		delete(h.subs, name)
		close(ch)
	}
	h.subsMu.Unlock()
}

func (h *Hub) ActiveTask(sessionID string) bool {
	h.taskMu.Lock()
	defer h.taskMu.Unlock()
	_, ok := h.tasks[sessionID]
	return ok
}

func (h *Hub) CancelTask(sessionID string) bool {
	h.taskMu.Lock()
	cancel, ok := h.tasks[sessionID]
	h.taskMu.Unlock()
	if ok {
		slog.Info("cancelling task", "session", sessionID)
		cancel()
	}
	for _, childID := range h.mgr.ListChildrenIDs(sessionID) {
		h.taskMu.Lock()
		childCancel, childOk := h.tasks[childID]
		h.taskMu.Unlock()
		if childOk {
			slog.Info("cancelling child task (parent canceled)", "session", childID, "parent", sessionID)
			childCancel()
		}
	}
	return ok
}

func (h *Hub) RegisterExternalTask(sessionID string, cancel context.CancelFunc) {
	h.taskMu.Lock()
	h.tasks[sessionID] = cancel
	h.taskMu.Unlock()
}

func (h *Hub) UnregisterExternalTask(sessionID string) {
	h.taskMu.Lock()
	delete(h.tasks, sessionID)
	h.taskMu.Unlock()
}

func (h *Hub) RunOnce(ctx context.Context, m Message) (string, string, error) {
	m.ensureID()
	name := "await-" + m.ID
	out := h.Subscribe(name)
	defer h.Unsubscribe(name)
	if err := h.Submit(m); err != nil {
		return "", m.SessionID, err
	}

	sessionID := m.SessionID
	for {
		select {
		case <-ctx.Done():
			return "", sessionID, ctx.Err()
		case msg, ok := <-out:
			if !ok {
				return "", sessionID, fmt.Errorf("stopped waiting: this call fell behind and its stream was dropped; the task is still running")
			}
			if msg.TaskID != m.ID {
				continue
			}
			if sessionID == "" && msg.SessionID != "" {
				sessionID = msg.SessionID
			}
			switch msg.Kind {
			case KindAssistant:
				return msg.Content, sessionID, nil
			case KindError:
				return "", sessionID, fmt.Errorf("%s", msg.Content)
			case KindStatus:
				if msg.Status == StatusSessionCreated && msg.SessionID != "" {
					sessionID = msg.SessionID
				}
			}
		}
	}
}

func (h *Hub) emit(m Message) {
	m.ensureID()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	var slow []string
	h.subsMu.RLock()
	for name, ch := range h.subs {
		select {
		case ch <- m:
		default:
			slow = append(slow, name)
		}
	}
	h.subsMu.RUnlock()
	for _, name := range slow {
		slog.Warn("subscriber queue full, dropping slow client", "subscriber", name, "kind", m.Kind)
		h.Unsubscribe(name)
	}
}

func (h *Hub) closeSubs() {
	h.closeMu.Lock()
	defer h.closeMu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	h.subsMu.Lock()
	for name, ch := range h.subs {
		delete(h.subs, name)
		close(ch)
	}
	h.subsMu.Unlock()
}

func (h *Hub) getRunCtx() context.Context {
	h.runMu.Lock()
	defer h.runMu.Unlock()
	if h.runCtx != nil {
		return h.runCtx
	}
	return context.Background()
}

func (h *Hub) process(m Message) {
	taskID := m.ID
	defer func() {
		if r := recover(); r != nil {
			slog.Error("task processing panicked, contained", "task", taskID, "session", m.SessionID, "panic", r)
			h.emit(NewErrorMessage(taskID, m.SessionID, m.SessionKey, m.Channel, fmt.Sprintf("task crashed (panic: %v)", r)))
		}
	}()
	sess, err := h.resolveSession(m)
	if err != nil {
		slog.Error("resolve session failed", "task", taskID, "channel", m.Channel, "err", err)
		h.emit(NewErrorMessage(taskID, m.SessionID, m.SessionKey, m.Channel, "failed to resolve session: "+err.Error()))
		return
	}

	lock := h.sessionLock(sess.ID())
	lock.Lock()
	defer lock.Unlock()

	if wd := sess.WorkDir(); wd != "" {
		if err := os.MkdirAll(wd, 0o755); err != nil {
			slog.Warn("ensure session workspace failed", "dir", wd, "err", err)
		}
	}

	taskCtx, cancel := context.WithCancel(h.getRunCtx())
	h.registerTask(sess.ID(), cancel)
	defer h.unregisterTask(sess.ID())
	h.emit(NewStatusMessage(taskID, sess.ID(), m.Channel, StatusTaskStarted, sess.ChannelKey()))

	slog.Info("task started", "task", taskID, "session", sess.ID(), "channel", m.Channel)
	content, reasoning, err := sess.Prompt(taskCtx, m.Content, m.Images, h.maxTurns)
	if err != nil {
		slog.Error("task failed", "task", taskID, "session", sess.ID(), "err", err)
		h.emit(NewErrorMessage(taskID, sess.ID(), sess.ChannelKey(), m.Channel, err.Error()))
		return
	}

	if taskCtx.Err() != nil {
		slog.Info("task interrupted", "task", taskID, "session", sess.ID())
		am := NewAssistantMessage(taskID, sess.ID(), m.Channel, content)
		am.SessionKey = sess.ChannelKey()
		am.Reasoning = reasoning
		h.emit(am)
		h.emit(NewStatusMessage(taskID, sess.ID(), m.Channel, StatusInterrupted, sess.ChannelKey()))
		return
	}

	slog.Info("task finished", "task", taskID, "session", sess.ID())
	am := NewAssistantMessage(taskID, sess.ID(), m.Channel, content)
	am.SessionKey = sess.ChannelKey()
	am.Reasoning = reasoning
	h.emit(am)
	h.emit(NewStatusMessage(taskID, sess.ID(), m.Channel, StatusTaskDone, sess.ChannelKey()))
}

func (h *Hub) resolveSession(m Message) (*session.Session, error) {
	if m.SessionID != "" {
		if s, err := h.mgr.Get(m.SessionID); err == nil && s != nil {
			return s, nil
		}
		slog.Warn("requested session not found, re-resolving", "session", m.SessionID, "task", m.ID)
	}
	if m.SessionKey != "" {
		if s, err := h.mgr.GetByChannelKey(m.SessionKey); err == nil && s != nil {
			return s, nil
		}
	}

	title := util.TruncateRunes(strings.TrimSpace(m.Content), session.FallbackTitleRunes)
	if title == "" {
		title = "Chat"
	}
	wid := m.WorkerID
	if wid == "" {
		wid = session.WorkerOtherID
	}
	birthDir := h.workDir
	if w := h.mgr.Workers(); w != nil {
		if rec, err := w.Get(wid); err == nil && rec != nil && rec.WorkDir != "" {
			birthDir = rec.WorkDir
		}
	}
	created, err := h.mgr.CreateSessionRecord(title, h.agentRole, birthDir, nil)
	if err != nil {
		return nil, err
	}
	if m.Mode != "" {
		if err := h.mgr.SetMode(created.ID(), m.Mode); err != nil {
			slog.Warn("set new session mode failed", "session", created.ID(), "mode", m.Mode, "err", err)
		}
	}
	if m.Model != "" {
		if err := h.mgr.SetModel(created.ID(), m.Model); err != nil {
			slog.Warn("set new session model failed", "session", created.ID(), "model", m.Model, "err", err)
		}
	}
	if m.Thinking != "" || m.ReasoningEffort != "" {
		created.SetReasoningOverride(m.Thinking, m.ReasoningEffort)
	}
	if m.SessionKey != "" {
		if err := h.mgr.SetChannelKey(created.ID(), m.SessionKey); err != nil {
			slog.Warn("bind channel key failed", "session", created.ID(), "key", m.SessionKey, "err", err)
		}
	}
	if err := h.mgr.MoveToWorker(created.ID(), wid); err != nil {
		slog.Warn("attach new session to worker failed", "session", created.ID(), "worker", wid, "err", err)
	}
	h.emit(NewStatusMessage(m.ID, created.ID(), m.Channel, StatusSessionCreated, m.SessionKey))
	return created, nil
}

func (h *Hub) sessionLock(sessionID string) *sync.Mutex {
	h.lockMu.Lock()
	defer h.lockMu.Unlock()
	l, ok := h.locks[sessionID]
	if !ok {
		l = &sync.Mutex{}
		h.locks[sessionID] = l
	}
	return l
}

func (h *Hub) registerTask(sessionID string, cancel context.CancelFunc) {
	h.taskMu.Lock()
	h.tasks[sessionID] = cancel
	h.taskMu.Unlock()
}

func (h *Hub) unregisterTask(sessionID string) {
	h.taskMu.Lock()
	delete(h.tasks, sessionID)
	h.taskMu.Unlock()
}

