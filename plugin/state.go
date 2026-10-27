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

// Enabled/disabled state lives OUTSIDE the plugin directory.
//
// It would be tempting to flip "enabled" in plugin.json, but that file is part of
// what the plugin catalogue checksums (mini_code-plugins/index.json lists a
// sha256 per file). Rewriting it would make every installed plugin look tampered
// with on the next upgrade, so the host keeps its own small state file instead:
//
//	~/.mini_code/plugins.state.json
//	{"disabled": ["skin-night"]}
//
// A plugin is off when either the manifest says so ("enabled": false, what a
// catalogue item ships with) or the state file lists it (what the user clicks).
// Both are respected, so toggling never has to touch the plugin's own files.

// PluginState is the on-disk shape.
type PluginState struct {
	Disabled []string `json:"disabled,omitempty"`
}

// stateFilePath is ~/.mini_code/plugins.state.json (not created on read).
func stateFilePath() (string, error) {
	base, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "plugins.state.json"), nil
}

// stateMu serializes read-modify-write of the state file: the plugin centre can
// be clicked twice in a row, and two concurrent rewrites would lose one.
var stateMu sync.Mutex

// ReadState loads the state file. A missing or broken file means "nothing is
// disabled" — a corrupt toggle list must never stop the agent from starting.
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

// IsDisabled reports whether the state file turns this plugin off.
func IsDisabled(id string) bool {
	for _, d := range ReadState().Disabled {
		if d == id {
			return true
		}
	}
	return false
}

// SetEnabled writes the toggle for one plugin and returns the new state.
// Enabling removes the id from the list (the manifest's own "enabled": false is
// NOT overridden — a catalogue item that ships disabled stays disabled until its
// manifest says otherwise; the state file only ever adds to the off set).
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
				continue // drop it: enabled
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
	// Write through a temp file + rename: a crash mid-write must not leave a
	// truncated state file (which would silently re-enable everything).
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

// Off reports whether the host should treat the plugin as switched off, taking
// both the manifest and the state file into account.
func (p Plugin) Off() bool {
	if !p.On() {
		return true
	}
	return IsDisabled(p.ID)
}

// ReasonOff explains why a plugin is off (empty when it is on), for the UI.
func (p Plugin) ReasonOff() string {
	if !p.On() {
		return "插件清单里写的是 enabled: false"
	}
	if IsDisabled(p.ID) {
		return "在插件中心里被关掉了"
	}
	return ""
}

// SetSource records where this plugin came from: "" (a directory dropped in by
// hand) or "catalogue" (installed from the remote repo). Discover fills it from
// the marker file the installer writes, so the centre can say "已安装" instead of
// offering to install the same version again.
func (p *Plugin) SetSource(s string) { p.Source = s }

// Discover returns every valid plugin under ~/.mini_code/plugins, sorted by id,
// INCLUDING the ones that are switched off.
//
// The plugin centre needs the off ones (that is where you turn them back on), so
// this is the listing the UI reads; Load is the filtered view the agent reads.
// A plugin whose manifest cannot be understood is still returned with LoadError
// set, so the page can say what is wrong instead of silently hiding it.
func Discover() []Plugin {
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
			// Keep a stub so the UI can show the directory and the reason.
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
