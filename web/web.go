// Package web is the web channel of mini_code: a single HTTP server that
// serves the built-in web UI and the JSON API on top of the message hub.
//
// The hub routes every inbound message to its owning session (creating a
// new session when none matches session_id / session_key), runs the agent
// loop, and emits produced messages back to all subscribers — the web UI
// follows them over a Server-Sent Events stream.
package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/user/mini_code/approval"
	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/util"
)

// Backend is what the HTTP server needs from the runtime: the hub, the
// session manager, the approval service and the live-config hot-apply
// operations.
type Backend interface {
	Hub() *channels.Hub
	Mgr() *session.SessionManager
	Groups() *session.GroupService
	Approvals() *approval.Service
	Status() StatusInfo
	GetConfig() *config.AppConfig
	SaveConfig(cfg *config.AppConfig) (*SaveResult, error)
}

// ModelInfo describes one configured model for the UI dropdown.
type ModelInfo struct {
	Name          string `json:"name"`
	ModelName     string `json:"model_name"`
	Provider      string `json:"provider"`
	ContextWindow int    `json:"context_window"`
}

// StatusInfo describes the runtime state shown in the UI header.
// Provider/ModelName/ContextWindow describe the DEFAULT model; Models
// carries the full list for the UI dropdown.
type StatusInfo struct {
	WebPort       int         `json:"web_port"`
	Provider      string      `json:"provider"`
	ModelName     string      `json:"model_name"`
	ContextWindow int         `json:"context_window"`
	Models        []ModelInfo `json:"models"`
	DefaultModel  string      `json:"default_model"`
	Compaction    bool        `json:"compaction_enabled"`
}

// SaveResult reports what a config save changed at runtime.
type SaveResult struct {
	Message      string   `json:"message"`
	ModelSwapped bool     `json:"model_swapped"`
	Notes        []string `json:"notes,omitempty"`
}

type server struct {
	be     Backend
	static func() (string, error)
	dirs   *DirIndex // in-memory folder index for the "@" picker
}

