package session

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
	"github.com/user/mini_code/util"
)

// Permission modes. The mode is a per-session attribute (not a per-agent
// one): the same role behaves differently depending on the session's mode.
// Sub-agent sessions snapshot their parent's mode at dispatch time.
const (
	ModeFull     = "full"     // all permitted operations run directly
	ModeCautious = "cautious" // destructive operations need UI approval
)

// NormalizeMode maps any input to a valid mode; unknown/empty values fall
// back to full (legacy sessions have no mode column value).
func NormalizeMode(m string) string {
	if strings.EqualFold(strings.TrimSpace(m), ModeCautious) {
		return ModeCautious
	}
	return ModeFull
}

type Session struct {
	record           *SessionRecord
	store            *SessionStore
	client           *provider.ModelClient
	agentCfg         *agent.AgentConfig
	manager          *SessionManager
	onToolCallStart  func(name string, params map[string]interface{})
	onToolCallEnd    func(name string, params map[string]interface{}, result string)
	onAssistantReply func(content string, reasoning string)
	onContextUsage   func(tokens int)
	onUsage          func(promptTokens, cachedTokens, completionTokens int, cacheReported bool, speed float64)

	// mu guards record.Messages against concurrent writers (the loop's
	// per-turn persistence, settlement-notice appends, manual compaction)
	// and readers (web API, fork snapshots). The store.Update call is kept
	// inside the critical section because it marshals the messages.
	mu sync.Mutex

	// pendingInjects holds user messages queued while a task is running
	// (e.g. group-chat messages for a busy member); the agent loop drains
	// them at the start of each turn (AgentLoop.SetInjections). Guarded by mu.
	pendingInjects []string
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

// ContextRemindersEnabled reports whether the context-usage notices
// (85%/90% window usage reminders) are on for this session. An absent
// metadata key keeps the default: enabled.
func (s *Session) ContextRemindersEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.record.Metadata["context_reminders"]
	if !ok {
		return true
	}
	b, _ := v.(bool)
	return b
}

// SetContextRemindersEnabled toggles the experimental context-usage notices for
// this session and persists the choice.
func (s *Session) SetContextRemindersEnabled(on bool) {
	s.mu.Lock()
	s.record.Metadata["context_reminders"] = on
	s.store.Update(s.record)
	s.mu.Unlock()
}

// ReasoningOverride returns the session's per-session thinking depth override
// (empty = use the model's configured default). It backs the composer's
// thinking control: the same model can think at different depths per
// conversation without editing the model config.
func (s *Session) ReasoningOverride() (thinking, effort string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thinking, _ = s.record.Metadata["thinking"].(string)
	effort, _ = s.record.Metadata["reasoning_effort"].(string)
	return
}

// SetReasoningOverride sets the per-session thinking depth and persists it.
// Empty values clear that part of the override (fall back to the model's
// configured default). It applies from the session's next task on.
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
	s.store.Update(s.record)
	s.mu.Unlock()
}

// lastUsageMetadataKey holds the session's most recent per-call provider
// usage report (cache-hit + output speed) in the session metadata, so the
// UI composer stats survive session switches, page refreshes and restarts.
const lastUsageMetadataKey = "last_usage"

// recordLastUsage persists the last per-call provider usage into the session
// metadata (whole-row update under the session lock, same pattern as
// SetContextRemindersEnabled). It is called from the agent loop after each
// successful LLM call, so the stored value is always the conversation's
// "last one" — exactly what the UI shows after the task settles.
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
		"speed":      speed, // tokens/second, average of the last LLM call; 0 = not reported
		"at":         time.Now().Format(time.RFC3339),
	}
	s.store.Update(s.record)
	s.mu.Unlock()
}

// LastUsage returns the persisted last-call usage report (see
// recordLastUsage); nil when the session has no LLM call recorded yet.
func (s *Session) LastUsage() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, _ := s.record.Metadata[lastUsageMetadataKey].(map[string]interface{})
	return m
}

