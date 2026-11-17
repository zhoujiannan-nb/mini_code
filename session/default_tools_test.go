package session

import (
	"testing"

	"github.com/user/mini_code/agent"
)

func TestDshSurfaceRegistered(t *testing.T) {
	pluginHome(t)
	cfg := agent.BuildAgentConfig()
	got := names(NewPromptBuilder(cfg, "C:\\work").GetFilteredDefinitions(cfg))

	for _, want := range []string{"read", "write", "edit", "glob", "grep", "pwsh", "read_image", "skill", "todo_write", "working_directory", "web_fetch"} {
		if !contains(got, want) {
			t.Fatalf("%s must be registered (dsh core surface); got %v", want, got)
		}
	}
	for _, gone := range []string{"copy_file", "move_file", "delete_file", "list_dir",
		"read_file", "write_file", "edit_file", "search_files", "exec", "readimg", "skills"} {
		if contains(got, gone) {
			t.Fatalf("%s must not be registered by default (see registerDefaultTools); got %v", gone, got)
		}
	}
}