// NewServer builds the full HTTP handler (API + embedded web UI) wired to
// the runtime backend.
func NewServer(be Backend) http.Handler {
	s := &server{be: be, static: embeddedPage, dirs: NewDirIndex()}
	go s.dirs.Run(context.Background())
	mux := http.NewServeMux()

	// --- web UI ---
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /index.html", s.handleIndex)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.HandleFunc("GET /avatars/{name}", s.handleAvatar)

	// --- API ---
	mux.HandleFunc("GET /api/healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/avatar", s.handleUserAvatarGet)
	mux.HandleFunc("POST /api/avatar", s.handleUserAvatarSet)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("POST /api/sessions/{id}/compact", s.handleCompactSession)
	mux.HandleFunc("POST /api/sessions/{id}/mode", s.handleSetMode)
	mux.HandleFunc("POST /api/sessions/{id}/context-reminders", s.handleSetContextReminders)
	mux.HandleFunc("POST /api/sessions/{id}/model", s.handleSetModel)
	mux.HandleFunc("POST /api/sessions/{id}/reasoning", s.handleSetReasoning)
	mux.HandleFunc("POST /api/sessions/{id}/rename", s.handleRenameSession)
	mux.HandleFunc("GET /api/sessions/{id}/children", s.handleListChildren)
	mux.HandleFunc("POST /api/messages", s.handleSendMessage)
	mux.HandleFunc("POST /api/sessions/{id}/cancel", s.handleCancel)
	mux.HandleFunc("GET /api/groups", s.handleGroupsList)
	mux.HandleFunc("POST /api/groups", s.handleGroupsCreate)
	mux.HandleFunc("GET /api/groups/{id}", s.handleGroupsDetail)
	mux.HandleFunc("POST /api/groups/{id}/background", s.handleGroupsSetBackground)
	mux.HandleFunc("POST /api/groups/{id}/rename", s.handleGroupsRename)
	mux.HandleFunc("DELETE /api/groups/{id}", s.handleGroupsDelete)
	mux.HandleFunc("DELETE /api/groups/{id}/messages", s.handleGroupsClearMessages)
	mux.HandleFunc("POST /api/groups/{id}/join", s.handleGroupsJoin)
	mux.HandleFunc("DELETE /api/groups/{id}/members/{sid}", s.handleGroupsLeave)
	mux.HandleFunc("POST /api/groups/{id}/members/{sid}/rename", s.handleGroupsMemberRename)
	mux.HandleFunc("POST /api/groups/{id}/members/{sid}/avatar", s.handleGroupsMemberAvatar)
	mux.HandleFunc("POST /api/groups/{id}/messages", s.handleGroupsSend)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handleSaveConfig)
	mux.HandleFunc("POST /api/llm/models", s.handleListModels)
	mux.HandleFunc("GET /api/fs/search", s.handleFsSearch)
	mux.HandleFunc("GET /api/approvals", s.handleListApprovals)
	mux.HandleFunc("POST /api/approvals/{id}/decision", s.handleApprovalDecision)

	// --- compatibility: the original simple goal API ---
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /goal", s.handleGoal)

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// --- UI ---

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	page, err := s.static()
	if err != nil {
		http.Error(w, "ui not available: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// UI 是内嵌的单文件:升级 exe 后必须拿到新页面,
	// 否则 WebView2/浏览器可能继续用本地缓存的旧 UI(桌面端缓存是持久化的)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(page))
}

// handleFavicon serves the embedded app icon as the browser tab favicon.
func (s *server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	b, err := uiFS.ReadFile("ui/favicon.ico")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(b)
}

// avatarNameRe whitelists the built-in group avatar file names.
var avatarNameRe = regexp.MustCompile(`^a[0-9]{3}\.svg$`)

// handleAvatar serves one embedded group avatar (web/ui/avatars/aNNN.svg).
func (s *server) handleAvatar(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !avatarNameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	b, err := uiFS.ReadFile("ui/avatars/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(b)
}

// --- user avatar (the human user, global; stored in the config so it
// survives restarts) ---

// userAvatarPrefixes are the accepted data-URL prefixes for an uploaded
// avatar (the UI canvas-compresses to 256x256 JPEG before sending).
var userAvatarPrefixes = []string{
	"data:image/jpeg;base64,",
	"data:image/png;base64,",
	"data:image/webp;base64,",
}

func (s *server) handleUserAvatarGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.be.GetConfig()
	writeJSON(w, http.StatusOK, map[string]string{"avatar": cfg.UserAvatar})
}

func (s *server) handleUserAvatarSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Avatar string `json:"avatar"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Avatar = strings.TrimSpace(req.Avatar)
	if req.Avatar != "" {
		rest, ok := "", false
		for _, p := range userAvatarPrefixes {
			if strings.HasPrefix(req.Avatar, p) {
				rest, ok = req.Avatar[len(p):], true
				break
			}
		}
		if !ok {
			writeErr(w, http.StatusBadRequest, "avatar must be a data URL (data:image/jpeg|png|webp;base64,...)")
			return
		}
		raw, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "avatar base64 is invalid: "+err.Error())
			return
		}
		if len(raw) > 4*1024*1024 {
			writeErr(w, http.StatusBadRequest, "avatar too large (max 4MB after decode)")
			return
		}
	}
	cfg := s.be.GetConfig()
	cfg.UserAvatar = req.Avatar
	if _, err := s.be.SaveConfig(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("user avatar updated", "bytes", len(req.Avatar))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "avatar": req.Avatar})
}

// --- health & status ---

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.be.Status())
}

// --- sessions ---

type SessionSummary struct {
	SessionID string `json:"session_id"`
	ParentID  string `json:"parent_id,omitempty"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Model     string `json:"model,omitempty"` // configured model name; empty = default model
	// Active is true only while this server is actually running a task for
	// the session (hub task registry). It is the authoritative "running
	// right now" flag for the UI: a persisted status of "running" can be
	// stale after a process restart, but an active task never is.
	Active       bool   `json:"active"`
	AgentRole        string `json:"agent_role"`
	Mode             string `json:"mode"`
	WorkDir          string `json:"work_dir"`
	ChannelKey       string `json:"channel_key,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	MessageCount     int    `json:"message_count"`
	ContextReminders bool   `json:"context_reminders"` // experimental context-usage notices (session-level, default on)
	// Thinking / ReasoningEffort are the per-session thinking-depth override
	// (composer control); empty = the model's configured default.
	Thinking        string `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

func (s *server) summarize(r *session.SessionRecord) SessionSummary {
	thinking, effort := reasoningOf(r)
	return SessionSummary{
		SessionID:        r.SessionID,
		ParentID:         r.ParentID,
		Title:            r.Title,
		Status:           r.Status,
		Active:           s.be.Hub().ActiveTask(r.SessionID),
		AgentRole:        r.AgentRole,
		Mode:             s.be.Mgr().ModeOf(r.SessionID),
		Model:            r.ModelKey,
		WorkDir:          r.WorkDir,
		ChannelKey:       r.ChannelKey,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
		MessageCount:     len(r.Messages),
		ContextReminders: contextRemindersOf(r),
		Thinking:         thinking,
		ReasoningEffort:  effort,
	}
}

// summarizeSummary builds the same API entry from a lightweight store row
func (s *server) summarizeSummary(r session.SessionSummary) SessionSummary {
	return SessionSummary{
		SessionID:    r.SessionID,
		ParentID:     r.ParentID,
		Title:        r.Title,
		Status:       r.Status,
		Active:       s.be.Hub().ActiveTask(r.SessionID),
		AgentRole:    r.AgentRole,
		Mode:         session.NormalizeMode(r.Mode),
		Model:        r.ModelKey,
		WorkDir:      r.WorkDir,
		ChannelKey:   r.ChannelKey,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		MessageCount: r.MessageCount,
	}
}

func (s *server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	// Root sessions only: sub-agent sessions (parent_id set) are internal.
	// The lightweight query skips conversation payloads, so the list stays
	// cheap enough for the UI to poll it on every SSE signal.
	records, err := s.be.Mgr().GetStore().ListSessionSummaries(limit, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 子 agent 在独立子会话里跑:父会话自己的回合结束后,只要(孙)子会话
	// 还有活动任务,父会话也要显示"运行中"(停止按钮行为一致:CancelTask
	// 本来就会级联取消子任务)。查询失败时退回仅看父会话自身状态。
	children, _ := s.be.Mgr().GetStore().ChildIDsByParent()
	out := make([]SessionSummary, 0, len(records))
	for _, r := range records {
		sum := s.summarizeSummary(r)
		if !sum.Active && childrenRunning(s.be.Hub(), r.SessionID, children) {
			sum.Active = true
		}
		out = append(out, sum)
	}
	writeJSON(w, http.StatusOK, out)
}

// childrenRunning reports whether any descendant sub-agent session of the
// given session (transitively) still has a live task in this process.
func childrenRunning(hub *channels.Hub, id string, children map[string][]string) bool {
	seen := map[string]bool{id: true}
	stack := []string{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range children[cur] {
			if seen[c] {
				continue
			}
			seen[c] = true
			if hub.ActiveTask(c) {
				return true
			}
			stack = append(stack, c)
		}
	}
	return false
}

func (s *server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.be.Mgr().Get(id)
	if err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	rec := sess.Record()
	msgs := sess.SnapshotMessages()
	sum := s.summarize(rec)
	if !sum.Active {
		// 与列表一致:后台子 agent 还在跑时,主会话按"运行中"返回
		// (前端据此恢复停止按钮;syncRunning 轮询也会随之收敛)。
		if children, err := s.be.Mgr().GetStore().ChildIDsByParent(); err == nil {
			if childrenRunning(s.be.Hub(), rec.SessionID, children) {
				sum.Active = true
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  sum,
		"messages": msgs,
		// Token estimate for the composer footer (xx/context_window).
		"tokens": util.CountMessagesTokens(msgs),
		// Context window of THIS session's model (the token bar must track
		// the per-session model, not the global default).
		"context_window": s.be.Mgr().ContextWindowFor(rec.ModelKey),
	})
}

// contextRemindersOf reads the experimental context-usage notices flag from a
// session record's metadata; an absent key keeps the default: enabled.
func contextRemindersOf(r *session.SessionRecord) bool {
	if r.Metadata == nil {
		return true
	}
	v, ok := r.Metadata["context_reminders"]
	if !ok {
		return true
	}
	b, _ := v.(bool)
	return b
}

// reasoningOf reads the per-session thinking-depth override from a session
// record's metadata (empty = the model's configured default).
func reasoningOf(r *session.SessionRecord) (thinking, effort string) {
	if r.Metadata == nil {
		return "", ""
	}
	thinking, _ = r.Metadata["thinking"].(string)
	effort, _ = r.Metadata["reasoning_effort"].(string)
	return thinking, effort
}

func (s *server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.be.Mgr().Get(id); err != nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	if err := s.be.Mgr().Delete(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("session deleted", "id", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "session_id": id})
}

// handleCompactSession manually compresses a session's conversation with
// the LLM summarizer (independent of the auto-compaction threshold).
func (s *server) handleCompactSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	before, after, err := s.be.Mgr().CompactSession(ctx, id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"before":     before,
		"after":      after,
		"compressed": after < before,
	})
}

// handleSetMode switches a session's permission mode (full / cautious).
// It takes effect from the next tool call on, even mid-task.
func (s *server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if sess, err := s.be.Mgr().Get(id); err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.be.Mgr().SetMode(id, req.Mode); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"session_id": id,
		"mode":       session.NormalizeMode(req.Mode),
	})
}

