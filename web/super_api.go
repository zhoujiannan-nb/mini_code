package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/user/mini_code/super"
)

// The super service is attached at startup rather than added to the Backend
// interface: the interface has hand-written fakes across the web tests, and
// the super panel is an optional subsystem — nil means "feature off", which
// every handler answers with 503 instead of a panic.
var superS *super.Service

// AttachSuperService wires the super subsystem into the HTTP layer.
func AttachSuperService(s *super.Service) { superS = s }

func (s *server) registerSuperRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/super/ensure", sRequireSuper(s.handleSuperEnsure))
	mux.HandleFunc("GET /api/super/overview", sRequireSuper(s.handleSuperOverview))
	mux.HandleFunc("POST /api/super/config", sRequireSuper(s.handleSuperConfigWrite))
	mux.HandleFunc("POST /api/super/workers/config", sRequireSuper(s.handleSuperWorkerConfig))
	mux.HandleFunc("POST /api/super/workers/retire", sRequireSuper(s.handleSuperWorkerRetire))
	mux.HandleFunc("POST /api/super/schedules/toggle", sRequireSuper(s.handleSuperScheduleToggle))
	mux.HandleFunc("POST /api/super/schedules/run", sRequireSuper(s.handleSuperScheduleRun))
	mux.HandleFunc("POST /api/super/channel/flush", sRequireSuper(s.handleSuperChannelFlush))
	mux.HandleFunc("POST /api/super/publish", sRequireSuper(s.handleSuperPublish))
	// v9 采集层 / 工单中心
	mux.HandleFunc("POST /api/super/sources/create", sRequireSuper(s.handleSuperSourceCreate))
	mux.HandleFunc("POST /api/super/sources/toggle", sRequireSuper(s.handleSuperSourceToggle))
	mux.HandleFunc("POST /api/super/sources/delete", sRequireSuper(s.handleSuperSourceDelete))
	mux.HandleFunc("POST /api/super/sources/test", sRequireSuper(s.handleSuperSourceTest))
	mux.HandleFunc("POST /api/super/sources/bind", sRequireSuper(s.handleSuperSourceBind))
	mux.HandleFunc("POST /api/super/sources/unbind", sRequireSuper(s.handleSuperSourceUnbind))
	mux.HandleFunc("POST /api/super/sources/seton", sRequireSuper(s.handleSuperSourceSetOn))
	mux.HandleFunc("POST /api/super/tickets/close", sRequireSuper(s.handleSuperTicketClose))
}

// sRequireSuper answers 503 for every super route while the subsystem is off.
func sRequireSuper(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if superS == nil {
			writeErr(w, http.StatusServiceUnavailable, "super 子系统未启用")
			return
		}
		h(w, r)
	}
}

// POST /api/super/ensure — find or create the resident super session; the
// sidebar entry opens it like any chat (the panel renders inside it).
func (s *server) handleSuperEnsure(w http.ResponseWriter, r *http.Request) {
	id, err := superS.EnsureSuper()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"session_id": id})
}

// GET /api/super/overview — one round trip for every panel tab.
func (s *server) handleSuperOverview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, superS.Overview())
}

// POST /api/super/sources/seton — 改源的处理要求(config_json.on_event)。
func (s *server) handleSuperSourceSetOn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		OnEvent string `json:"on_event"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "id 必填")
		return
	}
	if err := superS.SourceSetOnEvent(req.ID, req.OnEvent, "human"); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/super/config — the human boundary settings (audited per field).
func (s *server) handleSuperConfigWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkerCap       *int    `json:"worker_cap"`
		DefaultModel    *string `json:"default_model"`
		WSRoot          *string `json:"ws_root"`
		BatchSize       *int    `json:"batch_size"`
		FlushTimeoutMin *int    `json:"flush_timeout_min"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg := superS.Config()
	if req.WorkerCap != nil {
		cfg.WorkerCap = *req.WorkerCap
	}
	if req.DefaultModel != nil {
		cfg.DefaultModel = strings.TrimSpace(*req.DefaultModel)
	}
	if req.WSRoot != nil {
		cfg.WSRoot = strings.TrimSpace(*req.WSRoot)
	}
	if req.BatchSize != nil {
		cfg.BatchSize = *req.BatchSize
	}
	if req.FlushTimeoutMin != nil {
		cfg.FlushTimeoutMin = *req.FlushTimeoutMin
	}
	writeJSON(w, http.StatusOK, superS.SetConfig(cfg))
}

// POST /api/super/workers/config — per-worker human config (role_note / model).
func (s *server) handleSuperWorkerConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Note  string `json:"note"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	wk, err := superS.UpdateWorkerConfig(req.ID, req.Note, req.Model, "human")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

func (s *server) handleSuperScheduleToggle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.File) == "" {
		writeErr(w, http.StatusBadRequest, "file is required")
		return
	}
	on, err := superS.ToggleSchedule(req.File, "human")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": on})
}

func (s *server) handleSuperScheduleRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.File) == "" {
		writeErr(w, http.StatusBadRequest, "file is required")
		return
	}
	superS.RunScheduleNow(req.File, "human")
	writeJSON(w, http.StatusOK, map[string]string{"status": "triggered"})
}

// POST /api/super/channel/flush — human early-flush of the buffer pool
// (audited as actor=human, "manual").
func (s *server) handleSuperChannelFlush(w http.ResponseWriter, r *http.Request) {
	batchID, n, err := superS.Flush("manual")
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"batch_id": batchID, "count": n})
}

// POST /api/super/publish — external producers (a script that prefers an
// explicit call over stdout) push one message onto the channel.
func (s *server) handleSuperPublish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode    string `json:"mode"`
		Src     string `json:"src"`
		Payload string `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	m, err := superS.Publish(req.Mode, req.Src, req.Payload, "script")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, m)
}

// POST /api/super/workers/retire — 人类退役一个 worker(v9 起 worker 生命周期
// 归系统与人类,Router 无权)。
func (s *server) handleSuperWorkerRetire(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := superS.DeleteWorker(req.ID, "human"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "retired"})
}

// --- v9 采集源 / 工单 ---

func (s *server) handleSuperSourceCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind        string                 `json:"kind"`
		Name        string                 `json:"name"`
		Config      map[string]interface{} `json:"config"`
		PollSec     int                    `json:"poll_sec"`
		BindMode    string                 `json:"bind_mode"`
		BindSession string                 `json:"bind_session"`
		TTL         string                 `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cfgJSON := "{}"
	if req.Config != nil {
		b, err := json.Marshal(req.Config)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "config 无法编码")
			return
		}
		cfgJSON = string(b)
	}
	src, err := superS.CreateSource(req.Kind, req.Name, cfgJSON, req.PollSec, req.BindMode, req.BindSession, req.TTL, "human")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, src)
}

func (s *server) handleSuperSourceToggle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	on, err := superS.ToggleSource(req.ID, "human")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": on})
}

func (s *server) handleSuperSourceDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := superS.DeleteSource(req.ID, "human"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *server) handleSuperSourceTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	sum, err := superS.TestSource(req.ID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"summary": sum})
}

func (s *server) handleSuperSourceBind(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Session string `json:"session"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.Session) == "" {
		writeErr(w, http.StatusBadRequest, "id 与 session 必填")
		return
	}
	if err := superS.BindThread(req.ID, req.Session, "human"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "bound"})
}

func (s *server) handleSuperSourceUnbind(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Forever bool   `json:"forever"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := superS.Unbind(req.ID, req.Forever, "human"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "unsubscribed"})
}

func (s *server) handleSuperTicketClose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := superS.CloseTicket(req.ID, "human"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "closed"})
}
