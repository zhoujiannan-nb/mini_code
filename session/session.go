package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
	"github.com/user/mini_code/util"
)

type Session struct {
	record           *SessionRecord
	store            *SessionStore
	client           *provider.ModelClient
	agentCfg         *agent.AgentConfig
	manager          *SessionManager
	onToolCallStart  func(name string, params map[string]interface{})
	onToolCallEnd    func(name string, params map[string]interface{}, result string)
	onAssistantReply func(content string, reasoning string)
}

func (s *Session) ID() string             { return s.record.SessionID }
func (s *Session) ParentID() string       { return s.record.ParentID }
func (s *Session) Title() string          { return s.record.Title }
func (s *Session) Status() string         { return s.record.Status }
func (s *Session) WorkDir() string        { return s.record.WorkDir }
func (s *Session) AgentRole() string      { return s.record.AgentRole }
func (s *Session) ChannelKey() string     { return s.record.ChannelKey }
func (s *Session) Record() *SessionRecord { return s.record }

// Prompt runs one agent task and returns the final reply text together with
// the reasoning_content the model produced for it (may be empty).
//
// Panic containment: any panic inside the task (LLM path, compaction, tool
// execution, persistence) is converted into a clean task failure instead of
// crashing the whole process — the server keeps serving other sessions.
func (s *Session) Prompt(ctx context.Context, goal string, maxTurns int) (content string, reasoning string, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("agent task panicked, contained", "session", s.ID(), "panic", r)
			func() {
				defer func() { recover() }() // never let the bookkeeping itself re-panic
				s.updateStatus("failed")
			}()
			content = ""
			reasoning = ""
			err = fmt.Errorf("agent task crashed (panic: %v)", r)
		}
	}()
	s.updateStatus("running")

	cfg := s.agentCfg
	if cfg == nil {
		var err error
		cfg, err = agent.GetAgentConfig(s.record.AgentRole)
		if err != nil {
			s.updateStatus("failed")
			return "", "", fmt.Errorf("get agent config: %w", err)
		}
	}

	pb := NewPromptBuilder(cfg, s.record.WorkDir)
	isRootSession := s.record.ParentID == ""
	if cfg.Mode == "primary" && s.manager != nil && isRootSession {
		pb.EnableTaskTool(s.manager, s.ID(), s.record.WorkDir)
	}

	systemPrompt := pb.BuildSystemPrompt(s.record.WorkDir, "")
	defs := pb.GetFilteredDefinitions(cfg)

	// Persist the accumulated conversation to the database after every
	// completed turn, so an interrupted task can be resumed from the last
	// good state (interrupt recovery).
	turnHook := func(turn int, messages []provider.Message) {
		if len(messages) == 0 {
			return
		}
		s.record.Messages = messages
		s.store.Update(s.record)
	}

	loop := NewAgentLoop(s.client, maxTurns, pb.Tools, defs, s.onToolCallStart, s.onToolCallEnd, s.onAssistantReply, turnHook)
	if s.manager != nil {
		loop.compactionOn = s.manager.CompactionEnabled()
	}

	slog.Info("session prompt", "id", s.ID(), "goal", goal[:min(80, len(goal))])
	result, err := loop.Run(ctx, systemPrompt, goal, s.record.Messages)
	if err != nil {
		s.updateStatus("failed")
		return "", "", err
	}

	// Always save accumulated messages, even on interrupt.
	if len(result.Messages) > 0 {
		s.record.Messages = result.Messages
		s.store.Update(s.record)
	}

	reasoning = LastAssistantReasoning(result.Messages)

	if result.Interrupted {
		// Mark the session so a later run can resume from the persisted
		// messages (interrupt recovery).
		s.updateStatus("interrupted")
		slog.Info("session interrupted", "id", s.ID(), "turns", result.Turns)
		return result.Content, reasoning, nil
	}

	if result.Success {
		s.updateStatus("completed")
		slog.Info("session done", "id", s.ID(), "turns", result.Turns, "success", true)
		return result.Content, reasoning, nil
	}

	// Propagate the failure reason to the caller so the channel can show it
	// instead of silently stopping.
	s.updateStatus("failed")
	slog.Error("session failed", "id", s.ID(), "turns", result.Turns, "error", result.Error)
	return result.Content, reasoning, fmt.Errorf("agent task failed after %d turns: %s", result.Turns, result.Error)
}

func (s *Session) updateStatus(status string) {
	s.record.Status = status
	s.store.Update(s.record)
}