// handleSetContextReminders toggles the experimental context-usage notices for
// a session (session-level, default on). It takes effect from the session's
// next task on; the current in-flight task keeps its setting.
func (s *server) handleSetContextReminders(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.be.Mgr().Get(id)
	if err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sess.SetContextRemindersEnabled(req.Enabled)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id":        id,
		"context_reminders": req.Enabled,
	})
}

// handleSetModel switches a session's model (config model name; empty =
// default model). It takes effect from the session's next task on.
func (s *server) handleSetModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if sess, err := s.be.Mgr().Get(id); err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.be.Mgr().SetModel(id, req.Model); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(req.Model)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id":     id,
		"model":          name,
		"context_window": s.be.Mgr().ContextWindowFor(name),
	})
}

// validReasoningEffort reports whether effort is one of the values the
// DeepSeek / SGLang providers accept.
func validReasoningEffort(v string) bool {
	switch v {
	case "off", "low", "high", "max", "xhigh", "medium":
		return true
	}
	return false
}

// handleSetReasoning sets a session's per-session thinking-depth override
// (thinking switch + reasoning_effort); empty values clear it and fall back to
// the model's configured default. It takes effect from the session's next
// task on.
func (s *server) handleSetReasoning(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if sess, err := s.be.Mgr().Get(id); err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	var req struct {
		Thinking        string `json:"thinking"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Thinking = strings.TrimSpace(req.Thinking)
	req.ReasoningEffort = strings.TrimSpace(req.ReasoningEffort)
	if req.Thinking != "" && req.Thinking != "enabled" && req.Thinking != "disabled" {
		writeErr(w, http.StatusBadRequest, `thinking must be "enabled", "disabled" or empty`)
		return
	}
	if req.ReasoningEffort != "" && !validReasoningEffort(req.ReasoningEffort) {
		writeErr(w, http.StatusBadRequest, "invalid reasoning_effort: "+req.ReasoningEffort)
		return
	}
	if err := s.be.Mgr().SetReasoning(id, req.Thinking, req.ReasoningEffort); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"session_id":       id,
		"thinking":         req.Thinking,
		"reasoning_effort": req.ReasoningEffort,
	})
}

// handleRenameSession renames a session (the sidebar title). It is valid
// while a task is running; the UI picks up the new title from the response
// (and the next list poll).
func (s *server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if sess, err := s.be.Mgr().Get(id); err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.be.Mgr().RenameSession(id, req.Title); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	slog.Info("session renamed via web", "id", id, "title", strings.TrimSpace(req.Title))
	writeJSON(w, http.StatusOK, map[string]string{
		"session_id": id,
		"title":      strings.TrimSpace(req.Title),
	})
}

// handleListChildren returns the sub-agent sessions of one session with
// their live state (the UI renders them under the parent in the sidebar).
func (s *server) handleListChildren(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.be.Mgr().Get(id); err != nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	sums, err := s.be.Mgr().GetStore().ListChildren(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]SessionSummary, 0, len(sums))
	for _, c := range sums {
		out = append(out, s.summarizeSummary(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// --- messages (producer side of the channel) ---

type SendMessageRequest struct {
	SessionID        string   `json:"session_id"` // empty = let the hub create a session
	Content          string   `json:"content"`
	Images           []string `json:"images,omitempty"` // data URLs (data:image/...;base64,...); the model sees them on the first turn
	Mode             string   `json:"mode,omitempty"`   // permission mode for a NEW session (full / cautious)
	Model            string   `json:"model,omitempty"`  // model name for a NEW session (config key); ignored for existing sessions
	ContextReminders *bool    `json:"context_reminders,omitempty"` // new-session preference; nil = default (on)
	Thinking         string   `json:"thinking,omitempty"`          // thinking switch for a NEW session; ignored for existing sessions
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`  // thinking depth for a NEW session; ignored for existing sessions
}

const (
	maxImagesPerMessage = 4
	maxImageBytes       = 10 * 1024 * 1024 // per data URL (base64-encoded)
)

func (s *server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" && len(req.Images) == 0 {
		writeErr(w, http.StatusBadRequest, "content is required")
		return
	}
	if len(req.Content) > 20000 {
		writeErr(w, http.StatusBadRequest, "content too long (max 20000 bytes)")
		return
	}
	if len(req.Images) > maxImagesPerMessage {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("too many images (max %d)", maxImagesPerMessage))
		return
	}
	for i, img := range req.Images {
		if !strings.HasPrefix(img, "data:image/") {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("images[%d] must be a data URL (data:image/...;base64,...)", i))
			return
		}
		if len(img) > maxImageBytes {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("images[%d] too large (max 10MB)", i))
			return
		}
	}
	if req.SessionID != "" {
		if _, err := s.be.Mgr().Get(req.SessionID); err != nil {
			writeErr(w, http.StatusNotFound, "session not found: "+req.SessionID)
			return
		}
	}

	msg := channels.NewUserMessage(channels.ChannelWeb, req.Content)
	msg.SessionID = req.SessionID
	msg.Images = req.Images
	msg.Mode = req.Mode
	msg.Model = req.Model
	msg.ContextReminders = req.ContextReminders
	msg.Thinking = strings.TrimSpace(req.Thinking)
	msg.ReasoningEffort = strings.TrimSpace(req.ReasoningEffort)
	s.be.Hub().Submit(msg)

	resp := map[string]string{"task_id": msg.ID}
	if req.SessionID != "" {
		resp["session_id"] = req.SessionID
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *server) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	canceled := s.be.Hub().CancelTask(id)
	status := "cancelling"
	if !canceled {
		// No running task in this process: the session is idle (or the task
		// belongs to a different server instance). The UI uses this to tell
		// the user that there is nothing to stop.
		status = "no_active_task"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     status,
		"canceled":   canceled,
		"session_id": id,
	})
}

