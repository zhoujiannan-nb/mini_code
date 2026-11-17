package web

import (
	"strings"
	"testing"
)

func TestIndexServesModelTileEditor(t *testing.T) {
	page := uiSource(t)
	for _, want := range []string{
		`id="model-list" class="model-grid"`,
		`id="model-edit"`,
		`＋ 新增模型`,
		`function renderModelGrid`,
		`function renderModelEditor`,
		`function probeModel`,
		`/api/llm/probe`,
		`一键获取参数`,
		`data-act="probe"`,
		`field("top_k", "Top K"`,
		`max_tokens: 0, context_window: 0`,
		`留空/0 = 未知(不自动压缩)`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("embedded UI is missing %q", want)
		}
	}
	for _, old := range []string{
		`class="model-card"`, `mc-head`, `renderModelCards`,
		`c-compaction`, `compaction_enabled`, `reserve_tokens`, `上下文自动压缩`,
	} {
		if strings.Contains(page, old) {
			t.Errorf("embedded UI still carries a removed marker %q", old)
		}
	}
}
