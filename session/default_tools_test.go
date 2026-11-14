package session

import (
	"testing"

	"github.com/user/mini_code/agent"
)

// TestDshSurfaceRegistered pins the tool-face decision: the default toolset is
// dsh's core surface verbatim (names and schemas), and mc's historical names are
// gone. The job tools (job_output / job_list / job_kill) attach separately via
// EnableJobTools, and the root extras (subagent family, goals) via
// EnableAgentTools — workflow excepted, deliberately unregistered
// (see the note in EnableAgentTools) — so they are not asserted here.
func TestDshSurfaceRegistered(t *testing.T) {
	pluginHome(t) // isolate from whatever plugins are installed on this machine
	cfg := agent.BuildAgentConfig()
	got := names(NewPromptBuilder(cfg, "C:\\work").GetFilteredDefinitions(cfg))

	for _, want := range []string{"read", "write", "edit", "glob", "grep", "pwsh", "read_image", "skill", "todo_write", "working_directory", "web_fetch"} {
		if !contains(got, want) {
			t.Fatalf("%s must be registered (dsh core surface); got %v", want, got)
		}
	}
	// Retired names must not come back: every extra schema byte rides on every
	// request and busts the provider's cached prefix once per session.
	for _, gone := range []string{"copy_file", "move_file", "delete_file", "list_dir",
		"read_file", "write_file", "edit_file", "search_files", "exec", "readimg", "skills"} {
		if contains(got, gone) {
			t.Fatalf("%s must not be registered by default (see registerDefaultTools); got %v", gone, got)
		}
	}
}
