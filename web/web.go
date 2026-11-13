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
	"errors"
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
	"github.com/user/mini_code/job"
	"github.com/user/mini_code/plugin"
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
	// Start the process plugins that asked for autostart. It runs in the
	// background so a slow plugin (a python service taking 3s to bind its port)
	// never delays the UI from coming up. A failure is logged and ignored: a
	// broken plugin must not stop the agent from starting.
	go plugin.DefaultProcs().StartAutostart()
	mux := http.NewServeMux()

	// --- web UI ---
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /index.html", s.handleIndex)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.HandleFunc("GET /avatars/{name}", s.handleAvatar)

	// --- plugins (file-based drop-ins under ~/.mini_code/plugins) ---
	mux.HandleFunc("GET /plugins/", s.handlePluginAsset)
	// Plugin centre: one list for the settings page (installed + switched off),
	// plus toggles, uninstall and process control for kind-1 plugins.
	mux.HandleFunc("GET /api/plugins", s.handlePluginCentreList)
	mux.HandleFunc("POST /api/plugins/{id}/toggle", s.handlePluginToggle)
	mux.HandleFunc("DELETE /api/plugins/{id}", s.handlePluginUninstall)
	mux.HandleFunc("GET /api/plugins/processes", s.handlePluginProcessList)
	mux.HandleFunc("POST /api/plugins/{id}/process", s.handlePluginProcessAction)
	mux.HandleFunc("GET /api/plugins/{id}/logs", s.handlePluginProcessLogs)
	mux.HandleFunc("GET /api/plugins/catalog", s.handlePluginCatalog)
	mux.HandleFunc("POST /api/plugins/install", s.handlePluginInstall)
	mux.HandleFunc("GET /api/plugins/prompt", s.handlePluginMakePrompt)

	// --- startup animation clips (untrusted third-party HTML, see bootanim.go) ---
	//
	// A clip IS an appearance plugin with a "boot" block, so there is no separate
	// installer here anymore: clips are installed, toggled and uninstalled
	// through /api/plugins/*. Only the document route and the "what is
	// installed" list survive.
	mux.HandleFunc("GET /bootanim/", s.handleBootAnimAsset)
	mux.HandleFunc("GET /api/bootanim", s.handleBootAnimList)
	// The play settings (on/off, when, speed, mute) are edited from a boot
	// plugin's detail pane, so they get their own tiny endpoint: posting the
	// whole config from there would mean shipping the model list and the API
	// keys just to turn a checkbox.
	mux.HandleFunc("PUT /api/bootanim", s.handleBootAnimSave)

	// --- API ---
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
	mux.HandleFunc("POST /api/sessions/{id}/compact", s.handleCompactSession)
	mux.HandleFunc("POST /api/sessions/{id}/mode", s.handleSetMode)
	mux.HandleFunc("POST /api/sessions/{id}/context-reminders", s.handleSetContextReminders)
	mux.HandleFunc("POST /api/sessions/{id}/model", s.handleSetModel)
	mux.HandleFunc("POST /api/sessions/{id}/reasoning", s.handleSetReasoning)
	mux.HandleFunc("GET /api/sessions/{id}/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/sessions/{id}/jobs/output", s.handleJobOutput)
	mux.HandleFunc("POST /api/sessions/{id}/jobs/kill", s.handleKillJob)
	mux.HandleFunc("POST /api/sessions/{id}/jobs/clear", s.handleClearJobs)
	mux.HandleFunc("POST /api/sessions/{id}/rename", s.handleRenameSession)
	mux.HandleFunc("POST /api/sessions/{id}/archive", s.handleSetArchived)
	// Workers (工作大类): the sidebar switcher and the shared 公约 editor.
	// (Sessions can no longer be moved between workers: membership is fixed at
	// creation, so worker 目录 and session 工作目录 stay one folder.)
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
	mux.HandleFunc("POST /api/llm/probe", s.handleProbeModel)
	mux.HandleFunc("GET /api/fs/search", s.handleFsSearch)
	// A path in a chat message is clickable: the UI posts it here and the OS
	// opens it with its default application (see fsopen.go).
	mux.HandleFunc("POST /api/fs/open", s.handleFsOpen)
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
	// CSS/JS/SVG live as separate embedded files so they stay editable and
	// diffable; "/" itself still serves the single HTML document. Requests
	// with a whitelisted asset extension are answered here (before the page)
	// and never fall through to the HTML below.
	if s.tryServeUIAsset(w, r) {
		return
	}
	page, err := s.static()
	if err != nil {
		http.Error(w, "ui not available: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// UI 是内嵌的单文件:升级 exe 后必须拿到新页面,
	// 否则 WebView2/浏览器可能继续用本地缓存的旧 UI(桌面端缓存是持久化的)
	w.Header().Set("Cache-Control", "no-cache")
	// 开机动画的配置直接注入页面里的占位 script:内联播放器在首帧之前就知道
	// 要不要播、播哪个主题,不必等一次 /api 往返,首屏不会先白一下再盖黑幕。
	page = injectBootAnim(page, s.bootAnimBootJSON())
	// 插件皮肤/壁纸:同样在服务端注入,插件是纯落盘即生效(不改页面、不重编译)。
	// 注入点在内置样式之后,所以皮肤可以覆盖内置主题。
	page = injectPluginAssets(page)
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

// decodeAvatarBody reads {"avatar": "<data URL>"} out of an avatar POST and
// validates it. An empty avatar means "clear it back to the built-in default".
// Shared by the user-avatar and the agent-avatar endpoints so both accept
// exactly the same shapes and limits.
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

// saveAvatarConfig writes an avatar into the live config. pick names which
// field to set, so the two endpoints share the save/error/logging path.
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

// The agent has a single avatar for all sessions (see ui/js/groups.js:
// agentAvatarBox): empty config value = the built-in ink tile with "M".
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
	Active bool `json:"active"`
	// ChildrenActive is true while a (transitively) descendant sub-agent
	// session has a live task. It is a SEPARATE flag from Active on purpose:
	// background sub-agents run in their own child sessions, so the parent is
	// idle and must stay able to take new messages. The UI paints this as
	// "运行中" in the sidebar and lights the stop button with it, but it must
	// never lock the composer — that is what Active is for.
	ChildrenActive bool   `json:"children_active"`
	AgentRole      string `json:"agent_role"`
	Mode           string `json:"mode"`
	WorkDir        string `json:"work_dir"`
	ChannelKey     string `json:"channel_key,omitempty"`
	// WorkerID is the worker (工作大类) the session belongs to; "" = 未分配.
	WorkerID         string `json:"worker_id,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
 	MessageCount     int    `json:"message_count"`
	// Tokens is the session-wide total (prompt+completion over every provider
	// call); 0 for sessions predating the usage counter.
	Tokens int64 `json:"tokens"`
	ContextReminders bool   `json:"context_reminders"` // experimental context-usage notices (session-level, default on)
	// Thinking / ReasoningEffort are the per-session thinking-depth override
	// (composer control); empty = the model's configured default.
	Thinking        string `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// JobsRunning / JobsTotal describe this session's background commands in
	// THIS process (the live exec registry): JobsRunning counts the live ones,
	// JobsTotal is everything not yet pruned. JobsRunning>0 is the sidebar's
	// "后台运行中" flag; JobsTotal>0 is what makes the jobs trigger appear.
	// Like Active they are live-only by design: a session not loaded here has
	// no tracked work at all, which is correctly 0/0.
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
		ContextReminders: contextRemindersOf(r),
		Thinking:         thinking,
		ReasoningEffort:  effort,
	}
	sum.JobsRunning, sum.JobsTotal = s.jobCounts(r.SessionID)
	sum.Tokens = tokensOf(r.Metadata)
	return sum
}

