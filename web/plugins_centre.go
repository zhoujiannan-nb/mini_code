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
	Error string `json:"error,omitempty"`
	Process *plugin.ProcState `json:"process,omitempty"`
	UI *uiInfo `json:"ui,omitempty"`
	Boot *bootInfo `json:"boot,omitempty"`
	HasStyles    bool `json:"has_styles,omitempty"`
	HasWallpaper bool `json:"has_wallpaper,omitempty"`
	Source string `json:"source,omitempty"`
	Accent string `json:"accent,omitempty"`
}

type uiInfo struct {
	Slots  []string `json:"slots,omitempty"`
	Motion []string `json:"motion,omitempty"`
	Accent string   `json:"accent,omitempty"`
}

type bootInfo struct {
	Entry      string `json:"entry"`
	DurationMs int    `json:"duration_ms,omitempty"`
	MaxMs      int    `json:"max_ms,omitempty"`
}

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
		"slots": plugin.SlotNames(),
	})
}

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

func (s *server) handlePluginUninstall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validPluginID(id) {
		writeErr(w, http.StatusBadRequest, "非法的插件 id")
		return
	}
	p, ok := pluginByID(id)
	if !ok {
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
	_ = plugin.SetEnabled(id, true)
	writeJSON(w, http.StatusOK, map[string]string{"message": "已卸载 " + p.Name})
}

func (s *server) handlePluginProcessList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"processes": plugin.DefaultProcs().States(),
	})
}

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

func pluginByID(id string) (plugin.Plugin, bool) {
	for _, p := range plugin.Discover() {
		if p.ID == id && p.LoadError == "" {
			return p, true
		}
	}
	return plugin.Plugin{}, false
}

var _ = errors.New
