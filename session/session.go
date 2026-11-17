package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

const (
	ModeFull     = "full"
	ModeCautious = "cautious"
)

func NormalizeMode(m string) string {
	if strings.EqualFold(strings.TrimSpace(m), ModeCautious) {
		return ModeCautious
	}
	return ModeFull
}

type Session struct {
	record          *SessionRecord
	store           *SessionStore
	client          *provider.ModelClient
	agentCfg        *agent.AgentConfig
	manager         *SessionManager
	onToolCallStart func(name string, params map[string]interface{})
	onToolCallEnd   func(name string, params map[string]interface{}, result string)
	onToolOutput func(name string, chunk string)
	onToolArgs       func(name string, argsSoFar string)
	onAssistantReply func(content string, reasoning string)
	onContextUsage   func(tokens int)
	onUsage          func(promptTokens, cachedTokens, completionTokens int, cacheReported bool, speed float64)

	mu sync.Mutex

	pendingInjects []string

	writeAhead int

	injectWake chan struct{}

	goalStore   *tools.GoalStore
	sessionTodo *tools.TodoTool

	execTool *tools.ExecTool
	execDir  string

	obs *tools.Observation

	persistFailures int
	persistErr      string
	persistNoticed  bool

	msgRows     int
	msgLastJSON string
}

func lastMessageJSON(msgs []provider.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	b, err := json.Marshal(msgs[len(msgs)-1])
	if err != nil {
		return ""
	}
	return string(b)
}

const persistFailureNotice = "__MC_PERSIST_FAILED__ The conversation could not be saved to disk; see the app log for the underlying error. " +
	"Your work is still in memory and you can keep going, but if the app crashes now that work is lost. " +
	"Finish the current step, tell the user that persistence is failing, and stop unless they ask you to continue."

func persistRetry(fn func() error) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if attempt < 4 {
			time.Sleep(time.Duration(30*(1<<attempt)) * time.Millisecond)
		}
	}
	return err
}

func (s *Session) persistLocked() bool {
	if err := persistRetry(func() error { return s.store.Update(s.record) }); err != nil {
		s.notePersistFailure(err)
		return false
	}
	s.persistErr = ""
	s.msgRows = len(s.record.Messages)
	s.msgLastJSON = lastMessageJSON(s.record.Messages)
	return true
}

func (s *Session) notePersistFailure(err error) {
	s.persistFailures++
	s.persistErr = err.Error()
	slog.Error("session persist failed",
		"session", s.ID(), "failures", s.persistFailures, "err", err)
	if _, e := s.store.AppendEvent(s.ID(), EventPersistFailed, 0, map[string]any{
		"error": err.Error(), "failures": s.persistFailures,
	}); e != nil {
		slog.Error("persist-failure ledger row failed", "session", s.ID(), "err", e)
	}
	if !s.persistNoticed {
		s.persistNoticed = true
		s.pendingInjects = append(s.pendingInjects, persistFailureNotice)
		s.record.Messages = append(s.record.Messages, provider.Message{Role: "user", Content: persistFailureNotice})
	}
}

func (s *Session) persistTurnLocked() {
	msgs := s.record.Messages
	n := len(msgs)
	if n == s.msgRows && lastMessageJSON(msgs) == s.msgLastJSON {
		return
	}
	prefixUnchanged := s.msgRows == 0 || (n > s.msgRows && lastMessageJSON(msgs[:s.msgRows]) == s.msgLastJSON)
	if prefixUnchanged && n > s.msgRows {
		if err := persistRetry(func() error {
			return s.store.AppendMessages(s.ID(), msgs[s.msgRows:], n)
		}); err != nil {
			s.notePersistFailure(err)
			return
		}
		s.persistErr = ""
		s.msgRows = n
		s.msgLastJSON = lastMessageJSON(msgs)
		return
	}
	s.persistLocked()
}

func (s *Session) PersistHealth() (failures int, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistFailures, s.persistErr
}

func (s *Session) observation() *tools.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.obs == nil {
		s.obs = tools.NewObservation()
	}
	return s.obs
}