// tokensOf sums metadata.usage_total (written by recordLastUsage) from a
// loaded record; 0 when the session has no usage counter yet.
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

// summarizeSummary builds the same API entry from a lightweight store row
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

// jobCounts reports how many of the session's background commands are running
// and how many are visible at all, read from the live registry of the loaded
// session. An unloaded session has no registry (its work is not tracked by this
// process), so it reports 0/0 — which is the true answer, not a missing one.
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

// handleListJobs returns the session's background commands (live snapshot of
// the exec job registry). The UI polls this while the jobs popover is open, so
// the roster is fetched only while somebody is actually watching.
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

// handleJobOutput returns the buffered output of one background command for
// the UI's expand row. It reads without consuming (consume=false), so the
// model's own job_output reads and the settlement notice stay unaffected.
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

// handleKillJob stops one running background command on the human's behalf —
// the UI twin of the model's job_kill tool. It reports whether the registry
// accepted the request; the job settles to killed asynchronously (the process
// tree is being torn down), so the UI converges the row by polling instead of
// trusting this immediate answer.
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

// handleClearJobs drops every settled background command from the session's
// registry (Registry.Prune(0) — running commands are never touched), the UI
// twin of the panel's Clear button. Clearing must reach the registry, not just
// the popover: the roster is re-fetched from here on every poll, so a purely
// local wipe would resurrect the rows the human just dismissed. A session with
// no registry has nothing to clear, which is a success with cleared=0 rather
// than a 404 — the click only ever races a session teardown.
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
	// Root sessions only: sub-agent sessions (parent_id set) are internal.
	// The lightweight query skips conversation payloads, so the list stays
	// cheap enough for the UI to poll it on every SSE signal.
	// ?worker=<id> selects one worker's bucket; ?worker=(empty) is the
	// un-assigned one; the default is every visible session. ?archived=1 stays
	// as a compatibility alias for the built-in 已归档 bucket.
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
	// 子 agent 在独立子会话里跑:父会话自己的回合结束后,只要(孙)子会话
	// 还有活动任务,父会话在侧边栏也要显示"运行中",并允许从这里一键停止
	// (CancelTask 会级联取消子任务)。这是 children_active,不是 active:
	// 父会话此刻是空闲的,输入框必须继续能用。查询失败时只剩仅看父会话自身。
	children, _ := s.be.Mgr().GetStore().ChildIDsByParent()
	out := make([]SessionSummary, 0, len(records))
	for _, r := range records {
		sum := s.summarizeSummary(r)
		if !sum.Active {
			sum.ChildrenActive = childrenRunning(s.be.Hub(), r.SessionID, children)
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
	all := sess.SnapshotMessages()
	sum := s.summarize(rec)
	if !sum.Active {
		// 与列表一致:后台子 agent 还在跑时,主会话按"有子任务在跑"返回
		// (前端据此恢复停止按钮;syncRunning 轮询也会随之收敛)。输入框
		// 不受影响——主会话自己的回合已经结束了。
		if children, err := s.be.Mgr().GetStore().ChildIDsByParent(); err == nil {
			sum.ChildrenActive = childrenRunning(s.be.Hub(), rec.SessionID, children)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  sum,
		"messages": messageViews(all),
		// Token estimate for the composer footer (xx/context_window). Counted
		// over the full context, worker notices included.
		"tokens": util.CountMessagesTokens(all),
		// Context window of THIS session's model (the token bar must track
		// the per-session model, not the global default).
		"context_window": s.be.Mgr().ContextWindowFor(rec.ModelKey),
		// Last per-LLM-call usage report persisted in the session (DB):
		// the UI restores the composer stats (cache-hit + output speed)
		// from it on session open / page refresh. null when the session
		// has no recorded LLM call yet.
		"last_usage": sess.LastUsage(),
	})
}

// messageView is one transcript row for the browser: the provider fields the UI
// reads, plus `notice` — the kind of runtime-injected context that arrives as a
// role:"user" turn (worker 公约, context-usage reminder, sub-agent settlement,
// compaction instruction or summary, continue-guard steer). Without the label
// the page paints every one of them as the human speaking; the origin lives in
// session.NoticeKind and the message bytes stay untouched, so both the provider
// prefix cache and old sessions keep reading the same text.
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

// handleSessionEvents serves the session event ledger (session_event):
// append-only facts about every run — turns, messages, tool calls and
// results, compactions, the task outcome. The messages column stays the
// projection the model reads; this endpoint is the truth the live UI
// reconciles against after an SSE gap.
//
//	GET /api/sessions/{id}/events            first 500 events
//	GET /api/sessions/{id}/events?after=SEQ  page forward
//	GET /api/sessions/{id}/events?limit=N    larger page (max 5000)
//	GET /api/sessions/{id}/events?tail_run=1 only the current run: everything
//	                                         after the last task/end
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
		// Whether a task loop is live in THIS process right now — the
		// resync uses it to decide between "keep waiting" and "freeze".
		"active": s.be.Mgr().TaskRunning(id),
	})
}

