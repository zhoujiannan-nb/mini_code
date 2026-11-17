package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
)

// The embed set is explicit rather than "all:ui": web/ui/ is a front-end
// development tree that carries node_modules and test files, and embedding the
// whole directory put ~23 MB of toolchain files into every binary (and exposed
// them over /node_modules/...). Only what the page actually serves is shipped.
//
// ui/css also carries the woff2 font table (ui/css/fonts/: JetBrains Mono
// subsets, ~175 KB) that css/fonts.css declares. Fonts ride along in the binary
// on purpose: a monospace UI whose rendering depends on which fonts the machine
// happens to have installed is not one look but several.
//
//go:embed ui/index.html ui/css ui/js ui/favicon.ico
var uiFS embed.FS

// embeddedPage returns the built-in web UI page.
func embeddedPage() (string, error) {
	b, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		return "", fmt.Errorf("read embedded ui: %w", err)
	}
	return string(b), nil
}

// uiAssetExts whitelists the file types the UI asset route may serve. Anything
// else falls through to the index handler (see handleIndex), so a request for
// "/favicon.ico" or a bogus path keeps its previous behaviour.
var uiAssetExts = map[string]bool{
	".css":   true,
	".js":    true,
	".svg":   true,
	".png":   true,
	".webp":  true,
	".ico":   true,
	".woff2": true,
}

// uiAsset is one embedded UI asset, preloaded with its content type and ETag.
// Assets are embedded at build time and never change while the process runs,
// so they are read once at startup and served from memory.
type uiAsset struct {
	body        []byte
	contentType string
	etag        string
}

var (
	uiAssetsOnce sync.Once
	uiAssets     map[string]*uiAsset
	uiAssetsErr  error
)

// loadUIAssets walks the embedded ui/ tree and caches every whitelisted file
// under its URL path ("/css/base.css"). The startup walk doubles as a build
// assertion: index.html referencing a file that was not embedded shows up here
// as a startup warning instead of a silent 404 in the browser.
func loadUIAssets() (map[string]*uiAsset, error) {
	uiAssetsOnce.Do(func() {
		out := make(map[string]*uiAsset)
		err := fs.WalkDir(uiFS, "ui", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			ext := strings.ToLower(path.Ext(p))
			if !uiAssetExts[ext] {
				return nil
			}
			body, err := uiFS.ReadFile(p)
			if err != nil {
				return err
			}
			ct := mime.TypeByExtension(ext)
			if ct == "" {
				ct = "application/octet-stream"
			}
			if ext == ".js" {
				ct = "text/javascript; charset=utf-8" // mime pkg is inconsistent here
			}
			if ext == ".css" {
				ct = "text/css; charset=utf-8"
			}
			if ext == ".svg" {
				ct = "image/svg+xml"
			}
			// .woff2 的类型也给死:Windows 上 mime.TypeByExtension 查的是系统
			// 注册表,没登记过 .woff2 的机器会退回 application/octet-stream,再
			// 配上下面的 nosniff,就是一次静默的字体加载失败(界面悄悄退回
			// Consolas)。内嵌字体的渲染结果不该看宿主机的脸色,见 css/fonts.css。
			if ext == ".woff2" {
				ct = "font/woff2"
			}
			sum := sha256.Sum256(body)
			out["/"+strings.TrimPrefix(p, "ui/")] = &uiAsset{
				body:        body,
				contentType: ct,
				etag:        `W/"` + hex.EncodeToString(sum[:8]) + `"`,
			}
			return nil
		})
		if err != nil {
			uiAssetsErr = err
			return
		}
		uiAssets = out
		// Surface index.html references that have no embedded counterpart.
		if page, err := embeddedPage(); err == nil {
			for _, ref := range referencedAssets(page) {
				if _, ok := out[ref]; !ok {
					slog.Warn("ui: index.html references a missing asset", "path", ref)
				}
			}
		}
	})
	return uiAssets, uiAssetsErr
}

// referencedAssets extracts local href/src targets from the page (relative
// paths only: absolute URLs, data: URIs and fragments are ignored).
func referencedAssets(page string) []string {
	var out []string
	for _, attr := range []string{`href="`, `src="`} {
		rest := page
		for {
			i := strings.Index(rest, attr)
			if i < 0 {
				break
			}
			rest = rest[i+len(attr):]
			j := strings.IndexByte(rest, '"')
			if j < 0 {
				break
			}
			ref := rest[:j]
			rest = rest[j:]
			switch {
			case ref == "", strings.HasPrefix(ref, "data:"),
				strings.HasPrefix(ref, "http://"), strings.HasPrefix(ref, "https://"),
				strings.HasPrefix(ref, "//"), strings.HasPrefix(ref, "#"):
				continue
			}
			if !strings.HasPrefix(ref, "/") {
				ref = "/" + ref
			}
			out = append(out, ref)
		}
	}
	return out
}

// tryServeUIAsset serves an embedded UI asset for r.URL.Path and reports
// whether it handled the request. Paths without a whitelisted extension are
// left alone (return false) so the index route keeps serving them.
func (s *server) tryServeUIAsset(w http.ResponseWriter, r *http.Request) bool {
	ext := strings.ToLower(path.Ext(r.URL.Path))
	if !uiAssetExts[ext] {
		return false
	}
	assets, err := loadUIAssets()
	if err != nil {
		slog.Error("ui assets unavailable", "err", err)
		http.Error(w, "ui assets unavailable", http.StatusInternalServerError)
		return true
	}
	a := assets[r.URL.Path]
	if a == nil {
		http.NotFound(w, r)
		return true
	}
	h := w.Header()
	h.Set("Content-Type", a.contentType)
	// Assets are embedded, so a given exe always serves the same bytes: the
	// ETag lets the WebView2/browser cache survive across page loads without
	// ever pinning a stale UI after an upgrade (the tag changes with the bytes).
	h.Set("ETag", a.etag)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	if _, err := w.Write(a.body); err != nil {
		slog.Debug("ui asset write failed", "path", r.URL.Path, "err", err)
	}
	return true
}
