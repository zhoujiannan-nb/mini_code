package web

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/user/mini_code/approval"
	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/session"
)

// TestHandleAvatar verifies the embedded group avatar endpoint serves the
// SVG with the right content type and rejects non-avatar file names.
func TestHandleAvatar(t *testing.T) {
	s := &server{}

	req := httptest.NewRequest("GET", "/avatars/a001.svg", nil)
	req.SetPathValue("name", "a001.svg")
	rec := httptest.NewRecorder()
	s.handleAvatar(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Fatalf("content-type = %q, want image/svg+xml", ct)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "<svg") {
		t.Fatalf("body does not look like svg: %.60q", body)
	}

	for _, name := range []string{"../index.html", "index.html", "a1.svg", "a1000.svg", "a001.ico"} {
		req := httptest.NewRequest("GET", "/avatars/"+name, nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		s.handleAvatar(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("name %q: status = %d, want 404", name, rec.Code)
		}
	}
}

// avatarFakeBE is a minimal Backend for the user-avatar endpoints: it only
// backs the config get/save pair, keeping the persisted pointer alive.
type avatarFakeBE struct {
	cfg *config.AppConfig
}

func (f *avatarFakeBE) Hub() *channels.Hub            { return nil }
func (f *avatarFakeBE) Mgr() *session.SessionManager  { return nil }
func (f *avatarFakeBE) Groups() *session.GroupService { return nil }
func (f *avatarFakeBE) Approvals() *approval.Service  { return nil }
func (f *avatarFakeBE) Status() StatusInfo            { return StatusInfo{} }
func (f *avatarFakeBE) GetConfig() *config.AppConfig  { return f.cfg }
func (f *avatarFakeBE) SaveConfig(cfg *config.AppConfig) (*SaveResult, error) {
	f.cfg = cfg
	return &SaveResult{Message: "saved"}, nil
}

// TestUserAvatarSetGet verifies the user-avatar API: set -> persisted in the
// config -> readable back; rejects a bad data-URL prefix, invalid base64 and
// oversize payloads; an empty string clears the avatar.
func TestUserAvatarSetGet(t *testing.T) {
	be := &avatarFakeBE{cfg: &config.AppConfig{}}
	s := &server{be: be}

	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'})
	req := httptest.NewRequest("POST", "/api/avatar", strings.NewReader(`{"avatar":"`+dataURL+`"}`))
	rec := httptest.NewRecorder()
	s.handleUserAvatarSet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if be.cfg.UserAvatar != dataURL {
		t.Fatalf("set: cfg.UserAvatar = %q, want the stored data URL", be.cfg.UserAvatar)
	}

	req = httptest.NewRequest("GET", "/api/avatar", nil)
	rec = httptest.NewRecorder()
	s.handleUserAvatarGet(rec, req)
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), dataURL) {
		t.Fatalf("get: body does not contain the avatar: %.200q", body)
	}

	for name, payload := range map[string]string{
		"bad prefix": `{"avatar":"data:image/gif;base64,AAAA"}`,
		"bad base64": `{"avatar":"data:image/png;base64,@@@@"}`,
		"oversize":   `{"avatar":"data:image/jpeg;base64,` + base64.StdEncoding.EncodeToString(make([]byte, 4*1024*1024+1)) + `}`,
	} {
		req = httptest.NewRequest("POST", "/api/avatar", strings.NewReader(payload))
		rec = httptest.NewRecorder()
		s.handleUserAvatarSet(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, rec.Code)
		}
		if be.cfg.UserAvatar != dataURL {
			t.Fatalf("%s: avatar was clobbered: %q", name, be.cfg.UserAvatar)
		}
	}

	req = httptest.NewRequest("POST", "/api/avatar", strings.NewReader(`{"avatar":""}`))
	rec = httptest.NewRecorder()
	s.handleUserAvatarSet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: status = %d, want 200", rec.Code)
	}
	if be.cfg.UserAvatar != "" {
		t.Fatalf("clear: cfg.UserAvatar = %q, want empty", be.cfg.UserAvatar)
	}
}

// TestAgentAvatarSetGet covers the agent-side twin of the avatar API: setting
// the agent avatar must not touch the user avatar (the two share one config
// struct and one validation path), and clearing it falls back to the built-in
// default tile the UI renders.
func TestAgentAvatarSetGet(t *testing.T) {
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'})
	be := &avatarFakeBE{cfg: &config.AppConfig{UserAvatar: "data:image/png;base64,dXNlcg=="}}
	s := &server{be: be}

	req := httptest.NewRequest("POST", "/api/agent-avatar", strings.NewReader(`{"avatar":"`+dataURL+`"}`))
	rec := httptest.NewRecorder()
	s.handleAgentAvatarSet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if be.cfg.AgentAvatar != dataURL {
		t.Fatalf("set: cfg.AgentAvatar = %q, want the stored data URL", be.cfg.AgentAvatar)
	}
	if be.cfg.UserAvatar == dataURL || be.cfg.UserAvatar == "" {
		t.Fatalf("set: user avatar was clobbered: %q", be.cfg.UserAvatar)
	}

	req = httptest.NewRequest("GET", "/api/agent-avatar", nil)
	rec = httptest.NewRecorder()
	s.handleAgentAvatarGet(rec, req)
	if body, _ := io.ReadAll(rec.Body); !strings.Contains(string(body), dataURL) {
		t.Fatalf("get: body does not contain the avatar: %.200q", body)
	}

	req = httptest.NewRequest("POST", "/api/agent-avatar", strings.NewReader(`{"avatar":"data:image/gif;base64,AAAA"}`))
	rec = httptest.NewRecorder()
	s.handleAgentAvatarSet(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad prefix: status = %d, want 400", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/agent-avatar", strings.NewReader(`{"avatar":""}`))
	rec = httptest.NewRecorder()
	s.handleAgentAvatarSet(rec, req)
	if rec.Code != http.StatusOK || be.cfg.AgentAvatar != "" {
		t.Fatalf("clear: status = %d cfg.AgentAvatar = %q, want 200 and empty", rec.Code, be.cfg.AgentAvatar)
	}
}
