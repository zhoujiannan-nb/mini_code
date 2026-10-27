package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// This file backs the "click a path in the chat to open it" feature.
//
// The model writes paths all the time ("改好了,打开 D:\proj\index.html 看看"),
// and the only thing the user could do with one was copy it into Explorer.
// The UI turns those paths into links and posts them here; the server resolves
// the path and hands it to the OS default handler (Explorer / Finder /
// xdg-open), so a click really opens the file.
//
// The endpoint only ever *opens* something that already exists: a typo in the
// model's output produces a "file not found" toast instead of a stray window.

// fsOpener is a package-level hook so tests can assert the resolved path
// without launching Explorer (and without a window per test run).
var fsOpener = openLocalPath

type fsOpenRequest struct {
	// Path as it appeared in the chat. Absolute, or relative to Base.
	Path string `json:"path"`
	// Base is the session work dir: relative paths in the chat are relative
	// to the project the conversation is about, not to the server's cwd.
	Base string `json:"base"`
}

// handleFsOpen opens a path with the OS default application.
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

// resolveOpenPath turns what the model wrote into an absolute OS path.
// It returns a non-empty message when the input cannot be used at all.
//
// It tolerates the decorations a chat message puts around a path: the
// surrounding quotes/backticks survived into the link's data attribute, a
// trailing comma or full stop, a file:// URL, and forward slashes on Windows.
func resolveOpenPath(raw, base string) (string, string) {
	p := strings.TrimSpace(raw)
	p = strings.Trim(p, "\"'`")
	// Trailing punctuation belongs to the sentence, not to the file name.
	p = strings.TrimRight(p, ".,;:!?。、，；：！？）)】」》")
	if p == "" {
		return "", "path is required"
	}
	if lower := strings.ToLower(p); strings.HasPrefix(lower, "file://") {
		p = p[len("file://"):]
		if un, err := url.PathUnescape(p); err == nil {
			p = un
		}
		// file:///D:/x -> /D:/x
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
