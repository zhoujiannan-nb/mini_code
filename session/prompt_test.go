package session

import (
	"testing"
	"time"

	"github.com/user/mini_code/agent"
)

// TestBuildSystemPromptIsStatic verifies the system prompt no longer embeds
// a timestamp: it is the head of the request prefix the provider's prefix
// cache matches, so it must be identical across tasks of a session.
func TestBuildSystemPromptIsStatic(t *testing.T) {
	pb := &PromptBuilder{agentConfig: &agent.AgentConfig{Prompt: "you are a test agent"}}
	a := pb.BuildSystemPrompt("C:\\work", "")
	// More than one wall-clock second: a per-second timestamp in the prompt
	// would make the two renders differ.
	time.Sleep(1100 * time.Millisecond)
	b := pb.BuildSystemPrompt("C:\\work", "")
	if a != b {
		t.Fatal("system prompt must be stable across renders (a per-second timestamp would defeat the prefix cache)")
	}
}
