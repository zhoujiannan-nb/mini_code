package session

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/user/mini_code/config"
)

type pluginEntry struct {
	Path string
	Desc string
}

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
