// Startup animation ("boot anim") host side.
//
// A theme is a directory of static files (default ~/.mini_code/boot-anim/<id>/,
// overridable in the config) with a theme.json manifest and an HTML entry. The
// host does three things with it:
//
//  1. lists what is installed, and what the plugin repo offers;
//  2. downloads a theme from GitHub (raw files + sha256 from index.json) into
//     the theme directory, atomically;
//  3. serves the theme's files to the sandboxed iframe the UI puts on top of
//     the app while it starts, with a Content-Security-Policy that lets the
//     theme animate but not reach out to the network or the app's own API.
//
// Themes are untrusted third-party HTML, so they are served from their own path
// (/bootanim/<id>/...) with connect-src 'none' (no fetch/XHR/WebSocket),
// frame-src 'none' (no nested frames), script-src 'self' (no remote code) and
// form-action 'none'. The iframe additionally runs sandbox="allow-scripts" with
// no same-origin flag, so the theme cannot read the UI's storage or cookies.
package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/config"
)

// BootAnimTheme is one theme, either installed locally or only available in the
// online catalogue. The same struct serves both so the UI can render one card
// component for both lists.
type BootAnimTheme struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	NameEn      string   `json:"name_en,omitempty"`
	Author      string   `json:"author,omitempty"`
	Version     string   `json:"version,omitempty"`
	License     string   `json:"license,omitempty"`
	Description string   `json:"description,omitempty"`
	Entry       string   `json:"entry,omitempty"`
	Preview     string   `json:"preview,omitempty"`
	DurationMs  int      `json:"durationMs"`
	MaxMs       int      `json:"maxMs"`
	Interactive bool     `json:"interactive"`
	MinViewport int      `json:"minViewport"`
	Tags        []string `json:"tags,omitempty"`

	// Options is theme.json's "options" block: default query parameters the
	// player passes to the entry (brand text, mute, ...). Sanitized.
	Options map[string]any `json:"options,omitempty"`

	// URL / PreviewURL are served paths, set for installed themes only.
	URL        string `json:"url,omitempty"`
	PreviewURL string `json:"preview_url,omitempty"`

	Installed bool   `json:"installed"`
	Latest    bool   `json:"latest,omitempty"` // remote: already installed at this version
	Files     int    `json:"files,omitempty"`  // remote: file count in the bundle
	Error     string `json:"error,omitempty"`  // installed but unusable (bad manifest, missing entry)
}

// bootAnimManifest is the on-disk theme.json contract (see the plugin repo's
// docs/theme-contract.md). Unknown keys are ignored, so a newer theme can carry
// fields an older host has never heard of.
type bootAnimManifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	NameEn      string   `json:"nameEn"`
	Author      string   `json:"author"`
	Version     string   `json:"version"`
	License     string   `json:"license"`
	Description string   `json:"description"`
	Entry       string   `json:"entry"`
	Preview     string   `json:"preview"`
	DurationMs  int      `json:"durationMs"`
	MaxMs       int      `json:"maxMs"`
	Interactive bool     `json:"interactive"`
	MinViewport int      `json:"minViewport"`
	Tags        []string `json:"tags"`
}

// Remote catalogue (index.json of the plugin repo).
type bootAnimIndex struct {
	Schema int    `json:"schema"`
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	Kinds  struct {
		BootAnim []bootAnimIndexItem `json:"boot-anim"`
	} `json:"kinds"`
}

type bootAnimIndexItem struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	NameEn      string   `json:"nameEn"`
	Author      string   `json:"author"`
	Version     string   `json:"version"`
	License     string   `json:"license"`
	Description string   `json:"description"`
	Path        string   `json:"path"` // repo-root relative, e.g. boot-anim/mc-minimal
	Entry       string   `json:"entry"`
	Preview     string   `json:"preview"`
	DurationMs  int      `json:"durationMs"`
	MaxMs       int      `json:"maxMs"`
	Interactive bool     `json:"interactive"`
	MinViewport int      `json:"minViewport"`
	Tags        []string `json:"tags"`
	Files       []struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