func (s *Session) ID() string             { return s.record.SessionID }
func (s *Session) ParentID() string       { return s.record.ParentID }
func (s *Session) Title() string          { return s.record.Title }
func (s *Session) Status() string         { return s.record.Status }
func (s *Session) WorkDir() string        { return s.record.WorkDir }
func (s *Session) AgentRole() string      { return s.record.AgentRole }
func (s *Session) ModelKey() string       { return s.record.ModelKey }
func (s *Session) ChannelKey() string     { return s.record.ChannelKey }
func (s *Session) Record() *SessionRecord { return s.record }

func (s *Session) ReasoningOverride() (thinking, effort string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thinking, _ = s.record.Metadata["thinking"].(string)
	effort, _ = s.record.Metadata["reasoning_effort"].(string)
	return
}

func (s *Session) SetReasoningOverride(thinking, effort string) {
	s.mu.Lock()
	if s.record.Metadata == nil {
		s.record.Metadata = map[string]interface{}{}
	}
	setOrClear := func(k, v string) {
		if v == "" {
			delete(s.record.Metadata, k)
		} else {
			s.record.Metadata[k] = v
		}
	}
	setOrClear("thinking", thinking)
	setOrClear("reasoning_effort", effort)
	s.persistLocked()
	s.mu.Unlock()
}

const lastUsageMetadataKey = "last_usage"

func (s *Session) recordLastUsage(prompt, cached, completion int, reported bool, speed float64) {
	if prompt <= 0 && completion <= 0 {
		return
	}
	s.mu.Lock()
	if s.record.Metadata == nil {
		s.record.Metadata = map[string]interface{}{}
	}
	s.record.Metadata[lastUsageMetadataKey] = map[string]interface{}{
		"prompt":     prompt,
		"cached":     cached,
		"completion": completion,
		"reported":   reported,
		"speed":      speed,
		"at":         time.Now().Format(time.RFC3339),
	}
	s.addUsageTotal(prompt, cached, completion)
	s.persistLocked()
	s.mu.Unlock()
}

const usageTotalMetadataKey = "usage_total"

func (s *Session) addUsageTotal(prompt, cached, completion int) {
	tot, _ := s.record.Metadata[usageTotalMetadataKey].(map[string]interface{})
	if tot == nil {
		tot = map[string]interface{}{}
	}
	as64 := func(v interface{}) int64 {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int64:
			return n
		case int:
			return int64(n)
		}
		return 0
	}
	tot["prompt"] = as64(tot["prompt"]) + int64(prompt)
	tot["cached"] = as64(tot["cached"]) + int64(cached)
	tot["completion"] = as64(tot["completion"]) + int64(completion)
	s.record.Metadata[usageTotalMetadataKey] = tot
}

func (s *Session) LastUsage() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, _ := s.record.Metadata[lastUsageMetadataKey].(map[string]interface{})
	return m
}

func (s *Session) QueueUserMessage(text string) {
	s.mu.Lock()
	s.pendingInjects = append(s.pendingInjects, text)
	s.record.Messages = append(s.record.Messages, provider.Message{Role: "user", Content: text})
	s.writeAhead++
	if s.injectWake == nil {
		s.injectWake = make(chan struct{}, 1)
	}
	select {
	case s.injectWake <- struct{}{}:
	default:
	}
	s.persistLocked()
	s.mu.Unlock()
}

func (s *Session) HasPendingInput() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pendingInjects) > 0
}

func (s *Session) WaitForInput(ctx context.Context, timeout time.Duration) bool {
	s.mu.Lock()
	if len(s.pendingInjects) > 0 {
		s.mu.Unlock()
		return true
	}
	if s.injectWake == nil {
		s.injectWake = make(chan struct{}, 1)
	}
	wake := s.injectWake
	s.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-wake:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (s *Session) DrainUserMessages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pendingInjects
	s.pendingInjects = nil
	return out
}

func metadataString(rec *SessionRecord, key string) string {
	if rec == nil || rec.Metadata == nil {
		return ""
	}
	s, _ := rec.Metadata[key].(string)
	return s
}

const (
	workerNameMetaKey = "worker_name"
	workerDirMetaKey  = "worker_dir"
)

func (s *Session) SetWorkerMembership(workerID, name, workDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Metadata == nil {
		s.record.Metadata = map[string]interface{}{}
	}
	s.record.WorkerID = workerID
	if workDir != "" {
		s.record.WorkDir = workDir
		s.record.Metadata[workerDirMetaKey] = workDir
	}
	if workerID == "" {
		delete(s.record.Metadata, workerNameMetaKey)
		delete(s.record.Metadata, workerDirMetaKey)
	} else {
		s.record.Metadata[workerNameMetaKey] = name
	}
	s.persistLocked()
}

