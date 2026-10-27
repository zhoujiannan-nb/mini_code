package plugin

import (
	"strings"
	"sync"

	"github.com/user/mini_code/tools"
)

// The supervisor is process-wide: a plugin program is a property of the
// installation, not of a chat session, and two sessions must not each spawn
// their own copy of a python server. Both the web layer (the plugin centre) and
// the session layer (the model's control tools) therefore share this one.
var (
	procsOnce sync.Once
	procs     *ProcManager
)

// DefaultProcs returns the process-wide supervisor, creating it on first use.
func DefaultProcs() *ProcManager {
	procsOnce.Do(func() {
		procs = NewProcManager(Load)
	})
	return procs
}

// ProcTools returns one control tool per process plugin that asked to be
// exposed to the model (process.expose.tool). The tools are appended after the
// core list like every plugin tool, so installing one never moves the cached
// core segment.
//
// A plugin that does not set expose.tool contributes nothing: its program still
// runs and still shows up in the plugin centre, the model just cannot see it.
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

// ProcToolName is the tool name a process plugin gets when it exposes itself.
// It is exported so the permission layer can recognise plugin tools.
func ProcToolName(pluginID string) string {
	return "plugin_proc__" + strings.ReplaceAll(pluginID, "-", "_")
}
