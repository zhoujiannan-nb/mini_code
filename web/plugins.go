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
// deliberately narrower than "everything": a plugin ships styles, images and
// (for the third kind) HTML *fragments* it asked to have injected into a known
// slot — never a script. A script in the app's origin could read the session
// token and drive the agent, so .js is refused for every plugin, skin or not.
var pluginAssetExtTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".webp":  "image/webp",
	".gif":   "image/gif",
	".woff2": "font/woff2",
	".woff":  "font/woff",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	// Slot fragments. Served as text/html, but with the same locked-down CSP a
	// plugin stylesheet gets (default-src 'none'), so a fragment can carry
	// markup and inline style but cannot load or run anything.
	".html": "text/html; charset=utf-8",
	".htm":  "text/html; charset=utf-8",
}

// pluginInfo is the JSON shape of one plugin for GET /api/plugins. The centre's
// richer shape lives in plugins_centre.go (pluginCentreInfo); this one is kept
// because the UI's first implementation and its tests read it.
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
	// HTML is served ONLY when the manifest actually declares that exact file as
	// a slot fragment. Serving any .html a plugin happens to contain would turn
	// the plugin directory into a web root for arbitrary pages on the app's
	// origin; requiring the declaration keeps the surface equal to what the page
	// itself asks to be framed.
	if ext == ".html" || ext == ".htm" {
		if !p.DeclaresSlotHTML(rel) {
			http.Error(w, "html is only served for a declared ui.slots fragment", http.StatusForbidden)
			return
		}
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

// injectPluginAssets replaces pluginAssetsMarker with everything an installed
// plugin wants in the page: skin stylesheets, a wallpaper rule, slot fragments,
// the motion bindings the front-end reads.
//
// All of it is data. No plugin JavaScript is injected (or even servable), so a
// downloaded skin cannot reach the session token or drive the agent loop.
func injectPluginAssets(page string) string {
	if !strings.Contains(page, pluginAssetsMarker) {
		return page
	}
	var b strings.Builder
	var wallpaper string
	var accents []string
	// slots collects fragments per slot name, in plugin-id order (Load is
	// sorted), so the injection order is deterministic.
	slots := map[string][]slotFragment{}
	var slotOrder []string
	var motions []motionDecl

	for _, p := range plugin.Load() {
		for _, rel := range p.Styles {
			href := pluginAssetURL(p.ID, rel)
			b.WriteString(`<link rel="stylesheet" href="` + escAttr(href) + `"`)
			if media := styleMedia(rel); media != "" {
				b.WriteString(` media="(prefers-color-scheme: ` + media + `)"`)
			}
			b.WriteString(">\n")
		}
		if p.Wallpaper != "" && wallpaper == "" {
			// First wallpaper wins: two of them would fight over the same
			// property and the order would depend on Map iteration.
			wallpaper = pluginAssetURL(p.ID, p.Wallpaper)
		}
		if p.UI == nil {
			continue
		}
		if p.UI.Accent != "" {
			accents = append(accents, p.UI.Accent)
		}
		for _, sl := range p.UI.Slots {
			name := strings.TrimSpace(sl.Slot)
			if _, ok := slots[name]; !ok {
				slotOrder = append(slotOrder, name)
			}
			h := sl.Height
			if h <= 0 {
				h = 56
			}
			slots[name] = append(slots[name], slotFragment{
				plugin: p.ID,
				url:    pluginAssetURL(p.ID, sl.HTML),
				height: h,
			})
		}
		for _, mo := range p.UI.Motion {
			motions = append(motions, motionDecl{
				Plugin: p.ID,
				Event:  strings.ToLower(strings.TrimSpace(mo.Event)),
				Class:  strings.TrimSpace(mo.Class),
			})
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

	// Slots: one container per slot name holding a sandboxed iframe per fragment.
	// The iframe is created here (not by plugin HTML) so the plugin never gets a
	// say in the isolation attributes.
	for _, name := range slotOrder {
		var inner strings.Builder
		for _, f := range slots[name] {
			inner.WriteString(`<iframe class="mc-slot-frame" data-plugin="` + escAttr(f.plugin) +
				`" src="` + escAttr(f.url) + `" height="` + escAttr(itoa(f.height)) +
				// sandbox with no allow-scripts and no allow-same-origin: the
				// fragment can render, but it cannot run script or touch the
				// app's DOM, cookies or fetch its API.
				`" sandbox referrerpolicy="no-referrer" scrolling="no" loading="lazy"></iframe>`)
		}
		b.WriteString(`<div class="mc-slot" data-slot="` + escAttr(name) + `" hidden>` +
			inner.String() + `</div>` + "\n")
	}

	// Accent: the last plugin that declares one wins, the same "later overrides
	// earlier" rule a stylesheet follows.
	//
	// It must override the variables the shell actually reads (base.css defines
	// --accent/--accent-t per light and dark theme), and it must win against
	// both :root and html[data-theme="dark"]. That is why there is a dark variant
	// here instead of a single :root block: a single one would lose to the dark
	// theme's later, more specific rule and the accent would silently do nothing
	// in dark mode. Both selectors are also more specific than the theme's, so
	// source order does not matter either.
	if len(accents) > 0 {
		accent := accents[len(accents)-1]
		if shades, ok := accentShades(accent); ok {
			b.WriteString(`<style id="mc-plugin-accent">` +
				// The light rule is the plain theme, the dark rule repeats the
				// dark selector so it wins over base.css's dark block by order.
				`:root,html[data-theme="light"]{` + shades.light.css() + `}` +
				`html[data-theme="dark"]{` + shades.dark.css() + `}` +
				`</style>` + "\n")
		}
	}

	// Motion bindings travel as JSON in a <script type="application/json">: the
	// browser never executes that, and the front-end reads it with the same
	// JSON.parse. This is the only channel data reaches the animator through —
	// there is deliberately no plugin JS hook.
	if len(motions) > 0 {
		decl := uiDeclaration{Motion: motions}
		if enc, err := json.Marshal(decl); err == nil {
			// "</" would end the script element early; escape it (the payload is
			// JSON, so \/ is still a valid escape).
			b.WriteString(`<script id="mc-plugin-ui" type="application/json">` +
				strings.ReplaceAll(string(enc), "</", `<\/`) + `</script>` + "\n")
		}
	}
	return strings.Replace(page, pluginAssetsMarker, b.String(), 1)
}

// pluginAssetURL builds the served URL for one file inside a plugin.
func pluginAssetURL(id, rel string) string {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "/")
	// Strip an optional "?media=..." suffix: it is a declaration for the host,
	// not part of the file name.
	if i := strings.IndexByte(rel, '?'); i >= 0 {
		rel = rel[:i]
	}
	return pluginBasePath + id + "/" + rel
}

// styleMedia extracts the optional "?media=dark" suffix of a style entry.
func styleMedia(rel string) string {
	i := strings.Index(rel, "?media=")
	if i < 0 {
		return ""
	}
	if m := rel[i+len("?media="):]; m == "dark" || m == "light" {
		return m
	}
	return ""
}

// slotFragment is one injected fragment.
type slotFragment struct {
	plugin string
	url    string
	height int
}

// itoa renders a small non-negative int without pulling in strconv for one use.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [8]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

// motionDecl binds one app event to one CSS class.
type motionDecl struct {
	Plugin string `json:"plugin"`
	Event  string `json:"event"`
	Class  string `json:"class"`
}

// uiDeclaration is the JSON payload of #mc-plugin-ui.
type uiDeclaration struct {
	Motion []motionDecl `json:"motion,omitempty"`
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

// accentShades derives the theme variables a plugin accent has to override.
//
// base.css keeps six accent variables, and the shell reads all of them:
//
//	--accent      borders, focus rings, icons    (must read on the page bg)
//	--accent-btn  solid fill behind WHITE text   (must beat white)
//	--accent-t    accent-coloured TEXT on --surface (must be readable)
//	--accent-l    very light tint background
//	--accent-b    tint border
//
// A single value cannot serve all five: #ffd400 is fine as a border and
// unreadable as text, and as a button behind white text it is invisible. So the
// plugin supplies ONE colour (its intent) and these are derived from it:
// darkened in the light theme, lightened in the dark theme, which is exactly
// what base.css's own hand-picked pairs do (--accent-t is always a darker
// sibling of --accent in light mode, a lighter one in dark mode).
type accentSet struct {
	accent, text, btn, tint, tintBorder string
}

// css renders the declarations for one theme.
func (s accentSet) css() string {
	return "--accent:" + s.accent +
		";--accent-t:" + s.text +
		";--accent-btn:" + s.btn +
		";--accent-l:" + s.tint +
		";--accent-b:" + s.tintBorder
}

// accentShades parses "#rrggbb" (the only shape plugin.validateUI accepts) and
// returns the variable set for both themes.
func accentShades(hex string) (struct{ light, dark accentSet }, bool) {
	var out struct{ light, dark accentSet }
	r, g, b, ok := parseHex6(hex)
	if !ok {
		return out, false
	}
	// mixDark/mixLight blend toward black/white in sRGB, which is what a
	// hand-picked palette does and what keeps the ratio predictable.
	out.light = accentSet{
		accent:     hexOf(r, g, b),
		text:       hexOf(mixDark(r, .45), mixDark(g, .45), mixDark(b, .45)),
		btn:        hexOf(mixDark(r, .60), mixDark(g, .60), mixDark(b, .60)),
		tint:       "rgba(" + itoa(int(r)) + "," + itoa(int(g)) + "," + itoa(int(b)) + ",.13)",
		tintBorder: "rgba(" + itoa(int(r)) + "," + itoa(int(g)) + "," + itoa(int(b)) + ",.42)",
	}
	out.dark = accentSet{
		accent:     hexOf(r, g, b),
		text:       hexOf(mixLight(r, .45), mixLight(g, .45), mixLight(b, .45)),
		btn:        hexOf(mixDark(r, .35), mixDark(g, .35), mixDark(b, .35)),
		tint:       "rgba(" + itoa(int(r)) + "," + itoa(int(g)) + "," + itoa(int(b)) + ",.13)",
		tintBorder: "rgba(" + itoa(int(r)) + "," + itoa(int(g)) + "," + itoa(int(b)) + ",.42)",
	}
	return out, true
}

func mixDark(v, amount float64) float64 {
	return v * (1 - amount)
}

func mixLight(v, amount float64) float64 {
	return v + (255-v)*amount
}

func clamp8(v float64) int {
	n := int(v + .5)
	if n < 0 {
		return 0
	}
	if n > 255 {
		return 255
	}
	return n
}

func hexOf(r, g, b float64) string {
	const digits = "0123456789abcdef"
	vals := [3]int{clamp8(r), clamp8(g), clamp8(b)}
	out := make([]byte, 0, 7)
	out = append(out, '#')
	for _, v := range vals {
		out = append(out, digits[(v>>4)&0xF], digits[v&0xF])
	}
	return string(out)
}

func parseHex6(s string) (r, g, b float64, ok bool) {
	s = strings.TrimSpace(s)
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	var v [3]int
	for i := 0; i < 3; i++ {
		hi, ok1 := hexDigit(s[1+i*2])
		lo, ok2 := hexDigit(s[2+i*2])
		if !ok1 || !ok2 {
			return 0, 0, 0, false
		}
		v[i] = hi<<4 | lo
	}
	return float64(v[0]), float64(v[1]), float64(v[2]), true
}

func hexDigit(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

// ensure json is used (writeJSON lives in web.go, this keeps the import honest
// if the list handler changes shape).
var _ = json.Marshal
