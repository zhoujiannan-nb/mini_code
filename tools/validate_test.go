package tools

import (
	"strings"
	"testing"
)

// editSchema is the dsh-declared edit parameter surface, as decoded from
// dsh_tools.json (required is a []interface{}, types are plain strings).
func editSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path":   map[string]interface{}{"type": "string"},
			"old_string":  map[string]interface{}{"type": "string"},
			"new_string": map[string]interface{}{"type": "string"},
			"replace_all": map[string]interface{}{"type": "boolean"},
		},
		"required": []interface{}{"file_path", "old_string", "new_string"},
	}
}

func TestValidateEditMissingNewString(t *testing.T) {
	// The measured t2 incident shape: the model spelled the key "content".
	// No tool-specific rule exists anywhere — the declared schema alone must
	// refuse the call and say exactly what is missing.
	args := map[string]interface{}{
		"content":   "x",
		"file_path": "a.txt",
		"old_string": "y",
	}
	err := ValidateToolArgs("edit", editSchema(), args)
	if err == nil {
		t.Fatal("missing new_string must fail validation")
	}
	msg := err.Error()
	for _, want := range []string{
		`Validation failed for tool "edit":`,
		"- new_string: must have required property 'new_string'",
		"Received arguments:",
		`"content": "x"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error text missing %q:\n%s", want, msg)
		}
	}
}

func TestValidateAllRequiredPresentPasses(t *testing.T) {
	args := map[string]interface{}{"file_path": "a", "old_string": "b", "new_string": ""}
	if err := ValidateToolArgs("edit", editSchema(), args); err != nil {
		t.Fatalf("explicit empty new_string is a legitimate deletion, got: %v", err)
	}
}

func TestValidateTypeChecksAndPaths(t *testing.T) {
	args := map[string]interface{}{
		"file_path":   "a",
		"old_string":  "b",
		"new_string":  "c",
		"replace_all": "yes", // string where boolean declared
	}
	err := ValidateToolArgs("edit", editSchema(), args)
	if err == nil || !strings.Contains(err.Error(), "- replace_all: must be boolean") {
		t.Fatalf("want replace_all type error, got: %v", err)
	}
}

func TestNormalizeCoerceAndNulls(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command":     map[string]interface{}{"type": "string"},
			"timeoutMs":   map[string]interface{}{"type": "number"},
			"background":  map[string]interface{}{"type": "boolean"},
			"description": map[string]interface{}{"type": "string"},
		},
		"required": []interface{}{"description", "command"},
	}
	params := map[string]interface{}{
		"command":    "dir",
		"timeoutMs":  "5000",   // coerced to number
		"background": "true",   // coerced to bool
		"description": nil,     // null on a REQUIRED key stays and fails type check
		"limit":      nil,      // unknown key with null: removed
	}
	NormalizeAndCoerceArgs(schema, params)
	if _, present := params["limit"]; present {
		t.Error("null on unknown key should be dropped")
	}
	err := ValidateToolArgs("pwsh", schema, params)
	if err == nil || !strings.Contains(err.Error(), "- description: must be string") {
		t.Fatalf("required key held null must report type error, got: %v", err)
	}
	// with a real description the coerced values must validate clean
	params["description"] = "list files"
	NormalizeAndCoerceArgs(schema, params)
	if err := ValidateToolArgs("pwsh", schema, params); err != nil {
		t.Fatalf("coerced args must validate, got: %v", err)
	}
	if v, ok := params["timeoutMs"].(float64); !ok || v != 5000 {
		t.Errorf("timeoutMs not coerced: %#v", params["timeoutMs"])
	}
	if v, ok := params["background"].(bool); !ok || !v {
		t.Errorf("background not coerced: %#v", params["background"])
	}
	// a non-numeric string is NOT converted, and then fails the check
	params["timeoutMs"] = "soon"
	NormalizeAndCoerceArgs(schema, params)
	if v, ok := params["timeoutMs"].(string); !ok || v != "soon" {
		t.Errorf("non-numeric string must stay a string: %#v", params["timeoutMs"])
	}
	if err := ValidateToolArgs("pwsh", schema, params); err == nil ||
		!strings.Contains(err.Error(), "- timeoutMs: must be number") {
		t.Fatalf("want timeoutMs type error, got: %v", err)
	}
}

func TestNestedRequiredAndEnumAndAdditional(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"todos": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]interface{}{
						"content": map[string]interface{}{"type": "string"},
						"status":  map[string]interface{}{"type": "string", "enum": []interface{}{"pending", "in_progress", "completed"}},
					},
					"required": []interface{}{"content", "status"},
				},
			},
		},
		"required": []interface{}{"todos"},
	}
	args := map[string]interface{}{"todos": []interface{}{
		map[string]interface{}{"content": "a", "status": "done", "extra": true},
		map[string]interface{}{"status": "pending"},
	}}
	err := ValidateToolArgs("todo_write", schema, args)
	if err == nil {
		t.Fatal("nested violations must fail")
	}
	msg := err.Error()
	for _, want := range []string{
		"- todos.0.status: must be equal to one of the allowed values",
		"todos.0: must NOT have additional properties",
		"- todos.1.content: must have required property 'content'",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
}

func TestValidateRealAdvertisedSchemas(t *testing.T) {
	// Representative shipped tools: every one advertises a dsh-decoded
	// schema, so an empty call must be refused exactly when (and only when)
	// that schema declares required keys — with required-property messages.
	dir := t.TempDir()
	tools := []Tool{
		NewReadFileTool(dir), NewWriteFileTool(dir), NewEditFileTool(dir),
		NewGlobTool(dir), NewSearchTool(dir), NewReadImgTool(dir),
		NewTodoTool(), NewWebFetchTool(), NewGetGoalTool(nil),
	}
	for _, tool := range tools {
		schema := tool.Parameters()
		verr := ValidateToolArgs(tool.Name(), schema, map[string]interface{}{})
		if verr == nil {
			continue // no required keys: empty call is schema-legal
		}
		if !strings.Contains(verr.Error(), "must have required property") {
			t.Errorf("%s: empty-call rejection is not a required-property error:\n%s", tool.Name(), verr)
		}
	}
}
