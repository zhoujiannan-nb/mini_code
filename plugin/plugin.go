// Package plugin provides mini_code's file-based plugin layer.
//
// The core is deliberately NOT pluggable: the agent loop, the permission gate,
// context compaction and the base tools (read/write/edit/exec/search/...) are
// compiled in and cannot be replaced by a plugin. What IS pluggable are the
// peripheral things a user may want to swap:
//
//	tools      extra tools appended after the core tool list (e.g. a web
//	           search / browser driver wrapping an external program)
//	skin       extra stylesheets injected into the UI
//	wallpaper  a background image for the chat area
//	memory     a notes file appended to the system prompt
//	bootanim   handled by the boot animation host (config.BootAnim + web/bootanim.go),
//	           which already loads themes from a directory
//
// A plugin is a directory under ~/.mini_code/plugins/ containing plugin.json:
//
//	{
//	  "id": "web-search",
//	  "name": "Web Search",
//	  "version": "1.0.0",
//	  "description": "Bing/Google search through a local browser driver",
//	  "enabled": true,
//	  "tools": [{
//	    "name": "web_search",
//	    "description": "Search the web and return JSON results.",
//	    "parameters": {"type": "object", "properties": {...}, "required": ["query"]},
//	    "command": "python",
//	    "args": ["D:\\project_ai\\web_search\\ws.py", "search"],
//	    "timeout_seconds": 90
//	  }],
//	  "styles": ["skin.css"],
//	  "wallpaper": "bg.webp",
//	  "memory": "memory.md"
//	}
//
// Prefix-cache safety is a hard requirement, so the load order is fixed:
// plugins are returned sorted by id, tools are appended AFTER the core tool
// list (never interleaved), and every field comes from an on-disk file that
// only changes when the user edits it.
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

// toolIDPattern is the character set a plugin tool name may use. It has to be
// a valid function name for every provider's tool-calling API.
var toolIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// ToolSpec is one tool a plugin contributes.
type ToolSpec struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Parameters     map[string]any `json:"parameters"`
	Command        string         `json:"command"`
	Args           []string       `json:"args,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
}

// Manifest is the on-disk plugin.json contract. Unknown keys are ignored so a
// newer plugin can carry fields an older host has never heard of.
type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`
	// Kind is the coarse category the plugin centre groups by:
	//
	//	process  a companion program mini_code starts and supervises
	//	tool     extra tools appended to the model's tool list
	//	appearance  skins, wallpapers, slot fragments, animations (the UI half)
	//
	// It is optional: a manifest that omits it is categorised from what it
	// actually declares (see Plugin.Category), so every plugin written before
	// this field existed keeps working unchanged.
	Kind string `json:"kind,omitempty"`

	// Enabled is a pointer so a manifest without the key means "on" — the
	// common case is a plugin you just dropped in.
	Enabled *bool `json:"enabled,omitempty"`

	Tools     []ToolSpec    `json:"tools,omitempty"`
	Process   *ProcessSpec  `json:"process,omitempty"`
	Styles    []string      `json:"styles,omitempty"`
	Wallpaper string        `json:"wallpaper,omitempty"`
	Memory    string        `json:"memory,omitempty"`
	// UI declares the third kind: things the browser half renders (skins,
	// slot fragments, animations). See ui.go for the contract — note that a
	// desktop pet is NOT part of it, on purpose.
	UI *UISpec `json:"ui,omitempty"`
	// Boot declares that this plugin also ships a STARTUP ANIMATION. It is an
	// appearance-plugin surface, not a fourth kind: a clip is a static HTML
	// bundle, which is what an appearance plugin already is (see boot.go).
	Boot *BootSpec `json:"boot,omitempty"`
	// Panels is the older, flat way to declare the same thing; UI supersedes it
	// but both are read so a plugin written against the first draft still works.
	Panels []PanelSpec `json:"panels,omitempty"`
}

// Category names are the three buckets the plugin centre shows.
const (
	CategoryProcess    = "process"    // kind 1: a companion program
	CategoryTool       = "tool"       // kind 2: appended tools (search, skills, mcp…)
	CategoryAppearance = "appearance" // kind 3: skins, wallpapers, slots, animations
)