const (
	// bootAnimBasePath is the URL prefix themes are served from. It is *not*
	// under /api: a theme is a document, and keeping it on its own prefix is
	// what lets the CSP and the sandbox be applied to it alone.
	bootAnimBasePath = "/bootanim/"
	// Download limits. A theme is a handful of static files; anything bigger is
	// either a mistake or not a theme.
	bootAnimMaxFileBytes  = 8 << 20 // per file
	bootAnimMaxTotalBytes = 32 << 20
	bootAnimMaxFiles      = 64
	bootAnimRemoteTTL     = time.Minute
	bootAnimFetchTimeout  = 12 * time.Second
	// bootAnimPlaceholder marks where the server injects the player config into
	// the embedded UI. It must appear verbatim in web/ui/index.html.
	bootAnimPlaceholder = `<script id="mc-boot-cfg" type="application/json">null</script>`
)

// bootAnimCSP is served with every theme file. The theme may animate and play
// audio, and load its own files or remote images/fonts, but it may not talk to
// any server (connect-src 'none' also kills fetch/XHR/WebSocket), nest a frame,
// submit a form or load remote code.
const bootAnimCSP = "default-src 'none'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline' data: https:; " +
	"img-src 'self' data: blob: https:; " +
	"font-src 'self' data: https:; " +
	"media-src 'self' data: blob:; " +
	"connect-src 'none'; frame-src 'none'; child-src 'none'; object-src 'none'; " +
	"worker-src 'none'; manifest-src 'none'; form-action 'none'; base-uri 'none' "

// bootAnimExtTypes whitelists what may be read out of a theme directory, and
// with which Content-Type. Anything else (config.json of the app, a .go file a
// misconfigured dir points at, ...) is refused before it is touched.
var bootAnimExtTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".webp":  "image/webp",
	".gif":   "image/gif",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
	".woff":  "font/woff",
	".ttf":   "font/ttf",
	".mp3":   "audio/mpeg",
	".ogg":   "audio/ogg",
	".wav":   "audio/wav",
}

// bootAnimRefRe sanitizes the git ref used in raw.githubusercontent URLs.
var bootAnimRefRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// bootAnimGitHubBase is the raw-file host. A variable so a test can point the
// whole download path at a local server instead of GitHub.
var bootAnimGitHubBase = "https://raw.githubusercontent.com"

// bootAnimRawBase is the directory prefix of the raw files of a repo at a ref.
func bootAnimRawBase(repo, ref string) string {
	if !bootAnimRefRe.MatchString(ref) {
		ref = "main"
	}
	return bootAnimGitHubBase + "/" + repo + "/" + ref + "/"
}

// bootAnimRawURL builds https://raw.githubusercontent.com/<repo>/<ref>/<path>.
func bootAnimRawURL(repo, ref, p string) string {
	return bootAnimRawBase(repo, ref) + strings.TrimPrefix(p, "/")
}

// bootAnimRepo is the GitHub "owner/name" the online catalogue is read from.
func (s *server) bootAnimRepo() string {
	if cfg := s.be.GetConfig(); cfg != nil {
		b := cfg.BootAnim
		b.Normalize()
		return b.EffectiveRepo()
	}
	return config.DefaultBootAnimRepo
}

// bootAnimDir returns the directory themes live in, from the live config.
func (s *server) bootAnimDir() (string, error) {
	var b config.BootAnimConfig
	if cfg := s.be.GetConfig(); cfg != nil {
		b = cfg.BootAnim
	} else {
		b = config.DefaultBootAnimConfig()
	}
	b.Normalize()
	return b.DirPath()
}

