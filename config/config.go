package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	CompactionEnabled *bool      `json:"compaction_enabled,omitempty"`
	WebPort           int        `json:"web_port"`
	DingTalk          DingTalkConfig `json:"dingtalk"`
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
	if c.DingTalk.Enabled {
		if c.DingTalk.AppKey == "" || c.DingTalk.AppSecret == "" {
			return fmt.Errorf("dingtalk is enabled but app_key/app_secret is empty")
		}
	}
	return nil
}

// DingTalkConfig configures the DingTalk channel (Stream mode).
type DingTalkConfig struct {
	Enabled    bool     `json:"enabled"`
	AppKey     string   `json:"app_key"`     // DingTalk app ClientID
	AppSecret  string   `json:"app_secret"`  // DingTalk app ClientSecret
	RobotCode  string   `json:"robot_code"`  // optional robot code
	AllowStaff []string `json:"allow_staff"` // optional sender whitelist (staffId); empty = allow all

	// ProgressInterval: seconds between progress pings for a running task.
	// 0 (unset) = default 10; negative = progress pings disabled.
	ProgressInterval int `json:"progress_interval"`
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

// DefaultModelConfig returns sensible defaults for model configuration.
func DefaultModelConfig() ModelConfig {
	return ModelConfig{
		Provider:      "vllm",
		BaseURL:       "http://demo/proxy/model/qwen_code/",
		APIKey:        "demo",
		MaxTokens:     31072,
		ContextWindow: 131072,
		Temperature:   0.6,
		TopP:          0.9,
		ReserveTokens: 4096,
		ModelName:     "qwen3.6-35b-fp8",
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
// If the file does not exist, it triggers the first-run interactive setup.
func LoadConfig() (*AppConfig, error) {
	path, err := GetConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Welcome to mini_code! This is your first run.")
			fmt.Println("Let's set up your configuration.")
			fmt.Println("Press Enter to accept the default value shown in [brackets].")
			fmt.Println(strings.Repeat("-", 50))

			cfg, err := FirstRunSetup()
			if err != nil {
				return nil, err
			}
			if err := SaveConfig(cfg, path); err != nil {
				return nil, err
			}
			fmt.Println(strings.Repeat("-", 50))
			fmt.Printf("Config saved to %s\n", path)
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
	if cfg.DingTalk.ProgressInterval == 0 {
		cfg.DingTalk.ProgressInterval = 10
	}
}

// ask reads a line from stdin with a prompt, returning defaultValue if empty.
func ask(reader *bufio.Reader, prompt string, defaultValue string) string {
	fmt.Printf("%s [%s]: ", prompt, defaultValue)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue
	}
	return line
}

// askInt reads an integer from stdin with a prompt, returning defaultValue if empty.
func askInt(reader *bufio.Reader, prompt string, defaultValue int) (int, error) {
	s := ask(reader, prompt, strconv.Itoa(defaultValue))
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid number for %s: %s", prompt, s)
	}
	return v, nil
}

// askFloat reads a float from stdin with a prompt, returning defaultValue if empty.
func askFloat(reader *bufio.Reader, prompt string, defaultValue float64) (float64, error) {
	s := ask(reader, prompt, strconv.FormatFloat(defaultValue, 'f', 1, 64))
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number for %s: %s", prompt, s)
	}
	return v, nil
}

// FirstRunSetup interactively asks the user for each configuration field.
func FirstRunSetup() (*AppConfig, error) {
	reader := bufio.NewReader(os.Stdin)
	d := DefaultModelConfig()
	cfg := &AppConfig{}

	// --- Model (first run configures a single model; more can be added
	// later from the web settings page) ---
	fmt.Println("\n--- Model Configuration ---")

	m := ModelConfig{}
	m.Provider = ask(reader, "  Provider (vllm/ollama/openai)", d.Provider)

	m.BaseURL = ask(reader, "  Base URL", d.BaseURL)

	m.APIKey = ask(reader, "  API Key", d.APIKey)

	m.ModelName = ask(reader, "  Model Name", d.ModelName)

	maxTokens, err := askInt(reader, "  Max Tokens", d.MaxTokens)
	if err != nil {
		return nil, err
	}
	m.MaxTokens = maxTokens

	ctxWindow, err := askInt(reader, "  Context Window", d.ContextWindow)
	if err != nil {
		return nil, err
	}
	m.ContextWindow = ctxWindow

	temp, err := askFloat(reader, "  Temperature", d.Temperature)
	if err != nil {
		return nil, err
	}
	m.Temperature = temp

	topP, err := askFloat(reader, "  Top P", d.TopP)
	if err != nil {
		return nil, err
	}
	m.TopP = topP

	reserveTokens, err := askInt(reader, "  Reserve Tokens", d.ReserveTokens)
	if err != nil {
		return nil, err
	}
	m.ReserveTokens = reserveTokens

	m.Name = m.ModelName
	cfg.Models = []ModelConfig{m}
	cfg.DefaultModel = m.Name

	// --- Web ---
	fmt.Println("\n--- Web Configuration ---")
	webPort, err := askInt(reader, "  Web Port", 7500)
	if err != nil {
		return nil, err
	}
	cfg.WebPort = webPort

	// --- DingTalk (optional) ---
	fmt.Println("\n--- DingTalk Channel (optional) ---")
	enable := ask(reader, "  Enable DingTalk channel (y/n)", "n")
	if strings.EqualFold(enable, "y") || strings.EqualFold(enable, "yes") {
		cfg.DingTalk.Enabled = true
		cfg.DingTalk.AppKey = ask(reader, "  AppKey (ClientID)", "")
		cfg.DingTalk.AppSecret = ask(reader, "  AppSecret (ClientSecret)", "")
		if cfg.DingTalk.AppKey == "" || cfg.DingTalk.AppSecret == "" {
			return nil, fmt.Errorf("DingTalk channel enabled but AppKey/AppSecret is empty")
		}
	}

	return cfg, nil
}