// --- approvals (cautious mode) ---

// handleListApprovals returns the open approval requests (UI sync after a
// page reload: the SSE stream only carries events from now on).
func (s *server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	if s.be.Approvals() == nil {
		writeJSON(w, http.StatusOK, []approval.PendingInfo{})
		return
	}
	writeJSON(w, http.StatusOK, s.be.Approvals().Pending())
}

// handleApprovalDecision applies the user's decision to one request.
func (s *server) handleApprovalDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Decision string `json:"decision"` // "approve" | "deny"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	approved := req.Decision == "approve"
	if req.Decision != "approve" && req.Decision != "deny" {
		writeErr(w, http.StatusBadRequest, "decision must be 'approve' or 'deny'")
		return
	}
	if s.be.Approvals() == nil {
		writeErr(w, http.StatusConflict, "approval not found or already resolved")
		return
	}
	d, ok := s.be.Approvals().Resolve(id, approved)
	if !ok {
		writeErr(w, http.StatusConflict, "approval not found or already resolved")
		return
	}
	slog.Info("approval decided via web", "id", id, "approved", approved)
	writeJSON(w, http.StatusOK, d)
}

// --- events (consumer side: SSE stream of hub messages) ---

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	name := "sse-" + util.RandomHex(8)
	out := s.be.Hub().Subscribe(name)
	defer s.be.Hub().Unsubscribe(name)

	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			// 心跳用真实 SSE 事件(而非注释行):前端 EventSource 能收到,
			// 据此做存活检测——TCP 半开(收不到任何数据)时 UI 会在
			// 75s 内主动重连,而不是让转圈一直挂着
			fmt.Fprint(w, "event: ping\ndata: heartbeat\n\n")
			fl.Flush()
		case m, ok := <-out:
			if !ok {
				return
			}
			b, err := json.Marshal(m)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
}

