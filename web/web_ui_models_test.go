package web

import (
	"strings"
	"testing"
)

// TestIndexServesModelTileEditor verifies the settings dialog ships the block
// (tile) view of the model list: a grid of clickable model blocks, a "新增模型"
// button that opens the editor, and the one-click "获取参数" probe that fills
// the context window and sampling defaults from the serving framework.
func TestIndexServesModelTileEditor(t *testing.T) {
	// The UI is split into index.html + css/ + js/, so the markup, the model
	// editor's template strings and the probe call live in different files.
	// Assert against the whole shipped source, not the HTML shell alone.
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
		`max_tokens: 0, context_window: 0`, // a new model starts empty, not pre-filled
		`留空/0 = 未知(不自动压缩)`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("embedded UI is missing %q", want)
		}
	}
	// The old stacked per-model form (one long card per model, always shown)
	// must be gone: editing now happens in a panel behind a tile click. And so
	// must the things this version deliberately dropped — the compaction switch
	// (compression is always on) and the per-model reserve (unused, and a size
	// nobody can guess). A new model starts with nothing filled in: an unset
	// size or sampling value is simply not sent to the server.
	for _, old := range []string{
		`class="model-card"`, `mc-head`, `renderModelCards`,
		`c-compaction`, `compaction_enabled`, `reserve_tokens`, `上下文自动压缩`,
	} {
		if strings.Contains(page, old) {
			t.Errorf("embedded UI still carries a removed marker %q", old)
		}
	}
}
