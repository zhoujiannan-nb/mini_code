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
	Model    ModelConfig    `json:"model"`
	WebPort  int            `json:"web_port"`
	DingTalk DingTalkConfig `json:"dingtalk"`
}

// Validate checks the config is usable. Called before a web-driven save.
func (c *AppConfig) Validate() error {
	if c.Model.Provider == "" {
		return fmt.Errorf("model.provider is required")
	}
	if c.Model.BaseURL == "" {
		return fmt.Errorf("model.base_url is required")
	}
	if c.Model.ModelName == "" {
		return fmt.Errorf("model.model_name is required")
	}
	if c.Model.MaxTokens < 0 || c.Model.ContextWindow < 0 {
		return fmt.Errorf("model token limits must be >= 0")
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
	Provider      string  `json:"provider"`
	BaseURL       string  `json:"base_url"`
	APIKey        string  `json:"api_key"`
	ModelName     string  `json:"model_name"`
	MaxTokens     int     `json:"max_tokens"`
	ContextWindow int     `json:"context_window"`
	Temperature   float64 `json:"temperature"`
	TopP          float64 `json:"top_p"`
	ReserveTokens int     `json:"reserve_tokens"`

	// CompactionEnabled toggles automatic long-context compression.
	// Nil (key absent in config.json) = enabled (legacy behavior).
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

// applyDefaults fills in zero-value fields with defaults.
func applyDefaults(cfg *AppConfig) {
	d := DefaultModelConfig()
	if cfg.Model.Provider == "" {
		cfg.Model.Provider = d.Provider
	}
	if cfg.Model.BaseURL == "" {
		cfg.Model.BaseURL = d.BaseURL
	}
	if cfg.Model.APIKey == "" {
		cfg.Model.APIKey = d.APIKey
	}
	if cfg.Model.ModelName == "" {
		cfg.Model.ModelName = d.ModelName
	}
	if cfg.Model.MaxTokens == 0 {
		cfg.Model.MaxTokens = d.MaxTokens
	}
	if cfg.Model.ContextWindow == 0 {
		cfg.Model.ContextWindow = d.ContextWindow
	}
	if cfg.Model.Temperature == 0 {
		cfg.Model.Temperature = d.Temperature
	}
	if cfg.Model.TopP == 0 {
		cfg.Model.TopP = d.TopP
	}
	if cfg.Model.ReserveTokens == 0 {
		cfg.Model.ReserveTokens = d.ReserveTokens
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

	// --- Model ---
	fmt.Println("\n--- Model Configuration ---")

	cfg.Model.Provider = ask(reader, "  Provider (vllm/ollama/openai)", d.Provider)

	cfg.Model.BaseURL = ask(reader, "  Base URL", d.BaseURL)

	cfg.Model.APIKey = ask(reader, "  API Key", d.APIKey)

	cfg.Model.ModelName = ask(reader, "  Model Name", d.ModelName)

	maxTokens, err := askInt(reader, "  Max Tokens", d.MaxTokens)
	if err != nil {
		return nil, err
	}
	cfg.Model.MaxTokens = maxTokens

	ctxWindow, err := askInt(reader, "  Context Window", d.ContextWindow)
	if err != nil {
		return nil, err
	}
	cfg.Model.ContextWindow = ctxWindow

	temp, err := askFloat(reader, "  Temperature", d.Temperature)
	if err != nil {
		return nil, err
	}
	cfg.Model.Temperature = temp

	topP, err := askFloat(reader, "  Top P", d.TopP)
	if err != nil {
		return nil, err
	}
	cfg.Model.TopP = topP

	reserveTokens, err := askInt(reader, "  Reserve Tokens", d.ReserveTokens)
	if err != nil {
		return nil, err
	}
	cfg.Model.ReserveTokens = reserveTokens

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
