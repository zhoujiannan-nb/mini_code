package tools

import (
	"context"

	"github.com/user/mini_code/provider"
)

type ToolResult struct {
	Text  string
	Parts []provider.ContentPart
}

func NewTextResult(text string) *ToolResult {
	return &ToolResult{Text: text}
}

func NewImageResult(parts []provider.ContentPart) *ToolResult {
	return &ToolResult{Parts: parts}
}

func (r *ToolResult) HasMultimodal() bool {
	return len(r.Parts) > 0
}

type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]interface{}
	Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error)
	IsHidden() bool
}

// StreamTool is implemented by tools that can report output while they run, not
// only the final result. The agent loop passes a sink and the tool pushes
// chunks as they arrive.
//
// A sink is a UI feed, never a source of truth: it may be called from another
// goroutine, may drop or coalesce chunks, and its return says nothing about the
// tool's result. The final *ToolResult must stay exactly what Execute would
// have returned, so a tool that cannot stream simply does not implement this.
type StreamTool interface {
	ExecuteStreamed(ctx context.Context, params map[string]interface{}, sink func(string)) (*ToolResult, error)
}

func ToSchema(t Tool) provider.ToolSchema {
	params := t.Parameters()
	// Wire invariant: the model API validates `parameters.required` as an
	// array; a nil slice would marshal as null and be rejected with a 400.
	if req, ok := params["required"].([]string); ok && req == nil {
		fixed := make(map[string]interface{}, len(params)+1)
		for k, v := range params {
			fixed[k] = v
		}
		fixed["required"] = []string{}
		params = fixed
	}
	return provider.ToolSchema{
		Type: "function",
		Function: provider.ToolSchemaFunc{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  params,
		},
	}
}
