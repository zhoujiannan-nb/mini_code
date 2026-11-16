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
		`id="btn-model"`,   // v3:输入区的模型芯片(单击开菜单,双击开思考浮层)
		`id="think-badge"`, // v3:思考胶囊内的档位徽标
		`id="think-pop"`,
		`id="tp-effort"`,
		`id="ctx-total"`,   // v3:输入区的 token 胶囊(点开管理抽屉)
		`id="sess-stats"`, // v3:胶囊里的 ⓘ,弹会话统计浮层
		"openThinkingPop",
		"toggleCtxPanel", // 胶囊直连抽屉
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
