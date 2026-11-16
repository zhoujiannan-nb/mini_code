// Startup animation ("boot anim") host side.
//
// A startup clip is not its own subsystem: it is an APPEARANCE PLUGIN that also
// declares a "boot" block in plugin.json (see plugin/boot.go). That is the whole
// point — one concept to install (插件中心), one toggle, one uninstall path. The
// old separate theme directory and its own installer/listing are gone; what
// remains here is the part a browser cannot do on its own:
//
//  1. list the clips that are installed (i.e. the boot plugins that are enabled);
//  2. serve a clip's files from its plugin directory, with a policy that lets it
//     animate but not reach the network or the app's API;
//  3. inline the player config into index.html so the overlay is decided before
//     the first paint.
//
// A clip is untrusted third-party HTML, so it is served from its own path
// (/bootanim/<id>/...) with connect-src 'none' (no fetch/XHR/WebSocket),
// frame-src 'none' (no nested frames), script-src 'self' (no remote code) and
// form-action 'none'. The iframe additionally runs sandbox="allow-scripts" with
// no same-origin flag, so the clip cannot read the UI's storage or cookies.
// That is also why a boot plugin is the ONE plugin kind allowed to ship .js:
// its code runs in that sandboxed frame, never in the app's origin.
//
// Legacy note: clips installed before this change live in ~/.mini_code/boot-anim
// with a theme.json. They are still read, but only when the config names one
// explicitly (BootAnim.Theme), so an existing setup keeps working while new
// clips come from the plugin catalogue.
package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/plugin"
	"github.com/user/mini_code/util"
)

// BootAnimTheme is one startup clip, either an enabled boot plugin or a legacy
// theme directory. One struct serves both so the UI renders one card shape.
type BootAnimTheme struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	NameEn      string   `json:"name_en,omitempty"`
	Author      string   `json:"author,omitempty"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Entry       string   `json:"entry,omitempty"`
	Preview     string   `json:"preview,omitempty"`
	DurationMs  int      `json:"durationMs"`
	MaxMs       int      `json:"maxMs"`
	Interactive bool     `json:"interactive"`
	MinViewport int      `json:"minViewport"`
	Tags        []string `json:"tags,omitempty"`

	// Options is the clip's own default query parameters (brand text, mute…),
	// sanitized before it can end up in an injected URL.
	Options map[string]any `json:"options,omitempty"`

	// URL / PreviewURL are served paths.
	URL        string `json:"url,omitempty"`
	PreviewURL string `json:"preview_url,omitempty"`

	// Source says where the clip came from: "plugin" (the normal case now) or
	// "legacy" (a pre-plugin theme directory).
	Source string `json:"source,omitempty"`
	// Plugin is the owning plugin id (empty for a legacy theme).
	Plugin string `json:"plugin,omitempty"`
	// Installed is kept for the UI's old card: every listed clip is installed.
	Installed bool `json:"installed"`
	// Error explains why a plugin directory that claims a boot block is not
	// playable (missing entry, bad manifest).
	Error string `json:"error,omitempty"`
}

// bootThemeManifest is the legacy on-disk theme.json contract, still read so a
// boot plugin can omit timings and inherit them from a theme.json next to it.
type bootThemeManifest struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	NameEn      string         `json:"nameEn"`
	Author      string         `json:"author"`
	Version     string         `json:"version"`
	License     string         `json:"license"`
	Description string         `json:"description"`
	Entry       string         `json:"entry"`
	Preview     string         `json:"preview"`
	DurationMs  int            `json:"durationMs"`
	MaxMs       int            `json:"maxMs"`
	Interactive bool           `json:"interactive"`
	MinViewport int            `json:"minViewport"`
	Tags        []string       `json:"tags"`
	Options     map[string]any `json:"options"`
}

const (
	// bootAnimBasePath is the URL prefix clips are served from. It is *not*
	// under /api: a clip is a document, and keeping it on its own prefix is what
	// lets the CSP and the sandbox be applied to it alone.
	bootAnimBasePath = "/bootanim/"
	// bootAnimPlaceholder marks where the server injects the player config into
	// the embedded UI. It must appear verbatim in web/ui/index.html.
	bootAnimPlaceholder = `<script id="mc-boot-cfg" type="application/json">null</script>`
)

// bootAnimCSP is served with every clip file. The clip may animate and play
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

// bootAnimExtTypes whitelists what may be read out of a clip directory, and with
// which Content-Type. Anything else (the app's config.json, a .go file a
// misconfigured directory points at, …) is refused before it is touched.
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

// bootAnimGitHubBase is the raw-file host. A variable so the whole download path
// can be pointed at a local server instead of GitHub.
var bootAnimGitHubBase = "https://raw.githubusercontent.com"

// rawBaseURLRe accepts a mirror base like "https://raw.example.com" (scheme +
// host + optional path, no query, no fragment). It is deliberately strict: the
// value is used to build a download URL, and a loose one would let a misconfigured
// environment variable point the installer at an arbitrary endpoint.
var rawBaseURLRe = regexp.MustCompile(`^https?://[A-Za-z0-9._~:\[\]-]+(/[A-Za-z0-9._~/-]*)?$`)

