package session

import (
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
)

func TestShrinkOversizedTailStripsReasoningOldestFirst(t *testing.T) {
	out := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: SummaryBlock("## Goal\nx")},
		{Role: "assistant", Content: "old", ReasoningContent: strings.Repeat("old think ", 3000)},
		{Role: "assistant", Content: "mid", ReasoningContent: strings.Repeat("mid think ", 3000)},
		{Role: "assistant", Content: "newest", ReasoningContent: strings.Repeat("new think ", 100)},
	}
	est := func(ms []provider.Message) int {
		n := 0
		for _, m := range ms {
			n += len([]rune(m.GetText())) + len([]rune(m.ReasoningContent))
		}
		return n
	}
	budget := est([]provider.Message{out[0], out[1], out[4]}) + 100
	got := shrinkOversizedTail(out, budget, est)
	if got[2].ReasoningContent != "" || got[3].ReasoningContent != "" {
		t.Fatal("older reasoning should be stripped first")
	}
	if got[4].ReasoningContent == "" {
		t.Fatal("newest reasoning should survive when budget already met")
	}
	if est(got) > budget {
		t.Fatalf("still over budget: %d > %d", est(got), budget)
	}
}

func TestShrinkOversizedTailStripsNewestReasoningAsLastResort(t *testing.T) {
	out := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: SummaryBlock("## Goal\nx")},
		{Role: "assistant", Content: "newest", ReasoningContent: strings.Repeat("huge think ", 5000)},
	}
	est := func(ms []provider.Message) int {
		n := 0
		for _, m := range ms {
			n += len([]rune(m.GetText())) + len([]rune(m.ReasoningContent))
		}
		return n
	}
	got := shrinkOversizedTail(out, 500, est)
	if got[2].ReasoningContent != "" {
		t.Fatal("newest reasoning must be stripped when nothing else can meet the budget")
	}
}

func TestRebuildContextKeepsReasoningWhenWithinBudget(t *testing.T) {
	in := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "work"},
		{Role: "assistant", Content: "r1", ReasoningContent: "chain one"},
		{Role: "assistant", Content: "r2", ReasoningContent: "chain two"},
		{Role: "assistant", Content: "r3", ReasoningContent: "chain three"},
		BuildCompactRequest(""),
	}
	out, ok := RebuildContext(in, "summary", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("rebuild refused")
	}
	kept := 0
	for _, m := range out {
		if m.ReasoningContent != "" {
			kept++
		}
	}
	if kept != 3 {
		t.Fatalf("all retained reasoning should be passed back when within budget, kept %d", kept)
	}
}