// CompactSession manually compresses a session's persisted conversation with
// the LLM summarizer, regardless of the auto-compaction threshold. The
// session must not be running. It returns the token count before and after;
// after >= before means nothing was compactable (too short or no safe
// boundary). The compaction is applied to the next LLM call; an in-flight
// task (if one starts while compacting) keeps its own snapshot and the
// manual compaction is simply superseded by it — never data corruption.
func (m *SessionManager) CompactSession(ctx context.Context, sessionID string) (int, int, error) {
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return 0, 0, fmt.Errorf("会话不存在: %s", sessionID)
	}
	if s.Status() == "running" {
		return 0, 0, fmt.Errorf("会话正在运行中,请等待任务结束后再压缩")
	}
	msgs := s.Record().Messages
	if len(msgs) == 0 {
		return 0, 0, fmt.Errorf("会话还没有消息,无需压缩")
	}
	before := util.CountMessagesTokens(msgs)

	newMsgs, err := NewContextCompactor(m.clientRef()).Compact(ctx, msgs)
	if err != nil {
		return before, before, fmt.Errorf("压缩失败: %w", err)
	}
	after := util.CountMessagesTokens(newMsgs)
	if after >= before {
		return before, after, nil // nothing compactable
	}
	// Re-check: if a task started while the LLM summary was in flight, do
	// not clobber its working set.
	if s.Status() == "running" {
		return before, after, fmt.Errorf("压缩完成时会话已开始新任务,未应用(下次压缩生效)")
	}
	s.Record().Messages = newMsgs
	m.store.Update(s.Record())
	slog.Info("manual compaction applied", "session", sessionID, "before", before, "after", after)
	return before, after, nil
}

// SessionCallbacks carries the per-session event callbacks. A factory that
// produces one set of callbacks per session ID lets a message hub route each
// session's tool events into the bus, including sub-agent sessions.
type SessionCallbacks struct {
	OnToolStart func(name string, params map[string]interface{})
	OnToolEnd   func(name string, params map[string]interface{}, result string)
	OnReply     func(content string, reasoning string)
}

type SessionManager struct {
	client    *provider.ModelClient
	clientMu  sync.RWMutex
	store     *SessionStore
	sessions  map[string]*Session
	mu        sync.RWMutex
	onToolCallStart  func(name string, params map[string]interface{})
	onToolCallEnd    func(name string, params map[string]interface{}, result string)
	onAssistantReply func(content string, reasoning string)
	callbackFactory  func(sessionID string) *SessionCallbacks

	compactionMu sync.RWMutex
	compactionOn bool // default true (legacy behavior); hot-swappable via SetCompactionEnabled
}

// clientRef returns the current model client.
func (m *SessionManager) clientRef() *provider.ModelClient {
	m.clientMu.RLock()
	defer m.clientMu.RUnlock()
	return m.client
}

// SetClient hot-swaps the model client used by newly created sessions (and
// sub-agent sessions). Sessions already running keep using their existing
// client until they finish.
func (m *SessionManager) SetClient(c *provider.ModelClient) {
	m.clientMu.Lock()
	m.client = c
	m.clientMu.Unlock()
}

// SetCompactionEnabled hot-toggles automatic context compression. It applies
// to turns started after the call; in-flight turns keep their setting.
func (m *SessionManager) SetCompactionEnabled(on bool) {
	m.compactionMu.Lock()
	m.compactionOn = on
	m.compactionMu.Unlock()
}

// CompactionEnabled reports the current compaction toggle (default true).
func (m *SessionManager) CompactionEnabled() bool {
	m.compactionMu.RLock()
	defer m.compactionMu.RUnlock()
	return m.compactionOn
}

func NewSessionManager(client *provider.ModelClient, dbPath string) (*SessionManager, error) {
	store, err := NewSessionStore(dbPath)
	if err != nil {
		return nil, err
	}
	return &SessionManager{
		client:       client,
		store:        store,
		sessions:     make(map[string]*Session),
		compactionOn: true,
	}, nil
}

// NewDefaultSessionManager creates a SessionManager using the default database path (~/.mini_code/agent.db)
func NewDefaultSessionManager(client *provider.ModelClient) (*SessionManager, error) {
	store, err := NewDefaultSessionStore()
	if err != nil {
		return nil, err
	}
	return &SessionManager{
		client:       client,
		store:        store,
		sessions:     make(map[string]*Session),
		compactionOn: true,
	}, nil
}

// SetToolCallCallbacks sets global callbacks for tool call events (legacy,
// applies to every session).
func (m *SessionManager) SetToolCallCallbacks(onStart func(string, map[string]interface{}), onEnd func(string, map[string]interface{}, string), onReply func(string, string)) {
	m.onToolCallStart = onStart
	m.onToolCallEnd = onEnd
	m.onAssistantReply = onReply
}

