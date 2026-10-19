package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSizesAndSamplingAreNotInvented: a model that only says where it lives must
// keep its sizes and sampling parameters empty. Inventing them was what made the
// UI show a guess as if the server had reported it, and made the loop compact on a
// made-up window.
func TestSizesAndSamplingAreNotInvented(t *testing.T) {
	cfg := &AppConfig{Models: []ModelConfig{{
		Name: "m", Provider: "vllm", BaseURL: "http://h:1/v1", APIKey: "k", ModelName: "m1",
	}}}
	applyDefaults(cfg)
	m := cfg.Models[0]
	if m.MaxTokens != 0 || m.ContextWindow != 0 {
		t.Errorf("applyDefaults invented sizes: max_tokens=%d context_window=%d", m.MaxTokens, m.ContextWindow)
	}
	if m.Temperature != 0 || m.TopP != 0 || m.TopK != 0 {
		t.Errorf("applyDefaults invented sampling: %+v", m)
	}
	// The shipped first-run model must not carry a made-up window either.
	if d := DefaultModelConfig(); d.MaxTokens != 0 || d.ContextWindow != 0 {
		t.Errorf("default model carries invented sizes: %+v", d)
	}
	// And empty sizes are valid: unknown is not an error, it just disables the
	// window-based thresholds.
	if err := (&AppConfig{WebPort: 7500, Models: []ModelConfig{{Name: "m", Provider: "vllm", BaseURL: "u", ModelName: "m1"}}}).Validate(); err != nil {
		t.Errorf("a model without sizes should validate, got %v", err)
	}
}

// TestCompactionIsAlwaysOn: the switch is gone (UI and config), because a session
// that cannot compact simply runs past the model's window. A stored "false" —
// global or legacy per-model — must not turn compression off, and must not be
// written back.
func TestCompactionIsAlwaysOn(t *testing.T) {
	off := false
	cfg := &AppConfig{
		Models:            []ModelConfig{{Name: "m", Provider: "vllm", BaseURL: "u", ModelName: "m1"}},
		CompactionEnabled: &off,
	}
	cfg.Model.CompactionEnabled = &off
	applyDefaults(cfg)
	if !cfg.Compaction() {
		t.Fatal("automatic compaction reported off")
	}
	if cfg.Model.Compaction() != true {
		t.Fatal("legacy per-model compaction reported off")
	}

	path := filepath.Join(t.TempDir(), "config.json")
	// Saved straight from a config that still carries the key (a file written by
	// an older version, untouched by applyDefaults).
	stored := &AppConfig{
		Models:            []ModelConfig{{Name: "m", Provider: "vllm", BaseURL: "u", ModelName: "m1"}},
		CompactionEnabled: &off,
	}
	stored.Model.CompactionEnabled = &off
	if err := SaveConfig(stored, path); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(raw), "compaction_enabled") {
		t.Errorf("the deprecated toggle was written back: %s", raw)
	}
	// Reading it again (with the old key still in the file) stays on.
	var reloaded AppConfig
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reloaded.Compaction() {
		t.Error("reloaded config reported compaction off")
	}
}
