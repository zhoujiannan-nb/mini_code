package agent

import (
	"embed"
	"os"
	"path/filepath"
	"strings"
)

// embeddedPrompts ships the built-in agent prompt files inside the binary so
// a single portable executable works from any working directory.
//
//go:embed prompts/*.txt
var embeddedPrompts embed.FS

// LoadPrompt resolves the prompt text for the given filename (e.g.
// "build.txt"). A file at CWD-relative agent/prompts/<name> takes
// precedence so local overrides during development still win; otherwise the
// embedded copy is used. Returns "" if neither exists.
func LoadPrompt(filename string) string {
	if data, err := os.ReadFile(filepath.Join("agent", "prompts", filename)); err == nil {
		return strings.TrimSpace(string(data))
	}
	if data, err := embeddedPrompts.ReadFile("prompts/" + filename); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}
