package session

import (
	"github.com/user/mini_code/plugin"
)


func (pb *PromptBuilder) registerPluginTools() {
	plugins := plugin.Load()
	for _, t := range plugin.PluginTools(plugins) {
		if pb.Tools.Get(t.Name()) != nil {
			continue
		}
		pb.Tools.RegisterAppended(t)
	}
	for _, t := range plugin.ProcTools(plugins, plugin.DefaultProcs()) {
		if pb.Tools.Get(t.Name()) != nil {
			continue
		}
		pb.Tools.RegisterAppended(t)
	}
}
