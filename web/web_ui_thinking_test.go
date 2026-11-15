package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIndexServesComposerThinkingUI is a smoke test that the embedded web UI
// ships the composer thinking control (double-click model button) and the
// cache-hit indicator, and no longer carries the old config-dialog thinking
// rows.
func TestIndexServesComposerThinkingUI(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	resp, err := http.Get(api.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	page := string(b)

	// The shell is served from "/" but its behaviour lives in the JS modules
	// served from /js/*. Pull those in as well so this smoke test keeps
	// covering the thinking control after the front-end was split up.
	page = page + jsModulesForTest(t)

	want := []string{
		`id="btn-model"`,
		`id="think-badge"`,
		`id="think-pop"`,
		`id="tp-effort"`,
		`id="ctx-total"`,   // v4:「总 tok · 缓存命中」芯片(点开管理抽屉)
		`id="sess-stats"`, // v4:「N 轮 · M 步 · X tok/s」芯片(弹会话统计)
		"openThinkingPop",
		"toggleCtxPanel", // 底栏直连抽屉
	}
	for _, m := range want {
		if !strings.Contains(page, m) {
			t.Errorf("embedded UI is missing %q", m)
		}
	}
	if strings.Contains(page, "thinking-rows") {
		t.Error("embedded UI still carries the old config-dialog thinking rows")
	}
}
