package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNoChange is returned by a config save when nothing changed.
var ErrNoChange = errors.New("no config changes")

type AppConfig struct {
	// Model is the legacy single-model config. On load it is wrapped into
	// Models (see applyDefaults) and cleared, so saved configs only carry
	// the models list.
	Model        ModelConfig    `json:"model"`
	Models       []ModelConfig  `json:"models"`
	DefaultModel string         `json:"default_model"`

	// CompactionEnabled toggles automatic long-context compression
	// globally. Nil (key absent) = enabled (legacy behavior).
	CompactionEnabled *bool `json:"compaction_enabled,omitempty"`
	WebPort           int   `json:"web_port"`
	// UserAvatar is the web user's avatar: a data URL
	// (data:image/jpeg|png|webp;base64,...) uploaded from the UI.
	// Empty = the UI renders the first-character + hash-color fallback.
	UserAvatar string `json:"user_avatar,omitempty"`
}

// Compaction reports whether automatic context compression is on.
// An absent config key keeps the historical default: enabled.
func (c *AppConfig) Compaction() bool {
	return c.CompactionEnabled == nil || *c.CompactionEnabled
}

// ModelByName returns the model entry with the given name, or nil.
func (c *AppConfig) ModelByName(name string) *ModelConfig {
	for i := range c.Models {
		if c.Models[i].Name == name {
			return &c.Models[i]
		}
	}
	return nil
}

// DefaultModelCfg returns the default model entry (falling back to the
// first configured model), or nil when no model is configured.
func (c *AppConfig) DefaultModelCfg() *ModelConfig {
	if m := c.ModelByName(c.DefaultModel); m != nil {
		return m
	}
	if len(c.Models) > 0 {
		return &c.Models[0]
	}
	return nil
}

// Validate checks the config is usable. Called before a web-driven save.
func (c *AppConfig) Validate() error {
	if len(c.Models) == 0 {
		return fmt.Errorf("at least one model is required")
	}
	seen := make(map[string]bool, len(c.Models))
	for i := range c.Models {
		m := &c.Models[i]
		if m.Name == "" {
			return fmt.Errorf("models[%d].name is required", i)
		}
		if seen[m.Name] {
			return fmt.Errorf("duplicate model name %q", m.Name)
		}
		seen[m.Name] = true
		if m.Provider == "" {
			return fmt.Errorf("models[%d] (%s): provider is required", i, m.Name)
		}
		if m.BaseURL == "" {
			return fmt.Errorf("models[%d] (%s): base_url is required", i, m.Name)
		}
		if m.ModelName == "" {
			return fmt.Errorf("models[%d] (%s): model_name is required", i, m.Name)
		}
		if m.MaxTokens < 0 || m.ContextWindow < 0 {
			return fmt.Errorf("models[%d] (%s): token limits must be >= 0", i, m.Name)
		}
		if m.Thinking != "" && m.Thinking != "enabled" && m.Thinking != "disabled" {
			return fmt.Errorf("models[%d] (%s): thinking must be \"enabled\" or \"disabled\"", i, m.Name)
		}
		if m.ReasoningEffort != "" {
			// Valid reasoning_effort values differ per provider:
			//   deepseek: off / low / high / max
			//   sglang (Qwen3-style): xhigh / medium / low
			// Other providers ignore the field, so no restriction there.
			var ok bool
			switch m.Provider {
			case "deepseek":
				ok = m.ReasoningEffort == "off" || m.ReasoningEffort == "low" || m.ReasoningEffort == "high" || m.ReasoningEffort == "max"
			case "sglang":
				ok = m.ReasoningEffort == "xhigh" || m.ReasoningEffort == "medium" || m.ReasoningEffort == "low"
			default:
				ok = true
			}
			if !ok {
				return fmt.Errorf("models[%d] (%s): invalid reasoning_effort %q for provider %s", i, m.Name, m.ReasoningEffort, m.Provider)
			}
		}
	}
	if c.DefaultModel != "" && !seen[c.DefaultModel] {
		return fmt.Errorf("default_model %q not found in models", c.DefaultModel)
	}
	if c.WebPort < 1 || c.WebPort > 65535 {
		return fmt.Errorf("web_port must be between 1 and 65535")
	}
	return nil
}

type ModelConfig struct {
	// Name is the short key the UI shows in the model dropdown and that
	// sessions reference (empty = resolved to the model name on load).
	Name            string  `json:"name,omitempty"`
	Provider        string  `json:"provider"`
	BaseURL         string  `json:"base_url"`
	APIKey          string  `json:"api_key"`
	ModelName       string  `json:"model_name"`
	MaxTokens       int     `json:"max_tokens"`
	ContextWindow   int     `json:"context_window"`
	Temperature     float64 `json:"temperature"`
	TopP            float64 `json:"top_p"`
	ReserveTokens   int     `json:"reserve_tokens"`

	// Thinking and ReasoningEffort control the model's thinking depth. DeepSeek
	// and SGLang (Qwen3-style) both honor them; other providers ignore them.
	// Empty = provider default (DeepSeek: thinking on, effort high).
	Thinking        string `json:"thinking,omitempty"`         // "" | "enabled" | "disabled"
	ReasoningEffort string `json:"reasoning_effort,omitempty"` // deepseek: off/low/high/max; sglang: xhigh/medium/low

	// CompactionEnabled is the legacy per-model compaction toggle. It is
	// migrated to the global AppConfig.CompactionEnabled on load and no
	// longer read at runtime.
	CompactionEnabled *bool `json:"compaction_enabled,omitempty"`
}

