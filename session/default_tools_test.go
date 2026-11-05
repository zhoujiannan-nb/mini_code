package session

import (
	"testing"

	"github.com/user/mini_code/agent"
)

// TestCopyMoveToolsNotRegistered pins the tool-ablation decision: copy_file and
// move_file are dropped from the default toolset after two independent
// measurements recorded zero uses (an 82-sample three-arm ablation, plus 257
// real tool calls on the live instance), while the environment section already
// teaches the shell spellings of copy/move/ren. Keeping the implementations in
// tools/fileops.go means re-enabling them for one worker is a single Register
// call — but they must not silently come back for everyone: they cost 1414 B of
// tool schema on every request, and every schema change busts the provider's
// cached prefix once for every session.
func TestCopyMoveToolsNotRegistered(t *testing.T) {
	pluginHome(t) // isolate from whatever plugins are installed on this machine
	cfg := agent.BuildAgentConfig()
	got := names(NewPromptBuilder(cfg, "C:\\work").GetFilteredDefinitions(cfg))

	for _, gone := range []string{"copy_file", "move_file"} {
		if contains(got, gone) {
			t.Fatalf("%s must not be registered by default (see registerDefaultTools); got %v", gone, got)
		}
	}
	// The file tools that the ablation proved the model actually leans on must
	// stay — dropping list_dir/delete_file instead made the model re-implement
	// them through exec and cost more, not less.
	for _, keep := range []string{"read_file", "write_file", "edit_file", "delete_file", "list_dir", "exec", "search_files"} {
		if !contains(got, keep) {
			t.Fatalf("%s must stay registered; got %v", keep, got)
		}
	}
}