func (s *Session) setWorkerNameLabel(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Metadata == nil {
		s.record.Metadata = map[string]interface{}{}
	}
	s.record.Metadata[workerNameMetaKey] = name
	s.persistLocked()
}

func (s *Session) WorkerSnapshot() (workerID, name, dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record.WorkerID,
		metadataString(s.record, workerNameMetaKey),
		metadataString(s.record, workerDirMetaKey)
}

func (s *Session) sharedExec(dir string) *tools.ExecTool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.execTool != nil && s.execDir == dir {
		return s.execTool
	}
	if old := s.execTool; old != nil {
		go old.Close()
	}
	s.execTool = tools.NewExecTool(120, dir)
	s.execDir = dir
	return s.execTool
}

func (s *Session) stopBackground(reason string) {
	s.mu.Lock()
	ex := s.execTool
	s.execTool = nil
	s.mu.Unlock()
	if ex == nil {
		return
	}
	if n := ex.Jobs().CancelAll(s.ID(), reason); n > 0 {
		slog.Info("background commands stopped", "session", s.ID(), "count", n, "reason", reason)
	}
	ex.Close()
}

func (s *Session) preparePromptTools() (*agent.AgentConfig, *PromptBuilder, *tools.ContextState, error) {
	cfg := s.agentCfg
	if cfg == nil {
		var err error
		cfg, err = agent.GetAgentConfig(s.record.AgentRole)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("get agent config: %w", err)
		}
	}

	pb := NewPromptBuilder(cfg, s.record.WorkDir)
	pb.Tools.Register(s.TodoState())
	ex := s.sharedExec(s.record.WorkDir)
	pb.Tools.Register(ex)
	if cd, ok := pb.Tools.Get("working_directory").(*tools.WorkingDirectoryTool); ok {
		cd.SetExecHost(ex)
		cd.SetDir(ex.WorkingDir())
	}
	isRootSession := s.record.ParentID == ""
	if s.record.AgentRole == "super" && s.manager != nil && s.manager.Super() != nil {
		pb.goalStore = s.GoalState()
		s.manager.Super().RegisterTools(pb.Tools, s.ID())
	} else if cfg.Mode == "primary" && s.manager != nil && isRootSession {
		pb.goalStore = s.GoalState()
		pb.EnableAgentTools(s.manager, s.ID())
	}
	if s.manager != nil {
		pb.EnableJobTools(s.manager, s.ID())
	}
	ctxState := tools.NewContextState()

	return cfg, pb, ctxState, nil
}

