package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeOpener swaps the OS opener for the duration of a test so a passing
// run never launches Explorer.
func withFakeOpener(t *testing.T, seen *[]string) {
	t.Helper()
	old := fsOpener
	fsOpener = func(p string) error {
		*seen = append(*seen, p)
		return nil
	}
	t.Cleanup(func() { fsOpener = old })
}

// TestResolveOpenPath covers the decorations a chat message puts around a path:
// quotes, backticks, trailing punctuation, a file:// URL, and a relative path
// that has to be resolved against the session work dir.
func TestResolveOpenPath(t *testing.T) {
	base := `D:\proj`
	cases := []struct {
		in, base, want string
		wantErr        bool
	}{
		{in: `D:\proj\index.html`, base: base, want: `D:\proj\index.html`},
		{in: "  `D:\\proj\\index.html`  ", base: base, want: `D:\proj\index.html`},
		{in: `"D:\proj\index.html"`, base: base, want: `D:\proj\index.html`},
		{in: `D:\proj\index.html,`, base: base, want: `D:\proj\index.html`},
		{in: `D:\proj\index.html。`, base: base, want: `D:\proj\index.html`},
		{in: `D:/proj/index.html`, base: base, want: `D:\proj\index.html`},
		{in: `web/ui/index.html`, base: base, want: `D:\proj\web\ui\index.html`},
		{in: `.\web\index.html`, base: base, want: `D:\proj\web\index.html`},
		{in: `file:///D:/proj/index.html`, base: "", want: `D:\proj\index.html`},
		{in: `web/index.html`, base: "", wantErr: true}, // relative with no base
		{in: `   `, wantErr: true},
	}
	for _, c := range cases {
		got, msg := resolveOpenPath(c.in, c.base)
		if c.wantErr {
			if msg == "" {
				t.Errorf("resolveOpenPath(%q, %q) = %q, want an error", c.in, c.base, got)
			}
			continue
		}
		if msg != "" {
			t.Errorf("resolveOpenPath(%q, %q) failed: %s", c.in, c.base, msg)
			continue
		}
		if got != c.want {
			t.Errorf("resolveOpenPath(%q, %q) = %q, want %q", c.in, c.base, got, c.want)
		}
	}
}

// TestFsOpenAPIOpensExistingPath: a real file is handed to the opener, a
// missing one is a 404 (a typo must not open a stray window), and a relative
// path is resolved against the posted base directory.
func TestFsOpenAPIOpensExistingPath(t *testing.T) {
	be := &savingBackend{}
	srv := httptest.NewServer(NewServer(be))
	defer srv.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "index.html")
	if err := os.WriteFile(file, []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var seen []string
	withFakeOpener(t, &seen)

	post := func(body string) (*http.Response, map[string]string) {
		resp, err := http.Post(srv.URL+"/api/fs/open", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST /api/fs/open: %v", err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}

	// 1) absolute path to a real file
	body, _ := json.Marshal(map[string]string{"path": file})
	resp, out := post(string(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", resp.StatusCode, out)
	}
	if len(seen) != 1 || seen[0] != file {
		t.Fatalf("opener saw %v, want [%s]", seen, file)
	}
	if out["kind"] != "file" {
		t.Errorf("kind = %q, want file", out["kind"])
	}

	// 2) relative path resolved against the posted base
	seen = nil
	body, _ = json.Marshal(map[string]string{"path": "index.html", "base": dir})
	resp, out = post(string(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("relative: status = %d, want 200 (%v)", resp.StatusCode, out)
	}
	if len(seen) != 1 || seen[0] != file {
		t.Fatalf("relative: opener saw %v, want [%s]", seen, file)
	}

	// 3) a path that does not exist is a 404, and nothing is opened
	seen = nil
	body, _ = json.Marshal(map[string]string{"path": filepath.Join(dir, "nope.html")})
	resp, out = post(string(body))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing: status = %d, want 404 (%v)", resp.StatusCode, out)
	}
	if len(seen) != 0 {
		t.Fatalf("missing file was opened: %v", seen)
	}
	if !strings.Contains(out["error"], "不存在") {
		t.Errorf("missing: error = %q, want a 文件不存在 message", out["error"])
	}

	// 4) a directory is accepted too (Explorer / Finder window)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seen = nil
	body, _ = json.Marshal(map[string]string{"path": sub})
	resp, out = post(string(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dir: status = %d, want 200 (%v)", resp.StatusCode, out)
	}
	if out["kind"] != "dir" {
		t.Errorf("dir: kind = %q, want dir", out["kind"])
	}

	// 5) a relative path with no base cannot be resolved
	resp, _ = post(`{"path":"web/index.html"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no base: status = %d, want 400", resp.StatusCode)
	}
}

// TestFsOpenRejectsBadBody keeps the endpoint from panicking on garbage.
func TestFsOpenRejectsBadBody(t *testing.T) {
	be := &savingBackend{}
	srv := httptest.NewServer(NewServer(be))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/fs/open", "application/json", strings.NewReader("{oops"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
