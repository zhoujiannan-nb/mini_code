package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/user/mini_code/approval"
	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/job"
	"github.com/user/mini_code/plugin"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/util"
)

type Backend interface {
	Hub() *channels.Hub
	Mgr() *session.SessionManager
	Approvals() *approval.Service
	Status() StatusInfo
	GetConfig() *config.AppConfig
	SaveConfig(cfg *config.AppConfig) (*SaveResult, error)
}

type ModelInfo struct {
	Name          string `json:"name"`
	ModelName     string `json:"model_name"`
	Provider      string `json:"provider"`
	ContextWindow int    `json:"context_window"`
}

type StatusInfo struct {
	WebPort       int         `json:"web_port"`
	Provider      string      `json:"provider"`
	ModelName     string      `json:"model_name"`
	ContextWindow int         `json:"context_window"`
	Models        []ModelInfo `json:"models"`
	DefaultModel  string      `json:"default_model"`
	Compaction    bool        `json:"compaction_enabled"`
}

type SaveResult struct {
	Message      string   `json:"message"`
	ModelSwapped bool     `json:"model_swapped"`
	Notes        []string `json:"notes,omitempty"`
}

type server struct {
	be     Backend
	static func() (string, error)
	dirs   *DirIndex
}

func NewServer(be Backend) http.Handler {
	s := &server{be: be, static: embeddedPage, dirs: NewDirIndex()}
	go s.dirs.Run(context.Background())
	go plugin.DefaultProcs().StartAutostart()
	go util.WarmTokenizer()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /index.html", s.handleIndex)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)

	mux.HandleFunc("GET /plugins/", s.handlePluginAsset)
	mux.HandleFunc("GET /api/plugins", s.handlePluginCentreList)
	mux.HandleFunc("POST /api/plugins/{id}/toggle", s.handlePluginToggle)
	mux.HandleFunc("DELETE /api/plugins/{id}", s.handlePluginUninstall)
	mux.HandleFunc("GET /api/plugins/processes", s.handlePluginProcessList)
	mux.HandleFunc("POST /api/plugins/{id}/process", s.handlePluginProcessAction)
	mux.HandleFunc("GET /api/plugins/{id}/logs", s.handlePluginProcessLogs)
	mux.HandleFunc("GET /api/plugins/catalog", s.handlePluginCatalog)
	mux.HandleFunc("POST /api/plugins/install", s.handlePluginInstall)
	mux.HandleFunc("GET /api/plugins/prompt", s.handlePluginMakePrompt)

	mux.HandleFunc("GET /bootanim/", s.handleBootAnimAsset)
	mux.HandleFunc("GET /api/bootanim", s.handleBootAnimList)
	mux.HandleFunc("PUT /api/bootanim", s.handleBootAnimSave)

	mux.HandleFunc("GET /api/healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/avatar", s.handleUserAvatarGet)
	mux.HandleFunc("POST /api/avatar", s.handleUserAvatarSet)
	mux.HandleFunc("GET /api/agent-avatar", s.handleAgentAvatarGet)
	mux.HandleFunc("POST /api/agent-avatar", s.handleAgentAvatarSet)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("GET /api/sessions/{id}/events", s.handleSessionEvents)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("GET /api/sessions/{id}/context", s.handleSessionContext)
	mux.HandleFunc("GET /api/sessions/{id}/context/detail", s.handleSessionContextDetail)
	mux.HandleFunc("GET /api/sessions/{id}/trajectory", s.handleSessionTrajectory)
	mux.HandleFunc("POST /api/sessions/{id}/mode", s.handleSetMode)
	mux.HandleFunc("POST /api/sessions/{id}/model", s.handleSetModel)
	mux.HandleFunc("POST /api/sessions/{id}/reasoning", s.handleSetReasoning)
	mux.HandleFunc("GET /api/sessions/{id}/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/sessions/{id}/goal", s.handleGetGoal)
	mux.HandleFunc("GET /api/sessions/{id}/jobs/output", s.handleJobOutput)
	mux.HandleFunc("POST /api/sessions/{id}/jobs/kill", s.handleKillJob)
	mux.HandleFunc("POST /api/sessions/{id}/jobs/clear", s.handleClearJobs)
	mux.HandleFunc("POST /api/sessions/{id}/rename", s.handleRenameSession)
	mux.HandleFunc("POST /api/sessions/{id}/archive", s.handleSetArchived)
	mux.HandleFunc("GET /api/workers", s.handleWorkersList)
	mux.HandleFunc("POST /api/workers", s.handleWorkersCreate)
	mux.HandleFunc("POST /api/workers/{id}/summary", s.handleWorkersSummary)
	mux.HandleFunc("POST /api/workers/{id}/rename", s.handleWorkersRename)
	mux.HandleFunc("DELETE /api/workers/{id}", s.handleWorkersDelete)
	mux.HandleFunc("GET /api/workers/{id}/note", s.handleWorkersNoteGet)
	mux.HandleFunc("POST /api/workers/{id}/note", s.handleWorkersNoteWrite)
	mux.HandleFunc("GET /api/sessions/{id}/children", s.handleListChildren)
	mux.HandleFunc("POST /api/messages", s.handleSendMessage)
	mux.HandleFunc("POST /api/sessions/{id}/cancel", s.handleCancel)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handleSaveConfig)
	mux.HandleFunc("POST /api/llm/models", s.handleListModels)
	mux.HandleFunc("POST /api/llm/probe", s.handleProbeModel)
	mux.HandleFunc("GET /api/fs/search", s.handleFsSearch)
	mux.HandleFunc("POST /api/fs/open", s.handleFsOpen)
	mux.HandleFunc("GET /api/approvals", s.handleListApprovals)
	mux.HandleFunc("POST /api/approvals/{id}/decision", s.handleApprovalDecision)

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /goal", s.handleGoal)

	s.registerSuperRoutes(mux)

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


