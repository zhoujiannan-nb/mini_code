package plugin

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/user/mini_code/config"
)


type PluginState struct {
	Disabled []string `json:"disabled,omitempty"`
}

func stateFilePath() (string, error) {
	base, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "plugins.state.json"), nil
}

var stateMu sync.Mutex

func ReadState() PluginState {
	path, err := stateFilePath()
	if err != nil {
		return PluginState{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return PluginState{}
	}
	var st PluginState
	if err := json.Unmarshal(b, &st); err != nil {
		slog.Warn("plugins: plugins.state.json is not valid JSON, ignoring it", "err", err)
		return PluginState{}
	}
	return st
}

func IsDisabled(id string) bool {
	for _, d := range ReadState().Disabled {
		if d == id {
			return true
		}
	}
	return false
}

func SetEnabled(id string, on bool) error {
	if id == "" {
		return os.ErrInvalid
	}
	stateMu.Lock()
	defer stateMu.Unlock()

	st := ReadState()
	next := make([]string, 0, len(st.Disabled)+1)
	seen := false
	for _, d := range st.Disabled {
		if d == id {
			seen = true
			if on {
				continue
			}
		}
		next = append(next, d)
	}
	if !on && !seen {
		next = append(next, id)
	}
	sort.Strings(next)
	st.Disabled = next

	path, err := stateFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	slog.Info("plugins: toggle", "id", id, "enabled", on)
	return nil
}

func (p Plugin) Off() bool {
	if !p.On() {
		return true
	}
	return IsDisabled(p.ID)
}

func (p Plugin) ReasonOff() string {
	if !p.On() {
		return "插件清单里写的是 enabled: false"
	}
	if IsDisabled(p.ID) {
		return "在插件中心里被关掉了"
	}
	return ""
}

func (p *Plugin) SetSource(s string) { p.Source = s }

func Discover() []Plugin {
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
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)

	var out []Plugin
	seen := make(map[string]bool, len(dirs))
	for _, name := range dirs {
		p, err := loadOne(filepath.Join(root, name), name)
		if err != nil {
			out = append(out, Plugin{
				Dir:      filepath.Join(root, name),
				Manifest: Manifest{ID: name, Name: name},
				LoadError: err.Error(),
			})
			seen[name] = true
			continue
		}
		if seen[p.ID] {
			slog.Warn("plugins: skipped duplicate id", "id", p.ID, "dir", name)
			continue
		}
		seen[p.ID] = true
		if _, err := os.Stat(filepath.Join(p.Dir, ".catalogue")); err == nil {
			p.SetSource("catalogue")
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
