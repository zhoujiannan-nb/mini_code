package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

type nilRequiredTool struct{}

func (nilRequiredTool) Name() string { return "fake" }
func (nilRequiredTool) Description() string {
	return "fake tool"
}
func (nilRequiredTool) Parameters() map[string]interface{} {
	var nilSlice []string
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
		"required":   nilSlice,
	}
}
func (nilRequiredTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	return NewTextResult("ok"), nil
}
func (nilRequiredTool) IsHidden() bool { return false }

func marshalSchema(t *testing.T, tool Tool) []byte {
	t.Helper()
	out, err := json.Marshal(ToSchema(tool))
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	return out
}

func TestToSchemaNormalizesNilRequired(t *testing.T) {
	out := marshalSchema(t, nilRequiredTool{})
	if bytes.Contains(out, []byte("null")) {
		t.Fatalf("schema must not contain null, got: %s", out)
	}
	if !bytes.Contains(out, []byte(`"required":[]`)) {
		t.Fatalf("expected empty required array, got: %s", out)
	}
}

func TestListAgentsSchemaRequiredIsArray(t *testing.T) {
	out := marshalSchema(t, NewListAgentsTool("sess", nil))
	if bytes.Contains(out, []byte(`"required":null`)) {
		t.Fatalf("list_agents schema must not send required:null, got: %s", out)
	}
	if !bytes.Contains(out, []byte(`"required":[]`)) {
		t.Fatalf("expected empty required array, got: %s", out)
	}
}

func TestToSchemaDoesNotMutateToolParams(t *testing.T) {
	tool := nilRequiredTool{}
	marshalSchema(t, tool)
	after := tool.Parameters()["required"]
	if s, ok := after.([]string); !ok || s != nil {
		t.Fatalf("ToSchema mutated the tool's Parameters map, required is now: %v", after)
	}
}