// archivedMetadataKey marks a session as archived: hidden from the main
// conversation list (rendered in the collapsed "已归档" sidebar section
// instead). The flag is metadata-only and invisible to the agent loop, so
// archiving never changes the prompt prefix (KV-prefix-cache safe).
const archivedMetadataKey = "archived"

// SetArchived toggles the session's archived flag. It is valid while a task
// is running: a targeted metadata write under the session lock that neither
// touches the conversation nor the prompt prefix.
func (s *Session) SetArchived(on bool) {
	s.mu.Lock()
	if s.record.Metadata == nil {
		s.record.Metadata = map[string]interface{}{}
	}
	if on {
		s.record.Metadata[archivedMetadataKey] = true
	} else {
		delete(s.record.Metadata, archivedMetadataKey)
	}
	s.store.Update(s.record)
	s.mu.Unlock()
}

// Archived reports whether the session is archived (hidden from the main
// conversation list).
func (s *Session) Archived() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := s.record.Metadata[archivedMetadataKey].(bool)
	return b
}

// QueueUserMessage appends a user message to be injected into the running
// agent loop at its next turn (e.g. a group-chat message arriving while the
// session is busy). For an idle session use SessionManager.DeliverToSession,
// which starts a task instead.
//
// The message is ALSO persisted immediately (write-ahead): a web refresh
// re-renders only persisted history (SnapshotMessages), so without this a
// queued message would vanish from the view until the next turn drained it.
// No duplication: the loop's next persist overwrites the record with its own
// conversation (task-start snapshot + drained injections, the message once);
// the settle-drain path likewise appends it once via deliverIdle after the
// final save.
func (s *Session) QueueUserMessage(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingInjects = append(s.pendingInjects, text)
	s.record.Messages = append(s.record.Messages, provider.Message{Role: "user", Content: text})
	s.store.Update(s.record)
}

// DrainUserMessages returns and clears all queued user messages (called by
// the agent loop at the start of each turn, and once more when a task
// settles).
func (s *Session) DrainUserMessages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pendingInjects
	s.pendingInjects = nil
	return out
}

// SetGroupMembership records (or clears, when groupID == "") the group this
// session belongs to in its metadata. The group_chat tool registration and
// the shared group background are derived from that metadata on each task.
// The session's parent_id is intentionally left untouched — group
// membership is metadata, not the session tree.
func (s *Session) SetGroupMembership(groupID, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Metadata == nil {
		s.record.Metadata = map[string]interface{}{}
	}
	if groupID == "" {
		delete(s.record.Metadata, "group_id")
		delete(s.record.Metadata, "group_name")
	} else {
		s.record.Metadata["group_id"] = groupID
		s.record.Metadata["group_name"] = name
	}
	s.store.Update(s.record)
}

// preparePromptTools assembles the agent config, the prompt builder (tool set
// and system-prompt inputs) and the per-task context state.
//
// It is deliberately shared by Prompt (a live agent turn) and the manual
// compaction path: the tool schemas are rendered into the prompt PREFIX, so a
// compaction request must carry exactly the same schemas and system prompt as
// the session's normal turns. Any difference makes the request stop being a
// prefix of the previous one, the provider recomputes the whole conversation,
// and the user pays for it — hence one single source of truth.
func (s *Session) preparePromptTools() (*agent.AgentConfig, *PromptBuilder, *tools.ContextState, string, error) {
	cfg := s.agentCfg
	if cfg == nil {
		var err error
		cfg, err = agent.GetAgentConfig(s.record.AgentRole)
		if err != nil {
			return nil, nil, nil, "", fmt.Errorf("get agent config: %w", err)
		}
	}

	pb := NewPromptBuilder(cfg, s.record.WorkDir)
	isRootSession := s.record.ParentID == ""
	if cfg.Mode == "primary" && s.manager != nil && isRootSession {
		pb.EnableTaskTool(s.manager, s.ID(), s.record.WorkDir)
	}
	// Context-management tools (context_usage / compact_context), wired to a
	// per-task context state the agent loop updates each turn.
	ctxState := tools.NewContextState()
	pb.EnableContextTools(ctxState)

	// Group member: register the group_chat tool and append the shared
	// group background to the system prompt. Non-group sessions are
	// completely unchanged. The background is stable per group (it changes
	// only when the user edits it), so the per-session prompt prefix stays
	// cache-friendly.
	var groupBackground string
	if s.manager != nil {
		if gsvc := s.manager.Groups(); gsvc != nil {
			s.mu.Lock()
			gid, _ := s.record.Metadata["group_id"].(string)
			s.mu.Unlock()
			if gid != "" {
				pb.EnableGroupTool(s.ID(), gsvc.ToolAPIFor(s.ID()))
				if rec, err := gsvc.Get(gid); err == nil && rec != nil {
					groupBackground = rec.Background
				}
			}
		}
	}
	return cfg, pb, ctxState, groupBackground, nil
}

