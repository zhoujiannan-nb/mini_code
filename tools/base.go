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
	return provider.ToolSchema{
		Type: "function",
		Function: provider.ToolSchemaFunc{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		},
	}
}
