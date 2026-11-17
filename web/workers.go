package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/user/mini_code/session"
)


func (s *server) workerService() *session.WorkerService {
	m := s.be.Mgr()
	if m == nil {
		return nil
	}
	return m.Workers()
}

type workerView struct {
	WorkerID string `json:"worker_id"`
	Name     string `json:"name"`
	Summary  string `json:"summary"`
	WorkDir  string `json:"work_dir"`
	NoteVer  int    `json:"note_ver"`
	Builtin  bool   `json:"builtin"`
	Reserved  bool   `json:"reserved"`
	Sessions  int    `json:"sessions"`
	Active    int    `json:"active"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

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
	tally := s.activeByBucket()
	for _, rec := range workers {
		out = append(out, workerView{
			WorkerID:  rec.WorkerID,
			Name:      rec.Name,
			Summary:   rec.Summary,
			WorkDir:   rec.WorkDir,
			NoteVer:   rec.NoteVer,
			Builtin:   rec.Builtin,
			Reserved:  session.IsReservedWorker(rec.WorkerID),
			Sessions:  rec.Sessions,
			Active:    tally[rec.WorkerID],
			CreatedAt: rec.CreatedAt,
			UpdatedAt: rec.UpdatedAt,
		})
	}
	unassigned, err := s.be.Mgr().GetStore().ListBucketSummaries(session.WorkerUnassignedID, 1000, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"workers": out,
		"unassigned":        len(unassigned),
		"unassigned_active": tally[session.WorkerUnassignedID],
	})
}

func (s *server) activeByBucket() map[string]int {
	store := s.be.Mgr().GetStore()
	rows, err := store.ListBucketSummaries(session.WorkerAllID, 1000, 0)
	if err != nil {
		return map[string]int{}
	}
	children, _ := store.ChildIDsByParent()
	hub := s.be.Hub()
	out := make(map[string]int)
	for _, r := range rows {
		if !hub.ActiveTask(r.SessionID) && !childrenRunning(hub, r.SessionID, children) {
			continue
		}
		out[r.WorkerID]++
	}
	return out
}

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
		"note_ver":  rec.NoteVer,
	})
}

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

func (s *server) handleWorkersRename(w http.ResponseWriter, r *http.Request) {
	wsvc := s.workerService()
	if wsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "worker 功能未启用")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := wsvc.Rename(id, name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"worker_id": id, "name": name})
}

func (s *server) handleWorkersDelete(w http.ResponseWriter, r *http.Request) {
	wsvc := s.workerService()
	if wsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "worker 功能未启用")
		return
	}
	id := r.PathValue("id")
	if id == session.WorkerArchivedID {
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
	content, ver, err := wsvc.ReadNote(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"worker_id": rec.WorkerID,
		"ver":       ver,
		"content":   content,
	})
}

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
	ver, err := wsvc.WriteNote(id, req.Content)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"worker_id": id, "ver": ver, "status": "saved"})
}

