package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// streamDelta is a single SSE chunk of a streaming chat completion
// (OpenAI-compatible format used by vLLM and Ollama).
type streamDelta struct {
	Choices []struct {
		Delta struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []streamDeltaToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

// streamDeltaToolCall carries incremental tool call data; fields arrive
// piecewise across chunks and must be merged by index.
type streamDeltaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// streamAccumulator aggregates deltas into a final ChatResponse.
type streamAccumulator struct {
	content      string
	reasoning    string
	toolCalls    []ToolCall
	finishReason string
	usage        *Usage
}

func (a *streamAccumulator) mergeToolCalls(deltas []streamDeltaToolCall) {
	for _, d := range deltas {
		for len(a.toolCalls) <= d.Index {
			a.toolCalls = append(a.toolCalls, ToolCall{})
		}
		tc := &a.toolCalls[d.Index]
		if tc.ID == "" && d.ID != "" {
			tc.ID = d.ID
		}
		if tc.Type == "" && d.Type != "" {
			tc.Type = d.Type
		}
		if tc.Function.Name == "" && d.Function.Name != "" {
			tc.Function.Name = d.Function.Name
		}
		tc.Function.Arguments += d.Function.Arguments
	}
}

// ChatStream sends a streaming chat request. onChunk is invoked with the
// accumulated content/reasoning whenever a delta arrives. The returned
// ChatResponse is the fully aggregated result (same shape as Chat's).
// Retries happen only before the stream is established; a broken stream
// mid-flight is surfaced as an error instead of re-sending (which would
// duplicate content already rendered).
func (mc *ModelClient) ChatStream(ctx context.Context, messages []Message, tools []ToolSchema, onChunk func(StreamChunk), opts ...ChatOption) (*ChatResponse, error) {
	o := chatOptions{
		Temperature: 0.7,
		MaxTokens:   8192,
	}
	if p, ok := mc.provider.(*VLLMProvider); ok {
		o.Temperature = p.Temperature
		o.MaxTokens = p.MaxTokens
	}
	if p, ok := mc.provider.(*OllamaProvider); ok {
		o.Temperature = p.Temperature
		o.MaxTokens = p.MaxTokens
	}
	for _, opt := range opts {
		opt(&o)
	}

	req := chatRequest{
		Model:       mc.modelName(),
		Messages:    messages,
		Temperature: o.Temperature,
		MaxTokens:   o.MaxTokens,
		Stream:      true,
	}
	if len(tools) > 0 {
		req.Tools = tools
		req.ToolChoice = "auto"
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Retry only until the connection is established.
	maxRetries := 3
	var resp *http.Response
	for retry := 0; retry <= maxRetries; retry++ {
		resp, err = mc.doRequest(ctx, body)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if mc.isTokenError(err.Error()) {
				return nil, err
			}
			if retry < maxRetries {
				time.Sleep(time.Duration(1+rand.Intn(3)) * time.Second)
				continue
			}
			return nil, err
		}
		if resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			errText := string(respBody)
			if mc.isTokenError(errText) {
				return &ChatResponse{Error: fmt.Sprintf("API error: %d - %s", resp.StatusCode, errText)}, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if retry < maxRetries {
				time.Sleep(time.Duration(1+rand.Intn(3)) * time.Second)
				continue
			}
			return &ChatResponse{Error: fmt.Sprintf("API error: %d - %s", resp.StatusCode, errText)}, nil
		}
		break
	}
	if resp == nil {
		return &ChatResponse{Error: "max retries exceeded"}, nil
	}
	defer resp.Body.Close()

	return mc.readStream(ctx, resp.Body, onChunk)
}

// readStream consumes an SSE body and aggregates deltas into a ChatResponse.
func (mc *ModelClient) readStream(ctx context.Context, body io.Reader, onChunk func(StreamChunk)) (*ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var acc streamAccumulator
	finished := false
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			finished = true
			break
		}
		var delta streamDelta
		if err := json.Unmarshal([]byte(data), &delta); err != nil {
			return nil, fmt.Errorf("unmarshal stream chunk: %w", err)
		}
		if len(delta.Choices) == 0 {
			// Some servers send a trailing chunk carrying only usage.
			if delta.Usage != nil {
				acc.usage = delta.Usage
			}
			continue
		}
		ch := delta.Choices[0]
		acc.content += ch.Delta.Content
		acc.reasoning += ch.Delta.ReasoningContent
		acc.mergeToolCalls(ch.Delta.ToolCalls)
		if ch.FinishReason != "" {
			acc.finishReason = ch.FinishReason
			finished = true
		}
		if delta.Usage != nil {
			acc.usage = delta.Usage
		}
		if onChunk != nil && (ch.Delta.Content != "" || ch.Delta.ReasoningContent != "" || len(ch.Delta.ToolCalls) > 0) {
			onChunk(StreamChunk{Content: acc.content, ReasoningContent: acc.reasoning})
		}
	}
	if err := scanner.Err(); err != nil && !finished {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	// A well-formed SSE chat stream always ends with a chunk carrying a
	// finish_reason (or [DONE]). EOF without one means the connection
	// dropped mid-response. Never accept a partial result: a truncated
	// tool-call, once persisted, poisons the history and the next request
	// fails with "function.arguments must be valid JSON".
	if !finished {
		if acc.content == "" && acc.reasoning == "" && len(acc.toolCalls) == 0 {
			return nil, fmt.Errorf("stream ended unexpectedly")
		}
		return nil, fmt.Errorf("stream ended without finish_reason (connection dropped mid-response); partial result discarded")
	}
	return &ChatResponse{
		Content:          acc.content,
		ReasoningContent: acc.reasoning,
		ToolCalls:        acc.toolCalls,
		FinishReason:     acc.finishReason,
		Usage:            acc.usage,
	}, nil
}