// init applies the MINICODE_GITHUB_RAW override.
//
// It exists for two real cases: an offline/air-gapped install (serve the plugin
// repo from an internal mirror) and the test suite (point at a local HTTP server
// so the install path is exercised without hitting GitHub). The value is only
// ever read from the environment of this process, so whoever can set it could
// already run code as this user.
func init() {
	if v := strings.TrimSpace(os.Getenv("MINICODE_GITHUB_RAW")); v != "" {
		v = strings.TrimRight(v, "/")
		if rawBaseURLRe.MatchString(v) {
			bootAnimGitHubBase = v
			slog.Info("plugin raw-file host overridden", "base", v)
		} else {
			slog.Warn("MINICODE_GITHUB_RAW ignored: not an http(s) base URL", "value", v)
		}
	}
}

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

// bootAnimDir returns the LEGACY theme directory, from the live config. New
// clips live in plugin directories; this is only read for themes installed
// before the plugin layer existed.
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

// --- listing ---

// pluginBootTheme turns an enabled boot plugin into the card shape. Numbers the
// plugin.json does not carry are inherited from a theme.json next to it, so a
// ported theme keeps its timings without retyping them.
func pluginBootTheme(p plugin.Plugin) *BootAnimTheme {
	boot := p.Boot
	if boot == nil {
		return nil
	}
	t := &BootAnimTheme{
		ID:          p.ID,
		Name:        util.FirstNonEmpty(p.Name, p.ID),
		Author:      p.Author,
		Version:     p.Version,
		Description: p.Description,
		Entry:       p.BootEntryFile(),
		Preview:     boot.Preview,
		DurationMs:  boot.DurationMs,
		MaxMs:       boot.MaxMs,
		Interactive: boot.Interactive,
		MinViewport: boot.MinViewport,
		Tags:        boot.Tags,
		Source:      "plugin",
		Plugin:      p.ID,
		Installed:   true,
	}
	// theme.json: optional, only fills what the boot block left empty.
	if m, ok := readThemeManifest(p.Dir); ok {
		if t.Description == "" {
			t.Description = m.Description
		}
		if t.Author == "" {
			t.Author = m.Author
		}
		if t.Version == "" {
			t.Version = m.Version
		}
		if t.DurationMs == 0 {
			t.DurationMs = m.DurationMs
		}
		if t.MaxMs == 0 {
			t.MaxMs = m.MaxMs
		}
		if !t.Interactive {
			t.Interactive = m.Interactive
		}
		if t.MinViewport == 0 {
			t.MinViewport = m.MinViewport
		}
		if len(t.Tags) == 0 {
			t.Tags = m.Tags
		}
		t.Options = sanitizeBootAnimOptions(m.Options)
	}
	finishBootTheme(p.Dir, t)
	if t.Error != "" {
		// A plugin that says it has a clip but cannot produce one is reported,
		// not hidden: "my animation does not play" is undebuggable otherwise.
		slog.Warn("boot clip unusable", "plugin", p.ID, "err", t.Error)
	}
	return t
}

