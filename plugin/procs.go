package plugin

import (
	"strings"
	"sync"

	"github.com/user/mini_code/tools"
)

var (
	procsOnce sync.Once
	procs     *ProcManager
)

func DefaultProcs() *ProcManager {
	procsOnce.Do(func() {
		procs = NewProcManager(Load)
	})
	return procs
}

func ProcTools(plugins []Plugin, m *ProcManager) []tools.Tool {
	if m == nil {
		m = DefaultProcs()
	}
	var out []tools.Tool
	for _, p := range plugins {
		if p.Process == nil || p.Process.Expose == nil || !p.Process.Expose.Tool {
			continue
		}
		out = append(out, NewProcTool(p, m))
	}
	return out
}

func ProcToolName(pluginID string) string {
	return "plugin_proc__" + strings.ReplaceAll(pluginID, "-", "_")
}