// Category classifies the plugin. An explicit "kind" wins; otherwise it is
// derived from what the manifest actually declares, in the order process →
// tool → appearance. A manifest with nothing in it is "appearance" — the only
// bucket where an empty plugin is harmless (it just does nothing).
func (p Plugin) Category() string {
	switch strings.ToLower(strings.TrimSpace(p.Kind)) {
	case CategoryProcess, "program", "service", "daemon":
		return CategoryProcess
	case CategoryTool, "tools", "mcp", "skill":
		return CategoryTool
	case CategoryAppearance, "ui", "skin", "theme":
		return CategoryAppearance
	}
	// No explicit kind: infer. A plugin that both runs a process and adds tools
	// is filed under process, because that is the part the user has to trust
	// with executing code.
	if p.Process != nil {
		return CategoryProcess
	}
	if len(p.Tools) > 0 {
		return CategoryTool
	}
	return CategoryAppearance
}

// Plugin is a loaded, validated manifest plus the directory it came from.
type Plugin struct {
	Dir string
	Manifest
	// LoadError is set only by Discover for a directory whose plugin.json could
	// not be read or validated. Load never returns such a plugin.
	LoadError string `json:"-"`
	// Source is set by Discover: "catalogue" when the plugin was installed from
	// the remote repo (a marker file proves it), empty when a user dropped the
	// directory in by hand.
	Source string `json:"-"`
}

// Enabled reports whether the plugin is switched on (absent = on).
func (p Plugin) On() bool { return p.Enabled == nil || *p.Enabled }

// Kinds lists what the plugin contributes, in a fixed order, for the UI and the
// diagnostics endpoint.
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
	if p.Memory != "" {
		kinds = append(kinds, "memory")
	}
	return kinds
}

// DirPath is the plugin root: ~/.mini_code/plugins. It does not create the
// directory — a missing directory simply means "no plugins installed".
func DirPath() (string, error) {
	base, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "plugins"), nil
}

// Load reads every plugin under ~/.mini_code/plugins and returns them sorted by
// id (deterministic order keeps the prompt prefix stable across tasks).
//
// A broken plugin is skipped with a warning and never fails the load: a typo in
// one plugin.json must not stop the agent from starting.
func Load() []Plugin {
	root, err := DirPath()
	if err != nil {
		slog.Warn("plugins: cannot resolve plugin directory", "err", err)
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil // not installed / unreadable: no plugins
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
			continue // .install-* staging dirs are not plugins
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
	// Sort by id: the directory name and the manifest id are usually equal, but
	// the manifest is what the UI shows, so order by that.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LoadDir reads and validates exactly one plugin directory. The installer uses
// it after moving a downloaded bundle into place: validating under the real
// directory name is what catches a manifest whose id does not match its folder.
func LoadDir(dir, dirName string) (Plugin, error) {
	return loadOne(dir, dirName)
}

// loadOne reads and validates one plugin directory.
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
		p.ID = dirName // the directory name is a good default
	}
	if err := p.validate(); err != nil {
		return Plugin{}, err
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	return p, nil
}

// validate enforces the contract: a URL/path-safe id, valid tool names, and
// every referenced file inside the plugin directory.
func (p Plugin) validate() error {
	if !config.ValidBootAnimID(p.ID) {
		// Reuse the bootanim id rule: lowercase letters, digits, dashes, 1-64
		// chars, starts alphanumeric. It is exactly what a URL path segment and
		// a directory name need.
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
	// Legacy panels are lifted into p.UI by validateUI, so a manifest that only
	// used "panels" is validated exactly like one that used "ui".
	for _, rel := range append(append([]string{}, p.Styles...), p.Wallpaper, p.Memory) {
		if rel == "" {
			continue
		}
		if _, err := p.FilePath(rel); err != nil {
			return err
		}
	}
	// A process plugin's command is required; its args are not. The check runs
	// after validateUI so the "what is this plugin" errors surface first.
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

// FilePath resolves a manifest-relative path to an absolute path inside the
// plugin directory. Absolute paths and ".." are rejected: a plugin names files
// next to itself, it cannot reach into the rest of the filesystem through the
// asset route.
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
	// Belt and braces: verify the joined result is still inside p.Dir (catches
	// odd separators and Windows device paths).
	relToDir, err := filepath.Rel(p.Dir, full)
	if err != nil || relToDir == ".." || strings.HasPrefix(relToDir, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the plugin directory", rel)
	}
	return full, nil
}

// MemoryText returns the plugin's memory notes ("" when it has none or the file
// cannot be read). The text is a plain file, so it is identical across tasks
// until the user edits it — that keeps the prompt prefix cacheable.
func (p Plugin) MemoryText() string {
	if p.Memory == "" {
		return ""
	}
	path, err := p.FilePath(p.Memory)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(b))
	if len(text) > 16*1024 {
		text = text[:16*1024] + "\n…(truncated)"
	}
	return text
}
