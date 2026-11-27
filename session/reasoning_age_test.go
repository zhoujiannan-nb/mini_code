package session

import (
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
)

func TestAgeReasoningKeepsOnlyLastAssistant(t *testing.T) {
	messages := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "work"},
		{Role: "assistant", Content: "a1", ReasoningContent: strings.Repeat("think ", 1000)},
		{Role: "tool", Content: "out", ToolCallID: "c1"},
		{Role: "assistant", Content: "a2", ReasoningContent: strings.Repeat("think ", 1000)},
		{Role: "tool", Content: "out2", ToolCallID: "c2"},
		{Role: "assistant", Content: "a3", ReasoningContent: "fresh chain"},
	}
	ageReasoning(messages)
	if messages[2].ReasoningContent != "" || messages[4].ReasoningContent != "" {
		t.Fatal("older assistant reasoning must be cleared")
	}
	if messages[6].ReasoningContent != "fresh chain" {
		t.Fatal("the newest assistant reasoning must stay")
	}
}

func TestRebuildContextDropsStaleReasoning(t *testing.T) {
	in := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "work"},
		{Role: "assistant", Content: "old", ReasoningContent: strings.Repeat("stale ", 5000)},
		{Role: "assistant", Content: "r1", ReasoningContent: strings.Repeat("stale ", 5000)},
		{Role: "assistant", Content: "r2", ReasoningContent: strings.Repeat("stale ", 5000)},
		{Role: "assistant", Content: "r3", ReasoningContent: "keep me"},
		BuildCompactRequest(""),
	}
	out, ok := RebuildContext(in, "summary", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("rebuild refused")
	}
	for i, m := range out {
		if i == len(out)-1 {
			if m.ReasoningContent != "keep me" {
				t.Fatalf("newest reasoning should survive: %q", m.ReasoningContent)
			}
			continue
		}
		if m.ReasoningContent != "" {
			t.Fatalf("stale reasoning survived at %d (%d chars)", i, len(m.ReasoningContent))
		}
	}
}
