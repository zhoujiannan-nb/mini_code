package web

// Super-Agent(lite) panel API. The subsystem has exactly one feature —
// 定时任务 — so the surface is ensure / overview / schedules CRUD. Everything
// heavier (workers, channel, sources, tickets, publish) retired with v10.

import (
	"encoding/json"
	"net/http"

	"github.com/user/mini_code/super"
)

// superS is the one stop-shop super service wired at startup.
var superS *super.Service

// AttachSuperService wires the super service into the HTTP layer.
func AttachSuperService(s *super.Service) { superS = s }

func (s *server) registerSuperRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/super/ensure", sRequireSuper(s.handleSuperEnsure))
	mux.HandleFunc("GET /api/super/overview", sRequireSuper(s.handleSuperOverview))
	mux.HandleFunc("POST /api/super/schedules/create", sRequireSuper(s.handleSuperScheduleCreate))
	mux.HandleFunc("POST /api/super/schedules/delete", sRequireSuper(s.handleSuperScheduleDelete))
	mux.HandleFunc("POST /api/super/schedules/toggle", sRequireSuper(s.handleSuperScheduleToggle))
	mux.HandleFunc("POST /api/super/schedules/run", sRequireSuper(s.handleSuperScheduleRun))
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

// GET /api/super/overview — one round trip for the whole panel.
func (s *server) handleSuperOverview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, superS.Overview())
}

// POST /api/super/schedules/create — 建定时任务（到点发一句话到绑定会话）。
func (s *server) handleSuperScheduleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Text    string `json:"text"`
		Session string `json:"session"`
		Every   string `json:"every"`      // "30m"/"2h"…（与 every_sec 二选一）
		EverySec int   `json:"every_sec"` // 面板直填秒数优先
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	everySec := req.EverySec
	if everySec <= 0 && req.Every != "" {
		n, err := super.ParseEvery(req.Every)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		everySec = n
	}
	sc, err := superS.CreateSchedule(req.Name, req.Text, req.Session, everySec, "human")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

// POST /api/super/schedules/delete
func (s *server) handleSuperScheduleDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "id 必填")
		return
	}
	if err := superS.DeleteSchedule(req.ID, "human"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/super/schedules/toggle
func (s *server) handleSuperScheduleToggle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "id 必填")
		return
	}
	on, err := superS.ToggleSchedule(req.ID, "human")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "enabled": on})
}

// POST /api/super/schedules/run — 立即投递一次。
func (s *server) handleSuperScheduleRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "id 必填")
		return
	}
	if err := superS.RunScheduleNow(req.ID, "human"); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