// loadBootAnimThemeDir reads <dir>/<id>/theme.json and checks the entry exists.
// The returned theme has its served URLs filled in.
func loadBootAnimThemeDir(dir, id string) (*BootAnimTheme, error) {
	if !config.ValidBootAnimID(id) {
		return nil, fmt.Errorf("invalid theme id %q", id)
	}
	root := filepath.Join(dir, id)
	raw, err := os.ReadFile(filepath.Join(root, "theme.json"))
	if err != nil {
		return nil, fmt.Errorf("theme.json unreadable: %w", err)
	}
	var m bootAnimManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("theme.json is not valid JSON: %w", err)
	}
	if m.ID != "" && m.ID != id {
		return nil, fmt.Errorf("theme.json id %q does not match the directory name %q", m.ID, id)
	}
	t := &BootAnimTheme{
		ID:          id,
		Name:        firstNonEmpty(m.Name, id),
		NameEn:      m.NameEn,
		Author:      m.Author,
		Version:     m.Version,
		License:     m.License,
		Description: m.Description,
		Entry:       firstNonEmpty(m.Entry, "index.html"),
		Preview:     m.Preview,
		DurationMs:  m.DurationMs,
		MaxMs:       m.MaxMs,
		Interactive: m.Interactive,
		MinViewport: m.MinViewport,
		Tags:        m.Tags,
		Installed:   true,
	}
	// theme.json 的 options 会被播放器拼成入口的 query(见 bootAnimBootJSON),
	// 清单结构体没这个字段,单独取一次再收敛。
	var extra struct {
		Options map[string]any `json:"options"`
	}
	if json.Unmarshal(raw, &extra) == nil {
		t.Options = sanitizeBootAnimOptions(extra.Options)
	}
	if t.MaxMs < t.DurationMs {
		t.MaxMs = t.DurationMs
	}
	// The entry must be a plain relative path inside the theme and must exist:
	// without it there is nothing to play.
	if !bootAnimRelPathOK(t.Entry) {
		return nil, fmt.Errorf("entry %q is not a path inside the theme", t.Entry)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(t.Entry))); err != nil {
		return nil, fmt.Errorf("entry file %s not found", t.Entry)
	}
	t.URL = bootAnimBasePath + id + "/" + t.Entry
	if t.Preview != "" && bootAnimRelPathOK(t.Preview) {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(t.Preview))); err == nil {
			t.PreviewURL = bootAnimBasePath + id + "/" + t.Preview
		} else {
			t.Preview = ""
		}
	}
	return t, nil
}

// listBootAnimThemes scans the theme directory. A directory whose manifest is
// broken is still returned (with Error set) so the settings page can say why it
// is not selectable instead of silently hiding it.
func (s *server) listBootAnimThemes() (string, []*BootAnimTheme, error) {
	dir, err := s.bootAnimDir()
	if err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return dir, nil, nil // no directory yet = nothing installed
		}
		return dir, nil, err
	}
	out := make([]*BootAnimTheme, 0, len(entries))
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || strings.HasPrefix(id, ".") || !config.ValidBootAnimID(id) {
			continue // .install-* staging dirs and junk are not themes
		}
		t, err := loadBootAnimThemeDir(dir, id)
		if err != nil {
			out = append(out, &BootAnimTheme{ID: id, Name: id, Installed: true, Error: err.Error()})
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return dir, out, nil
}

// activeBootAnimTheme resolves what should actually play: the configured theme,
// installed and usable. ok=false means the page must not show an overlay.
func (s *server) activeBootAnimTheme() (*BootAnimTheme, bool) {
	cfg := s.be.GetConfig()
	if cfg == nil || !cfg.BootAnim.Enabled || cfg.BootAnim.Theme == "" {
		return nil, false
	}
	dir, err := s.bootAnimDir()
	if err != nil {
		return nil, false
	}
	t, err := loadBootAnimThemeDir(dir, cfg.BootAnim.Theme)
	if err != nil || t == nil {
		return nil, false
	}
	return t, true
}

// --- handlers ---

// handleBootAnimList: GET /api/bootanim → {dir, themes:[...]}.
func (s *server) handleBootAnimList(w http.ResponseWriter, r *http.Request) {
	dir, themes, err := s.listBootAnimThemes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取主题目录失败: "+err.Error())
		return
	}
	if themes == nil {
		themes = []*BootAnimTheme{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "themes": themes})
}

