// Plugin centre API: the endpoints behind 设置 → 插件中心.
//
// Everything here works on the same on-disk truth as the rest of the plugin
// layer (~/.mini_code/plugins/<id>/plugin.json), never on a cached copy, so a
// plugin dropped in by hand shows up on the next reload without a restart.
//
//	GET    /api/plugins            list + category + state (the centre's data)
//	POST   /api/plugins/{id}/toggle  {"enabled": bool}
//	DELETE /api/plugins/{id}         uninstall (removes the directory)
//	GET    /api/plugins/processes    live state of every process plugin
//	POST   /api/plugins/{id}/process {"action": "start" | "stop" | "restart"}
//	GET    /api/plugins/{id}/logs    log tail of a process plugin
//	GET    /api/plugins/catalog      the remote catalogue, annotated
//	POST   /api/plugins/install      {"id": "..."} download + verify + install
//	GET    /api/plugins/prompt/{kind} the "make me a plugin" prompt for a session
//
// The listing deliberately reports a plugin that failed to load (with `error`)
// rather than hiding it: "my plugin does not show up" is undebuggable otherwise.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/user/mini_code/plugin"
)

// pluginCentreInfo is one row of the plugin centre.
type pluginCentreInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Author      string   `json:"author,omitempty"`
	Description string   `json:"description,omitempty"`
	Category    string   `json:"category"`
	Kinds       []string `json:"kinds"`
	Tools       []string `json:"tools,omitempty"`
	Enabled     bool     `json:"enabled"`
	Reason      string   `json:"reason,omitempty"`
	Dir         string   `json:"dir"`
	// Error explains why a directory could not be used (bad JSON, bad path…).
	Error string `json:"error,omitempty"`
	// Process carries the live state of a kind-1 plugin.
	Process *plugin.ProcState `json:"process,omitempty"`
	// UI describes what a kind-3 plugin changes, for the preview line.
	UI *uiInfo `json:"ui,omitempty"`
	// Boot tells the card this plugin also ships a startup animation.
	Boot *bootInfo `json:"boot,omitempty"`
	// Styles/Wallpaper flags let the card say what it brings.
	HasStyles    bool `json:"has_styles,omitempty"`
	HasWallpaper bool `json:"has_wallpaper,omitempty"`
	// Source is "local" (dropped into the directory) or "catalogue" (installed
	// from the remote repo). Only the latter can be safely uninstalled by id.
	Source string `json:"source,omitempty"`
	// Accent is the kind-3 accent colour, for the swatch in the card.
	Accent string `json:"accent,omitempty"`
}

// uiInfo summarises the third kind without leaking file contents.
type uiInfo struct {
	Slots  []string `json:"slots,omitempty"`
	Motion []string `json:"motion,omitempty"`
	Accent string   `json:"accent,omitempty"`
}

// bootInfo tells the card that this appearance plugin also ships a startup
// animation (see plugin/boot.go). It is what turns "install a theme" from a
// separate settings tab into two buttons on a normal plugin card.
type bootInfo struct {
	Entry      string `json:"entry"`
	DurationMs int    `json:"duration_ms,omitempty"`
	MaxMs      int    `json:"max_ms,omitempty"`
}

// handlePluginCentreList: GET /api/plugins.
//
// It lists EVERY plugin directory that loaded, including the switched-off ones,
// because that is what the centre needs to offer a toggle. The agent's view
// (plugin.Load) stays filtered.
func (s *server) handlePluginCentreList(w http.ResponseWriter, r *http.Request) {
	procs := plugin.DefaultProcs()
	all := plugin.Discover()
	out := make([]pluginCentreInfo, 0, len(all))
	for _, p := range all {
		info := pluginCentreInfo{
			ID:          p.ID,
			Name:        p.Name,
			Version:     p.Version,
			Author:      p.Author,
			Description: p.Description,
			Category:    p.Category(),
			Kinds:       p.Kinds(),
			Enabled:     !p.Off(),
			Reason:      p.ReasonOff(),
			Dir:          p.Dir,
			Error:        p.LoadError,
			HasStyles:    len(p.Styles) > 0,
			HasWallpaper: p.Wallpaper != "",
			Source:       p.Source,
		}
		if info.Kinds == nil {
			info.Kinds = []string{}
		}
		for _, t := range p.Manifest.Tools {
			info.Tools = append(info.Tools, t.Name)
		}
		// Kind-1 plugins do not list their control tool in the manifest: it is
		// synthesised by the host from process.expose.tool. Report it anyway, or
		// the plugin centre says "this plugin gives the model nothing" about a
		// plugin that does.
		if p.Process != nil && p.Process.Expose != nil && p.Process.Expose.Tool {
			info.Tools = append(info.Tools, plugin.ProcToolName(p.ID))
		}
		sort.Strings(info.Tools)
		if p.Process != nil {
			st := procs.StateOf(p)
			info.Process = &st
		}
		if p.UI != nil {
			ui := &uiInfo{Accent: p.UI.Accent}
			for _, sl := range p.UI.Slots {
				ui.Slots = append(ui.Slots, sl.Slot)
			}
			for _, mo := range p.UI.Motion {
				ui.Motion = append(ui.Motion, mo.Event)
			}
			info.UI = ui
			if info.Accent == "" {
				info.Accent = p.UI.Accent
			}
		}
		// A startup animation is an appearance-plugin surface, not a fourth
		// kind: the card shows "开机动画" plus its two actions from this block.
		if p.Boot != nil {
			info.Boot = &bootInfo{
				Entry:      p.BootEntryFile(),
				DurationMs: p.Boot.DurationMs,
				MaxMs:      p.Boot.MaxMs,
			}
		}
		out = append(out, info)
	}
	dir, _ := plugin.DirPath()
	writeJSON(w, http.StatusOK, map[string]any{
		"plugins": out,
		"dir":     dir,
		// Slots the page provides, so the "make a plugin" documentation and the
		// UI can agree on one list.
		"slots": plugin.SlotNames(),
	})
}

