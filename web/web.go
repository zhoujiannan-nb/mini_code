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
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/util"
)

// Backend is what the HTTP server needs from the runtime: the hub, the
// session manager and the live-config hot-apply operations.
type Backend interface {
	Hub() *channels.Hub
	Mgr() *session.SessionManager
	Status() StatusInfo
	GetConfig() *config.AppConfig
	SaveConfig(cfg *config.AppConfig) (*SaveResult, error)
}

// StatusInfo describes the runtime state shown in the UI header.
type StatusInfo struct {
	WebPort       int    `json:"web_port"`
	Provider      string `json:"provider"`
	ModelName     string `json:"model_name"`
	ContextWindow int    `json:"context_window"`
	Compaction    bool   `json:"compaction_enabled"`
	DTEnabled     bool   `json:"dingtalk_enabled"`
	DTRunning     bool   `json:"dingtalk_running"`
}

// SaveResult reports what a config save changed at runtime.
type SaveResult struct {
	Message      string   `json:"message"`
	ModelSwapped bool     `json:"model_swapped"`
	DingTalk     string   `json:"dingtalk,omitempty"`
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

	// --- API ---
	mux.HandleFunc("GET /api/healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("POST /api/sessions/{id}/compact", s.handleCompactSession)
	mux.HandleFunc("POST /api/messages", s.handleSendMessage)
	mux.HandleFunc("POST /api/sessions/{id}/cancel", s.handleCancel)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handleSaveConfig)
	mux.HandleFunc("GET /api/fs/search", s.handleFsSearch)

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
	w.Write([]byte(page))
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
	SessionID    string `json:"session_id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	// Active is true only while this server is actually running a task for
	// the session (hub task registry). It is the authoritative "running
	// right now" flag for the UI: a persisted status of "running" can be
	// stale after a process restart, but an active task never is.
	Active       bool   `json:"active"`
	AgentRole    string `json:"agent_role"`
	WorkDir      string `json:"work_dir"`
	ChannelKey   string `json:"channel_key,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	MessageCount int    `json:"message_count"`
}

func (s *server) summarize(r *session.SessionRecord) SessionSummary {
	return SessionSummary{
		SessionID:    r.SessionID,
		Title:        r.Title,
		Status:       r.Status,
		Active:       s.be.Hub().ActiveTask(r.SessionID),
		AgentRole:    r.AgentRole,
		WorkDir:      r.WorkDir,
		ChannelKey:   r.ChannelKey,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		MessageCount: len(r.Messages),
	}
}

// summarizeSummary builds the same API entry from a lightweight store row
// (list endpoint — no conversation payloads are loaded server-side).
func (s *server) summarizeSummary(r session.SessionSummary) SessionSummary {
	return SessionSummary{
		SessionID:    r.SessionID,
		Title:        r.Title,
		Status:       r.Status,
		Active:       s.be.Hub().ActiveTask(r.SessionID),
		AgentRole:    r.AgentRole,
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
	out := make([]SessionSummary, 0, len(records))
	for _, r := range records {
		out = append(out, s.summarizeSummary(r))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.be.Mgr().Get(id)
	if err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	rec := sess.Record()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  s.summarize(rec),
		"messages": rec.Messages,
		// Token estimate for the composer footer (xx/context_window).
		"tokens": util.CountMessagesTokens(rec.Messages),
	})
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

// --- messages (producer side of the channel) ---

type SendMessageRequest struct {
	SessionID string `json:"session_id"` // empty = let the hub create a session
	Content   string `json:"content"`
}

func (s *server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		writeErr(w, http.StatusBadRequest, "content is required")
		return
	}
	if len(req.Content) > 20000 {
		writeErr(w, http.StatusBadRequest, "content too long (max 20000 bytes)")
		return
	}
	if req.SessionID != "" {
		if _, err := s.be.Mgr().Get(req.SessionID); err != nil {
			writeErr(w, http.StatusNotFound, "session not found: "+req.SessionID)
			return
		}
	}

	msg := channels.NewUserMessage(channels.ChannelWeb, req.Content)
	msg.SessionID = req.SessionID
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
			fmt.Fprint(w, ": ping\n\n")
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
	SessionKey string `json:"session_key,omitempty"` // external conversation key (e.g. DingTalk conversationId)
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
