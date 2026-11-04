package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/user/mini_code/session"
)

// --- workers (工作大类) ---
//
// A worker is one line of work: a shared workspace directory plus a shared
// convention file (worker.md) that every session of the worker is told to
// follow. These handlers back the sidebar's worker switcher, the "移动到 Worker"
// action, and the convention editor. See session.WorkerService for the rules
// (in particular why a session only ever reads its own convention snapshot).

// workerService returns the wired worker service (nil = not wired).
func (s *server) workerService() *session.WorkerService {
	m := s.be.Mgr()
	if m == nil {
		return nil
	}
	return m.Workers()
}

// workerView is one worker bucket as the sidebar needs it.
type workerView struct {
	WorkerID  string `json:"worker_id"`
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	WorkDir   string `json:"work_dir"`
	NotePath  string `json:"note_path"`
	Builtin   bool   `json:"builtin"`
	Sessions  int    `json:"sessions"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// handleWorkersList returns the buckets plus the count of unassigned sessions.
// The switcher builds its own list out of this ("全部" is a client-side view,
// "未分配" is the unassigned count).
func (s *server) handleWorkersList(w http.ResponseWriter, r *http.Request) {
	wsvc := s.workerService()
	if wsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "worker 功能未启用")
		return
	}
	workers, err := wsvc.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]workerView, 0, len(workers))
	for _, rec := range workers {
		out = append(out, workerView{
			WorkerID:  rec.WorkerID,
			Name:      rec.Name,
			Summary:   rec.Summary,
			WorkDir:   rec.WorkDir,
			NotePath:  rec.NotePath,
			Builtin:   rec.Builtin,
			Sessions:  rec.Sessions,
			CreatedAt: rec.CreatedAt,
			UpdatedAt: rec.UpdatedAt,
		})
	}
	// The unassigned count comes from the same lightweight bucket query the
	// list uses, so the two can never disagree.
	unassigned, err := s.be.Mgr().GetStore().ListBucketSummaries(session.WorkerUnassignedID, 1000, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"workers":    out,
		"unassigned": len(unassigned),
	})
}

// handleWorkersCreate makes a worker: name/summary, an optional base directory
// (default: the server's own working directory), an optional copy_from to seed
// the shared workspace from an existing folder or file. When session_id is
// given the new session joins immediately (that is the "会话里让 agent 建" path
// and the "新建会话前先建 worker" path alike).
func (s *server) handleWorkersCreate(w http.ResponseWriter, r *http.Request) {
	wsvc := s.workerService()
	if wsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "worker 功能未启用")
		return
	}
	var req struct {
		Name      string `json:"name"`
		Summary   string `json:"summary"`
		BaseDir   string `json:"base_dir"`
		CopyFrom  string `json:"copy_from"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.BaseDir == "" {
		req.BaseDir = s.be.Hub().WorkDirFor("")
	}
	rec, err := wsvc.Create(req.Name, req.Summary, req.BaseDir, req.CopyFrom)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.SessionID != "" {
		if err := wsvc.Move(req.SessionID, rec.WorkerID); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"worker_id": rec.WorkerID,
				"name":      rec.Name,
				"work_dir":  rec.WorkDir,
				"note_path": rec.NotePath,
				"warning":   "worker 已创建,但会话加入失败: " + err.Error(),
			})
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"worker_id": rec.WorkerID,
		"name":      rec.Name,
		"summary":   rec.Summary,
		"work_dir":  rec.WorkDir,
		"note_path": rec.NotePath,
	})
}

// handleWorkersSummary rewrites a worker's summary (the blurb other sessions
// read when they decide where they belong).
func (s *server) handleWorkersSummary(w http.ResponseWriter, r *http.Request) {
	wsvc := s.workerService()
	if wsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "worker 功能未启用")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Summary string `json:"summary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := wsvc.UpdateSummary(id, req.Summary); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"worker_id": id, "summary": strings.TrimSpace(req.Summary)})
}

// handleWorkersDelete deletes a worker together with every session in it (the
// workspace directory on disk is left alone).
func (s *server) handleWorkersDelete(w http.ResponseWriter, r *http.Request) {
	wsvc := s.workerService()
	if wsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "worker 功能未启用")
		return
	}
	id := r.PathValue("id")
	if id == session.WorkerArchivedID {
		// Deleting the bucket is fine, but the sessions in it are the old
		// archive: let the user do that deliberately, session by session.
		writeErr(w, http.StatusBadRequest, "已归档是内置分桶,不能整体删除;请逐个恢复或删除里面的会话")
		return
	}
	n, err := wsvc.Delete(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"worker_id": id, "deleted_sessions": n})
}

// handleWorkersNoteGet returns the convention file (公约) as it is on disk —
// which is deliberately NOT what a running session sees (those carry a
// snapshot; see session.WorkerService.RefreshNoteSnapshot).
func (s *server) handleWorkersNoteGet(w http.ResponseWriter, r *http.Request) {
	wsvc := s.workerService()
	if wsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "worker 功能未启用")
		return
	}
	id := r.PathValue("id")
	rec, err := wsvc.Get(id)
	if err != nil || rec == nil {
		writeErr(w, http.StatusNotFound, "worker not found: "+id)
		return
	}
	content, err := wsvc.ReadNote(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"worker_id": id,
		"path":      rec.NotePath,
		"content":   content,
	})
}

// handleWorkersNoteWrite saves the convention file. A human editing it through
// the sidebar IS the authorization (unlike an agent's write, which the runtime
// gate always confirms). Sessions keep their snapshots; refresh_one / refresh
// below are what makes a session pick the new text up.
func (s *server) handleWorkersNoteWrite(w http.ResponseWriter, r *http.Request) {
	wsvc := s.workerService()
	if wsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "worker 功能未启用")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := wsvc.WriteNote(id, req.Content); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"worker_id": id, "status": "saved"})
}

// handleSessionsMoveWorker is the "移动到 Worker: xx" action: worker_id "" (or
// "*") moves the session back to 未分配. The move rewrites the session's working
// directory, so a session that already has a conversation recomputes its prompt
// prefix on its next request — the UI says so in the confirmation.
func (s *server) handleSessionsMoveWorker(w http.ResponseWriter, r *http.Request) {
	wsvc := s.workerService()
	if wsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "worker 功能未启用")
		return
	}
	id := r.PathValue("id")
	if sess, err := s.be.Mgr().Get(id); err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	var req struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.WorkerID != session.WorkerUnassignedID && req.WorkerID != session.WorkerAllID {
		rec, err := wsvc.Get(req.WorkerID)
		if err != nil || rec == nil {
			writeErr(w, http.StatusNotFound, "worker not found: "+req.WorkerID)
			return
		}
	}
	if err := s.be.Mgr().MoveToWorker(id, req.WorkerID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"session_id": id, "worker_id": req.WorkerID})
}

// handleSessionsWorkerRefresh reloads one session's convention snapshot from
// disk (the sidebar's "刷新公约" button). Explicit by design: it is the only way
// a live session picks up an edited 公约, and it costs that session one prefix
// recompute.
func (s *server) handleSessionsWorkerRefresh(w http.ResponseWriter, r *http.Request) {
	wsvc := s.workerService()
	if wsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "worker 功能未启用")
		return
	}
	id := r.PathValue("id")
	if sess, err := s.be.Mgr().Get(id); err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	changed, err := wsvc.RefreshNoteSnapshot(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"session_id": id, "changed": changed})
}