func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if s.tryServeUIAsset(w, r) {
		return
	}
	page, err := s.static()
	if err != nil {
		http.Error(w, "ui not available: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	page = injectBootAnim(page, s.bootAnimBootJSON())
	page = injectPluginAssets(page)
	w.Write([]byte(page))
}

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


var userAvatarPrefixes = []string{
	"data:image/jpeg;base64,",
	"data:image/png;base64,",
	"data:image/webp;base64,",
}

func decodeAvatarBody(r *http.Request) (string, error) {
	var req struct {
		Avatar string `json:"avatar"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", errors.New("invalid request body")
	}
	avatar := strings.TrimSpace(req.Avatar)
	if avatar == "" {
		return "", nil
	}
	rest, ok := "", false
	for _, p := range userAvatarPrefixes {
		if strings.HasPrefix(avatar, p) {
			rest, ok = avatar[len(p):], true
			break
		}
	}
	if !ok {
		return "", errors.New("avatar must be a data URL (data:image/jpeg|png|webp;base64,...)")
	}
	raw, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		return "", fmt.Errorf("avatar base64 is invalid: %v", err)
	}
	if len(raw) > 4*1024*1024 {
		return "", errors.New("avatar too large (max 4MB after decode)")
	}
	return avatar, nil
}

func (s *server) saveAvatarConfig(w http.ResponseWriter, avatar string, pick func(*config.AppConfig, string)) {
	cfg := s.be.GetConfig()
	pick(cfg, avatar)
	if _, err := s.be.SaveConfig(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("avatar updated", "bytes", len(avatar))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "avatar": avatar})
}

func (s *server) handleUserAvatarGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.be.GetConfig()
	writeJSON(w, http.StatusOK, map[string]string{"avatar": cfg.UserAvatar})
}

func (s *server) handleUserAvatarSet(w http.ResponseWriter, r *http.Request) {
	avatar, err := decodeAvatarBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.saveAvatarConfig(w, avatar, func(c *config.AppConfig, v string) { c.UserAvatar = v })
}

func (s *server) handleAgentAvatarGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.be.GetConfig()
	writeJSON(w, http.StatusOK, map[string]string{"avatar": cfg.AgentAvatar})
}

func (s *server) handleAgentAvatarSet(w http.ResponseWriter, r *http.Request) {
	avatar, err := decodeAvatarBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.saveAvatarConfig(w, avatar, func(c *config.AppConfig, v string) { c.AgentAvatar = v })
}


func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.be.Status())
}


type SessionSummary struct {
	SessionID string `json:"session_id"`
	ParentID  string `json:"parent_id,omitempty"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Model     string `json:"model,omitempty"`
	Active bool `json:"active"`
	ChildrenActive bool   `json:"children_active"`
	AgentRole      string `json:"agent_role"`
	Mode           string `json:"mode"`
	WorkDir        string `json:"work_dir"`
	ChannelKey     string `json:"channel_key,omitempty"`
	WorkerID         string `json:"worker_id,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
 	MessageCount     int    `json:"message_count"`
	Tokens int64 `json:"tokens"`
	Thinking        string `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	JobsRunning int `json:"jobs_running,omitempty"`
	JobsTotal   int `json:"jobs_total,omitempty"`
}

func (s *server) summarize(r *session.SessionRecord) SessionSummary {
	thinking, effort := reasoningOf(r)
	sum := SessionSummary{
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
		WorkerID:         r.WorkerID,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
		MessageCount:     len(r.Messages),
		Thinking:         thinking,
		ReasoningEffort:  effort,
	}
	sum.JobsRunning, sum.JobsTotal = s.jobCounts(r.SessionID)
	sum.Tokens = tokensOf(r.Metadata)
	return sum
}

func tokensOf(metadata map[string]interface{}) int64 {
	tot, _ := metadata["usage_total"].(map[string]interface{})
	var n int64
	for _, k := range []string{"prompt", "completion"} {
		switch v := tot[k].(type) {
		case float64:
			n += int64(v)
		case int64:
			n += v
		}
	}
	return n
}

func (s *server) summarizeSummary(r session.SessionSummary) SessionSummary {
	sum := SessionSummary{
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
		WorkerID:     r.WorkerID,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		MessageCount: r.MessageCount,
		Tokens:       int64(r.TotalPrompt) + int64(r.TotalCompletion),
	}
	sum.JobsRunning, sum.JobsTotal = s.jobCounts(r.SessionID)
	return sum
}

func (s *server) jobCounts(id string) (running, total int) {
	sess := s.be.Mgr().Peek(id)
	if sess == nil {
		return 0, 0
	}
	reg := sess.Jobs()
	if reg == nil {
		return 0, 0
	}
	for _, sn := range reg.List("") {
		total++
		if sn.Status == job.StatusRunning {
			running++
		}
	}
	return running, total
}

func (s *server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	jobs := []job.Snapshot{}
	running := 0
	if sess := s.be.Mgr().Peek(id); sess != nil {
		if reg := sess.Jobs(); reg != nil {
			jobs = reg.List("")
			for _, sn := range jobs {
				if sn.Status == job.StatusRunning {
					running++
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"jobs":    jobs,
		"running": running,
	})
}

func (s *server) handleGetGoal(w http.ResponseWriter, r *http.Request) {
	sess := s.be.Mgr().Peek(r.PathValue("id"))
	if sess == nil {
		writeErr(w, http.StatusNotFound, "session not loaded")
		return
	}
	snap := sess.GoalState().Snapshot()
	if snap == "" {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(snap))
}

func (s *server) handleJobOutput(w http.ResponseWriter, r *http.Request) {
	sess := s.be.Mgr().Peek(r.PathValue("id"))
	if sess == nil {
		writeErr(w, http.StatusNotFound, "session not loaded")
		return
	}
	reg := sess.Jobs()
	if reg == nil {
		writeErr(w, http.StatusNotFound, "job not found")
		return
	}
	j := reg.Get(r.URL.Query().Get("job"))
	if j == nil {
		writeErr(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"output": j.Output(false)})
}

func (s *server) handleKillJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		JobID  string `json:"job_id"`
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.JobID) == "" {
		writeErr(w, http.StatusBadRequest, "job_id is required")
		return
	}
	sess := s.be.Mgr().Peek(id)
	if sess == nil {
		writeErr(w, http.StatusNotFound, "session not loaded or not found: "+id)
		return
	}
	reg := sess.Jobs()
	if reg == nil {
		writeErr(w, http.StatusNotFound, "no background commands in this session")
		return
	}
	j := reg.Get(req.JobID)
	if j == nil {
		writeErr(w, http.StatusNotFound, "no job with id: "+req.JobID)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "stopped from the UI"
	}
	killed := j.Cancel(reason)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"killed": killed,
		"job_id": req.JobID,
		"status": string(j.Status()),
	})
}

