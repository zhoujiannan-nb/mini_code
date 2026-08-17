package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

type Session struct {
	record            *SessionRecord
	store             *SessionStore
	client            *provider.ModelClient
	agentCfg          *agent.AgentConfig
	manager           *SessionManager
	onToolCallDecided func(string, string, string)
	onToolCallStart   func(string, string)
	onToolCallEnd     func(string, string, string)
	onAssistantReply  func(string, string)
}

func (s *Session) ID() string             { return s.record.SessionID }
func (s *Session) ParentID() string       { return s.record.ParentID }
func (s *Session) Title() string          { return s.record.Title }
func (s *Session) Status() string         { return s.record.Status }
func (s *Session) WorkDir() string        { return s.record.WorkDir }
func (s *Session) AgentRole() string      { return s.record.AgentRole }
func (s *Session) Record() *SessionRecord { return s.record }

func (s *Session) Prompt(ctx context.Context, goal string, maxTurns int) (string, error) {
	s.updateStatus("running")

	cfg := s.agentCfg
	if cfg == nil {
		var err error
		cfg, err = agent.GetAgentConfig(s.record.AgentRole)
		if err != nil {
			s.updateStatus("failed")
			return "", fmt.Errorf("get agent config: %w", err)
		}
	}

	pb := NewPromptBuilder(cfg, s.record.WorkDir)
	isRootSession := s.record.ParentID == ""
	if cfg.Mode == "primary" && s.manager != nil && isRootSession {
		pb.EnableTaskTool(s.manager, s.ID(), s.record.WorkDir)
	}

	systemPrompt := pb.BuildSystemPrompt(s.record.WorkDir, "")
	defs := pb.GetFilteredDefinitions(cfg)

	loop := NewAgentLoop(s.client, maxTurns, pb.Tools, defs, s.onToolCallDecided, s.onToolCallStart, s.onToolCallEnd, s.onAssistantReply)

	slog.Info("session prompt", "id", s.ID(), "goal", goal[:min(80, len(goal))])
	result, err := loop.Run(ctx, systemPrompt, goal, s.record.Messages)
	if err != nil {
		s.updateStatus("failed")
		return "", err
	}

	// Always save accumulated messages, even on interrupt.
	if len(result.Messages) > 0 {
		s.record.Messages = result.Messages
		s.store.Update(s.record)
	}

	if result.Interrupted {
		slog.Info("session interrupted", "id", s.ID(), "turns", result.Turns)
		return result.Content, nil
	}

	if result.Success {
		s.updateStatus("completed")
	} else {
		s.updateStatus("failed")
	}

	slog.Info("session done", "id", s.ID(), "turns", result.Turns, "success", result.Success, "error", result.Error)
	return result.Content, nil
}

func (s *Session) updateStatus(status string) {
	s.record.Status = status
	s.store.Update(s.record)
}

type SessionManager struct {
	client            *provider.ModelClient
	store             *SessionStore
	sessions          map[string]*Session
	mu                sync.RWMutex
	onToolCallDecided func(string, string, string)
	onToolCallStart   func(string, string)
	onToolCallEnd     func(string, string, string)
	onAssistantReply  func(string, string)
}

func NewSessionManager(client *provider.ModelClient, dbPath string) (*SessionManager, error) {
	store, err := NewSessionStore(dbPath)
	if err != nil {
		return nil, err
	}
	return &SessionManager{
		client:   client,
		store:    store,
		sessions: make(map[string]*Session),
	}, nil
}

// NewDefaultSessionManager creates a SessionManager using the default database path (~/.mini_code/agent.db)
func NewDefaultSessionManager(client *provider.ModelClient) (*SessionManager, error) {
	store, err := NewDefaultSessionStore()
	if err != nil {
		return nil, err
	}
	return &SessionManager{
		client:   client,
		store:    store,
		sessions: make(map[string]*Session),
	}, nil
}

// SetToolCallCallbacks sets the callbacks for tool call events
func (m *SessionManager) SetToolCallCallbacks(onDecided func(string, string, string), onStart func(string, string), onEnd func(string, string, string), onReply func(string, string)) {
	m.onToolCallDecided = onDecided
	m.onToolCallStart = onStart
	m.onToolCallEnd = onEnd
	m.onAssistantReply = onReply
}

func (m *SessionManager) CreateSession(title, agentRole, workDir string, parentID *string) (tools.SessionPrompter, error) {
	pid := ""
	if parentID != nil {
		pid = *parentID
	}
	record := NewSessionRecord(title, agentRole, workDir, pid)
	if err := m.store.Create(record); err != nil {
		return nil, err
	}

	cfg, _ := agent.GetAgentConfig(agentRole)
	session := &Session{
		record:            record,
		store:             m.store,
		client:            m.client,
		agentCfg:          cfg,
		manager:           m,
		onToolCallDecided: m.onToolCallDecided,
		onToolCallStart:   m.onToolCallStart,
		onToolCallEnd:     m.onToolCallEnd,
		onAssistantReply:  m.onAssistantReply,
	}

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

	cfg, _ := agent.GetAgentConfig(record.AgentRole)
	session := &Session{
		record:            record,
		store:             m.store,
		client:            m.client,
		agentCfg:          cfg,
		manager:           m,
		onToolCallDecided: m.onToolCallDecided,
		onToolCallStart:   m.onToolCallStart,
		onToolCallEnd:     m.onToolCallEnd,
		onAssistantReply:  m.onAssistantReply,
	}

	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()
	return session, nil
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
		cfg, _ := agent.GetAgentConfig(r.AgentRole)
		sessions = append(sessions, &Session{
			record:            r,
			store:             m.store,
			client:            m.client,
			agentCfg:          cfg,
			manager:           m,
			onToolCallDecided: m.onToolCallDecided,
			onToolCallStart:   m.onToolCallStart,
			onToolCallEnd:     m.onToolCallEnd,
			onAssistantReply:  m.onAssistantReply,
		})
	}
	return sessions, nil
}

func (m *SessionManager) RunTask(ctx context.Context, goal, title, agentRole, workDir string, maxTurns int) (string, error) {
	session, err := m.CreateSession(title, agentRole, workDir, nil)
	if err != nil {
		return "", err
	}
	return session.Prompt(ctx, goal, maxTurns)
}

// ContinueTask continues a conversation on an existing session
func (m *SessionManager) ContinueTask(ctx context.Context, sessionID string, goal string, maxTurns int) (string, error) {
	session, err := m.Get(sessionID)
	if err != nil || session == nil {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}
	return session.Prompt(ctx, goal, maxTurns)
}

func (m *SessionManager) Close() error {
	return m.store.Close()
}

// GetStore returns the session store for external access
func (m *SessionManager) GetStore() *SessionStore {
	return m.store
}
