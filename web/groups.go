package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/user/mini_code/session"
)

// --- groups (group chats) ---
//
// A group is a public channel plus tools (see session.GroupService). These
// handlers cover the web UI: create/list/open groups, manage membership
// (join/leave/rename member), edit the shared background, and post user
// messages to the group channel (whole group or one @member).

// groupService returns the wired group service (nil = not wired).
func (s *server) groupService() *session.GroupService { return s.be.Groups() }

// groupMemberView is a group member as shown in the UI: identity + live
// session state (running/idle).
type groupMemberView struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status"` // running | idle | offline
	Avatar    string `json:"avatar,omitempty"`
}

func (s *server) groupMemberViews(g *session.GroupRecord) []groupMemberView {
	out := make([]groupMemberView, 0, len(g.Members))
	for _, m := range g.Members {
		v := groupMemberView{SessionID: m.SessionID, Name: m.Name, Status: "idle", Avatar: m.Avatar}
		if sess, err := s.be.Mgr().Get(m.SessionID); err == nil && sess != nil {
			v.Title = sess.Record().Title
			// Hub task registry = "running right now, on this server".
			// The persisted status can be stale after a restart.
			if s.be.Hub().ActiveTask(m.SessionID) {
				v.Status = "running"
			}
		} else {
			v.Status = "offline"
		}
		out = append(out, v)
	}
	return out
}

func (s *server) handleGroupsList(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	groups, err := gsvc.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(groups))
	for i := range groups {
		out = append(out, map[string]interface{}{
			"group_id":     groups[i].GroupID,
			"title":        groups[i].Title,
			"member_count": len(groups[i].Members),
			"members":      s.groupMemberViews(&groups[i]),
			"updated_at":   groups[i].UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleGroupsCreate(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	var req struct {
		Title      string `json:"title"`
		Background string `json:"background"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rec, err := gsvc.Create(req.Title, req.Background)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"group_id": rec.GroupID,
		"title":    rec.Title,
		"members":  s.groupMemberViews(rec),
	})
}

// handleGroupsDetail returns the group, live member states and the recent
// channel log (the group view).
func (s *server) handleGroupsDetail(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	id := r.PathValue("id")
	rec, err := gsvc.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	log, err := gsvc.Log(id, 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group": map[string]interface{}{
			"group_id":   rec.GroupID,
			"title":      rec.Title,
			"background": rec.Background,
			"updated_at": rec.UpdatedAt,
		},
		"members":  s.groupMemberViews(rec),
		"messages": log,
	})
}

func (s *server) handleGroupsSetBackground(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Background string `json:"background"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := gsvc.SetBackground(id, req.Background); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"group_id": id, "status": "ok"})
}

func (s *server) handleGroupsRename(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := gsvc.Rename(id, req.Title); err != nil {
		st := http.StatusBadRequest
		if strings.Contains(err.Error(), "不存在") {
			st = http.StatusNotFound
		}
		writeErr(w, st, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"group_id": id, "title": strings.TrimSpace(req.Title)})
}

func (s *server) handleGroupsDelete(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	id := r.PathValue("id")
	if err := gsvc.DeleteGroup(id); err != nil {
		st := http.StatusBadRequest
		if strings.Contains(err.Error(), "不存在") {
			st = http.StatusNotFound
		}
		writeErr(w, st, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"group_id": id, "status": "deleted"})
}

func (s *server) handleGroupsJoin(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	id := r.PathValue("id")
	var req struct {
		SessionID string `json:"session_id"`
		Name      string `json:"name"` // empty = random 哈基-N号
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		writeErr(w, http.StatusBadRequest, "session_id 不能为空")
		return
	}
	member, err := gsvc.Join(id, req.SessionID, req.Name)
	if err != nil {
		st := http.StatusBadRequest
		if strings.Contains(err.Error(), "已在群聊") {
			st = http.StatusConflict
		}
		writeErr(w, st, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group_id": id,
		"member":   map[string]string{"session_id": member.SessionID, "name": member.Name},
	})
}

func (s *server) handleGroupsLeave(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	if err := gsvc.Leave(id, sid); err != nil {
		st := http.StatusBadRequest
		if strings.Contains(err.Error(), "不存在") {
			st = http.StatusNotFound
		}
		writeErr(w, st, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"group_id": id, "session_id": sid, "status": "left"})
}

func (s *server) handleGroupsMemberRename(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := gsvc.RenameMember(id, sid, req.Name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"group_id": id, "session_id": sid, "status": "renamed"})
}

// handleGroupsMemberAvatar changes a member's in-group avatar (a001..a100).
func (s *server) handleGroupsMemberAvatar(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	var req struct {
		Avatar string `json:"avatar"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := gsvc.SetMemberAvatar(id, sid, strings.TrimSpace(req.Avatar)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"group_id": id, "session_id": sid, "status": "avatar_updated"})
}

// handleGroupsSend posts a user message to the group channel and delivers it
// to the addressed members (or the whole group).
func (s *server) handleGroupsSend(w http.ResponseWriter, r *http.Request) {
	gsvc := s.groupService()
	if gsvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "群聊功能未启用")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Content string `json:"content"`
		Target  string `json:"target"` // member name, or ""/group/all = whole group
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	n, err := gsvc.UserMessage(id, req.Content, req.Target)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"group_id": id,
		"status":   "delivered",
		"targets":  n,
	})
}