func (s *Session) Prompt(ctx context.Context, goal string, images []string, maxTurns int) (content string, reasoning string, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("agent task panicked, contained", "session", s.ID(), "panic", r)
			func() {
				defer func() { recover() }()
				s.updateStatus("failed")
				s.store.AppendEvent(s.ID(), EventTaskEnd, 0, map[string]any{"status": "failed", "error": fmt.Sprintf("panic: %v", r)})
			}()
			content = ""
			reasoning = ""
			err = fmt.Errorf("agent task crashed (panic: %v)", r)
		}
	}()
	s.updateStatus("running")
	s.mu.Lock()
	s.persistNoticed = false
	s.mu.Unlock()

	cfg, pb, ctxState, err := s.preparePromptTools()
	if err != nil {
		s.updateStatus("failed")
		return "", "", err
	}
	s.markGoalRoundOrigin(goal)
	s.mu.Lock()
	mdl := s.client
	s.mu.Unlock()
	if mdl != nil {
		pb.SetModelName(mdl.ModelName())
	}
	systemPrompt := pb.BuildSystemPrompt(s.record.WorkDir, "")
	defs := pb.GetFilteredDefinitions(cfg)

	turnHook := func(turn int, messages []provider.Message) {
		if len(messages) == 0 {
			return
		}
		s.mu.Lock()
		s.record.Messages = append([]provider.Message(nil), messages...)
		s.persistTurnLocked()
		s.mu.Unlock()
	}

	s.mu.Lock()
	client := s.client
	thinking, _ := s.record.Metadata["thinking"].(string)
	effort, _ := s.record.Metadata["reasoning_effort"].(string)
	s.mu.Unlock()
	if client != nil && (thinking != "" || effort != "") {
		client = client.WithReasoning(thinking, effort)
	}
	loop := NewAgentLoop(client, maxTurns, pb.Tools, defs, s.onToolCallStart, s.onToolCallEnd, s.onAssistantReply, turnHook)
	loop.SetEventSink(func(typ string, turn int, payload map[string]any) {
		if _, err := s.store.AppendEvent(s.ID(), typ, turn, payload); err != nil {
			slog.Error("session event append failed", "session", s.ID(), "type", typ, "err", err)
		}
	})
	loop.SetSpillDir(s.store.SpillDir(), s.ID())
	loop.SetJobOwner(s.ID())
	loop.SetObservation(s.observation(), s.record.WorkDir)
	loop.SetOnToolOutput(s.onToolOutput)
	loop.SetOnToolArgs(s.onToolArgs)
	if s.manager != nil {
		loop.compactionOn = s.manager.CompactionEnabled()
		loop.SetGate(s.gateFor(cfg))
	}
	if len(images) > 0 {
		loop.SetUserImages(images)
	}
	loop.SetInjections(s.DrainUserMessages)
	if s.manager != nil && s.manager.Workers() != nil {
		loop.SetWorkerNotice(s.WorkerNotice)
	}
	loop.SetContextState(ctxState)
	loop.SetOnContextUsage(s.onContextUsage)
	if s.onUsage != nil {
		loop.SetOnUsage(func(prompt, cached, completion int, reported bool, speed float64) {
			s.recordLastUsage(prompt, cached, completion, reported, speed)
			s.onUsage(prompt, cached, completion, reported, speed)
		})
	}

	s.maybeGenerateTitle([]provider.Message{{Role: "user", Content: goal}})
	s.mu.Lock()
	history := s.record.Messages
	if n := len(s.pendingInjects); n > 0 {
		m := 0
		for m < n && m < len(history) {
			hm := history[len(history)-1-m]
			if hm.Role != "user" || hm.GetText() != s.pendingInjects[n-1-m] {
				break
			}
			m++
		}
		if m > 0 {
			s.pendingInjects = s.pendingInjects[:n-m]
			s.writeAhead -= m
		}
	}
	if s.writeAhead > 0 && len(history) > 0 {
		if last := history[len(history)-1]; last.Role == "user" && last.GetText() == goal {
			history = history[:len(history)-1]
			s.writeAhead--
		}
	}
	s.mu.Unlock()
	startPayload := map[string]any{"goal": goal, "images": len(images)}
	if client != nil {
		startPayload["model"] = client.ModelName()
	}
	if _, err := s.store.AppendEvent(s.ID(), EventTaskStart, 0, startPayload); err != nil {
		slog.Error("session event append failed", "session", s.ID(), "type", EventTaskStart, "err", err)
	}
	result, err := loop.Run(ctx, systemPrompt, goal, history)
	if err != nil {
		s.updateStatus("failed")
		return "", "", err
	}

	if len(result.Messages) > 0 {
		s.mu.Lock()
		s.record.Messages = append([]provider.Message(nil), result.Messages...)
		s.writeAhead = 0
		s.persistLocked()
		s.mu.Unlock()
	}

	reasoning = LastAssistantReasoning(result.Messages)

	if result.Interrupted {
		s.updateStatus("interrupted")
		return result.Content, reasoning, nil
	}

	if result.Success {
		s.updateStatus("completed")
		if s.manager != nil {
			if texts := s.DrainUserMessages(); len(texts) > 0 {
				for _, t := range texts {
					s.manager.deliverIdle(s.ID(), t)
				}
			} else {
				s.driveGoalRound()
			}
		}
		return result.Content, reasoning, nil
	}

	s.updateStatus("failed")
	return result.Content, reasoning, fmt.Errorf("agent task failed after %d turns: %s", result.Turns, result.Error)
}

func (s *Session) updateStatus(status string) {
	s.record.Status = status
	s.persistLocked()
}

type SessionCallbacks struct {
	OnToolStart func(name string, params map[string]interface{})
	OnToolEnd   func(name string, params map[string]interface{}, result string)
	OnToolOutput func(name string, chunk string)
	OnToolArgs     func(name string, argsSoFar string)
	OnReply        func(content string, reasoning string)
	OnContextUsage func(tokens int)
	OnUsage        func(promptTokens, cachedTokens, completionTokens int, cacheReported bool, speed float64)
}