// Compaction reports whether automatic context compression is on.
// An absent config key keeps the historical default: enabled.
func (m ModelConfig) Compaction() bool {
	return m.CompactionEnabled == nil || *m.CompactionEnabled
}

// DefaultModelConfig returns the model configuration used for the
// auto-generated first-run config (and as fallback defaults for fields
// missing in existing configs). The api_key is a placeholder the user
// must replace with a real key.
func DefaultModelConfig() ModelConfig {
	return ModelConfig{
		Name:          "Qwen3.8-27B-FP82",
		Provider:      "sglang",
		BaseURL:       "http://ubasic.yuanshi-sec.com:5708/proxy/model/qwen3.82",
		APIKey:        "这里填你的key",
		ModelName:     "Qwen3.8-27B-FP8",
		MaxTokens:     31072,
		ContextWindow: 200000,
		Temperature:   1,
		TopP:          0.95,
		ReserveTokens: 4096,
	}
}

// DefaultAppConfig returns the config auto-generated on first run: the
// default model (with a placeholder API key) and the standard web port.
func DefaultAppConfig() *AppConfig {
	m := DefaultModelConfig()
	return &AppConfig{
		Models:       []ModelConfig{m},
		DefaultModel: m.Name,
		WebPort:      7500,
	}
}

// GetConfigDir returns ~/.mini_code/ directory, creating it if necessary.
func GetConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot get home directory: %w", err)
	}
	dir := filepath.Join(home, ".mini_code")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("cannot create config directory %s: %w", dir, err)
	}
	return dir, nil
}

// GetConfigPath returns the path to ~/.mini_code/config.json.
func GetConfigPath() (string, error) {
	dir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// SaveConfig writes the AppConfig to the given path as indented JSON.
func SaveConfig(cfg *AppConfig, path string) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("cannot write config to %s: %w", path, err)
	}
	return nil
}

// LoadConfig loads the config from ~/.mini_code/config.json.
// If the file does not exist (first run), it generates a default config
// automatically — no interactive setup. The user only needs to fill in
// the API key afterwards (edit the file or use the web settings page).
func LoadConfig() (*AppConfig, error) {
	path, err := GetConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultAppConfig()
			if err := SaveConfig(cfg, path); err != nil {
				return nil, err
			}
			fmt.Println("Welcome to mini_code! This is your first run.")
			fmt.Printf("Generated a default config at %s\n", path)
			fmt.Println("Next step: replace the placeholder api_key (\"这里填你的key\") with your own key.")
			return cfg, nil
		}
		return nil, fmt.Errorf("cannot read config: %w", err)
	}

	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config format: %w", err)
	}

	applyDefaults(&cfg)
	return &cfg, nil
}

// applyDefaults fills in zero-value fields with defaults. It also migrates
// legacy single-model configs: a "model" entry without a "models" list is
// wrapped into a one-element list, and the legacy per-model compaction
// toggle is moved to the global switch.
func applyDefaults(cfg *AppConfig) {
	d := DefaultModelConfig()

	// Legacy per-model compaction → global switch (before the legacy
	// model entry is consumed and cleared below).
	if cfg.CompactionEnabled == nil && cfg.Model.CompactionEnabled != nil {
		cfg.CompactionEnabled = cfg.Model.CompactionEnabled
	}

	// Legacy single-model config → models list.
	if len(cfg.Models) == 0 {
		legacy := cfg.Model
		if legacy.ModelName == "" {
			legacy = d
		}
		cfg.Models = []ModelConfig{legacy}
		cfg.Model = ModelConfig{} // do not write the legacy key back
	}

	for i := range cfg.Models {
		m := &cfg.Models[i]
		if m.Provider == "" {
			m.Provider = d.Provider
		}
		if m.BaseURL == "" {
			m.BaseURL = d.BaseURL
		}
		if m.APIKey == "" {
			m.APIKey = d.APIKey
		}
		if m.ModelName == "" {
			m.ModelName = d.ModelName
		}
		if m.Name == "" {
			m.Name = m.ModelName
		}
		if m.MaxTokens == 0 {
			m.MaxTokens = d.MaxTokens
		}
		if m.ContextWindow == 0 {
			m.ContextWindow = d.ContextWindow
		}
		if m.Temperature == 0 {
			m.Temperature = d.Temperature
		}
		if m.TopP == 0 {
			m.TopP = d.TopP
		}
		if m.ReserveTokens == 0 {
			m.ReserveTokens = d.ReserveTokens
		}
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = cfg.Models[0].Name
	}
	if cfg.WebPort == 0 {
		cfg.WebPort = 7500
	}
}


