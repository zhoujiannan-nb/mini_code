package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

func TestUIAssetsEmbedded(t *testing.T) {
	page, err := embeddedPage()
	if err != nil {
		t.Fatalf("embeddedPage: %v", err)
	}
	assets, err := loadUIAssets()
	if err != nil {
		t.Fatalf("loadUIAssets: %v", err)
	}
	refs := 0
	for _, ref := range referencedAssets(page) {
		refs++
		if assets[ref] == nil {
			t.Errorf("index.html references %q but it is not embedded", ref)
		}
	}
	if refs == 0 {
		t.Fatal("index.html references no local assets; the CSS/JS split is gone")
	}
	for _, want := range []string{"/css/base.css", "/css/layers.css", "/js/main.js"} {
		if assets[want] == nil {
			t.Errorf("expected embedded asset %q", want)
		}
	}
}

func TestUIFontTableEmbedded(t *testing.T) {
	assets, err := loadUIAssets()
	if err != nil {
		t.Fatalf("loadUIAssets: %v", err)
	}
	table := assets["/css/fonts.css"]
	if table == nil {
		t.Fatal("/css/fonts.css is not embedded, but index.html @imports it")
	}
	css := string(table.body)
	if !strings.Contains(css, "font-family: 'JetBrains Mono'") {
		t.Error("css/fonts.css declares no JetBrains Mono family")
	}
	srcs := regexp.MustCompile(`url\("([^"]+)"\)`).FindAllStringSubmatch(css, -1)
	if len(srcs) == 0 {
		t.Fatal("css/fonts.css has no @font-face src to check")
	}
	refs := make(map[string]bool, len(srcs))
	for _, m := range srcs {
		p := path.Join("/css", m[1])
		refs[p] = true
		a := assets[p]
		if a == nil {
			t.Errorf("css/fonts.css references %q but it is not embedded", p)
			continue
		}
		if !bytes.HasPrefix(a.body, []byte("wOF2")) {
			t.Errorf("%s is not a woff2 blob (%d bytes)", p, len(a.body))
		}
	}
	for p, a := range assets {
		if !strings.HasSuffix(p, ".woff2") {
			continue
		}
		if !refs[p] {
			t.Errorf("%s is embedded (%d bytes) but no @font-face names it", p, len(a.body))
		}
	}
	if assets["/css/fonts/jetbrainsmono-normal-latin.woff2"] == nil {
		t.Error("the latin subset is not embedded")
	}
	if _, err := os.Stat("ui/css/fonts/OFL.txt"); err != nil {
		t.Errorf("ui/css/fonts/OFL.txt: %v", err)
	}
}

func TestUIAssetRouteServes(t *testing.T) {
	be := newTestBackend(t)
	srv := httptest.NewServer(NewServer(be))
	defer srv.Close()

	assets, err := loadUIAssets()
	if err != nil {
		t.Fatalf("loadUIAssets: %v", err)
	}

	for ref, wantCT := range map[string]string{
		"/css/base.css":   "text/css",
		"/css/layers.css": "text/css",
		"/js/main.js":     "javascript",
		"/js/lib/api.js":  "javascript",
		"/css/fonts/jetbrainsmono-normal-latin.woff2": "font/woff2",
	} {
		resp, err := http.Get(srv.URL + ref)
		if err != nil {
			t.Fatalf("GET %s: %v", ref, err)
		}
		body := resp.Body
		ct := resp.Header.Get("Content-Type")
		etag := resp.Header.Get("ETag")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", ref, resp.StatusCode)
		}
		if !strings.Contains(ct, wantCT) {
			t.Errorf("GET %s Content-Type = %q, want substring %q", ref, ct, wantCT)
		}
		if etag == "" {
			t.Errorf("GET %s has no ETag", ref)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s X-Content-Type-Options = %q, want nosniff", ref, got)
		}
		body.Close()

		req, _ := http.NewRequest(http.MethodGet, srv.URL+ref, nil)
		req.Header.Set("If-None-Match", etag)
		cond, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s (conditional): %v", ref, err)
		}
		cond.Body.Close()
		if cond.StatusCode != http.StatusNotModified {
			t.Errorf("GET %s with If-None-Match status = %d, want 304", ref, cond.StatusCode)
		}
	}

	resp, err := http.Get(srv.URL + "/js/does-not-exist.js")
	if err != nil {
		t.Fatalf("GET missing asset: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing asset status = %d, want 404", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/favicon.ico")
	if err != nil {
		t.Fatalf("GET /favicon.ico: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /favicon.ico status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "image/") {
		t.Errorf("GET /favicon.ico Content-Type = %q, want image/*", resp.Header.Get("Content-Type"))
	}

	resp, err = http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}
	if seen := len(assets); seen == 0 {
		t.Fatal("no UI assets loaded")
	}
}
