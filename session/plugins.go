package session

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/user/mini_code/config"
)

// pluginEntry is one line of ~/.mini_code/plugins.md: a path to a user
// plugin (an executable or a project directory) plus an optional
// description.
type pluginEntry struct {
	Path string
	Desc string
}

// parsePluginsLine splits one plugins.md line into path + optional
// description. The description follows the marker "desc:" or
// "description:" (half- or full-width colon); a line without the marker
// is a bare path.
func parsePluginsLine(line string) (pluginEntry, bool) {
	for _, sep := range []string{"desc:", "desc：", "description:", "description："} {
		if i := strings.Index(line, sep); i >= 0 {
			path := strings.TrimSpace(line[:i])
			if path == "" {
				return pluginEntry{}, false
			}
			return pluginEntry{Path: path, Desc: strings.TrimSpace(line[i+len(sep):])}, true
		}
	}
	path := strings.TrimSpace(line)
	if path == "" {
		return pluginEntry{}, false
	}
	return pluginEntry{Path: path}, true
}

// loadPlugins reads ~/.mini_code/plugins.md: one plugin per line, blank
// lines and '#' comments ignored. A missing or empty file yields an empty
// slice — the plugin section is simply not rendered.
func loadPlugins() []pluginEntry {
	dir, err := config.GetConfigDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "plugins.md"))
	if err != nil {
		return nil
	}
	var plugins []pluginEntry
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if p, ok := parsePluginsLine(line); ok {
			plugins = append(plugins, p)
		}
	}
	return plugins
}

// pluginsSection renders the [plugins] block for the system prompt, or ""
// when no plugins are configured. The list is stable across tasks (it only
// changes when plugins.md is edited), so — unlike a timestamp — it does not
// defeat the provider's prefix cache.
func pluginsSection() string {
	plugins := loadPlugins()
	if len(plugins) == 0 {
		return ""
	}
	lines := []string{"\n[plugins]\n"}
	for _, p := range plugins {
		if p.Desc != "" {
			lines = append(lines, "- "+p.Path+" — "+p.Desc)
		} else {
			lines = append(lines, "- "+p.Path)
		}
	}
	return strings.Join(lines, "\n")
}
