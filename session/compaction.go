package session

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/util"
)

const (
	compactThreshold = 0.85
	recentKeep       = 6
)

var compactSystemPrompt = "You are an anchored context summarization assistant for coding sessions."

var compactUserTemplate = `Please compress the following conversation history into a concise structured summary.

**Goal**: %s

**Conversation History**:
%s

**Requirements**:
- Preserve key facts, file paths, important data from tool results, and decision rationale
- Use exact names and numbers, do not generalize
- Focus on currently unfinished tasks and latest progress
- Output strictly following the 5 sections below

**Output Format**:

## Goal
(Overall task goal and original user intent)

## Current Instructions
(The most recent key user instructions or subtasks)

## Discoveries
(Key observations, important tool results, problem diagnoses)

## Accomplished
(Completed work, modified files, verified parts)

## Relevant Files
(List of currently active or important files with brief status)

---
The compressed context will be used to continue the task.`

type ContextCompactor struct {
	client *provider.ModelClient
}

func NewContextCompactor(client *provider.ModelClient) *ContextCompactor {
	return &ContextCompactor{client: client}
}

func (c *ContextCompactor) ShouldCompact(currentTokens, maxInput int) bool {
	if maxInput <= 0 {
		return false
	}
	return float64(currentTokens)/float64(maxInput) >= compactThreshold
}

func (c *ContextCompactor) CheckAndCompress(ctx context.Context, messages []provider.Message, maxInput int) ([]provider.Message, error) {
	currentTokens := util.CountMessagesTokens(messages)
	if !c.ShouldCompact(currentTokens, maxInput) {
		return messages, nil
	}
	slog.Info("triggering context compaction", "tokens", currentTokens, "max", maxInput)
	newMessages, err := c.Compact(ctx, messages)
	if err != nil {
		return messages, nil
	}
	newTokens := util.CountMessagesTokens(newMessages)
	slog.Info("compaction complete", "before", currentTokens, "after", newTokens)
	return newMessages, nil
}

func (c *ContextCompactor) Compact(ctx context.Context, messages []provider.Message) ([]provider.Message, error) {
	var systemMsg *provider.Message
	var nonSystem []provider.Message
	for _, msg := range messages {
		if msg.Role == "system" && systemMsg == nil {
			m := msg
			systemMsg = &m
		} else {
			nonSystem = append(nonSystem, msg)
		}
	}
	if len(nonSystem) == 0 {
		return messages, nil
	}
	if len(nonSystem) <= recentKeep {
		return messages, nil
	}

	goal := ""
	for _, msg := range nonSystem {
		goall := msg.GetText()
		if goall != "" {
			goal = goall
			break
		}
	}

	start := len(nonSystem) - recentKeep
	// The recent window must never start with a tool message: a tool result
	// is only valid API input while the assistant message that declared its
	// tool_calls is kept alongside it. Walk the boundary back to a safe
	// spot; if the tail is one unbroken tool exchange, skip this compaction.
	for start > 0 && nonSystem[start].Role == "tool" {
		start--
	}
	if start <= 0 {
		slog.Warn("compaction skipped: no safe boundary for recent window")
		return messages, nil
	}
	slog.Info("context compacting", "total", len(nonSystem), "dropped", start, "kept", len(nonSystem)-start)

	toCompact := nonSystem[:start]
	recent := nonSystem[start:]

	historyText := c.formatMessages(toCompact)
	summary, err := c.callLLMCompact(ctx, goal, historyText)
	if err != nil {
		// Surface the error to callers. CheckAndCompress (auto path)
		// deliberately ignores it and keeps the original context; the manual
		// path reports it to the user instead of writing an empty summary.
		return messages, fmt.Errorf("生成压缩摘要失败: %w", err)
	}

	var newMessages []provider.Message
	if systemMsg != nil {
		newMessages = append(newMessages, *systemMsg)
	}
	newMessages = append(newMessages, provider.Message{
		Role:    "user",
		Content: "<previous-summary>\nCompressed summary:\n\n" + summary + "\n</previous-summary>",
	})
	newMessages = append(newMessages, provider.Message{
		Role:    "assistant",
		Content: "Understood. I have the compressed context and will continue from where we left off.",
	})
	newMessages = append(newMessages, recent...)
	return newMessages, nil
}

func (c *ContextCompactor) formatMessages(messages []provider.Message) string {
	var parts []string
	for _, msg := range messages {
		content := msg.GetText()
		if len(content) > 2000 {
			content = content[:2000] + fmt.Sprintf("... [truncated, total %d chars]", len(content))
		}
		role := strings.ToUpper(msg.Role)
		parts = append(parts, fmt.Sprintf("[%s]: %s", role, content))
	}
	return strings.Join(parts, "\n\n")
}

func (c *ContextCompactor) callLLMCompact(ctx context.Context, goal, historyText string) (string, error) {
	systemPrompt := loadCompactionPrompt()
	userPrompt := fmt.Sprintf(compactUserTemplate, goal, historyText)
	messages := []provider.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	resp, err := c.client.Chat(ctx, messages, nil, provider.WithTemperature(0.3), provider.WithMaxTokens(8192))
	if err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", fmt.Errorf("%s", resp.Error)
	}
	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		// Reasoning models can spend their output budget (or place the
		// summary) in reasoning_content; fall back to it rather than
		// writing an empty <previous-summary> block.
		summary = strings.TrimSpace(resp.ReasoningContent)
	}
	if summary == "" {
		return "", fmt.Errorf("模型返回空摘要")
	}
	return summary, nil
}

func loadCompactionPrompt() string {
	// CWD override (agent/prompts/compaction.txt) or the copy embedded in
	// the binary; last resort is the minimal built-in prompt.
	if p := agent.LoadPrompt("compaction.txt"); p != "" {
		return p
	}
	return compactSystemPrompt
}