type SessionManager struct {
	models          *provider.ModelRegistry
	store           *SessionStore
	sessions        map[string]*Session
	mu              sync.RWMutex
	callbackFactory func(sessionID string) *SessionCallbacks

	compactionMu sync.RWMutex
	compactionOn bool

	titleMu    sync.RWMutex
	titleOn    bool
	titleModel string

	modeMu sync.RWMutex
	modes  map[string]string

	approverMu sync.RWMutex
	approver   Approver

	subagentsMu sync.RWMutex
	subagents   *SubagentService

	workersMu sync.RWMutex
	workers   *WorkerService

	superMu          sync.RWMutex
	superRegistrar   SuperRegistrar

	deliverMu   sync.RWMutex
	deliverHook func(sessionID, text string)

	activeCheckMu   sync.RWMutex
	activeTaskCheck func(sessionID string) bool
}

func (m *SessionManager) clientFor(modelKey string) *provider.ModelClient {
	return m.models.Client(modelKey)
}

func (m *SessionManager) SetModels(clients map[string]*provider.ModelClient, defaultName string) {
	m.models.Set(clients, defaultName)
}

func (m *SessionManager) Models() *provider.ModelRegistry {
	return m.models
}

func (m *SessionManager) ContextWindowFor(modelKey string) int {
	return m.models.ContextWindow(modelKey)
}

func (m *SessionManager) SetCompactionEnabled(on bool) {
	m.compactionMu.Lock()
	m.compactionOn = on
	m.compactionMu.Unlock()
}

func (m *SessionManager) CompactionEnabled() bool {
	m.compactionMu.RLock()
	defer m.compactionMu.RUnlock()
	return m.compactionOn
}

func NewRegistryWithDefault(client *provider.ModelClient) *provider.ModelRegistry {
	r := provider.NewModelRegistry()
	if client != nil {
		r.Set(map[string]*provider.ModelClient{"": client}, "")
	}
	return r
}

func NewSessionManager(client *provider.ModelClient, dbPath string) (*SessionManager, error) {
	store, err := NewSessionStore(dbPath)
	if err != nil {
		return nil, err
	}
	registry := NewRegistryWithDefault(client)
	return &SessionManager{
		models:       registry,
		store:        store,
		sessions:     make(map[string]*Session),
		compactionOn: true,
		modes:        make(map[string]string),
	}, nil
}

func NewDefaultSessionManager(client *provider.ModelClient) (*SessionManager, error) {
	store, err := NewDefaultSessionStore()
	if err != nil {
		return nil, err
	}
	registry := NewRegistryWithDefault(client)
	return &SessionManager{
		models:       registry,
		store:        store,
		sessions:     make(map[string]*Session),
		compactionOn: true,
		modes:        make(map[string]string),
	}, nil
}


func (m *SessionManager) ModeOf(sessionID string) string {
	m.modeMu.RLock()
	if mode, ok := m.modes[sessionID]; ok {
		m.modeMu.RUnlock()
		return mode
	}
	m.modeMu.RUnlock()

	m.mu.RLock()
	s, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		rec, err := m.store.Get(sessionID)
		if err != nil || rec == nil {
			return ModeFull
		}
		mode := NormalizeMode(rec.Mode)
		m.modeMu.Lock()
		m.modes[sessionID] = mode
		m.modeMu.Unlock()
		return mode
	}
	mode := NormalizeMode(s.record.Mode)
	m.modeMu.Lock()
	m.modes[sessionID] = mode
	m.modeMu.Unlock()
	return mode
}

func (m *SessionManager) SetMode(sessionID, mode string) error {
	mode = NormalizeMode(mode)
	if s, err := m.Get(sessionID); err != nil || s == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if err := m.store.SetMode(sessionID, mode); err != nil {
		return err
	}
	m.modeMu.Lock()
	m.modes[sessionID] = mode
	m.modeMu.Unlock()
	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		s.record.Mode = mode
	}
	m.mu.Unlock()
	slog.Info("session mode changed", "session", sessionID, "mode", mode)
	return nil
}