func (s *server) handleClearJobs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cleared := 0
	if sess := s.be.Mgr().Peek(id); sess != nil {
		if reg := sess.Jobs(); reg != nil {
			cleared = reg.Prune(0)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"cleared": cleared})
}

func (s *server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	bucket := session.WorkerAllID
	if q, ok := r.URL.Query()["worker"]; ok {
		bucket = q[0]
		if bucket == "" {
			bucket = session.WorkerUnassignedID
		}
	} else if r.URL.Query().Get("archived") == "1" {
		bucket = session.WorkerArchivedID
	}
	records, err := s.be.Mgr().GetStore().ListBucketSummaries(bucket, limit, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	children, _ := s.be.Mgr().GetStore().ChildIDsByParent()
	var superHide map[string]bool
	if superS != nil {
		superHide = superS.RelatedSessionIDs()
	}
	out := make([]SessionSummary, 0, len(records))
	for _, r := range records {
		if superHide[r.SessionID] {
			continue
		}
		sum := s.summarizeSummary(r)
		if !sum.Active {
			sum.ChildrenActive = childrenRunning(s.be.Hub(), r.SessionID, children)
		}
		out = append(out, sum)
	}
	writeJSON(w, http.StatusOK, out)
}

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
	all := sess.SnapshotMessages()
	sum := s.summarize(rec)
	if !sum.Active {
		if children, err := s.be.Mgr().GetStore().ChildIDsByParent(); err == nil {
			sum.ChildrenActive = childrenRunning(s.be.Hub(), rec.SessionID, children)
		}
	}
	persistFailures, persistErr := sess.PersistHealth()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  sum,
		"messages": messageViews(all),
		"tokens": sess.CalibratedTokenCount(),
		"usage_tokens": tokensOf(rec.Metadata),
		"context_window": s.be.Mgr().ContextWindowFor(rec.ModelKey),
		"last_usage": sess.LastUsage(),
		"persist_failures": persistFailures,
		"persist_error":    persistErr,
	})
}

