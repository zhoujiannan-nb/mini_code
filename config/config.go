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
	Model        ModelConfig   `json:"model"`
	Models       []ModelConfig `json:"models"`
	DefaultModel string        `json:"default_model"`

	// CompactionEnabled is deprecated and ignored: automatic long-context
	// compression is always on (applyDefaults clears this key), kept only so an
	// older config file still unmarshals.
	CompactionEnabled *bool `json:"compaction_enabled,omitempty"`

	WebPort int `json:"web_port"`
	// UserAvatar is the web user's avatar: a data URL
	// (data:image/jpeg|png|webp;base64,...) uploaded from the UI.
	// Empty = the UI renders the first-character + hash-color fallback.
	UserAvatar string `json:"user_avatar,omitempty"`

	// AgentAvatar is the assistant's avatar, same data-URL format, uploaded from
	// the settings dialog. The agent has one face for every session (a plain
	// ink tile with "M"), so this is a global override, not per-session state.
	// Empty = that built-in default tile.
	AgentAvatar string `json:"agent_avatar,omitempty"`

	// BootAnim configures the startup animation overlay (see bootanim.go).
	// An all-zero value from an older config file is filled with defaults
	// (applyBootAnimDefaults), which is also where the fields get normalized.
	BootAnim BootAnimConfig `json:"boot_anim"`
}

// Compaction reports whether automatic context compression is on. It is always
// on: keeping a long session inside the window is not optional, and the switch
// that used to allow turning it off let sessions run into provider-side length
// errors, so it was removed. The config key is still parsed for compatibility.
func (c *AppConfig) Compaction() bool { return true }

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
		// 0 on a sampling field means "unset" (the field is then not sent), so
		// only an out-of-range non-zero value is an error.
		if m.Temperature < 0 || m.Temperature > 2 {
			return fmt.Errorf("models[%d] (%s): temperature must be 0 (not set) or between 0 and 2", i, m.Name)
		}
		if m.TopP < 0 || m.TopP > 1 {
			return fmt.Errorf("models[%d] (%s): top_p must be 0 (not set) or between 0 and 1", i, m.Name)
		}
		if m.TopK < 0 {
			return fmt.Errorf("models[%d] (%s): top_k must be 0 (not set) or greater", i, m.Name)
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
	return c.BootAnim.Validate()
}

type ModelConfig struct {
	// Name is the short key the UI shows in the model dropdown and that
	// sessions reference (empty = resolved to the model name on load).
	Name          string `json:"name,omitempty"`
	Provider      string `json:"provider"`
	BaseURL       string `json:"base_url"`
	APIKey        string `json:"api_key"`
	ModelName     string `json:"model_name"`
	MaxTokens     int    `json:"max_tokens"`
	ContextWindow int    `json:"context_window"`
	// Sampling parameters are optional: 0 means "not set" and the field is then
	// left out of the request entirely, so the serving framework applies its own
	// (usually model-specific) default. Set a value only to override that.
	Temperature   float64 `json:"temperature"`
	TopP          float64 `json:"top_p"`
	TopK          int     `json:"top_k,omitempty"`
	ReserveTokens int     `json:"reserve_tokens"`

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

// Compaction reports whether automatic context compression is on. Legacy: the
// per-model switch was retired together with the global one, so this is true.
func (m ModelConfig) Compaction() bool { return true }

// DefaultModelConfig returns the model configuration used for the
// auto-generated first-run config (and as fallback defaults for fields
// missing in existing configs). The api_key is a placeholder the user
// must replace with a real key.
func DefaultModelConfig() ModelConfig {
	return ModelConfig{
		Name:      "Qwen3.8-27B-FP82",
		Provider:  "sglang",
		BaseURL:   "http://ubasic.yuanshi-sec.com:5708/proxy/model/qwen3.82",
		APIKey:    "这里填你的key",
		ModelName: "Qwen3.8-27B-FP8",
		// No max_tokens / context_window: filled by the user or by the "probe"
		// button in the UI, never guessed (see applyDefaults).
		// No temperature / top_p: left to the framework (see applyDefaults).
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
		BootAnim:     DefaultBootAnimConfig(),
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
	// The compaction toggle is retired (compression is always on), so neither the
	// global key nor the legacy per-model ones are ever written back: an older
	// config file loses them on the first save after the upgrade.
	saved := *cfg
	saved.CompactionEnabled = nil
	saved.Model.CompactionEnabled = nil
	if len(cfg.Models) > 0 {
		models := make([]ModelConfig, len(cfg.Models))
		copy(models, cfg.Models)
		for i := range models {
			models[i].CompactionEnabled = nil
		}
		saved.Models = models
	}
	data, err := json.MarshalIndent(&saved, "", "  ")
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
// wrapped into a one-element list.
func applyDefaults(cfg *AppConfig) {
	d := DefaultModelConfig()

	// Automatic compaction is always on — it is what keeps a long session inside
	// the window, so it no longer has a switch in the UI or in the config. Any
	// stored toggle (including the legacy per-model one) is dropped here.
	cfg.CompactionEnabled = nil

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
		// Sizes and sampling parameters are deliberately NOT defaulted. An unset
		// max_tokens / context_window / temperature / top_p / top_k stays unset
		// and is then omitted from the request, which leaves the choice to the
		// serving framework (its own default is usually the model's recommended
		// value). Inventing one here would silently override it — and a made-up
		// context window would silently drive the compaction thresholds, which is
		// worse than no automatic compaction at all.
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = cfg.Models[0].Name
	}
	if cfg.WebPort == 0 {
		cfg.WebPort = 7500
	}
	applyBootAnimDefaults(cfg)
}