func (m *SessionManager) SetModel(sessionID, name string) error {
	name = strings.TrimSpace(name)
	if name != "" && !m.models.Has(name) {
		return fmt.Errorf("未知模型: %s", name)
	}
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}
	if err := m.store.SetModelKey(sessionID, name); err != nil {
		return err
	}
	s.mu.Lock()
	s.record.ModelKey = name
	s.client = m.clientFor(name)
	s.mu.Unlock()
	slog.Info("session model changed", "session", sessionID, "model", name)
	return nil
}

func (m *SessionManager) SetReasoning(sessionID, thinking, effort string) error {
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}
	s.SetReasoningOverride(thinking, effort)
	slog.Info("session reasoning changed", "session", sessionID, "thinking", thinking, "effort", effort)
	return nil
}


const MaxTitleLen = 60

func (m *SessionManager) RenameSession(sessionID, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("标题不能为空")
	}
	if len([]rune(title)) > MaxTitleLen {
		return fmt.Errorf("标题过长(最多 %d 字)", MaxTitleLen)
	}
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}
	if err := m.store.SetTitle(sessionID, title); err != nil {
		return err
	}
	if err := m.store.SetTitleSrc(sessionID, TitleSrcUser); err != nil {
		slog.Warn("title source not recorded", "session", sessionID, "err", err)
	}
	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		s.record.Title = title
		if s.record.Metadata == nil {
			s.record.Metadata = map[string]interface{}{}
		}
		s.record.Metadata[titleSrcMetaKey] = TitleSrcUser
	}
	m.mu.Unlock()
	slog.Info("session renamed", "session", sessionID, "title", title)
	return nil
}


func (m *SessionManager) SetApprover(a Approver) {
	m.approverMu.Lock()
	m.approver = a
	m.approverMu.Unlock()
}

func (m *SessionManager) approverRef() Approver {
	m.approverMu.RLock()
	defer m.approverMu.RUnlock()
	return m.approver
}


func (m *SessionManager) SetSubagents(s *SubagentService) {
	m.subagentsMu.Lock()
	m.subagents = s
	m.subagentsMu.Unlock()
}

func (m *SessionManager) Subagents() *SubagentService {
	m.subagentsMu.RLock()
	defer m.subagentsMu.RUnlock()
	return m.subagents
}


func (m *SessionManager) SetWorkers(w *WorkerService) {
	m.workersMu.Lock()
	m.workers = w
	m.workersMu.Unlock()
}

func (m *SessionManager) Workers() *WorkerService {
	m.workersMu.RLock()
	defer m.workersMu.RUnlock()
	return m.workers
}


type SuperRegistrar interface {
	RegisterTools(reg *tools.ToolRegistry, sessionID string)
}

func (m *SessionManager) SetSuperRegistrar(r SuperRegistrar) {
	m.superMu.Lock()
	m.superRegistrar = r
	m.superMu.Unlock()
}

func (m *SessionManager) Super() SuperRegistrar {
	m.superMu.RLock()
	defer m.superMu.RUnlock()
	return m.superRegistrar
}


func (m *SessionManager) SetDeliverHook(fn func(sessionID, text string)) {
	m.deliverMu.Lock()
	m.deliverHook = fn
	m.deliverMu.Unlock()
}

func (m *SessionManager) SetActiveTaskCheck(fn func(sessionID string) bool) {
	m.activeCheckMu.Lock()
	m.activeTaskCheck = fn
	m.activeCheckMu.Unlock()
}

func (m *SessionManager) TaskRunning(sessionID string) bool {
	m.activeCheckMu.RLock()
	fn := m.activeTaskCheck
	m.activeCheckMu.RUnlock()
	if fn != nil {
		return fn(sessionID)
	}
	if s, err := m.Get(sessionID); err == nil && s != nil {
		return s.Status() == "running"
	}
	return false
}

func (m *SessionManager) deliverIdle(sessionID, text string) {
	m.deliverMu.RLock()
	fn := m.deliverHook
	m.deliverMu.RUnlock()
	if fn == nil {
		slog.Warn("deliver: no hook wired; message dropped", "session", sessionID)
		return
	}
	fn(sessionID, text)
}

func (m *SessionManager) DeliverToSession(sessionID, text string) error {
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if m.TaskRunning(sessionID) {
		s.QueueUserMessage(text)
		slog.Info("user message queued for the next loop turn (session running)", "session", sessionID)
		return nil
	}
	m.deliverIdle(sessionID, text)
	return nil
}


