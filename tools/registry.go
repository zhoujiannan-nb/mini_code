package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/user/uclaw/provider"
)

type ToolRegistry struct {
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

func (r *ToolRegistry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) Unregister(name string) {
	delete(r.tools, name)
}

func (r *ToolRegistry) Get(name string) Tool {
	return r.tools[name]
}

func (r *ToolRegistry) Has(name string) bool {
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
	return defs
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, params map[string]interface{}) (*ToolResult, error) {
	hint := "\n\n[Analyze the error above and try a different approach.]"
	t := r.tools[name]
	if t == nil {
		names := make([]string, 0, len(r.tools))
		for n := range r.tools {
			names = append(names, n)
		}
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

func (r *ToolRegistry) ToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	return names
}
