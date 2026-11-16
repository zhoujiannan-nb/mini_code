package session

import (
	"github.com/user/mini_code/plugin"
)

// Plugin notes are NOT part of the system prompt — neither the text nor even
// a pointer to it. A plugin is, to the model, a tool; a tool documents itself
// through its own description. The former "[memory: <id>]" injection (and its
// successor, a "[plugin notes]" pointer block) made every session carry
// bytes it rarely needed; dsh has nothing like it, and neither do we now.

// registerPluginTools appends the tools contributed by enabled plugins AFTER
// the core tools. Ordering matters: the core tool list is the head of the
// request prefix the provider's prefix cache matches, so plugin tools must never
// be interleaved into it (see tools.ToolRegistry.RegisterAppended).
func (pb *PromptBuilder) registerPluginTools() {
	plugins := plugin.Load()
	for _, t := range plugin.PluginTools(plugins) {
		if pb.Tools.Get(t.Name()) != nil {
			// A plugin may not shadow a core tool: that would change what an
			// existing, cached tool name does.
			continue
		}
		pb.Tools.RegisterAppended(t)
	}
	// Process plugins (kind 1) that asked to be exposed get one control tool
	// each. They are appended too, for the same cache reason.
	for _, t := range plugin.ProcTools(plugins, plugin.DefaultProcs()) {
		if pb.Tools.Get(t.Name()) != nil {
			continue
		}
		pb.Tools.RegisterAppended(t)
	}
}