// handlePluginToggle: POST /api/plugins/{id}/toggle {"enabled": bool}.
func (s *server) handlePluginToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validPluginID(id) {
		writeErr(w, http.StatusBadRequest, "非法的插件 id")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "请求体需要 {\"enabled\": true|false}")
		return
	}
	if _, ok := pluginByID(id); !ok {
		writeErr(w, http.StatusNotFound, "插件未安装")
		return
	}
	if err := plugin.SetEnabled(id, *req.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存开关失败: "+err.Error())
		return
	}
	// Turning a process plugin off must also stop its program: otherwise the
	// "off" switch would leave a child running, which is the opposite of what
	// the user asked for.
	if !*req.Enabled {
		if p, ok := pluginByID(id); ok && p.Process != nil {
			if err := plugin.DefaultProcs().Stop(id); err != nil {
				slog.Debug("plugin centre: stop on disable", "id", id, "err", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": map[bool]string{true: "已启用", false: "已停用"}[*req.Enabled],
		"enabled": *req.Enabled,
	})
}

// handlePluginUninstall: DELETE /api/plugins/{id}.
//
// The directory is removed, which is what "卸载" means for a drop-in plugin. A
// process plugin is stopped first so its executable is not held open on Windows.
func (s *server) handlePluginUninstall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validPluginID(id) {
		writeErr(w, http.StatusBadRequest, "非法的插件 id")
		return
	}
	p, ok := pluginByID(id)
	if !ok {
		// Distinguish "not installed at all" from "a directory that is not a
		// plugin": the second one must not be deleted, and saying which is which
		// is the difference between a usable error and a scary one.
		root, err := plugin.DirPath()
		if err == nil {
			if _, statErr := os.Stat(filepath.Join(root, id)); statErr == nil {
				writeErr(w, http.StatusBadRequest, "该目录里没有可用的 plugin.json,不是插件,不敢删")
				return
			}
		}
		writeErr(w, http.StatusNotFound, "插件未安装")
		return
	}
	// Refuse to delete anything that is not a plugin directory: the manifest must
	// be there for us to be sure what we are removing.
	if _, err := os.Stat(filepath.Join(p.Dir, "plugin.json")); err != nil {
		writeErr(w, http.StatusBadRequest, "该目录里没有 plugin.json,不敢删")
		return
	}
	if p.Process != nil {
		_ = plugin.DefaultProcs().Stop(id)
	}
	if err := os.RemoveAll(p.Dir); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除失败(文件可能被占用): "+err.Error())
		return
	}
	_ = plugin.SetEnabled(id, true) // drop the toggle entry so a reinstall starts clean
	writeJSON(w, http.StatusOK, map[string]string{"message": "已卸载 " + p.Name})
}

// handlePluginProcessList: GET /api/plugins/processes.
func (s *server) handlePluginProcessList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"processes": plugin.DefaultProcs().States(),
	})
}

// handlePluginProcessAction: POST /api/plugins/{id}/process {"action": "..."}.
func (s *server) handlePluginProcessAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validPluginID(id) {
		writeErr(w, http.StatusBadRequest, "非法的插件 id")
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p, ok := pluginByID(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "插件未安装")
		return
	}
	if p.Process == nil {
		writeErr(w, http.StatusBadRequest, "这个插件不是程序类插件")
		return
	}
	procs := plugin.DefaultProcs()
	action := strings.ToLower(strings.TrimSpace(req.Action))
	var err error
	switch action {
	case "start":
		err = procs.Start(id)
	case "stop":
		err = procs.Stop(id)
	case "restart":
		// Stop is allowed to fail (it was not running); the start is what matters.
		_ = procs.Stop(id)
		err = procs.Start(id)
	default:
		writeErr(w, http.StatusBadRequest, "action 只能是 start / stop / restart")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	st := procs.StateOf(p)
	writeJSON(w, http.StatusOK, map[string]any{
		"message": map[string]string{"start": "已启动", "stop": "已停止", "restart": "已重启"}[action],
		"process": st,
	})
}

// handlePluginProcessLogs: GET /api/plugins/{id}/logs?n=50.
func (s *server) handlePluginProcessLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validPluginID(id) {
		writeErr(w, http.StatusBadRequest, "非法的插件 id")
		return
	}
	n := 50
	if v := r.URL.Query().Get("n"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			n = 50
		}
	}
	if n < 1 {
		n = 1
	}
	if n > 200 {
		n = 200
	}
	logs := plugin.DefaultProcs().LogTail(id)
	if len(logs) > n {
		logs = logs[len(logs)-n:]
	}
	if logs == nil {
		logs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "lines": logs})
}

// pluginByID finds a plugin (enabled or not) by id.
func pluginByID(id string) (plugin.Plugin, bool) {
	for _, p := range plugin.Discover() {
		if p.ID == id && p.LoadError == "" {
			return p, true
		}
	}
	return plugin.Plugin{}, false
}

// writeErrJSON exists so the plugin handlers can share one error shape with the
// rest of the API (writeErr lives in web.go and takes a message).
var _ = errors.New
