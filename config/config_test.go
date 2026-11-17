package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if d := DefaultModelConfig(); d.MaxTokens != 0 || d.ContextWindow != 0 {
		t.Errorf("default model carries invented sizes: %+v", d)
	}
	if err := (&AppConfig{WebPort: 7500, Models: []ModelConfig{{Name: "m", Provider: "vllm", BaseURL: "u", ModelName: "m1"}}}).Validate(); err != nil {
		t.Errorf("a model without sizes should validate, got %v", err)
	}
}

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
	var reloaded AppConfig
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reloaded.Compaction() {
		t.Error("reloaded config reported compaction off")
	}
}
