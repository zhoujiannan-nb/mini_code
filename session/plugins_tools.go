package session

import (
	"strings"

	"github.com/user/mini_code/plugin"
)

// pluginMemorySection renders the "[plugin memory]" block: the notes of every enabled
// plugin, in plugin-id order. Naming the plugins here (and not in the tools
// block) keeps the prompt stable: the text only changes when a plugin's memory
// file changes on disk.
//
// Returns "" when no plugin contributes memory, so an installation without
// plugins produces a byte-identical prompt to the one before plugins existed.
func pluginMemorySection() string {
	plugins := plugin.Load()
	var b strings.Builder
	for _, p := range plugins {
		mem := p.MemoryText()
		if mem == "" {
			continue
		}
		b.WriteString("\n[memory: ")
		b.WriteString(p.ID)
		b.WriteString("]\n")
		b.WriteString(mem)
		b.WriteString("\n")
	}
	return b.String()
}

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