// Prompt runs one agent task and returns the final reply text together with
// the reasoning_content the model produced for it (may be empty).
//
// images optionally carries image data URLs (data:image/...;base64,...) that
// are attached to the initial user message as multimodal content, so the
// model sees them on the first turn (e.g. screenshots pasted in the web UI).
//
// Panic containment: any panic inside the task (LLM path, compaction, tool
// execution, persistence) is converted into a clean task failure instead of
// crashing the whole process — the server keeps serving other sessions.
func (s *Session) Prompt(ctx context.Context, goal string, images []string, maxTurns int) (content string, reasoning string, err error) {
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

	cfg, pb, ctxState, groupBackground, err := s.preparePromptTools()
	if err != nil {
		s.updateStatus("failed")
		return "", "", err
	}
	systemPrompt := pb.BuildSystemPrompt(s.record.WorkDir, groupBackground)
	defs := pb.GetFilteredDefinitions(cfg)

	// Persist the accumulated conversation to the database after every
	// completed turn, so an interrupted task can be resumed from the last
	// good state (interrupt recovery).
	//
	// The slice is COPIED before storing: the loop keeps appending to its
	// working slice after this call returns, without holding s.mu. Aliasing
	// it into the record would let the loop's next append race with
	// QueueUserMessage's append (both writing the same backing slot) and
	// with the json.Marshal inside store.Update — torn DB rows, clobbered
	// queued messages, and a restored history that no longer matches what
	// was actually requested (cold prefix cache).
	turnHook := func(turn int, messages []provider.Message) {
		if len(messages) == 0 {
			return
		}
		s.mu.Lock()
		s.record.Messages = append([]provider.Message(nil), messages...)
		s.store.Update(s.record)
		s.mu.Unlock()
	}

	// Snapshot the session's client under the session lock: a concurrent
	// SetModel may swap it, and this task must run to completion on the
	// client it started with. Apply the per-session thinking override (the
	// composer's control) on top of the model's configured default.
	s.mu.Lock()
	client := s.client
	thinking, _ := s.record.Metadata["thinking"].(string)
	effort, _ := s.record.Metadata["reasoning_effort"].(string)
	s.mu.Unlock()
	if client != nil && (thinking != "" || effort != "") {
		client = client.WithReasoning(thinking, effort)
	}
	loop := NewAgentLoop(client, maxTurns, pb.Tools, defs, s.onToolCallStart, s.onToolCallEnd, s.onAssistantReply, turnHook)
	if s.manager != nil {
		loop.compactionOn = s.manager.CompactionEnabled()
		// Runtime permission gate: role permissions + cautious-mode
		// approval. Applied to every tool call, including in sub-agent
		// sessions (which snapshot the parent's mode at dispatch time).
		loop.SetGate(s.gateFor(cfg))
	}
	if len(images) > 0 {
		loop.SetUserImages(images)
	}
	// Mid-task user messages (group-chat messages for a running session) are
	// drained from the session at the start of every turn.
	loop.SetInjections(s.DrainUserMessages)
	// Wire the shared context state (context tools) and the experimental
	// context-usage notices toggle (session-level, default on).
	loop.SetContextState(ctxState)
	loop.SetContextRemindersOn(s.ContextRemindersEnabled())
	loop.SetOnContextUsage(s.onContextUsage)
	// Wrap the usage callback: first persist the last per-call usage into the
	// session (DB) so the composer stats restore after a stop/refresh, then
	// forward it to the channel for the live UI update.
	if s.onUsage != nil {
		loop.SetOnUsage(func(prompt, cached, completion int, reported bool, speed float64) {
			s.recordLastUsage(prompt, cached, completion, reported, speed)
			s.onUsage(prompt, cached, completion, reported, speed)
		})
	}

	slog.Info("session prompt", "id", s.ID(), "goal", goal[:min(80, len(goal))])
	s.mu.Lock()
	history := s.record.Messages
	s.mu.Unlock()
	result, err := loop.Run(ctx, systemPrompt, goal, history)
	if err != nil {
		s.updateStatus("failed")
		return "", "", err
	}

	// Always save accumulated messages, even on interrupt. Copy for the
	// same reason as the turn hook above: the record must never alias a
	// slice owned by (a possibly still-running) loop.
	if len(result.Messages) > 0 {
		s.mu.Lock()
		s.record.Messages = append([]provider.Message(nil), result.Messages...)
		s.store.Update(s.record)
		s.mu.Unlock()
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
		// Messages queued between the final turn's drain and task completion
		// would otherwise wait for the next manual task; dispatch them now as
		// a fresh task. (Not done after an interrupt: the user may have
		// cancelled the whole line of work.)
		if s.manager != nil {
			if texts := s.DrainUserMessages(); len(texts) > 0 {
				for _, t := range texts {
					s.manager.deliverIdle(s.ID(), t)
				}
			}
		}
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
	if m.TaskRunning(sessionID) {
		return 0, 0, fmt.Errorf("会话正在运行中,请等待任务结束后再压缩")
	}
	s.mu.Lock()
	msgs := s.Record().Messages
	s.mu.Unlock()
	if len(msgs) == 0 {
		return 0, 0, fmt.Errorf("会话还没有消息,无需压缩")
	}
	before := util.CountMessagesTokens(msgs)

	// Compact with the session's own model, so the summary is produced by
	// the same model the conversation runs on. The per-session thinking
	// override is applied here too (as in Prompt): it changes the rendered
	// template, so omitting it would put the request on a different prefix.
	s.mu.Lock()
	client := s.client
	thinking, _ := s.record.Metadata["thinking"].(string)
	effort, _ := s.record.Metadata["reasoning_effort"].(string)
	s.mu.Unlock()
	if client == nil {
		return before, before, fmt.Errorf("会话未配置模型,无法压缩")
	}
	if thinking != "" || effort != "" {
		client = client.WithReasoning(thinking, effort)
	}
	// The tool schemas must match a normal turn exactly — they are part of the
	// cached prompt prefix (see preparePromptTools).
	cfg, pb, _, _, err := s.preparePromptTools()
	if err != nil {
		return before, before, fmt.Errorf("准备压缩上下文失败: %w", err)
	}
	defs := pb.GetFilteredDefinitions(cfg)
	newMsgs, err := NewContextCompactor(client).SummarizeAndRebuild(ctx, msgs, defs, "")
	if err != nil {
		return before, before, fmt.Errorf("压缩失败: %w", err)
	}
	after := util.CountMessagesTokens(newMsgs)
	if after >= before {
		return before, after, nil // nothing compactable
	}
	// Re-check: if a task started while the LLM summary was in flight, do
	// not clobber its working set.
	if m.TaskRunning(sessionID) {
		return before, after, fmt.Errorf("压缩完成时会话已开始新任务,未应用(下次压缩生效)")
	}
	s.mu.Lock()
	s.Record().Messages = newMsgs
	m.store.Update(s.Record())
	s.mu.Unlock()
	slog.Info("manual compaction applied", "session", sessionID, "before", before, "after", after)
	return before, after, nil
}

// SessionCallbacks carries the per-session event callbacks. A factory that
// produces one set of callbacks per session ID lets a message hub route each
// session's tool events into the bus, including sub-agent sessions.
type SessionCallbacks struct {
	OnToolStart    func(name string, params map[string]interface{})
	OnToolEnd      func(name string, params map[string]interface{}, result string)
	OnReply        func(content string, reasoning string)
	OnContextUsage func(tokens int) // per-turn context token count (UI token bar)
	OnUsage        func(promptTokens, cachedTokens, completionTokens int, cacheReported bool, speed float64) // real per-call usage (cache-hit + output-speed indicator)
}

type SessionManager struct {
	// models is the registry of configured model clients. Sessions
	// reference one by name (record.ModelKey); empty/unknown names
	// resolve to the default model. It is hot-replaced on config save.
	models *provider.ModelRegistry
	store  *SessionStore
	sessions  map[string]*Session
	mu        sync.RWMutex
	callbackFactory func(sessionID string) *SessionCallbacks

	compactionMu sync.RWMutex
	compactionOn bool // default true (legacy behavior); hot-swappable via SetCompactionEnabled

	// modes is the authoritative, race-free view of each session's
	// permission mode. The runtime gate reads it on every tool call while
	// the web API may change it concurrently, so it has its own lock.
	modeMu sync.RWMutex
	modes  map[string]string

	// approver resolves cautious-mode approval requests (wired by the
	// approval service); nil = approvals are auto-denied.
	approverMu sync.RWMutex
	approver   Approver

	// subagents manages background sub-agent jobs (wired at startup).
	subagentsMu sync.RWMutex
	subagents   *SubagentService

	// groups is the group-chat service (wired at startup; nil = groups off).
	groupsMu sync.RWMutex
	groups   *GroupService

	// deliverHook delivers a user message to an IDLE session (i.e. starts a
	// task); the runtime wires it to the message hub.
	deliverMu   sync.RWMutex
	deliverHook func(sessionID, text string)

	// activeTaskCheck is the authoritative "a task is running in THIS
	// process right now" check (the runtime wires it to the hub's
	// in-memory task registry). The persisted status "running" can be
	// stale after a restart/crash, which would otherwise make deliveries
	// queue forever into a session that has no live loop.
	activeCheckMu   sync.RWMutex
	activeTaskCheck func(sessionID string) bool

	// injections queues settlement notifications for sessions whose task
	// is still running (a running loop owns its message list; the queued
	// text is appended once the task settles, see FlushInjections).
	injMu      sync.Mutex
	injections map[string][]string
}

// clientFor resolves the model client for a session's model key. Empty or
// unknown keys fall back to the default model's client (nil when the
// registry is empty, e.g. in tests that inject prompt functions).
func (m *SessionManager) clientFor(modelKey string) *provider.ModelClient {
	return m.models.Client(modelKey)
}

// SetModels hot-replaces the model registry (startup / config save).
// Sessions already running keep using their existing client until they
// finish; idle sessions keep their snapshot too, so a config edit applies
// to sessions from the next model switch or reload — same semantics as the
// legacy single-client hot-swap.
func (m *SessionManager) SetModels(clients map[string]*provider.ModelClient, defaultName string) {
	m.models.Set(clients, defaultName)
}

// Models exposes the model registry (read-only use by the web layer).
func (m *SessionManager) Models() *provider.ModelRegistry {
	return m.models
}

// ContextWindowFor returns the context window of the given model key
// (default model for empty/unknown keys); 0 when nothing resolves.
func (m *SessionManager) ContextWindowFor(modelKey string) int {
	return m.models.ContextWindow(modelKey)
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

// NewRegistryWithDefault builds a model registry whose default model is
// the given client (nil client → empty registry, for tests that inject
// prompt functions instead of a real model).
func NewRegistryWithDefault(client *provider.ModelClient) *provider.ModelRegistry {
	r := provider.NewModelRegistry()
	if client != nil {
		r.Set(map[string]*provider.ModelClient{"": client}, "")
	}
	return r
}

// NewSessionManager creates a SessionManager. The client (may be nil in
// tests) is registered as the default model; a multi-model registry can be
// installed later via SetModels.
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
		injections:   make(map[string][]string),
	}, nil
}

