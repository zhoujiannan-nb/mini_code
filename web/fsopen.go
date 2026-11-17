package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)


var fsOpener = openLocalPath

type fsOpenRequest struct {
	Path string `json:"path"`
	Base string `json:"base"`
}

func (s *server) handleFsOpen(w http.ResponseWriter, r *http.Request) {
	var req fsOpenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	target, msg := resolveOpenPath(req.Path, req.Base)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		writeErr(w, http.StatusNotFound, "文件不存在: "+target)
		return
	}
	if err := fsOpener(target); err != nil {
		writeErr(w, http.StatusInternalServerError, "打开失败: "+err.Error())
		return
	}
	kind := "file"
	if info.IsDir() {
		kind = "dir"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "opened",
		"kind":   kind,
		"path":   target,
	})
}

func resolveOpenPath(raw, base string) (string, string) {
	p := strings.TrimSpace(raw)
	p = strings.Trim(p, "\"'`")
	p = strings.TrimRight(p, ".,;:!?。、，；：！？）)】」》")
	if p == "" {
		return "", "path is required"
	}
	if lower := strings.ToLower(p); strings.HasPrefix(lower, "file://") {
		p = p[len("file://"):]
		if un, err := url.PathUnescape(p); err == nil {
			p = un
		}
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
	}
	if !filepath.IsAbs(p) {
		if strings.TrimSpace(base) == "" {
			return "", "relative path needs a base directory: " + p
		}
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p), ""
}
