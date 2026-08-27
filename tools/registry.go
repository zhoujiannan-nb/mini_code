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
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

func (r *ToolRegistry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) Get(name string) Tool {
	return r.tools[name]
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
