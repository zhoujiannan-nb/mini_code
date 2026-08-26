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