// NewDefaultSessionManager creates a SessionManager using the default database path (~/.mini_code/agent.db)
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
		injections:   make(map[string][]string),
	}, nil
}

// --- permission mode ---

// ModeOf returns the session's current permission mode (ModeFull /
// ModeCautious). Unknown sessions fall back to the stored record, then to
// full.
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

// SetMode switches a session's permission mode. It applies to tool calls
// from the next one on, including for a task that is currently running.
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

// SetModel switches a session's model (config model name; empty = default
// model). It applies from the next task on — a task that is currently
// running keeps the client it started with (same semantics as the legacy
// global model hot-swap). The in-memory record and client snapshot are
// synced under the session lock so a concurrent Prompt either sees the old
// pair or the new pair, never a mix.
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

// SetReasoning sets a session's per-session thinking depth override (thinking
// switch + reasoning_effort); empty values clear it and fall back to the
// model's configured default. It applies from the session's next task on — a
// task that is currently running keeps the client it started with.
func (m *SessionManager) SetReasoning(sessionID, thinking, effort string) error {
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}
	s.SetReasoningOverride(thinking, effort)
	slog.Info("session reasoning changed", "session", sessionID, "thinking", thinking, "effort", effort)
	return nil
}

// --- session title ---

// MaxTitleLen caps the length of a user-chosen session title (in runes).
const MaxTitleLen = 60