// contextRemindersOf reads the context-usage notices flag from a session
// record's metadata; an absent key keeps the default: enabled.
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

// handleSetArchived is the compatibility alias of "移动到 Worker": the archived
// flag is now the built-in 已归档 bucket, so an old client (or a bookmarked call)
// still works and both spellings keep one source of truth. Archived sessions
// stay fully functional, and because the built-in bucket carries no directory
// and no convention file, the move changes nothing the model sees — the prefix
// stays byte-identical (cache-safe).
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
	Images           []string `json:"images,omitempty"`            // data URLs (data:image/...;base64,...); the model sees them on the first turn
	Mode             string   `json:"mode,omitempty"`              // permission mode for a NEW session (full / cautious)
	Model            string   `json:"model,omitempty"`             // model name for a NEW session (config key); ignored for existing sessions
	ContextReminders *bool    `json:"context_reminders,omitempty"` // new-session preference; nil = default (on)
	Thinking         string   `json:"thinking,omitempty"`          // thinking switch for a NEW session; ignored for existing sessions
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`  // thinking depth for a NEW session; ignored for existing sessions
	WorkerID         string   `json:"worker_id,omitempty"`         // worker (工作大类) for a NEW session; ignored for existing sessions
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
	msg.WorkerID = strings.TrimSpace(req.WorkerID)
	s.be.Hub().Submit(msg)

	resp := map[string]string{"task_id": msg.ID}
	if req.SessionID != "" {
		resp["session_id"] = req.SessionID
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *server) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Count the live sub-agent jobs before the cancel cascades: the UI has to
	// tell "nothing was running" from "only background sub-agents were" (the
	// parent's own turn had already ended, so canceled=false there too).
	kids := 0
	for _, c := range s.be.Mgr().ListChildrenIDs(id) {
		if s.be.Hub().ActiveTask(c) {
			kids++
		}
	}
	canceled := s.be.Hub().CancelTask(id)
	status := "cancelling"
	if !canceled {
		// No running task in this process: the session is idle (or the task
		// belongs to a different server instance). The UI uses this to tell
		// the user that there is nothing to stop.
		status = "no_active_task"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            status,
		"canceled":          canceled,
		"children_canceled": kids,
		"session_id":        id,
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
	// A config posted by a UI that predates the boot-anim block carries an
	// all-zero one: keep the live setting instead of wiping it, and normalize
	// whatever was sent so a missing mode/speed is filled rather than rejected.
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

// handleProbeModel asks the serving framework what it knows about one model:
// context window, output cap and default sampling parameters (temperature /
// top_p / top_k). It backs the settings dialog's "一键获取" button. Anything the
// deployment does not report is simply absent from the response, and the UI
// leaves that config field empty — an empty field is never sent to the model,
// so the framework's own default applies.
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
