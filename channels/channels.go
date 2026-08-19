// Package channels implements the unified message channel (message bus) of
// mini_code.
//
// All message sources (CLI, HTTP server, DingTalk, ...) produce inbound
// messages into the Hub. The Hub consumes them, resolves the owning session
// for each message — creating a new session when the message does not belong
// to any session — runs the agent loop, and produces outbound messages
// (assistant replies, tool events, errors, lifecycle notices) that are fanned
// out to every subscriber.
//
// Session routing is keyed on session_id: an inbound message carries either
// an explicit session_id (continue that conversation), a stable session_key
// (e.g. a DingTalk conversationId — the first message of a conversation
// creates and binds a new session), or neither (a new session is created).
//
// Every completed agent turn persists the conversation to the database (see
// session.AgentLoop), so an interrupted task can be resumed from the last
// persisted state after a restart.
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

// Message kinds flowing through the hub.
const (
	KindUser      = "user"             // inbound user message from a channel
	KindAssistant = "assistant"        // final assistant reply for a task
	KindToolStart = "tool_start"       // a tool call is starting
	KindToolEnd   = "tool_end"         // a tool call has finished
	KindStatus    = "status"           // lifecycle notice (see Status* constants)
	KindError     = "error"            // task failed
	KindReasoning = "reasoning_delta"  // live (cumulative) reasoning_content of the model
	KindContent   = "content_delta"    // live (cumulative) assistant text of the model
)

// Status payloads for Kind == KindStatus (carried in Status).
const (
	StatusSessionCreated = "session_created"
	StatusTaskDone       = "task_done"
	StatusInterrupted    = "interrupted"
)

// Channel names used across the project.
const (
	ChannelCLI      = "cli"
	ChannelWeb      = "web"
	ChannelDingTalk = "dingtalk"
)

