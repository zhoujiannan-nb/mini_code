// Plugin surface of the web channel.
//
// Three things live here:
//
//  1. GET /api/plugins — the installed plugin list, for the UI/diagnostics.
//  2. GET /plugins/<id>/<file...> — serves a plugin's own files (skin css,
//     wallpaper image) from ~/.mini_code/plugins/<id>/, with the same
//     containment and content-type guards the boot animation themes get.
//  3. injectPluginAssets — writes the <link>/<style> tags for plugin skins and
//     wallpapers into index.html at serve time, so a plugin is a pure drop-in:
//     no page edit, no rebuild.
//
// Plugins are never trusted with the app's own origin: the asset route only
// serves whitelisted static extensions from inside the plugin directory, and the
// UI's scripts are not reachable from it.
package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/user/mini_code/plugin"
)

const (
	pluginBasePath = "/plugins/"
	// pluginAssetsMarker is where index.html wants the plugin styles injected.
	// It sits after the two built-in stylesheets so a skin can override them.
	pluginAssetsMarker = "<!--mc-plugin-assets-->"
)

// pluginAssetExtTypes whitelists what a plugin may have served. It is
// deliberately narrower than "everything": a plugin ships styles and images,
// not scripts (a script in the app's origin could read the session token) and
// not HTML.
var pluginAssetExtTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".webp":  "image/webp",
	".gif":   "image/gif",
	".woff2": "font/woff2",
}

// pluginInfo is the JSON shape of one plugin for GET /api/plugins.
type pluginInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Author      string   `json:"author,omitempty"`
	Description string   `json:"description,omitempty"`
	Kinds       []string `json:"kinds"`
	Tools       []string `json:"tools,omitempty"`
	Dir         string   `json:"dir"`
}

// handlePluginsList: GET /api/plugins.
func (s *server) handlePluginsList(w http.ResponseWriter, r *http.Request) {
	plugins := plugin.Load()
	out := make([]pluginInfo, 0, len(plugins))
	for _, p := range plugins {
		info := pluginInfo{
			ID:          p.ID,
			Name:        p.Name,
			Version:     p.Version,
			Author:      p.Author,
			Description: p.Description,
			Kinds:       p.Kinds(),
			Dir:         p.Dir,
		}
		for _, t := range p.Manifest.Tools {
			info.Tools = append(info.Tools, t.Name)
		}
		sort.Strings(info.Tools)
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": out})
}

// handlePluginAsset: GET /plugins/<id>/<file...>.
func (s *server) handlePluginAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, pluginBasePath)
	i := strings.Index(rest, "/")
	if i <= 0 || i == len(rest)-1 {
		http.NotFound(w, r)
		return
	}
	id, rel := rest[:i], rest[i+1:]
	if !validPluginID(id) || !pluginRelPathOK(rel) {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(path.Ext(rel))
	ct, ok := pluginAssetExtTypes[ext]
	if !ok {
		// A missing type would otherwise be served as text/plain and could be
		// sniffed into something executable in the app's origin.
		http.Error(w, "file type not allowed", http.StatusForbidden)
		return
	}
	p, ok := installedPlugin(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	target, err := p.FilePath(rel)
	if err != nil {
		http.Error(w, "path escapes the plugin directory", http.StatusForbidden)
		return
	}
	// Belt and braces (same reasoning as the boot animation themes): verify
	// containment after resolving symlinks, so a symlink inside the plugin
	// directory cannot expose a file outside it.
	resolvedRoot, err := filepath.EvalSymlinks(p.Dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if inside, _ := pathWithin(resolvedRoot, resolved); !inside {
		http.Error(w, "path escapes the plugin directory", http.StatusForbidden)
		return
	}
	f, err := os.Open(resolved)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	// Plugin files are edited in place by their authors: never cache.
	h.Set("Cache-Control", "no-store")
	// A plugin stylesheet must not be able to pull in remote resources.
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; font-src 'self'")
	http.ServeContent(w, r, filepath.Base(rel), st.ModTime(), f)
}

// injectPluginAssets replaces pluginAssetsMarker with the <link>/<style> tags
// for the installed skins and wallpapers. A page without the marker (e.g. an
// older embedded UI) is returned unchanged.
func injectPluginAssets(page string) string {
	if !strings.Contains(page, pluginAssetsMarker) {
		return page
	}
	var b strings.Builder
	var wallpaper string
	for _, p := range plugin.Load() {
		for _, rel := range p.Styles {
			media := ""
			name := rel
			// A style entry may carry an optional "?media=..." suffix so a skin
			// can be limited to one colour scheme without extra JS.
			if i := strings.Index(name, "?media="); i >= 0 {
				media = name[i+len("?media="):]
				name = name[:i]
			}
			href := pluginBasePath + p.ID + "/" + strings.TrimPrefix(filepath.ToSlash(name), "/")
			b.WriteString(`<link rel="stylesheet" href="` + escAttr(href) + `"`)
			if media == "dark" || media == "light" {
				b.WriteString(` media="(prefers-color-scheme: ` + media + `)"`)
			}
			b.WriteString(">\n")
		}
		if p.Wallpaper != "" && wallpaper == "" {
			// First wallpaper wins: two of them would fight over the same
			// property and the order would depend on Map iteration.
			wallpaper = pluginBasePath + p.ID + "/" + strings.TrimPrefix(filepath.ToSlash(p.Wallpaper), "/")
		}
	}
	if wallpaper != "" {
		// `no-repeat` is explicit on purpose: the default is repeat, and a
		// wallpaper that tiles is never what someone meant. `cover` scales it to
		// fill the message column, and `scroll` (the default) keeps it pinned to
		// the box instead of scrolling away with the conversation.
		b.WriteString(`<style id="mc-plugin-wallpaper">#messages{background-image:url("` +
			escAttr(wallpaper) + `");background-size:cover;background-position:center;` +
			`background-repeat:no-repeat;background-attachment:scroll}</style>` + "\n")
	}
	return strings.Replace(page, pluginAssetsMarker, b.String(), 1)
}

// escAttr escapes a value for a double-quoted HTML attribute.
func escAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// installedPlugin finds an enabled plugin by id.
func installedPlugin(id string) (plugin.Plugin, bool) {
	for _, p := range plugin.Load() {
		if p.ID == id {
			return p, true
		}
	}
	return plugin.Plugin{}, false
}

// validPluginID mirrors plugin's id rule (lowercase letters, digits, dashes).
func validPluginID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i, c := range id {
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
		if !ok {
			return false
		}
		if i == 0 && !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// pluginRelPathOK rejects absolute paths and any ".." segment.
func pluginRelPathOK(rel string) bool {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.Contains(rel, `\`) {
		return false
	}
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, "?") || strings.Contains(rel, "#") {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// ensure json is used (writeJSON lives in web.go, this keeps the import honest
// if the list handler changes shape).
var _ = json.Marshal
