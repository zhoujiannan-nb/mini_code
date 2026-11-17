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
	pluginAssetsMarker = "<!--mc-plugin-assets-->"
)

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
	".html": "text/html; charset=utf-8",
	".htm":  "text/html; charset=utf-8",
}

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
		http.Error(w, "file type not allowed", http.StatusForbidden)
		return
	}
	p, ok := installedPlugin(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
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
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; font-src 'self'")
	http.ServeContent(w, r, filepath.Base(rel), st.ModTime(), f)
}

func injectPluginAssets(page string) string {
	if !strings.Contains(page, pluginAssetsMarker) {
		return page
	}
	var b strings.Builder
	var wallpaper string
	var accents []string
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
		b.WriteString(`<style id="mc-plugin-wallpaper">#messages{background-image:url("` +
			escAttr(wallpaper) + `");background-size:cover;background-position:center;` +
			`background-repeat:no-repeat;background-attachment:scroll}</style>` + "\n")
	}

	for _, name := range slotOrder {
		var inner strings.Builder
		for _, f := range slots[name] {
			inner.WriteString(`<iframe class="mc-slot-frame" data-plugin="` + escAttr(f.plugin) +
				`" src="` + escAttr(f.url) + `" height="` + escAttr(itoa(f.height)) +
				`" sandbox referrerpolicy="no-referrer" scrolling="no" loading="lazy"></iframe>`)
		}
		b.WriteString(`<div class="mc-slot" data-slot="` + escAttr(name) + `" hidden>` +
			inner.String() + `</div>` + "\n")
	}

	if len(accents) > 0 {
		accent := accents[len(accents)-1]
		if shades, ok := accentShades(accent); ok {
			b.WriteString(`<style id="mc-plugin-accent">` +
				`:root,html[data-theme="light"]{` + shades.light.css() + `}` +
				`html[data-theme="dark"]{` + shades.dark.css() + `}` +
				`</style>` + "\n")
		}
	}

	if len(motions) > 0 {
		decl := uiDeclaration{Motion: motions}
		if enc, err := json.Marshal(decl); err == nil {
			b.WriteString(`<script id="mc-plugin-ui" type="application/json">` +
				strings.ReplaceAll(string(enc), "</", `<\/`) + `</script>` + "\n")
		}
	}
	return strings.Replace(page, pluginAssetsMarker, b.String(), 1)
}

func pluginAssetURL(id, rel string) string {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "/")
	if i := strings.IndexByte(rel, '?'); i >= 0 {
		rel = rel[:i]
	}
	return pluginBasePath + id + "/" + rel
}

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

type slotFragment struct {
	plugin string
	url    string
	height int
}

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

type motionDecl struct {
	Plugin string `json:"plugin"`
	Event  string `json:"event"`
	Class  string `json:"class"`
}

type uiDeclaration struct {
	Motion []motionDecl `json:"motion,omitempty"`
}

func escAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func installedPlugin(id string) (plugin.Plugin, bool) {
	for _, p := range plugin.Load() {
		if p.ID == id {
			return p, true
		}
	}
	return plugin.Plugin{}, false
}

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

type accentSet struct {
	accent, text, btn, tint, tintBorder string
}

func (s accentSet) css() string {
	return "--accent:" + s.accent +
		";--accent-t:" + s.text +
		";--accent-btn:" + s.btn +
		";--accent-l:" + s.tint +
		";--accent-b:" + s.tintBorder
}

func accentShades(hex string) (struct{ light, dark accentSet }, bool) {
	var out struct{ light, dark accentSet }
	r, g, b, ok := parseHex6(hex)
	if !ok {
		return out, false
	}
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

var _ = json.Marshal