func (m *SessionManager) InjectMessage(sessionID, text string) error {
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if m.TaskRunning(sessionID) {
		s.QueueUserMessage(text)
		slog.Info("settlement notice queued for the next loop turn (session running)", "session", sessionID)
		return nil
	}
	if m.hasDeliverHook() {
		m.deliverIdle(sessionID, text)
		slog.Info("settlement notice dispatched as a fresh task (session idle)", "session", sessionID)
		return nil
	}
	return m.appendMessage(sessionID, text)
}

func (m *SessionManager) hasDeliverHook() bool {
	m.deliverMu.RLock()
	defer m.deliverMu.RUnlock()
	return m.deliverHook != nil
}

func (m *SessionManager) appendMessage(sessionID, text string) error {
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	s.mu.Lock()
	s.record.Messages = append(s.record.Messages, provider.Message{Role: "user", Content: text})
	saved := s.persistLocked()
	s.mu.Unlock()
	if !saved {
		return fmt.Errorf("message not persisted for session %s", sessionID)
	}
	return nil
}

func (s *Session) SnapshotMessages() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provider.Message, len(s.record.Messages))
	copy(out, s.record.Messages)
	return out
}

func (m *SessionManager) CopyMessages(sessionID string) []provider.Message {
	m.mu.RLock()
	s, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return s.SnapshotMessages()
}

func (m *SessionManager) ListChildrenIDs(parentID string) []string {
	sums, err := m.store.ListChildren(parentID)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(sums))
	for _, s := range sums {
		ids = append(ids, s.SessionID)
	}
	return ids
}

func (m *SessionManager) SetSessionCallbackFactory(f func(sessionID string) *SessionCallbacks) {
	m.mu.Lock()
	m.callbackFactory = f
	m.mu.Unlock()
}

func (m *SessionManager) callbacksFor(sessionID string) *SessionCallbacks {
	m.mu.RLock()
	f := m.callbackFactory
	m.mu.RUnlock()
	if f != nil {
		if cbs := f(sessionID); cbs != nil {
			return cbs
		}
	}
	return nil
}

func (m *SessionManager) newSession(record *SessionRecord) *Session {
	cfg, _ := agent.GetAgentConfig(record.AgentRole)
	cbs := m.callbacksFor(record.SessionID)
	s := &Session{
		record:   record,
		store:    m.store,
		client:   m.clientFor(record.ModelKey),
		agentCfg: cfg,
		manager:  m,
	}
	s.msgRows = len(record.Messages)
	s.msgLastJSON = lastMessageJSON(record.Messages)
	if cbs != nil {
		s.onToolCallStart = cbs.OnToolStart
		s.onToolCallEnd = cbs.OnToolEnd
		s.onToolOutput = cbs.OnToolOutput
		s.onToolArgs = cbs.OnToolArgs
		s.onAssistantReply = cbs.OnReply
		s.onContextUsage = cbs.OnContextUsage
		s.onUsage = cbs.OnUsage
	}
	m.modeMu.Lock()
	if _, ok := m.modes[record.SessionID]; !ok {
		m.modes[record.SessionID] = NormalizeMode(record.Mode)
	}
	m.modeMu.Unlock()
	return s
}

func (m *SessionManager) CreateSessionRecord(title, agentRole, workDir string, parentID *string) (*Session, error) {
	pid := ""
	if parentID != nil {
		pid = *parentID
	}
	record := NewSessionRecord(title, agentRole, workDir, pid)
	if pid == "" {
		record.WorkerID = WorkerOtherID
	}
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

func (m *SessionManager) SetChannelKey(sessionID, key string) error {
	if err := m.store.SetChannelKey(sessionID, key); err != nil {
		return err
	}
	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		s.record.ChannelKey = key
	}
	m.mu.Unlock()
	return nil
}

func (m *SessionManager) Delete(sessionID string) error {
	m.mu.Lock()
	s := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	if s != nil {
		s.stopBackground("session deleted")
	}
	return m.store.Delete(sessionID)
}

func (m *SessionManager) Close() error {
	m.mu.Lock()
	live := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		live = append(live, s)
	}
	m.mu.Unlock()
	for _, s := range live {
		s.stopBackground("app closed")
	}
	return m.store.Close()
}

func (m *SessionManager) GetStore() *SessionStore {
	return m.store
}