// finishBootTheme fills in the served URLs and validates the entry exists.
func finishBootTheme(dir string, t *BootAnimTheme) {
	if !bootAnimRelPathOK(t.Entry) {
		t.Error = "entry " + t.Entry + " is not a path inside the plugin"
		return
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(t.Entry))); err != nil {
		t.Error = "entry file " + t.Entry + " not found"
		return
	}
	t.URL = bootAnimBasePath + t.ID + "/" + t.Entry
	if t.Preview != "" && bootAnimRelPathOK(t.Preview) {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(t.Preview))); err == nil {
			t.PreviewURL = bootAnimBasePath + t.ID + "/" + t.Preview
		} else {
			t.Preview = ""
		}
	}
}

// readThemeManifest reads a legacy theme.json out of dir (ok=false when absent).
func readThemeManifest(dir string) (bootThemeManifest, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "theme.json"))
	if err != nil {
		return bootThemeManifest{}, false
	}
	var m bootThemeManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		slog.Warn("theme.json is not valid JSON", "dir", dir, "err", err)
		return bootThemeManifest{}, false
	}
	return m, true
}

// loadLegacyBootTheme reads <dir>/<id>/theme.json. Kept for themes installed
// before clips became plugins.
func loadLegacyBootTheme(dir, id string) (*BootAnimTheme, error) {
	if !config.ValidBootAnimID(id) {
		return nil, os.ErrNotExist
	}
	root := filepath.Join(dir, id)
	m, ok := readThemeManifest(root)
	if !ok {
		return nil, os.ErrNotExist
	}
	t := &BootAnimTheme{
		ID:          id,
		Name:        util.FirstNonEmpty(m.Name, id),
		NameEn:      m.NameEn,
		Author:      m.Author,
		Version:     m.Version,
		Description: m.Description,
		Entry:       util.FirstNonEmpty(m.Entry, plugin.BootEntryDefault),
		Preview:     m.Preview,
		DurationMs:  m.DurationMs,
		MaxMs:       m.MaxMs,
		Interactive: m.Interactive,
		MinViewport: m.MinViewport,
		Tags:        m.Tags,
		Options:     sanitizeBootAnimOptions(m.Options),
		Source:      "legacy",
		Installed:   true,
	}
	if t.MaxMs < t.DurationMs {
		t.MaxMs = t.DurationMs
	}
	finishBootTheme(root, t)
	if t.Error != "" {
		return t, os.ErrInvalid
	}
	return t, nil
}

// listLegacyBootThemes scans the legacy theme directory (may not exist).
func (s *server) listLegacyBootThemes() []*BootAnimTheme {
	dir, err := s.bootAnimDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*BootAnimTheme
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || strings.HasPrefix(id, ".") || !config.ValidBootAnimID(id) {
			continue
		}
		t, err := loadLegacyBootTheme(dir, id)
		if t == nil {
			continue
		}
		if err != nil && t.Error == "" {
			t.Error = err.Error()
		}
		out = append(out, t)
	}
	return out
}

