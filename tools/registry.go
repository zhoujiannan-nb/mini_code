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
	extra map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool), extra: make(map[string]Tool)}
}

func (r *ToolRegistry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) RegisterAppended(t Tool) {
	if r.extra == nil {
		r.extra = make(map[string]Tool)
	}
	r.extra[t.Name()] = t
}

func (r *ToolRegistry) Get(name string) Tool {
	if t, ok := r.tools[name]; ok {
		return t
	}
	return r.extra[name]
}

func (r *ToolRegistry) IsAppended(name string) bool {
	_, ok := r.extra[name]
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
	sort.Slice(defs, func(i, j int) bool { return defs[i].Function.Name < defs[j].Function.Name })

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
		sort.Strings(names)
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
