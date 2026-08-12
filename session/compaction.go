package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/user/uclaw/provider"
	"github.com/user/uclaw/util"
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

	toCompact := nonSystem[:len(nonSystem)-recentKeep]
	recent := nonSystem[len(nonSystem)-recentKeep:]

	historyText := c.formatMessages(toCompact)
	summary, err := c.callLLMCompact(ctx, goal, historyText)
	if err != nil {
		return messages, nil
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
	resp, err := c.client.Chat(ctx, messages, nil, provider.WithTemperature(0.3), provider.WithMaxTokens(4096))
	if err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", fmt.Errorf("%s", resp.Error)
	}
	return strings.TrimSpace(resp.Content), nil
}

func loadCompactionPrompt() string {
	path := filepath.Join("agent", "prompts", "compaction.txt")
	data, err := readFile(path)
	if err != nil {
		return compactSystemPrompt
	}
	return strings.TrimSpace(data)
}

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