// Message is the unified envelope flowing through the hub.
type Message struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Channel    string    `json:"channel"`
	TaskID     string    `json:"task_id,omitempty"` // on terminal messages: inbound message ID that triggered the task
	Status     string    `json:"status,omitempty"`  // on KindStatus messages: one of the Status* constants
	SessionID  string    `json:"session_id,omitempty"`
	SessionKey string    `json:"session_key,omitempty"` // stable external conversation key (e.g. DingTalk conversationId)
	Content    string    `json:"content"`
	Reasoning  string    `json:"reasoning,omitempty"` // on assistant messages: model reasoning_content; on KindReasoning: cumulative live reasoning
	ToolName   string    `json:"tool_name,omitempty"`
	ToolArgs   string    `json:"tool_args,omitempty"`
	ToolResult string    `json:"tool_result,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func (m *Message) ensureID() {
	if m.ID == "" {
		m.ID = util.RandomHex(12)
	}
}

// NewUserMessage builds an inbound user message for the given channel.
func NewUserMessage(channel, content string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindUser, Channel: channel, Content: content, CreatedAt: time.Now()}
}

// NewAssistantMessage builds the terminal assistant-reply message of a task.
func NewAssistantMessage(taskID, sessionID, channel, content string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindAssistant, TaskID: taskID, Channel: channel, SessionID: sessionID, Content: content, CreatedAt: time.Now()}
}

// NewReasoningDeltaMessage builds a live reasoning update. Content carries
// the cumulative reasoning_text produced so far in the current LLM call.
func NewReasoningDeltaMessage(sessionID, sessionKey, content string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindReasoning, SessionID: sessionID, SessionKey: sessionKey, Reasoning: content, CreatedAt: time.Now()}
}

// NewContentDeltaMessage builds a live text update. Content carries the
// cumulative assistant text produced so far in the current LLM call.
func NewContentDeltaMessage(sessionID, sessionKey, content string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindContent, SessionID: sessionID, SessionKey: sessionKey, Content: content, CreatedAt: time.Now()}
}

// NewErrorMessage builds the terminal error message of a task.
func NewErrorMessage(taskID, sessionID, sessionKey, channel, text string) Message {
	return Message{ID: util.RandomHex(12), Kind: KindError, TaskID: taskID, Channel: channel, SessionID: sessionID, SessionKey: sessionKey, Content: text, CreatedAt: time.Now()}
}

// NewStatusMessage builds a lifecycle notice for a task.
func NewStatusMessage(taskID, sessionID, channel, status, sessionKey string) Message {
	content := status
	if status == StatusSessionCreated {
		content = "new session created"
	}
	return Message{ID: util.RandomHex(12), Kind: KindStatus, TaskID: taskID, Channel: channel, Status: status, SessionID: sessionID, SessionKey: sessionKey, Content: content, CreatedAt: time.Now()}
}

// NewToolStartMessage builds a tool-call-started message.
func NewToolStartMessage(sessionID, sessionKey, name string, params map[string]interface{}) Message {
	var args string
	if b, err := json.Marshal(params); err == nil {
		args = string(b)
	}
	return Message{ID: util.RandomHex(12), Kind: KindToolStart, SessionID: sessionID, SessionKey: sessionKey, ToolName: name, ToolArgs: args, CreatedAt: time.Now()}
}

// NewToolEndMessage builds a tool-call-finished message.
func NewToolEndMessage(sessionID, sessionKey, name string, params map[string]interface{}, result string) Message {
	var args string
	if b, err := json.Marshal(params); err == nil {
		args = string(b)
	}
	return Message{ID: util.RandomHex(12), Kind: KindToolEnd, SessionID: sessionID, SessionKey: sessionKey, ToolName: name, ToolArgs: args, ToolResult: result, CreatedAt: time.Now()}
}

// Hub is the message channel: producers Submit inbound messages, the hub
// consumes them and drives the agent loop, and subscribers consume the
// produced outbound messages.
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
	locks  map[string]*sync.Mutex // per-session serialization

	taskMu sync.Mutex
	tasks  map[string]context.CancelFunc // sessionID -> cancel of the running task

	closed  bool
	closeMu sync.Mutex
}

// NewHub creates a hub bound to a session manager. agentRole and workDir are
// used for sessions that are created on demand; maxTurns is the per-task
// turn budget (0 means the loop default).
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

// WorkDirFor returns the working directory a session key is bound to: the
// existing session's workdir, or the shared workDir a new session would get.
// The directory is created when it does not exist yet, so callers (e.g. the
// DingTalk file downloader) can write into it before the session is created.
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

// Attach wires message production into the session manager: every session
// (including sub-agent sessions created by the task tool) reports its tool
// events into the hub, so they reach all channel subscribers.
func (h *Hub) Attach(mgr *session.SessionManager) {
	mgr.SetSessionCallbackFactory(func(sessionID string) *session.SessionCallbacks {
		// The channel key is resolved lazily per event: the factory runs at
		// session creation, before the hub binds the message's session key.
		var lastReasoning, lastContent string // dedupe: stream deltas only when the text grows
		return &session.SessionCallbacks{
			OnToolStart: func(name string, params map[string]interface{}) {
				h.emit(NewToolStartMessage(sessionID, h.channelKeyFor(sessionID), name, params))
			},
			OnToolEnd: func(name string, params map[string]interface{}, result string) {
				h.emit(NewToolEndMessage(sessionID, h.channelKeyFor(sessionID), name, params, result))
			},
			OnReply: func(content string, reasoning string) {
				// Stream reasoning and text live so the UI can show the
				// model thinking and typing while it works. The final
				// assistant message (with both fields) still terminates
				// the task.
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
}

// channelKeyFor resolves the external conversation key of a session, walking
// up to the parent for sub-agent sessions (which carry no key of their own).
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

// Run consumes inbound messages until ctx is done. It must be started once
// (e.g. in a goroutine). Inbound user messages are processed concurrently
// across sessions; messages of the same session are serialized.
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
				// Non-user inbound (e.g. status emitted by a channel
				// adapter): just fan it out.
				h.emit(m)
				continue
			}
			go h.process(m)
		}
	}
}

// Submit produces an inbound message on the hub (non-blocking; a full input
// queue drops the message with a warning).
func (h *Hub) Submit(m Message) {
	m.ensureID()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	select {
	case h.in <- m:
	default:
		slog.Error("hub input queue full, message dropped", "id", m.ID, "kind", m.Kind)
	}
}

// Subscribe registers (or returns) the outbound stream for a subscriber name.
// All subscribers receive all produced messages; consumers filter as needed.
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

// Unsubscribe removes a subscriber and closes its stream.
func (h *Hub) Unsubscribe(name string) {
	h.subsMu.Lock()
	ch, ok := h.subs[name]
	if ok {
		delete(h.subs, name)
		close(ch)
	}
	h.subsMu.Unlock()
}

// CancelTask cancels the currently running task of a session (if any).
func (h *Hub) CancelTask(sessionID string) {
	h.taskMu.Lock()
	cancel, ok := h.tasks[sessionID]
	h.taskMu.Unlock()
	if ok {
		slog.Info("cancelling task", "session", sessionID)
		cancel()
	}
}

// RunOnce submits an inbound message and blocks until the task it triggered
// produces a terminal message (assistant reply or error). It returns the
// final content, the resolved session ID, and any error. The task keeps
// running in the background if ctx is cancelled first.
func (h *Hub) RunOnce(ctx context.Context, m Message) (string, string, error) {
	m.ensureID()
	name := "await-" + m.ID
	out := h.Subscribe(name)
	defer h.Unsubscribe(name)
	h.Submit(m)

	sessionID := m.SessionID
	for {
		select {
		case <-ctx.Done():
			return "", sessionID, ctx.Err()
		case msg, ok := <-out:
			if !ok {
				return "", sessionID, fmt.Errorf("hub closed")
			}
			if msg.TaskID != m.ID {
				continue // belongs to another task
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

// emit fans a produced message out to every subscriber (non-blocking; slow
// consumers get dropped messages plus a warning).
func (h *Hub) emit(m Message) {
	m.ensureID()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	h.subsMu.RLock()
	defer h.subsMu.RUnlock()
	for name, ch := range h.subs {
		select {
		case ch <- m:
		default:
			slog.Warn("subscriber queue full, message dropped", "subscriber", name, "kind", m.Kind)
		}
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

// process resolves the owning session for one inbound user message, runs the
// agent loop, and emits the produced messages.
func (h *Hub) process(m Message) {
	taskID := m.ID
	sess, err := h.resolveSession(m)
	if err != nil {
		slog.Error("resolve session failed", "task", taskID, "channel", m.Channel, "err", err)
		h.emit(NewErrorMessage(taskID, m.SessionID, m.SessionKey, m.Channel, "failed to resolve session: "+err.Error()))
		return
	}

	lock := h.sessionLock(sess.ID())
	lock.Lock()
	defer lock.Unlock()

	// Make sure the session's workspace exists (it may have been removed
	// while the process was down).
	if wd := sess.WorkDir(); wd != "" {
		if err := os.MkdirAll(wd, 0o755); err != nil {
			slog.Warn("ensure session workspace failed", "dir", wd, "err", err)
		}
	}

	taskCtx, cancel := context.WithCancel(h.getRunCtx())
	// process() holds the per-session lock across Prompt, so register and
	// unregister bracket the only running task of this session.
	h.registerTask(sess.ID(), cancel)
	defer h.unregisterTask(sess.ID())

	slog.Info("task started", "task", taskID, "session", sess.ID(), "channel", m.Channel)
	content, reasoning, err := sess.Prompt(taskCtx, m.Content, h.maxTurns)
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

// resolveSession decides which session an inbound message belongs to:
//  1. an explicit SessionID that still exists;
//  2. an existing session bound to the message's SessionKey;
//  3. otherwise a brand-new session is created (and bound to the key).
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

	// The message does not belong to any session: create a new one.
	title := truncateRunes(strings.TrimSpace(m.Content), 40)
	if title == "" {
		title = "Chat"
	}
	created, err := h.mgr.CreateSessionRecord(title, h.agentRole, h.workDir, nil)
	if err != nil {
		return nil, err
	}
	if m.SessionKey != "" {
		if err := h.mgr.SetChannelKey(created.ID(), m.SessionKey); err != nil {
			slog.Warn("bind channel key failed", "session", created.ID(), "key", m.SessionKey, "err", err)
		}
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

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