// handleBootAnimAsset: GET /bootanim/<id>/<file...> serves a theme file.
func (s *server) handleBootAnimAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, bootAnimBasePath)
	i := strings.Index(rest, "/")
	if i <= 0 || i == len(rest)-1 {
		writeErr(w, http.StatusNotFound, "theme file not found")
		return
	}
	id, rel := rest[:i], rest[i+1:]
	if !config.ValidBootAnimID(id) || !bootAnimRelPathOK(rel) {
		writeErr(w, http.StatusNotFound, "theme file not found")
		return
	}
	ext := strings.ToLower(path.Ext(rel))
	ct, ok := bootAnimExtTypes[ext]
	if !ok {
		writeErr(w, http.StatusForbidden, "file type not allowed")
		return
	}
	dir, err := s.bootAnimDir()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	root := filepath.Join(dir, id)
	target := filepath.Join(root, filepath.FromSlash(rel))
	// Belt and braces: bootAnimRelPathOK already rejects ".." and absolute
	// paths, but the theme dir is user-configurable, so verify containment
	// again — including where a symlink inside the theme actually lands.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		writeErr(w, http.StatusNotFound, "theme not found")
		return
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		writeErr(w, http.StatusNotFound, "theme file not found")
		return
	}
	if inside, _ := pathWithin(resolvedRoot, resolved); !inside {
		writeErr(w, http.StatusForbidden, "path escapes the theme directory")
		return
	}
	f, err := os.Open(resolved)
	if err != nil {
		writeErr(w, http.StatusNotFound, "theme file not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, "theme file not found")
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Security-Policy", bootAnimCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// Themes are edited in place by their authors: never let the webview keep a
	// stale copy of one.
	h.Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(rel), st.ModTime(), f)
}

// handleBootAnimRemote: GET /api/bootanim/remote → the plugin repo catalogue,
// annotated with what is already installed.
func (s *server) handleBootAnimRemote(w http.ResponseWriter, r *http.Request) {
	idx, err := s.remoteBootAnimIndex()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_, installed, _ := s.listBootAnimThemes()
	have := map[string]string{}
	for _, t := range installed {
		if t.Error == "" {
			have[t.ID] = t.Version
		}
	}
	themes := make([]*BootAnimTheme, 0, len(idx.Kinds.BootAnim))
	for _, it := range idx.Kinds.BootAnim {
		t := indexItemTheme(it)
		t.Installed = have[it.ID] != ""
		t.Latest = t.Installed && have[it.ID] == it.Version
		themes = append(themes, t)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repo":       idx.Repo,
		"ref":        idx.Ref,
		"fetched_at": time.Now().Unix(),
		"cached_for": int(bootAnimRemoteTTL.Seconds()),
		"themes":     themes,
	})
}

// handleBootAnimInstall: POST /api/bootanim/install {id} downloads every file
// the catalogue lists for that theme, checks each sha256, and swaps the theme
// directory in.
func (s *server) handleBootAnimInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if !config.ValidBootAnimID(req.ID) {
		writeErr(w, http.StatusBadRequest, "非法的主题 id")
		return
	}
	idx, err := s.remoteBootAnimIndex()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var item *bootAnimIndexItem
	for i := range idx.Kinds.BootAnim {
		if idx.Kinds.BootAnim[i].ID == req.ID {
			item = &idx.Kinds.BootAnim[i]
			break
		}
	}
	if item == nil {
		writeErr(w, http.StatusNotFound, "插件仓库里没有这个主题")
		return
	}
	dir, err := s.bootAnimDir()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// rawBase is built from the configured repo, i.e. the one this index was
	// fetched from, so a catalogue cannot point the download somewhere else.
	rawBase := bootAnimRawBase(s.bootAnimRepo(), idx.Ref)
	t, err := installBootAnimTheme(dir, rawBase, item)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "已安装 " + t.Name, "theme": t})
}

// handleBootAnimDelete: DELETE /api/bootanim/{id} removes an installed theme.
// Deleting the selected theme is allowed — the player simply does not run until
// another one is picked, because playback requires the files to be there.
func (s *server) handleBootAnimDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !config.ValidBootAnimID(id) {
		writeErr(w, http.StatusBadRequest, "非法的主题 id")
		return
	}
	dir, err := s.bootAnimDir()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	root := filepath.Join(dir, id)
	if _, err := os.Stat(filepath.Join(root, "theme.json")); err != nil {
		writeErr(w, http.StatusNotFound, "主题未安装")
		return
	}
	if err := os.RemoveAll(root); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "已卸载"})
}

// --- remote catalogue ---

// remoteCache memoizes index.json for bootAnimRemoteTTL per repo, so opening
// the settings page repeatedly does not hammer GitHub.
var remoteCache = struct {
	mu    sync.Mutex
	key   string
	at    time.Time
	idx   *bootAnimIndex
	err   error
	errAt time.Time
}{}