// RenameSession renames a session (the sidebar title). It is safe while a
// task is running: the loop persists the shared record pointer, so its next
// per-turn write carries the new title; syncing the in-memory record here
// (like SetMode / SetChannelKey) keeps a later whole-row persistence from
// resurrecting the old title.
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
	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		s.record.Title = title
	}
	m.mu.Unlock()
	slog.Info("session renamed", "session", sessionID, "title", title)
	return nil
}

// SetArchived toggles a session's archived flag: an archived session is
// fully functional but hidden from the main conversation list (the UI
// renders it in the collapsed "已归档" section). The flag lives in the
// session metadata and is invisible to the agent loop, so archiving never
// changes the prompt prefix (KV-prefix-cache safe). Valid while a task is
// running (targeted metadata write under the session lock).
func (m *SessionManager) SetArchived(sessionID string, on bool) error {
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}
	s.SetArchived(on)
	slog.Info("session archived state changed", "session", sessionID, "archived", on)
	return nil
}

// --- approver (cautious-mode approval) ---

// SetApprover wires the approval service used by the runtime gate.
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

// --- sub-agent service ---

// SetSubagents wires the background sub-agent service (task tool).
func (m *SessionManager) SetSubagents(s *SubagentService) {
	m.subagentsMu.Lock()
	m.subagents = s
	m.subagentsMu.Unlock()
}