// --- config (read + hot-apply) ---

func (s *server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.be.GetConfig())
}

func (s *server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.AppConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := cfg.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.be.SaveConfig(&cfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleListModels proxies the provider's OpenAI-compatible /models endpoint so
// the web UI's "fetch models" button can populate the model picker. The caller
// supplies the base_url and api_key to probe (not necessarily a saved model).
func (s *server) handleListModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if req.BaseURL == "" {
		writeErr(w, http.StatusBadRequest, "base_url is required")
		return
	}
	models, err := provider.ListModels(req.BaseURL, req.APIKey)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"models": models})
}

// --- folder search (the "@" picker in the composer) ---

func (s *server) handleFsSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	hits, indexing := s.dirs.Search(q, limit)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"dirs":     hits,
		"indexing": indexing,
		"count":    len(hits),
	})
}

// --- compatibility: the original synchronous goal API ---

type GoalRequest struct {
	Goal       string `json:"goal"`
	SessionID  string `json:"session_id,omitempty"`  // continue this session
	SessionKey string `json:"session_key,omitempty"` // external conversation key
}

type GoalResponse struct {
	Data      string `json:"data"`
	SessionID string `json:"session_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *server) handleGoal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req GoalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Goal) == "" {
		http.Error(w, "goal is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	msg := channels.NewUserMessage(channels.ChannelWeb, req.Goal)
	msg.SessionID = req.SessionID
	msg.SessionKey = req.SessionKey

	data, sessionID, err := s.be.Hub().RunOnce(ctx, msg)
	resp := GoalResponse{Data: data, SessionID: sessionID}
	if err != nil {
		resp.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}
