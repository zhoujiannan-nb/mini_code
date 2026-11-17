package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)


type AppConfig struct {
	Model        ModelConfig   `json:"model"`
	Models       []ModelConfig `json:"models"`
	DefaultModel string        `json:"default_model"`

	CompactionEnabled *bool `json:"compaction_enabled,omitempty"`

	TitleAuto *bool `json:"title_auto,omitempty"`

	TitleModel string `json:"title_model,omitempty"`

	WebPort int `json:"web_port"`
	UserAvatar string `json:"user_avatar,omitempty"`

	AgentAvatar string `json:"agent_avatar,omitempty"`

	BootAnim BootAnimConfig `json:"boot_anim"`
}

func (c *AppConfig) Compaction() bool { return true }

func (c *AppConfig) TitleAutoEnabled() bool { return c.TitleAuto == nil || *c.TitleAuto }

func (c *AppConfig) ModelByName(name string) *ModelConfig {
	for i := range c.Models {
		if c.Models[i].Name == name {
			return &c.Models[i]
		}
	}
	return nil
}

func (c *AppConfig) DefaultModelCfg() *ModelConfig {
	if m := c.ModelByName(c.DefaultModel); m != nil {
		return m
	}
	if len(c.Models) > 0 {
		return &c.Models[0]
	}
	return nil
}

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
			var ok bool
			switch m.Provider {
			case "deepseek":
				ok = m.ReasoningEffort == "off" || m.ReasoningEffort == "low" || m.ReasoningEffort == "high" || m.ReasoningEffort == "max"
			case "sglang", "vllm":
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
	Name          string `json:"name,omitempty"`
	Provider      string `json:"provider"`
	BaseURL       string `json:"base_url"`
	APIKey        string `json:"api_key"`
	ModelName     string `json:"model_name"`
	MaxTokens     int    `json:"max_tokens"`
	ContextWindow int    `json:"context_window"`
	Temperature   float64 `json:"temperature"`
	TopP          float64 `json:"top_p"`
	TopK          int     `json:"top_k,omitempty"`
	ReserveTokens int     `json:"reserve_tokens"`

	Thinking        string `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	CompactionEnabled *bool `json:"compaction_enabled,omitempty"`
}

func (m ModelConfig) Compaction() bool { return true }

func DefaultModelConfig() ModelConfig {
	return ModelConfig{
		Name:      "Qwen3.8-27B-FP82",
		Provider:  "sglang",
		BaseURL:   "http://ubasic.yuanshi-sec.com:5708/proxy/model/qwen3.82",
		APIKey:    "这里填你的key",
		ModelName: "Qwen3.8-27B-FP8",
	}
}

func DefaultAppConfig() *AppConfig {
	m := DefaultModelConfig()
	return &AppConfig{
		Models:       []ModelConfig{m},
		DefaultModel: m.Name,
		WebPort:      7500,
		BootAnim:     DefaultBootAnimConfig(),
	}
}

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

func GetConfigPath() (string, error) {
	dir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func SaveConfig(cfg *AppConfig, path string) error {
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

func applyDefaults(cfg *AppConfig) {
	d := DefaultModelConfig()

	cfg.CompactionEnabled = nil

	if len(cfg.Models) == 0 {
		legacy := cfg.Model
		if legacy.ModelName == "" {
			legacy = d
		}
		cfg.Models = []ModelConfig{legacy}
		cfg.Model = ModelConfig{}
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
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = cfg.Models[0].Name
	}
	if cfg.WebPort == 0 {
		cfg.WebPort = 7500
	}
	applyBootAnimDefaults(cfg)
}
