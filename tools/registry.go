package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/user/mini_code/provider"
)

type ToolRegistry struct {
	tools map[string]Tool
	// extra holds tools that are appended AFTER the sorted core list (plugin
	// tools). The core list is the head of the prompt prefix the provider's
	// prefix cache matches; if a plugin tool were merged into the sorted core
	// list, enabling a plugin would shift the position of every core tool after
	// it and throw the cache away. Appending keeps the core segment byte-for-byte
	// identical whether or not plugins are installed.
	extra map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool), extra: make(map[string]Tool)}
}

func (r *ToolRegistry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// RegisterAppended registers a tool whose schema is emitted after the whole
// core list (see the extra field). Plugin tools go through here.
func (r *ToolRegistry) RegisterAppended(t Tool) {
	if r.extra == nil {
		r.extra = make(map[string]Tool)
	}
	r.extra[t.Name()] = t
}

// Get returns the tool registered under name (core first, then appended).
func (r *ToolRegistry) Get(name string) Tool {
	if t, ok := r.tools[name]; ok {
		return t
	}
	return r.extra[name]
}

// IsAppended reports whether name belongs to an appended (plugin) tool. The
// permission tables in agent.AgentConfig are compiled in and know nothing about
// tools a user installed later, so the callers use this to allow plugin tools by
// default instead of silently filtering them out.
func (r *ToolRegistry) IsAppended(name string) bool {
	_, ok := r.extra[name]
	return ok
}

// AppendedIsSlash reports whether name is a core tool (used by tests/diagnostics
// to assert plugin tools never leak into the core segment).
func (r *ToolRegistry) IsCore(name string) bool {
	_, ok := r.tools[name]
	return ok
}

func (r *ToolRegistry) GetDefinitions(visibleOnly bool) []provider.ToolSchema {
	var defs []provider.ToolSchema
	for _, t := range r.tools {
		if visibleOnly && t.IsHidden() {
			continue
		}
		defs = append(defs, ToSchema(t))
	}
	// Sort by name: Go randomizes map iteration order, and the tools array is
	// part of the request prefix the provider's prefix cache matches. An
	// unstable order would re-render that prefix differently on every new
	// task, so no request of a new task could ever hit the previous task's
	// cached prefix.
	sort.Slice(defs, func(i, j int) bool { return defs[i].Function.Name < defs[j].Function.Name })

	// Appended tools (plugins) follow the sorted core list, also name-sorted so
	// their own order is stable. Nothing above this line changes when a plugin
	// is added, which is what keeps the cache warm across plugin installs.
	if len(r.extra) > 0 {
		appended := make([]provider.ToolSchema, 0, len(r.extra))
		for _, t := range r.extra {
			if visibleOnly && t.IsHidden() {
				continue
			}
			appended = append(appended, ToSchema(t))
		}
		sort.Slice(appended, func(i, j int) bool { return appended[i].Function.Name < appended[j].Function.Name })
		defs = append(defs, appended...)
	}
	return defs
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, params map[string]interface{}) (*ToolResult, error) {
	hint := "\n\n[Analyze the error above and try a different approach.]"
	t := r.Get(name)
	if t == nil {
		names := make([]string, 0, len(r.tools)+len(r.extra))
		for n := range r.tools {
			names = append(names, n)
		}
		for n := range r.extra {
			names = append(names, n)
		}
		sort.Strings(names) // stable order: the message reaches the model as a tool result
		return nil, fmt.Errorf("tool '%s' not found. Available: %s%s", name, strings.Join(names, ", "), hint)
	}

	result, err := t.Execute(ctx, params)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error executing %s: %s%s", name, err, hint)), nil
	}
	if result.Text != "" && strings.HasPrefix(result.Text, "Error") {
		return NewTextResult(result.Text + hint), nil
	}
	return result, nil
}