type messageView struct {
	Role             string              `json:"role"`
	Content          interface{}         `json:"content"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCalls        []provider.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string              `json:"tool_call_id,omitempty"`
	Name             string              `json:"name,omitempty"`
	Notice           string              `json:"notice,omitempty"`
}

func messageViews(msgs []provider.Message) []messageView {
	out := make([]messageView, 0, len(msgs))
	for _, m := range msgs {
		v := messageView{
			Role:             m.Role,
			ReasoningContent: m.ReasoningContent,
			ToolCalls:        m.ToolCalls,
			ToolCallID:       m.ToolCallID,
			Name:             m.Name,
		}
		if len(m.ContentParts) > 0 {
			v.Content = m.ContentParts
		} else {
			v.Content = m.Content
		}
		if kind, ok := session.NoticeKind(m); ok {
			v.Notice = kind
		}
		out = append(out, v)
	}
	return out
}

func (s *server) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	after := int64(0)
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "after 必须是非负整数")
			return
		}
		after = n
	}
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "limit 必须是正整数")
			return
		}
		limit = n
	}
	store := s.be.Mgr().GetStore()
	var events []session.EventRecord
	var err error
	if q.Get("tail_run") == "1" {
		events, err = store.TailRunEvents(id)
	} else {
		events, err = store.ListEvents(id, after, limit)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	lastSeq := after
	if len(events) > 0 {
		lastSeq = events[len(events)-1].Seq
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events":   events,
		"last_seq": lastSeq,
		"active": s.be.Mgr().TaskRunning(id),
	})
}

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

func validReasoningEffort(v string) bool {
	switch v {
	case "off", "low", "high", "max", "xhigh", "medium":
		return true
	}
	return false
}

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

func (s *server) handleSetArchived(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if sess, err := s.be.Mgr().Get(id); err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	var req struct {
		Archived bool `json:"archived"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	target := session.WorkerUnassignedID
	if req.Archived {
		target = session.WorkerArchivedID
	}
	if err := s.be.Mgr().MoveToWorker(id, target); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": id,
		"archived":   req.Archived,
		"worker_id":  target,
	})
}

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