// bootThemes is the full list the settings page and the player see: boot
// plugins first (the normal case), then legacy theme directories.
func (s *server) bootThemes() []*BootAnimTheme {
	out := make([]*BootAnimTheme, 0, 4)
	seen := map[string]bool{}
	for _, p := range plugin.Load() {
		if p.Boot == nil {
			continue
		}
		t := pluginBootTheme(p)
		if t == nil {
			continue
		}
		seen[t.ID] = true
		out = append(out, t)
	}
	for _, t := range s.listLegacyBootThemes() {
		if seen[t.ID] {
			continue // a plugin of the same id wins over a stale directory
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// activeBootAnimTheme resolves what should actually play. ok=false means the
// page must not show an overlay.
//
// Selection is deliberately boring: the config may name one id (a clip the user
// pinned, or a legacy theme); otherwise the first enabled boot plugin by id
// plays. A boot screen is global — there is no meaningful way to show two.
func (s *server) activeBootAnimTheme() (*BootAnimTheme, bool) {
	cfg := s.be.GetConfig()
	if cfg == nil || !cfg.BootAnim.Enabled {
		return nil, false
	}
	want := strings.TrimSpace(cfg.BootAnim.Theme)
	for _, t := range s.bootThemes() {
		if t.Error != "" {
			continue
		}
		if want == "" || t.ID == want {
			return t, true
		}
	}
	return nil, false
}

// bootSourceDir locates the directory a clip's files are served from: a boot
// plugin's own directory, or the legacy theme directory.
func (s *server) bootSourceDir(id string) (string, bool) {
	if p, ok := plugin.BootPluginByID(id); ok {
		return p.Dir, true
	}
	dir, err := s.bootAnimDir()
	if err != nil {
		return "", false
	}
	root := filepath.Join(dir, id)
	if m, ok := readThemeManifest(root); ok && config.ValidBootAnimID(id) {
		_ = m
		return root, true
	}
	return "", false
}

// --- handlers ---

// handleBootAnimList: GET /api/bootanim → {dir, themes:[...], play:{...}}.
//
// play carries the live play settings so the plugin detail pane can render its
// controls without a second request to /api/config (which would also hand the
// page the API keys it has no use for).
func (s *server) handleBootAnimList(w http.ResponseWriter, r *http.Request) {
	themes := s.bootThemes()
	legacyDir, _ := s.bootAnimDir()
	pluginDir, _ := plugin.DirPath()
	out := map[string]any{
		"themes": themes,
		// dir is what the page shows: clips now live in the plugin directory.
		"dir":        pluginDir,
		"legacy_dir": legacyDir,
		"source":     "plugins",
	}
	if cfg := s.be.GetConfig(); cfg != nil {
		b := cfg.BootAnim
		b.Normalize()
		out["play"] = map[string]any{
			"enabled": b.Enabled,
			"theme":   b.Theme,
			"mode":    b.Mode,
			"mute":    b.Mute,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// bootAnimPatch is the body of PUT /api/bootanim: only the play switches, with
// pointers so an omitted field keeps its current value. This is what the plugin
// detail pane sends — turning the animation on must not require re-posting the
// model list from a dialog that never showed it.
type bootAnimPatch struct {
	Enabled *bool   `json:"enabled"`
	Theme   *string `json:"theme"`
	Mode    *string `json:"mode"`
	Mute    *bool   `json:"mute"`
}

// handleBootAnimSave: PUT /api/bootanim — merge a partial play-settings update
// into the live config and save.
func (s *server) handleBootAnimSave(w http.ResponseWriter, r *http.Request) {
	var patch bootAnimPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cur := s.be.GetConfig()
	if cur == nil {
		writeErr(w, http.StatusInternalServerError, "配置尚未加载")
		return
	}
	cfg := *cur // shallow copy is enough: only the boot block is touched
	cfg.BootAnim.Normalize()
	b := cfg.BootAnim
	if patch.Enabled != nil {
		b.Enabled = *patch.Enabled
	}
	if patch.Mute != nil {
		b.Mute = *patch.Mute
	}
	if patch.Mode != nil {
		// Validate before Normalize: Normalize would quietly turn "weekly" into
		// "session", i.e. answer 200 to a request it did not honour.
		m := *patch.Mode
		if m != config.BootAnimModeSession && m != config.BootAnimModeAlways &&
			m != config.BootAnimModeDaily {
			writeErr(w, http.StatusBadRequest,
				"boot_anim.mode must be session, always or daily")
			return
		}
		b.Mode = m
	}
	if patch.Theme != nil {
		id := strings.TrimSpace(*patch.Theme)
		// A clip can only be selected if it is actually installed and playable,
		// otherwise the player would silently show nothing.
		if id != "" {
			known := false
			for _, t := range s.bootThemes() {
				if t.ID == id && t.Error == "" {
					known = true
					break
				}
			}
			if !known {
				writeErr(w, http.StatusBadRequest, "这台机器上没有可播的「"+id+"」")
				return
			}
		}
		b.Theme = id
	}
	b.Normalize()
	cfg.BootAnim = b
	// Only the block this endpoint owns is validated. Running the whole
	// AppConfig.Validate here would reject the patch whenever the saved model
	// list happens to be incomplete — and the caller never touched it.
	if err := b.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.be.SaveConfig(&cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"play": map[string]any{
			"enabled": b.Enabled,
			"theme":   b.Theme,
			"mode":    b.Mode,
			"mute":    b.Mute,
		},
	})
}

// handleBootAnimAsset: GET /bootanim/<id>/<file...> serves a clip file.
func (s *server) handleBootAnimAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, bootAnimBasePath)
	i := strings.Index(rest, "/")
	if i <= 0 || i == len(rest)-1 {
		writeErr(w, http.StatusNotFound, "clip file not found")
		return
	}
	id, rel := rest[:i], rest[i+1:]
	if !config.ValidBootAnimID(id) || !bootAnimRelPathOK(rel) {
		writeErr(w, http.StatusNotFound, "clip file not found")
		return
	}
	ext := strings.ToLower(path.Ext(rel))
	ct, ok := bootAnimExtTypes[ext]
	if !ok {
		writeErr(w, http.StatusForbidden, "file type not allowed")
		return
	}
	root, ok := s.bootSourceDir(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "clip not installed")
		return
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	// Belt and braces: bootAnimRelPathOK already rejects ".." and absolute
	// paths, but a plugin directory is user-writable, so verify containment
	// again — including where a symlink inside it actually lands.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		writeErr(w, http.StatusNotFound, "clip not found")
		return
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		writeErr(w, http.StatusNotFound, "clip file not found")
		return
	}
	if inside, _ := pathWithin(resolvedRoot, resolved); !inside {
		writeErr(w, http.StatusForbidden, "path escapes the clip directory")
		return
	}
	f, err := os.Open(resolved)
	if err != nil {
		writeErr(w, http.StatusNotFound, "clip file not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, "clip file not found")
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Security-Policy", bootAnimCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// Clips are edited in place by their authors: never let the webview keep a
	// stale copy of one.
	h.Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(rel), st.ModTime(), f)
}

// --- helpers ---

// bootAnimRelPathOK reports whether p is a plain relative path that stays inside
// the clip directory. Backslashes and a drive letter are rejected outright: they
// would only be needed to escape.
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


// BootAnimBootConfig is what the page's inline player reads out of the JSON
// block the server injects into the embedded UI.
type BootAnimBootConfig struct {
	Enabled     bool   `json:"enabled"`
	ID          string `json:"id"`
	Entry       string `json:"entry"` // absolute URL path into /bootanim/
	Mode        string `json:"mode"`
	Name        string `json:"name,omitempty"`
	Interactive bool   `json:"interactive"`
	DurationMs  int    `json:"durationMs"`
	MaxMs       int    `json:"maxMs"`
	MinViewport int    `json:"minViewport"`
	// Mute is the user's "no sound" switch: a clip with voice cues reads it and
	// stays quiet. Default false = sound allowed (a bool in a nested struct can
	// only mean "off" in its zero value, so the default has to be the loud one).
	Mute bool `json:"mute"`
	// Options are the selected clip's own default query parameters.
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
	if !ok || t.URL == "" {
		return "null"
	}
	// maxMs is the hard stop the player enforces; keep it sane even if the
	// manifest is nonsense, so a broken clip can never hold the UI hostage.
	maxMs := t.MaxMs
	if maxMs < 500 {
		maxMs = 500
	}
	if maxMs > plugin.BootMaxMsCeiling {
		maxMs = plugin.BootMaxMsCeiling
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
	// A clip manifest can carry user text; "</script>" inside the JSON would
	// close the block early. "<\/" is the same string to the JSON parser.
	return strings.ReplaceAll(string(out), "</", `<\/`)
}

// bootAnimOptionKeyRe keeps clip-supplied query keys boring: short lowercase
// words. Anything else is dropped before it can end up in an injected URL.
var bootAnimOptionKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// sanitizeBootAnimOptions trims a clip's "options" block to what is safe to hand
// to the entry as a query string: a handful of short keys with scalar values.
// Keys the player sets itself are dropped here too, so a clip cannot override
// e.g. the play mode the user picked.
func sanitizeBootAnimOptions(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	drop := map[string]bool{"speed": true, "name": true, "interactive": true, "mute": true, "mode": true}
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

// bootAnimServedAt exists so a stale import (time) stays honest if the player
// loses its time-based logic; it is also a handy debug value.
var _ = time.Now
