package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

	page = page + jsModulesForTest(t)

	want := []string{
		`id="btn-model"`,
		`id="think-badge"`,
		`id="think-pop"`,
		`id="tp-effort"`,
		`id="ctx-total"`,
		`id="sess-stats"`,
		"openThinkingPop",
		"toggleCtxPanel",
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
