package tools

import (
	"context"
	"testing"
)

// stubTool is a minimal Tool for registry ordering tests.
type stubTool struct{ name string }

func (s stubTool) Name() string                          { return s.name }
func (s stubTool) Description() string                   { return "stub " + s.name }
func (s stubTool) Parameters() map[string]interface{}    { return map[string]interface{}{"type": "object"} }
func (s stubTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	return NewTextResult("ok"), nil
}
func (s stubTool) IsHidden() bool { return false }

// TestGetDefinitionsStableOrder verifies the definitions come back in a
// deterministic order across calls. Go randomizes map iteration, and the
// tools array is part of the request prefix the provider's prefix cache
// matches — an unstable order would re-render the prompt prefix on every
// new task and defeat the cache for the whole conversation.
func TestGetDefinitionsStableOrder(t *testing.T) {
	names := []string{
		"read_file", "write_file", "exec", "search_files", "list_dir",
		"glob", "copy_file", "delete_file", "move_file", "edit_file",
		"skills", "read_img",
	}
	r := NewToolRegistry()
	for _, n := range names {
		r.Register(stubTool{n})
	}

	first := r.GetDefinitions(false)
	if len(first) != len(names) {
		t.Fatalf("expected %d definitions, got %d", len(names), len(first))
	}
	// Sorted by name.
	for i := 1; i < len(first); i++ {
		if first[i-1].Function.Name >= first[i].Function.Name {
			t.Fatalf("definitions not sorted by name: %q !< %q", first[i-1].Function.Name, first[i].Function.Name)
		}
	}
	// Stable across many calls (each call would draw a fresh random map
	// order under the old implementation).
	for trial := 0; trial < 100; trial++ {
		defs := r.GetDefinitions(false)
		for i, d := range defs {
			if d.Function.Name != first[i].Function.Name {
				t.Fatalf("trial %d: definition %d is %q, want %q (unstable order)", trial, i, d.Function.Name, first[i].Function.Name)
			}
		}
	}
}
