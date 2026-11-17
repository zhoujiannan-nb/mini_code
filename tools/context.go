package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type ContextState struct {
	mu             sync.Mutex
	tokens         int
	maxInput       int
	compactionAt   int
	pendingCompact bool
	pendingInstr   string
}

func NewContextState() *ContextState {
	return &ContextState{}
}

func (c *ContextState) SetUsage(tokens, maxInput, compactionAt int) {
	c.mu.Lock()
	c.tokens = tokens
	c.maxInput = maxInput
	c.compactionAt = compactionAt
	c.mu.Unlock()
}

func (c *ContextState) CompactionAt() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.compactionAt
}

func (c *ContextState) Usage() (tokens, maxInput int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens, c.maxInput
}

func (c *ContextState) RequestCompaction(instruction string) {
	c.mu.Lock()
	c.pendingCompact = true
	c.pendingInstr = instruction
	c.mu.Unlock()
}

func (c *ContextState) TakePendingCompaction() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, i := c.pendingCompact, c.pendingInstr
	c.pendingCompact = false
	c.pendingInstr = ""
	return b, i
}

type ContextUsageTool struct {
	state *ContextState
}

func NewContextUsageTool(state *ContextState) *ContextUsageTool {
	return &ContextUsageTool{state: state}
}

func (t *ContextUsageTool) Name() string { return "context_usage" }
func (t *ContextUsageTool) Description() string {
	return "Query the current context (token) usage of this session: how many tokens the conversation is using, the max input budget, and the percentage. Read-only: the session compacts its own context automatically when the window fills."
}
func (t *ContextUsageTool) IsHidden() bool { return false }
func (t *ContextUsageTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}
func (t *ContextUsageTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	tokens, maxInput := t.state.Usage()
	if maxInput <= 0 {
		return NewTextResult(fmt.Sprintf("Context usage: %d tokens (budget unknown).", tokens)), nil
	}
	pct := float64(tokens) / float64(maxInput) * 100
	if at := t.state.CompactionAt(); at > 0 {
		return NewTextResult(fmt.Sprintf("Context usage: %d / %d input tokens (%.1f%%). This session will compact automatically once it reaches %d tokens; compact_context is disabled for now.", tokens, maxInput, pct, at)), nil
	}
	return NewTextResult(fmt.Sprintf("Context usage: %d / %d input tokens (%.1f%%). No automatic compaction point is known for this session, and compact_context is disabled for now.", tokens, maxInput, pct)), nil
}

type CompactContextTool struct {
	state *ContextState
}

func NewCompactContextTool(state *ContextState) *CompactContextTool {
	return &CompactContextTool{state: state}
}

func (t *CompactContextTool) Name() string { return "compact_context" }
func (t *CompactContextTool) Description() string {
	return "Actively compress this session's context now, regardless of the automatic threshold. The next model turn is spent writing the summary; the conversation then restarts from [system prompt][summary][the last few messages]. Optionally pass an instruction telling the summary what to focus on or preserve."
}
func (t *CompactContextTool) IsHidden() bool { return false }
func (t *CompactContextTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"instruction": map[string]interface{}{
				"type":        "string",
				"description": "What the summary should focus on or preserve. Optional but recommended.",
			},
		},
	}
}
func (t *CompactContextTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	instruction, _ := params["instruction"].(string)
	t.state.RequestCompaction(instruction)
	if instruction == "" {
		return NewTextResult("Compaction scheduled. The context will be compressed before the next model call. Continue working; the compressed context takes effect on the next turn."), nil
	}
	echo := []rune(strings.TrimSpace(instruction))
	const maxEcho = 200
	suffix := ""
	if len(echo) > maxEcho {
		echo = echo[:maxEcho]
		suffix = "…"
	}
	return NewTextResult(fmt.Sprintf("Compaction scheduled. The context will be compressed before the next model call, focusing on: %q. Continue working; the compressed context takes effect on the next turn.", string(echo)+suffix)), nil
}