func (s *server) remoteBootAnimIndex() (*bootAnimIndex, error) {
	repo := s.bootAnimRepo()
	if !config.ValidBootAnimRepo(repo) {
		return nil, fmt.Errorf("插件仓库格式应为 owner/name,当前是 %q", repo)
	}
	url := bootAnimRawURL(repo, "main", "index.json")

	remoteCache.mu.Lock()
	defer remoteCache.mu.Unlock()
	if remoteCache.key == url && !remoteCache.at.IsZero() && time.Since(remoteCache.at) < bootAnimRemoteTTL {
		return remoteCache.idx, nil
	}
	// A failure is cached for much less time, so one offline click does not
	// make the button look broken for a minute.
	if remoteCache.key == url && remoteCache.err != nil && time.Since(remoteCache.errAt) < 5*time.Second {
		return nil, remoteCache.err
	}
	idx, err := fetchBootAnimIndex(url)
	if err != nil {
		remoteCache.key, remoteCache.err, remoteCache.errAt = url, err, time.Now()
		return nil, err
	}
	remoteCache.key, remoteCache.idx, remoteCache.at, remoteCache.err = url, idx, time.Now(), nil
	return idx, nil
}

func fetchBootAnimIndex(url string) (*bootAnimIndex, error) {
	client := &http.Client{Timeout: bootAnimFetchTimeout}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mini_code")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法访问插件仓库(需要能访问 raw.githubusercontent.com): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("插件仓库里没有 index.json(确认仓库地址和 main 分支)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("插件仓库返回 HTTP %d", resp.StatusCode)
	}
	var idx bootAnimIndex
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&idx); err != nil {
		return nil, fmt.Errorf("插件仓库的 index.json 解析失败: %v", err)
	}
	if !bootAnimRefRe.MatchString(idx.Ref) {
		idx.Ref = "main"
	}
	return &idx, nil
}

// --- install ---

// installMu serializes installs: two of them at once would both try to rename
// the same target directory.
var installMu sync.Mutex

// installBootAnimTheme downloads one catalogue entry into <dir>/<id>. Files go
// to a staging directory first and are checked against index.json, so a half
// downloaded theme never replaces a working one.
func installBootAnimTheme(dir, rawBase string, item *bootAnimIndexItem) (*BootAnimTheme, error) {
	if !config.ValidBootAnimID(item.ID) {
		return nil, fmt.Errorf("非法的主题 id %q", item.ID)
	}
	if len(item.Files) == 0 || len(item.Files) > bootAnimMaxFiles {
		return nil, fmt.Errorf("主题文件数不合法(%d)", len(item.Files))
	}
	// The manifest has to be part of the bundle: the local listing reads it.
	var haveManifest bool
	for _, f := range item.Files {
		if filepath.Base(filepath.FromSlash(f.Path)) == "theme.json" && path.Dir(path.Clean(f.Path)) == path.Clean(item.Path) {
			haveManifest = true
		}
	}
	if !haveManifest {
		return nil, errors.New("插件目录的 index.json 里缺 theme.json,无法安装")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("无法创建主题目录 %s: %w", dir, err)
	}

	installMu.Lock()
	defer installMu.Unlock()

	suffix := bootAnimRandSuffix()
	stage := filepath.Join(dir, ".install-"+item.ID+"-"+suffix)
	if err := os.MkdirAll(stage, 0755); err != nil {
		return nil, fmt.Errorf("无法创建临时目录: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(stage)
		}
	}()

	client := &http.Client{Timeout: bootAnimFetchTimeout}
	var total int64
	for _, f := range item.Files {
		name, err := bootAnimBundleRelPath(item.Path, f.Path)
		if err != nil {
			return nil, err
		}
		if f.Size < 0 || f.Size > bootAnimMaxFileBytes {
			return nil, fmt.Errorf("%s 超过单个文件大小上限", name)
		}
		total += f.Size
		if total > bootAnimMaxTotalBytes {
			return nil, errors.New("主题总大小超过上限")
		}
		if err := bootAnimDownloadFile(client, rawBootAnimURL(rawBase, f.Path), filepath.Join(stage, filepath.FromSlash(name)), f); err != nil {
			return nil, err
		}
	}

	target := filepath.Join(dir, item.ID)
	old := target + ".old-" + suffix
	if oldExists(target) {
		if err := os.Rename(target, old); err != nil {
			return nil, fmt.Errorf("旧版本无法让位(文件可能正被占用): %w", err)
		}
	}
	rollback := func() {
		os.RemoveAll(target)
		if oldExists(old) {
			os.Rename(old, target)
		}
	}
	if err := os.Rename(stage, target); err != nil {
		rollback()
		return nil, fmt.Errorf("安装失败: %w", err)
	}
	cleanup = false

	// Validate under the real name (the manifest's id has to match the directory)
	// and put the previous version back if the new one is unusable.
	installed, err := loadBootAnimThemeDir(dir, item.ID)
	if err != nil {
		rollback()
		return nil, fmt.Errorf("下载完成但主题不可用,已回滚: %w", err)
	}
	os.RemoveAll(old)
	// 校验过才留得住:每个文件都比对过 sha256/大小(见 bootAnimDownloadFile),
	// 装完在日志里留一行,用户能自己确认"读取/核验"确实完成了。
	slog.Info("boot theme installed", "id", installed.ID, "version", installed.Version,
		"files", len(item.Files), "bytes", total, "dir", target)
	return installed, nil
}

