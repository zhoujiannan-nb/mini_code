package tools

import (
	"context"
	"fmt"
	"sync"
)

// ContextState is the shared, thread-safe view of the current context that
// the agent loop updates each turn and the context tools (context_usage /
// compact_context) read. It also carries a pending active-compaction request
// set by the compact_context tool and consumed by the loop at the next turn's
// compaction step.
//
// The loop is the single writer of tokens/maxInput (once per turn, right
// before the LLM call) and the single consumer of the pending request, so the
// mutex only guards against the tool goroutines reading while the loop writes.
type ContextState struct {
	mu             sync.Mutex
	tokens         int
	maxInput       int
	pendingCompact bool
	pendingInstr   string
}

func NewContextState() *ContextState {
	return &ContextState{}
}

// SetUsage records the current context token count and the max input budget.
func (c *ContextState) SetUsage(tokens, maxInput int) {
	c.mu.Lock()
	c.tokens = tokens
	c.maxInput = maxInput
	c.mu.Unlock()
}

// Usage returns the current context token count and the max input budget.
func (c *ContextState) Usage() (tokens, maxInput int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens, c.maxInput
}

// RequestCompaction asks the loop to compact the current context before the
// next LLM call, using the model-supplied instruction.
func (c *ContextState) RequestCompaction(instruction string) {
	c.mu.Lock()
	c.pendingCompact = true
	c.pendingInstr = instruction
	c.mu.Unlock()
}

// TakePendingCompaction returns (and clears) any pending active-compaction
// request. The loop calls this at the start of each turn's compaction step.
func (c *ContextState) TakePendingCompaction() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, i := c.pendingCompact, c.pendingInstr
	c.pendingCompact = false
	c.pendingInstr = ""
	return b, i
}

// ContextUsageTool reports the current context token usage of the session.
type ContextUsageTool struct {
	state *ContextState
}

func NewContextUsageTool(state *ContextState) *ContextUsageTool {
	return &ContextUsageTool{state: state}
}

func (t *ContextUsageTool) Name() string { return "context_usage" }
func (t *ContextUsageTool) Description() string {
	return "Query the current context (token) usage of this session: how many tokens the conversation is using, the max input budget, and the percentage. Use it to decide whether to call compact_context to summarize older work."
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
	trigger := int(float64(maxInput) * 0.80)
	return NewTextResult(fmt.Sprintf("Context usage: %d / %d tokens (%.1f%%). Automatic compaction triggers at 80%% (%d tokens); you can call compact_context earlier yourself.", tokens, maxInput, pct, trigger)), nil
}

// CompactContextTool lets the model actively request a context compaction,
// providing its own instruction for what the summary should focus on. The
// request is consumed by the agent loop before the next LLM call, so the
// compressed context takes effect on the following turn.
type CompactContextTool struct {
	state *ContextState
}

func NewCompactContextTool(state *ContextState) *CompactContextTool {
	return &CompactContextTool{state: state}
}

func (t *CompactContextTool) Name() string { return "compact_context" }
func (t *CompactContextTool) Description() string {
	return "Actively compress this session's context now, regardless of the automatic threshold. Provide an instruction describing what the summary should focus on or preserve (e.g. 'keep the test results and the failing case'); with it the compression prompt is yours instead of the fixed one. The next model turn is spent writing that summary, and the conversation then restarts from [system prompt][summary][the last few messages], so nothing else is lost from the model's working set."
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
	return NewTextResult(fmt.Sprintf("Compaction scheduled. The context will be compressed before the next model call, focusing on: %q. Continue working; the compressed context takes effect on the next turn.", instruction)), nil
}
