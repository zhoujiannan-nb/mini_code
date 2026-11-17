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

	Options map[string]any `json:"options,omitempty"`

	URL        string `json:"url,omitempty"`
	PreviewURL string `json:"preview_url,omitempty"`

	Source string `json:"source,omitempty"`
	Plugin string `json:"plugin,omitempty"`
	Installed bool `json:"installed"`
	Error string `json:"error,omitempty"`
}

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
	bootAnimBasePath = "/bootanim/"
	bootAnimPlaceholder = `<script id="mc-boot-cfg" type="application/json">null</script>`
)

const bootAnimCSP = "default-src 'none'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline' data: https:; " +
	"img-src 'self' data: blob: https:; " +
	"font-src 'self' data: https:; " +
	"media-src 'self' data: blob:; " +
	"connect-src 'none'; frame-src 'none'; child-src 'none'; object-src 'none'; " +
	"worker-src 'none'; manifest-src 'none'; form-action 'none'; base-uri 'none' "

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

var bootAnimRefRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

var bootAnimGitHubBase = "https://raw.githubusercontent.com"

var rawBaseURLRe = regexp.MustCompile(`^https?://[A-Za-z0-9._~:\[\]-]+(/[A-Za-z0-9._~/-]*)?$`)

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

func bootAnimRawBase(repo, ref string) string {
	if !bootAnimRefRe.MatchString(ref) {
		ref = "main"
	}
	return bootAnimGitHubBase + "/" + repo + "/" + ref + "/"
}

func bootAnimRawURL(repo, ref, p string) string {
	return bootAnimRawBase(repo, ref) + strings.TrimPrefix(p, "/")
}

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
		slog.Warn("boot clip unusable", "plugin", p.ID, "err", t.Error)
	}
	return t
}

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
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

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


func (s *server) handleBootAnimList(w http.ResponseWriter, r *http.Request) {
	themes := s.bootThemes()
	legacyDir, _ := s.bootAnimDir()
	pluginDir, _ := plugin.DirPath()
	out := map[string]any{
		"themes": themes,
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

type bootAnimPatch struct {
	Enabled *bool   `json:"enabled"`
	Theme   *string `json:"theme"`
	Mode    *string `json:"mode"`
	Mute    *bool   `json:"mute"`
}

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
	cfg := *cur
	cfg.BootAnim.Normalize()
	b := cfg.BootAnim
	if patch.Enabled != nil {
		b.Enabled = *patch.Enabled
	}
	if patch.Mute != nil {
		b.Mute = *patch.Mute
	}
	if patch.Mode != nil {
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
	h.Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(rel), st.ModTime(), f)
}


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

func pathWithin(root, target string) (bool, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}


type BootAnimBootConfig struct {
	Enabled     bool   `json:"enabled"`
	ID          string `json:"id"`
	Entry       string `json:"entry"`
	Mode        string `json:"mode"`
	Name        string `json:"name,omitempty"`
	Interactive bool   `json:"interactive"`
	DurationMs  int    `json:"durationMs"`
	MaxMs       int    `json:"maxMs"`
	MinViewport int    `json:"minViewport"`
	Mute bool `json:"mute"`
	Options map[string]any `json:"options,omitempty"`
}

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
	return strings.ReplaceAll(string(out), "</", `<\/`)
}

var bootAnimOptionKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

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

func injectBootAnim(page, cfgJSON string) string {
	if !strings.Contains(page, bootAnimPlaceholder) {
		return page
	}
	return strings.Replace(page, bootAnimPlaceholder,
		`<script id="mc-boot-cfg" type="application/json">`+cfgJSON+`</script>`, 1)
}

var _ = time.Now