// bootAnimBundleRelPath maps a repo-root-relative catalogue path to the file
// name it gets inside the theme directory, refusing anything outside the theme.
func bootAnimBundleRelPath(itemPath, p string) (string, error) {
	clean := path.Clean(strings.TrimSpace(p))
	prefix := path.Clean(itemPath) + "/"
	if !strings.HasPrefix(clean, prefix) {
		return "", fmt.Errorf("主题文件路径不在主题目录内: %s", p)
	}
	name := strings.TrimPrefix(clean, prefix)
	if name == "" || !bootAnimRelPathOK(name) {
		return "", fmt.Errorf("非法的主题文件路径: %s", p)
	}
	if _, ok := bootAnimExtTypes[strings.ToLower(path.Ext(name))]; !ok {
		return "", fmt.Errorf("不允许的主题文件类型: %s", name)
	}
	return name, nil
}

func rawBootAnimURL(rawBase, p string) string {
	return rawBase + path.Clean(strings.TrimSpace(p))
}

// bootAnimDownloadFile fetches one raw file and verifies its size and sha256.
func bootAnimDownloadFile(client *http.Client, url, dst string, f struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("下载 %s 失败: %v", f.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 失败: HTTP %d", f.Path, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, bootAnimMaxFileBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("写入 %s 失败: %v", f.Path, err)
	}
	if f.Size > 0 && n != f.Size {
		os.Remove(tmp)
		return fmt.Errorf("%s 大小不符(期望 %d,实际 %d)", f.Path, f.Size, n)
	}
	if f.SHA256 != "" && hex.EncodeToString(h.Sum(nil)) != strings.ToLower(f.SHA256) {
		os.Remove(tmp)
		return fmt.Errorf("%s 校验失败(sha256 不匹配)", f.Path)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// --- helpers ---

// bootAnimRelPathOK reports whether p is a plain relative path that stays inside
// the theme directory. Backslashes and a drive letter are rejected outright:
// they would only be needed to escape.
func bootAnimRelPathOK(p string) bool {
	if p == "" || len(p) > 200 {
		return false
	}
	if strings.ContainsAny(p, "\\") || strings.HasPrefix(p, "/") || strings.Contains(p, ":") {
		return false
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return false
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// pathWithin reports whether target is root itself or lives under it.
func pathWithin(root, target string) (bool, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

func bootAnimRandSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func oldExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// indexItemTheme converts a catalogue entry into the shared card shape.
func indexItemTheme(it bootAnimIndexItem) *BootAnimTheme {
	return &BootAnimTheme{
		ID:          it.ID,
		Name:        firstNonEmpty(it.Name, it.ID),
		NameEn:      it.NameEn,
		Author:      it.Author,
		Version:     it.Version,
		License:     it.License,
		Description: it.Description,
		Entry:       firstNonEmpty(it.Entry, "index.html"),
		Preview:     it.Preview,
		DurationMs:  it.DurationMs,
		MaxMs:       it.MaxMs,
		Interactive: it.Interactive,
		MinViewport: it.MinViewport,
		Tags:        it.Tags,
		Files:       len(it.Files),
	}
}

// BootAnimBootConfig is what the page's inline player reads out of the JSON
// block the server injects into the embedded UI.
type BootAnimBootConfig struct {
	Enabled     bool    `json:"enabled"`
	ID          string  `json:"id"`
	Entry       string  `json:"entry"` // absolute URL path into /bootanim/
	Mode        string  `json:"mode"`
	Speed       float64 `json:"speed"`
	Name        string  `json:"name,omitempty"`
	Interactive bool    `json:"interactive"`
	DurationMs  int     `json:"durationMs"`
	MaxMs       int     `json:"maxMs"`
	MinViewport int     `json:"minViewport"`
	// Mute is the user's "no sound" switch: a theme with voice cues reads it and
	// stays quiet. Default false = sound allowed (a bool in a nested struct can
	// only mean "off" in its zero value, so the default has to be the loud one).
	Mute bool `json:"mute"`
	// Options are the selected theme's own default query parameters.
	Options map[string]any `json:"options,omitempty"`
}

// bootAnimBootJSON renders that block. It returns "null" when there is nothing
// to play, which is what the placeholder already says — the player then does
// exactly nothing, without a second request.
func (s *server) bootAnimBootJSON() string {
	cfg := s.be.GetConfig()
	if cfg == nil || !cfg.BootAnim.Enabled {
		return "null"
	}
	b := cfg.BootAnim
	b.Normalize()
	t, ok := s.activeBootAnimTheme()
	if !ok {
		return "null"
	}
	// maxMs is the hard stop the player enforces; keep it sane even if the
	// manifest is nonsense, so a broken theme can never hold the UI hostage.
	maxMs := t.MaxMs
	if maxMs < 500 {
		maxMs = 500
	}
	if maxMs > 30000 {
		maxMs = 30000
	}
	duration := t.DurationMs
	if duration <= 0 || duration > maxMs {
		duration = maxMs
	}
	minVp := t.MinViewport
	if minVp <= 0 {
		minVp = 320
	}
	out, err := json.Marshal(BootAnimBootConfig{
		Enabled:     true,
		ID:          t.ID,
		Entry:       t.URL,
		Mode:        b.Mode,
		Speed:       b.Speed,
		Name:        b.Name,
		Interactive: b.Interactive && t.Interactive,
		Mute:        b.Mute,
		Options:     t.Options,
		DurationMs:  duration,
		MaxMs:       maxMs,
		MinViewport: minVp,
	})
	if err != nil {
		return "null"
	}
	// A theme manifest can carry user text; "</script>" inside the JSON would
	// close the block early. "<\/" is the same string to the JSON parser.
	return strings.ReplaceAll(string(out), "</", `<\/`)
}

// bootAnimOptionKeyRe keeps theme-supplied query keys boring: short lowercase
// words. Anything else is dropped before it can end up in an injected URL.
var bootAnimOptionKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// sanitizeBootAnimOptions trims a theme's "options" block to what is safe to
// hand to the entry as a query string: a handful of short keys with scalar
// values. Keys the player sets itself are dropped here too, so a theme cannot
// override e.g. the speed the user picked.
func sanitizeBootAnimOptions(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	drop := map[string]bool{"speed": true, "name": true, "interactive": true, "mute": true}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if len(out) >= 16 || drop[k] || !bootAnimOptionKeyRe.MatchString(k) {
			continue
		}
		switch val := v.(type) {
		case bool:
			if val {
				out[k] = 1
			} else {
				out[k] = 0
			}
		case float64:
			out[k] = val
		case string:
			val = strings.TrimSpace(val)
			if val != "" && len(val) <= 64 {
				out[k] = val
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// injectBootAnim replaces the placeholder script in the embedded UI with the
// live config. Doing it server side (rather than fetching /api/bootanim from the
// page) is what keeps the overlay from flashing white after the UI paints.
func injectBootAnim(page, cfgJSON string) string {
	if !strings.Contains(page, bootAnimPlaceholder) {
		return page
	}
	return strings.Replace(page, bootAnimPlaceholder,
		`<script id="mc-boot-cfg" type="application/json">`+cfgJSON+`</script>`, 1)
}
