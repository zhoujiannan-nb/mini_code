package session

import (
	"encoding/json"
	"testing"

	"github.com/user/mini_code/provider"
)

func schemas(names ...string) []provider.ToolSchema {
	out := make([]provider.ToolSchema, 0, len(names))
	for _, n := range names {
		out = append(out, provider.ToolSchema{Type: "function", Function: provider.ToolSchemaFunc{Name: n}})
	}
	return out
}

func namesOf(defs []provider.ToolSchema) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

func TestParseDisabledTools(t *testing.T) {
	cases := map[string]int{"": 0, "copy_file": 1, " a , b ,,c ": 3, "exec,exec": 1}
	for v, want := range cases {
		if got := parseDisabledTools(v); len(got) != want {
			t.Errorf("parseDisabledTools(%q) = %d entries, want %d", v, len(got), want)
		}
	}
}

func TestFilterOutTools(t *testing.T) {
	defs := schemas("read_file", "copy_file", "web_search", "task")

	if got := filterOutTools(defs, nil); len(got) != 4 {
		t.Fatalf("an empty ablation set must be a no-op, got %v", namesOf(got))
	}
	got := filterOutTools(defs, parseDisabledTools("copy_file,web_search"))
	want := []string{"read_file", "task"}
	if b, _ := json.Marshal(namesOf(got)); string(b) != `["read_file","task"]` {
		t.Fatalf("after ablation = %v, want %v", b, want)
	}
	if got := filterOutTools(defs, parseDisabledTools("read_file,copy_file,web_search,task")); len(got) != 0 {
		t.Fatalf("ablating every tool should empty the list, got %v", namesOf(got))
	}
}
