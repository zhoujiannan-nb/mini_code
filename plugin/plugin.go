package plugin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/user/mini_code/config"
)

var toolIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

type ToolSpec struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Parameters     map[string]any `json:"parameters"`
	Command        string         `json:"command"`
	Args           []string       `json:"args,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
}

type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`
	Kind string `json:"kind,omitempty"`

	Enabled *bool `json:"enabled,omitempty"`

	Tools     []ToolSpec    `json:"tools,omitempty"`
	Process   *ProcessSpec  `json:"process,omitempty"`
	Styles    []string      `json:"styles,omitempty"`
	Wallpaper string        `json:"wallpaper,omitempty"`
	UI *UISpec `json:"ui,omitempty"`
	Boot *BootSpec `json:"boot,omitempty"`
	Panels []PanelSpec `json:"panels,omitempty"`
}

const (
	CategoryProcess    = "process"
	CategoryTool       = "tool"
	CategoryAppearance = "appearance"
)

func (p Plugin) Category() string {
	switch strings.ToLower(strings.TrimSpace(p.Kind)) {
	case CategoryProcess, "program", "service", "daemon":
		return CategoryProcess
	case CategoryTool, "tools", "mcp", "skill":
		return CategoryTool
	case CategoryAppearance, "ui", "skin", "theme":
		return CategoryAppearance
	}
	if p.Process != nil {
		return CategoryProcess
	}
	if len(p.Tools) > 0 {
		return CategoryTool
	}
	return CategoryAppearance
}

type Plugin struct {
	Dir string
	Manifest
	LoadError string `json:"-"`
	Source string `json:"-"`
}

func (p Plugin) On() bool { return p.Enabled == nil || *p.Enabled }

func (p Plugin) Kinds() []string {
	var kinds []string
	if p.Process != nil {
		kinds = append(kinds, "process")
	}
	if len(p.Manifest.Tools) > 0 {
		kinds = append(kinds, "tools")
	}
	if len(p.Styles) > 0 {
		kinds = append(kinds, "skin")
	}
	if p.Wallpaper != "" {
		kinds = append(kinds, "wallpaper")
	}
	if p.UI != nil {
		if len(p.UI.Slots) > 0 {
			kinds = append(kinds, "slots")
		}
		if len(p.UI.Motion) > 0 {
			kinds = append(kinds, "motion")
		}
		if p.UI.Accent != "" {
			kinds = append(kinds, "accent")
		}
	}
	if p.Boot != nil {
		kinds = append(kinds, "boot")
	}
	return kinds
}

func DirPath() (string, error) {
	base, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "plugins"), nil
}

func Load() []Plugin {
	root, err := DirPath()
	if err != nil {
		slog.Warn("plugins: cannot resolve plugin directory", "err", err)
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)

	var out []Plugin
	seen := make(map[string]bool, len(dirs))
	for _, name := range dirs {
		if strings.HasPrefix(name, ".") {
			continue
		}
		p, err := loadOne(filepath.Join(root, name), name)
		if err != nil {
			slog.Warn("plugins: skipped", "dir", name, "err", err)
			continue
		}
		if p.Off() {
			slog.Debug("plugins: disabled", "id", p.ID, "reason", p.ReasonOff())
			continue
		}
		if seen[p.ID] {
			slog.Warn("plugins: skipped duplicate id", "id", p.ID, "dir", name)
			continue
		}
		seen[p.ID] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func LoadDir(dir, dirName string) (Plugin, error) {
	return loadOne(dir, dirName)
}

func loadOne(dir, dirName string) (Plugin, error) {
	path := filepath.Join(dir, "plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Plugin{}, fmt.Errorf("read plugin.json: %w", err)
	}
	var p Plugin
	if err := json.Unmarshal(data, &p.Manifest); err != nil {
		return Plugin{}, fmt.Errorf("parse plugin.json: %w", err)
	}
	p.Dir = dir
	if p.ID == "" {
		p.ID = dirName
	}
	if err := p.validate(); err != nil {
		return Plugin{}, err
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	return p, nil
}

func (p Plugin) validate() error {
	if !config.ValidBootAnimID(p.ID) {
		return fmt.Errorf("plugin id %q must be lowercase letters, digits and dashes", p.ID)
	}
	toolNames := make(map[string]bool, len(p.Tools))
	for i := range p.Tools {
		t := &p.Tools[i]
		if !toolIDPattern.MatchString(t.Name) {
			return fmt.Errorf("tool[%d] name %q must match ^[a-z][a-z0-9_]{1,63}$", i, t.Name)
		}
		if toolNames[t.Name] {
			return fmt.Errorf("duplicate tool name %q", t.Name)
		}
		toolNames[t.Name] = true
		if strings.TrimSpace(t.Command) == "" {
			return fmt.Errorf("tool %q has no command", t.Name)
		}
		if t.TimeoutSeconds <= 0 {
			t.TimeoutSeconds = 60
		}
		if t.TimeoutSeconds > 600 {
			t.TimeoutSeconds = 600
		}
		if strings.TrimSpace(t.Description) == "" {
			t.Description = "Plugin tool " + t.Name
		}
	}
	if err := p.validateUI(); err != nil {
		return err
	}
	if err := p.validateBoot(); err != nil {
		return err
	}
	for _, rel := range append(append([]string{}, p.Styles...), p.Wallpaper) {
		if rel == "" {
			continue
		}
		if _, err := p.FilePath(rel); err != nil {
			return err
		}
	}
	if p.Process != nil {
		if strings.TrimSpace(p.Process.Command) == "" {
			return fmt.Errorf("process.command is empty")
		}
		if p.Process.ReadyTimeoutSeconds < 0 {
			return fmt.Errorf("process.ready_timeout_seconds must not be negative")
		}
		if p.Process.ReadyTimeoutSeconds > 300 {
			p.Process.ReadyTimeoutSeconds = 300
		}
		switch strings.ToLower(strings.TrimSpace(p.Process.Restart)) {
		case "", "always", "on-failure", "never":
		default:
			return fmt.Errorf("process.restart %q must be always, on-failure or never", p.Process.Restart)
		}
		if hc := p.Process.HealthCheck; hc != nil {
			switch strings.ToLower(strings.TrimSpace(hc.Type)) {
			case "", "http", "tcp", "process":
			default:
				return fmt.Errorf("process.health_check.type %q must be http, tcp or process", hc.Type)
			}
			if hc.Type != "" && !strings.EqualFold(hc.Type, "process") && strings.TrimSpace(hc.URL) == "" {
				return fmt.Errorf("process.health_check.url is required for type %q", hc.Type)
			}
			if u := strings.TrimSpace(hc.URL); u != "" && strings.EqualFold(hc.Type, "http") &&
				!strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				return fmt.Errorf("process.health_check.url %q must be an http(s) URL for type http", u)
			}
		}
	}
	return nil
}

func (p Plugin) FilePath(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", fmt.Errorf("path %q must be relative to the plugin directory", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the plugin directory", rel)
	}
	full := filepath.Join(p.Dir, clean)
	relToDir, err := filepath.Rel(p.Dir, full)
	if err != nil || relToDir == ".." || strings.HasPrefix(relToDir, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the plugin directory", rel)
	}
	return full, nil
}