// SetSessionCallbackFactory registers a per-session callback factory. When
// set, it takes precedence over the global callbacks: each session (including
// sub-agent sessions) gets its own callbacks resolved by session ID.
func (m *SessionManager) SetSessionCallbackFactory(f func(sessionID string) *SessionCallbacks) {
	m.mu.Lock()
	m.callbackFactory = f
	m.mu.Unlock()
}

// callbacksFor resolves the effective callbacks for a session ID.
func (m *SessionManager) callbacksFor(sessionID string) *SessionCallbacks {
	m.mu.RLock()
	f := m.callbackFactory
	legacyStart, legacyEnd, legacyReply := m.onToolCallStart, m.onToolCallEnd, m.onAssistantReply
	m.mu.RUnlock()

	if f != nil {
		if cbs := f(sessionID); cbs != nil {
			return cbs
		}
	}
	if legacyStart != nil || legacyEnd != nil || legacyReply != nil {
		return &SessionCallbacks{OnToolStart: legacyStart, OnToolEnd: legacyEnd, OnReply: legacyReply}
	}
	return nil
}

// newSession builds a Session with its event callbacks resolved.
func (m *SessionManager) newSession(record *SessionRecord) *Session {
	cfg, _ := agent.GetAgentConfig(record.AgentRole)
	cbs := m.callbacksFor(record.SessionID)
	s := &Session{
		record:   record,
		store:    m.store,
		client:   m.clientRef(),
		agentCfg: cfg,
		manager:  m,
	}
	if cbs != nil {
		s.onToolCallStart = cbs.OnToolStart
		s.onToolCallEnd = cbs.OnToolEnd
		s.onAssistantReply = cbs.OnReply
	}
	return s
}

func (m *SessionManager) CreateSession(title, agentRole, workDir string, parentID *string) (tools.SessionPrompter, error) {
	return m.CreateSessionRecord(title, agentRole, workDir, parentID)
}

// CreateSessionRecord creates a session and returns the concrete *Session
// (use this when the caller needs session-level APIs such as ID()).
func (m *SessionManager) CreateSessionRecord(title, agentRole, workDir string, parentID *string) (*Session, error) {
	pid := ""
	if parentID != nil {
		pid = *parentID
	}
	record := NewSessionRecord(title, agentRole, workDir, pid)
	if err := m.store.Create(record); err != nil {
		return nil, err
	}

	session := m.newSession(record)

	m.mu.Lock()
	m.sessions[record.SessionID] = session
	m.mu.Unlock()

	slog.Info("session created", "id", record.SessionID, "role", agentRole, "parent", pid)
	return session, nil
}

func (m *SessionManager) Get(sessionID string) (*Session, error) {
	m.mu.RLock()
	if s, ok := m.sessions[sessionID]; ok {
		m.mu.RUnlock()
		return s, nil
	}
	m.mu.RUnlock()

	record, err := m.store.Get(sessionID)
	if err != nil || record == nil {
		return nil, err
	}

	session := m.newSession(record)

	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()
	return session, nil
}

// GetByChannelKey returns the most recently updated session bound to the given
// channel key (e.g. a DingTalk conversation id). It returns nil when the key
// is not bound to any session.
func (m *SessionManager) GetByChannelKey(key string) (*Session, error) {
	if key == "" {
		return nil, nil
	}

	record, err := m.store.GetByChannelKey(key)
	if err != nil || record == nil {
		return nil, err
	}
	session := m.newSession(record)
	m.mu.Lock()
	m.sessions[record.SessionID] = session
	m.mu.Unlock()
	return session, nil
}

// SetChannelKey binds a stable external channel key (e.g. a DingTalk
// conversation id) to a session, so future messages carrying that key are
// routed to the same session.
func (m *SessionManager) SetChannelKey(sessionID, key string) error {
	if err := m.store.SetChannelKey(sessionID, key); err != nil {
		return err
	}
	// Keep the cached in-memory record in sync so that a later whole-row
	// persistence (store.Update) cannot clobber the binding with a stale
	// empty key.
	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		s.record.ChannelKey = key
	}
	m.mu.Unlock()
	return nil
}

func (m *SessionManager) Delete(sessionID string) error {
	m.mu.Lock()
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	return m.store.Delete(sessionID)
}

func (m *SessionManager) List(parentID, status string, limit, offset int) ([]*Session, error) {
	records, err := m.store.ListSessions(parentID, status, limit, offset)
	if err != nil {
		return nil, err
	}
	var sessions []*Session
	for _, r := range records {
		sessions = append(sessions, m.newSession(r))
	}
	return sessions, nil
}

func (m *SessionManager) Close() error {
	return m.store.Close()
}

// GetStore returns the session store for external access
func (m *SessionManager) GetStore() *SessionStore {
	return m.store
}