type SendMessageRequest struct {
	SessionID        string   `json:"session_id"`
	Content          string   `json:"content"`
	Images           []string `json:"images,omitempty"`
	Mode             string   `json:"mode,omitempty"`
	Model            string   `json:"model,omitempty"`
	Thinking         string   `json:"thinking,omitempty"`
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`
	WorkerID         string   `json:"worker_id,omitempty"`
}

const (
	maxImagesPerMessage = 4
	maxImageBytes       = 10 * 1024 * 1024
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
	msg.Thinking = strings.TrimSpace(req.Thinking)
	msg.ReasoningEffort = strings.TrimSpace(req.ReasoningEffort)
	msg.WorkerID = strings.TrimSpace(req.WorkerID)
	if req.SessionID != "" && len(req.Images) == 0 && s.be.Hub().ActiveTask(req.SessionID) {
		if sess, err := s.be.Mgr().Get(req.SessionID); err == nil {
			sess.QueueUserMessage(req.Content)
			writeJSON(w, http.StatusAccepted, map[string]string{
				"mode":       "steer",
				"session_id": req.SessionID,
			})
			return
		}
	}
	if err := s.be.Hub().Submit(msg); err != nil {
		w.Header().Set("Retry-After", "2")
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}

	resp := map[string]string{"task_id": msg.ID}
	if req.SessionID != "" {
		resp["session_id"] = req.SessionID
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *server) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	kids := 0
	for _, c := range s.be.Mgr().ListChildrenIDs(id) {
		if s.be.Hub().ActiveTask(c) {
			kids++
		}
	}
	canceled := s.be.Hub().CancelTask(id)
	status := "cancelling"
	if !canceled {
		status = "no_active_task"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            status,
		"canceled":          canceled,
		"children_canceled": kids,
		"session_id":        id,
	})
}


func (s *server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	if s.be.Approvals() == nil {
		writeJSON(w, http.StatusOK, []approval.PendingInfo{})
		return
	}
	writeJSON(w, http.StatusOK, s.be.Approvals().Pending())
}

func (s *server) handleApprovalDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Decision string `json:"decision"`
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


func (s *server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.be.GetConfig())
}

func (s *server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.AppConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if cfg.BootAnim.IsZero() {
		if cur := s.be.GetConfig(); cur != nil {
			cfg.BootAnim = cur.BootAnim
		}
	}
	cfg.BootAnim.Normalize()
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

func (s *server) handleProbeModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL   string `json:"base_url"`
		APIKey    string `json:"api_key"`
		ModelName string `json:"model_name"`
		Provider  string `json:"provider"`
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
	params, err := provider.ProbeModelParams(req.BaseURL, req.APIKey, strings.TrimSpace(req.ModelName), req.Provider)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, params)
}

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


type GoalRequest struct {
	Goal       string `json:"goal"`
	SessionID  string `json:"session_id,omitempty"`
	SessionKey string `json:"session_key,omitempty"`
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
