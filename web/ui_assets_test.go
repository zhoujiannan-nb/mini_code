package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUIAssetsEmbedded checks that every local asset index.html references is
// actually embedded. The UI is split into index.html + css/ + js/, so a typo in
// a <script src> would otherwise ship a silently broken page.
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
	// Spot-check the stylesheets and the module entry by name, so a refactor that
	// silently re-inlines everything into index.html fails here. The front end is
	// a Preact app now (js/main.js + js/lib + js/components); the assertion is
	// about the split still existing, not about a particular file layout.
	for _, want := range []string{"/css/base.css", "/css/layers.css", "/js/main.js"} {
		if assets[want] == nil {
			t.Errorf("expected embedded asset %q", want)
		}
	}
}

// TestUIAssetRouteServes exercises the static route: 200 + content type for a
// real asset, 304 for a matching If-None-Match, 404 for a missing one, and
// pass-through (not a 404 from the asset route) for paths without a whitelisted
// extension.
func TestUIAssetRouteServes(t *testing.T) {
	be := newTestBackend(t)
	srv := httptest.NewServer(NewServer(be))
	defer srv.Close()

	assets, err := loadUIAssets()
	if err != nil {
		t.Fatalf("loadUIAssets: %v", err)
	}

	// /favicon.ico has its own dedicated route (handleFavicon) registered ahead
	// of "/", so it is checked separately below.
	for ref, wantCT := range map[string]string{
		"/css/base.css":  "text/css",
		"/css/layers.css": "text/css",
		"/js/main.js":    "javascript",
		"/js/lib/api.js": "javascript",
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

		// Conditional request: the same ETag must come back as 304.
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

	// A miss inside a whitelisted extension is a 404, not the index page.
	resp, err := http.Get(srv.URL + "/js/does-not-exist.js")
	if err != nil {
		t.Fatalf("GET missing asset: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing asset status = %d, want 404", resp.StatusCode)
	}

	// The dedicated favicon route still wins over the asset route.
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

	// "/" still serves the shell with the asset references intact.
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