// Subagents returns the wired sub-agent service (nil before wiring).
func (m *SessionManager) Subagents() *SubagentService {
	m.subagentsMu.RLock()
	defer m.subagentsMu.RUnlock()
	return m.subagents
}

// --- group chat ---

// SetGroups wires the group-chat service.
func (m *SessionManager) SetGroups(g *GroupService) {
	m.groupsMu.Lock()
	m.groups = g
	m.groupsMu.Unlock()
}

// Groups returns the wired group-chat service (nil before wiring).
func (m *SessionManager) Groups() *GroupService {
	m.groupsMu.RLock()
	defer m.groupsMu.RUnlock()
	return m.groups
}

// --- mid-task user message delivery ---

// SetDeliverHook installs the hook used to deliver user messages to idle
// sessions (i.e. start a task); the runtime wires it to the message hub.
func (m *SessionManager) SetDeliverHook(fn func(sessionID, text string)) {
	m.deliverMu.Lock()
	m.deliverHook = fn
	m.deliverMu.Unlock()
}

// SetActiveTaskCheck installs the live-task check used to decide whether a
// session has a running agent loop in this process. The runtime wires it to
// the hub's task registry; without it the persisted status is the fallback.
func (m *SessionManager) SetActiveTaskCheck(fn func(sessionID string) bool) {
	m.activeCheckMu.Lock()
	m.activeTaskCheck = fn
	m.activeCheckMu.Unlock()
}

