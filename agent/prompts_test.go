package agent

import "testing"

// TestLoadPromptEmbeddedFallback verifies the binary is self-contained:
// tests run with CWD = the agent/ package directory, where the CWD-relative
// "agent/prompts/<name>" path does NOT exist, so only the embedded copy can
// answer. This is exactly the situation of a single portable binary run from
// an arbitrary directory.
func TestLoadPromptEmbeddedFallback(t *testing.T) {
	for _, name := range []string{"build.txt", "explore.txt", "generate.txt", "compaction.txt"} {
		if got := LoadPrompt(name); got == "" {
			t.Errorf("LoadPrompt(%q) = empty: embedded prompt missing", name)
		}
	}
	if got := LoadPrompt("no-such-file.txt"); got != "" {
		t.Errorf("LoadPrompt(no-such-file.txt) = %q, want empty", got)
	}
}