// TaskRunning reports whether the session has a live task loop in this
// process. It prefers the wired live check (never stale) and falls back to
// the persisted status only when no check is wired (tests, non-web runs).
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

// DeliverToSession delivers a user message to a session: when the session has
// a live task loop the message is queued and injected at the agent loop's
// next turn; otherwise it is dispatched as a normal task via the deliver
// hook. TaskRunning (not the persisted status) is the queue-vs-start signal,
// so a stale "running" row after a restart cannot swallow the message.
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

// --- settlement notifications ---

// InjectMessage appends a system-generated user message (e.g. a background
// sub-agent settlement notice) to a session's conversation. When the
// session's task is still running, the loop owns its message list, so the
// text is queued and applied by FlushInjections once the task settles —
// this keeps the notice from being clobbered by the loop's per-turn
// persistence.
func (m *SessionManager) InjectMessage(sessionID, text string) error {
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if m.TaskRunning(sessionID) {
		m.injMu.Lock()
		m.injections[sessionID] = append(m.injections[sessionID], text)
		m.injMu.Unlock()
		slog.Info("settlement notice queued (session running)", "session", sessionID)
		return nil
	}
	return m.appendMessage(sessionID, text)
}

// FlushInjections applies queued settlement notices. The hub calls it right
// after a task's Prompt returns (the loop has just persisted its final
// messages), so the notices land on top of the final state.
func (m *SessionManager) FlushInjections(sessionID string) {
	m.injMu.Lock()
	texts := m.injections[sessionID]
	delete(m.injections, sessionID)
	m.injMu.Unlock()
	for _, t := range texts {
		if err := m.appendMessage(sessionID, t); err != nil {
			slog.Warn("flush injection failed", "session", sessionID, "err", err)
		}
	}
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
	m.store.Update(s.record)
	s.mu.Unlock()
	return nil
}

// SnapshotMessages returns a copy of the session's persisted conversation
// (safe to read while a task may be persisting concurrently).
func (s *Session) SnapshotMessages() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provider.Message, len(s.record.Messages))
	copy(out, s.record.Messages)
	return out
}

// CopyMessages returns a snapshot of a session's persisted conversation
// (safe to read while a task may be persisting concurrently).
func (m *SessionManager) CopyMessages(sessionID string) []provider.Message {
	m.mu.RLock()
	s, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return s.SnapshotMessages()
}

// ListChildrenIDs returns the session IDs of all direct sub-agent sessions
// of the given parent (from the store). Used to cascade cancellations.
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

// SetSessionCallbackFactory registers the per-session callback factory.
// Every session (including sub-agent sessions) resolves its callbacks by
// session ID through this factory; it is wired by the message hub.
func (m *SessionManager) SetSessionCallbackFactory(f func(sessionID string) *SessionCallbacks) {
	m.mu.Lock()
	m.callbackFactory = f
	m.mu.Unlock()
}

// callbacksFor resolves the effective callbacks for a session ID.
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

// newSession builds a Session with its event callbacks resolved.
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
	if cbs != nil {
		s.onToolCallStart = cbs.OnToolStart
		s.onToolCallEnd = cbs.OnToolEnd
		s.onAssistantReply = cbs.OnReply
		s.onContextUsage = cbs.OnContextUsage
		s.onUsage = cbs.OnUsage
	}
	// Register the session's mode so the runtime gate can read it without
	// touching the (concurrently mutated) record.
	m.modeMu.Lock()
	if _, ok := m.modes[record.SessionID]; !ok {
		m.modes[record.SessionID] = NormalizeMode(record.Mode)
	}
	m.modeMu.Unlock()
	return s
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
// channel key (an external conversation id). It returns nil when the key
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

// SetChannelKey binds a stable external channel key (an external
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

func (m *SessionManager) Close() error {
	return m.store.Close()
}

// GetStore returns the session store for external access
func (m *SessionManager) GetStore() *SessionStore {
	return m.store
}
