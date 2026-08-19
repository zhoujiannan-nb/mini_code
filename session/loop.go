package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
	"github.com/user/mini_code/util"
)

type LoopResult struct {
	Success     bool
	Interrupted bool
	Content     string
	Messages    []provider.Message
	Turns       int
	Error       string
}

type AgentLoop struct {
	client           *provider.ModelClient
	maxTurns         int
	tools            *tools.ToolRegistry
	toolDefinitions  []provider.ToolSchema
	compactor        *ContextCompactor
	compactionOn     bool // false = skip CheckAndCompress entirely
	onToolCallStart  func(name string, params map[string]interface{})
	onToolCallEnd    func(name string, params map[string]interface{}, result string)
	onAssistantReply func(content string, reasoning string)
	onTurn           func(turn int, messages []provider.Message)
}

func NewAgentLoop(client *provider.ModelClient, maxTurns int, toolRegistry *tools.ToolRegistry, defs []provider.ToolSchema, onToolCallStart func(string, map[string]interface{}), onToolCallEnd func(string, map[string]interface{}, string), onAssistantReply func(string, string), onTurn func(int, []provider.Message)) *AgentLoop {
	if maxTurns <= 0 {
		maxTurns = 999
	}
	if toolRegistry == nil {
		toolRegistry = tools.NewToolRegistry()
	}
	return &AgentLoop{
		client:           client,
		maxTurns:         maxTurns,
		tools:            toolRegistry,
		toolDefinitions:  defs,
		compactor:        NewContextCompactor(client),
		compactionOn:     true, // default keeps legacy behavior; callers may opt out
		onToolCallStart:  onToolCallStart,
		onToolCallEnd:    onToolCallEnd,
		onAssistantReply: onAssistantReply,
		onTurn:           onTurn,
	}
}

// persistTurn invokes the per-turn persistence hook. The hook saves the
// accumulated conversation to the database after every completed turn so an
// interrupted task can be resumed from the last good state.
func (l *AgentLoop) persistTurn(turn int, messages []provider.Message) {
	if l.onTurn != nil {
		l.onTurn(turn, messages)
	}
}

func (l *AgentLoop) Run(ctx context.Context, systemPrompt, userMessage string, history []provider.Message) (*LoopResult, error) {
	var messages []provider.Message
	if len(history) > 0 {
		messages = append(messages, history...)
		if len(history) == 0 || history[0].Role != "system" {
			messages = append([]provider.Message{{Role: "system", Content: systemPrompt}}, messages...)
		}
		// Only append user message if history doesn't already end with it
		if len(messages) == 0 || messages[len(messages)-1].Role != "user" || messages[len(messages)-1].Content != userMessage {
			messages = append(messages, provider.Message{Role: "user", Content: userMessage})
		}
	} else {
		messages = []provider.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		}
	}

	slog.Info("AgentLoop started", "initial_tokens", util.CountMessagesTokens(messages), "max_input", l.client.GetMaxInput())

	for turn := 1; turn <= l.maxTurns; turn++ {
		slog.Info(fmt.Sprintf("=== Turn %d ===", turn))

		// Check for interrupt before each turn
		if ctx.Err() != nil {
			slog.Info("agent loop interrupted", "turn", turn, "collected_messages", len(messages))
			return &LoopResult{Interrupted: true, Messages: messages, Turns: turn - 1}, nil
		}

		// compact context (skipped entirely when the compaction toggle is off)
		if l.compactionOn {
			var err error
			messages, err = l.compactor.CheckAndCompress(ctx, messages, l.client.GetMaxInput())
			if err != nil {
				slog.Warn("compaction failed", "err", err)
			}
			if ctx.Err() != nil {
				slog.Info("agent loop interrupted after compaction", "turn", turn)
				return &LoopResult{Interrupted: true, Messages: messages, Turns: turn}, nil
			}
		}
		l.persistTurn(turn, messages)

		// call LLM (streaming: chunks are forwarded to the UI via throttled callback)
		var flusher *streamFlusher
		if l.onAssistantReply != nil {
			flusher = newStreamFlusher(l.onAssistantReply)
			flusher.start()
		}
		resp, err := l.client.ChatStream(ctx, messages, l.toToolSchemas(l.toolDefinitions), func(chunk provider.StreamChunk) {
			if flusher != nil {
				flusher.push(chunk.Content, chunk.ReasoningContent)
			}
		})
		if flusher != nil {
			flusher.stop() // emit any tail and release the ticker before leaving this turn
		}
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("agent loop interrupted during LLM call", "turn", turn)
				return &LoopResult{Interrupted: true, Messages: messages, Turns: turn}, nil
			}
			return &LoopResult{Success: false, Messages: messages, Turns: turn, Error: err.Error()}, nil
		}
		if resp.Error != "" {
			return &LoopResult{Success: false, Messages: messages, Turns: turn, Error: resp.Error}, nil
		}

		// parse response
		parsed := l.parseResponse(resp)
		slog.Info("agent response", "content", parsed.content)

		if parsed.toolCallError {
			messages = append(messages, provider.Message{Role: "assistant", Content: parsed.toolCallErrorContent, ReasoningContent: resp.ReasoningContent})
			messages = append(messages, provider.Message{Role: "user", Content: "The above tool call format is incorrect. Use structured tool_calls with valid JSON."})
			l.persistTurn(turn, messages)
			continue
		}

		if len(parsed.toolCalls) > 0 {
			if ctx.Err() != nil {
				slog.Info("agent loop interrupted before tool execution", "turn", turn)
				return &LoopResult{Interrupted: true, Messages: messages, Turns: turn}, nil
			}
			// Ensure every tool call has a unique ID for UI tracking
			for i := range parsed.toolCalls {
				if parsed.toolCalls[i].ID == "" {
					parsed.toolCalls[i].ID = "call_" + util.RandomHex(8)
				}
			}
			// Notify consumers of assistant text reply (if any)
			if parsed.content != "" && l.onAssistantReply != nil {
				l.onAssistantReply(parsed.content, resp.ReasoningContent)
			}
			messages = append(messages, provider.Message{
				Role:             "assistant",
				Content:          parsed.content,
				ReasoningContent: resp.ReasoningContent,
				ToolCalls:        parsed.toolCalls,
			})
			messages = l.handleToolCalls(ctx, messages, parsed.toolCalls)
			if ctx.Err() != nil {
				slog.Info("agent loop interrupted during tool execution", "turn", turn)
				return &LoopResult{Interrupted: true, Messages: messages, Turns: turn}, nil
			}
			// Turn complete: persist the conversation for interrupt recovery.
			l.persistTurn(turn, messages)
			continue
		}

		// pure text response -> done
		messages = append(messages, provider.Message{Role: "assistant", Content: parsed.content, ReasoningContent: resp.ReasoningContent})
		l.persistTurn(turn, messages)
		return &LoopResult{Success: true, Content: parsed.content, Messages: messages, Turns: turn}, nil
	}

	l.persistTurn(l.maxTurns, messages)
	return &LoopResult{Success: false, Messages: messages, Turns: l.maxTurns, Error: fmt.Sprintf("max turns reached (%d)", l.maxTurns)}, nil
}

type parsedResponse struct {
	content              string
	toolCalls            []provider.ToolCall
	shouldTerminate      bool
	toolCallError        bool
	toolCallErrorContent string
}

func (l *AgentLoop) parseResponse(resp *provider.ChatResponse) parsedResponse {
	content := resp.Content
	toolCalls := resp.ToolCalls

	if content != "" && len(toolCalls) == 0 {
		extracted := l.tryExtractTextToolCall(content)
		if extracted != nil {
			if extracted.status == "success" {
				toolCalls = extracted.toolCalls
				content = ""
			} else {
				return parsedResponse{toolCallError: true, toolCallErrorContent: "Tool call parsing failed. Use correct format and parameters."}
			}
		}
	}

	// Guard: never persist or execute a tool call whose arguments are not
	// valid JSON (e.g. a response cut off mid-arguments). If such a call
	// were saved, the API would reject the whole history on every later
	// request with "function.arguments must be valid JSON". Treat it like
	// any other malformed tool call: report it and let the model retry.
	for i := range toolCalls {
		args := strings.TrimSpace(toolCalls[i].Function.Arguments)
		if args == "" {
			toolCalls[i].Function.Arguments = "{}"
		} else if !json.Valid([]byte(args)) {
			return parsedResponse{toolCallError: true, toolCallErrorContent: "Tool call arguments are not valid JSON. Retry with well-formed JSON arguments."}
		}
	}

	if resp.FinishReason == "stop" && len(toolCalls) == 0 {
		return parsedResponse{content: content, toolCalls: toolCalls, shouldTerminate: true}
	}
	return parsedResponse{content: content, toolCalls: toolCalls}
}

type extractedCalls struct {
	status    string
	toolCalls []provider.ToolCall
}

var toolCallTagRe = regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`)

func (l *AgentLoop) tryExtractTextToolCall(text string) *extractedCalls {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	matches := toolCallTagRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	var calls []provider.ToolCall
	for _, m := range matches {
		content := strings.TrimSpace(m[1])
		tc := l.parseToolCallJSON(content)
		if tc != nil {
			calls = append(calls, *tc)
			continue
		}
		tc = l.parseToolCallJSON(content + "}")
		if tc != nil {
			calls = append(calls, *tc)
			continue
		}
		return &extractedCalls{status: "error"}
	}
	if len(calls) > 0 {
		return &extractedCalls{status: "success", toolCalls: calls}
	}
	return nil
}

func (l *AgentLoop) parseToolCallJSON(content string) *provider.ToolCall {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil
	}
	var name, args string
	if fn, ok := parsed["function"].(map[string]interface{}); ok {
		name, _ = fn["name"].(string)
		if a, ok := fn["arguments"].(string); ok {
			args = a
		} else {
			b, _ := json.Marshal(fn["arguments"])
			args = string(b)
		}
	} else if n, ok := parsed["name"].(string); ok {
		name = n
		if a, ok := parsed["arguments"].(string); ok {
			args = a
		} else {
			b, _ := json.Marshal(parsed["arguments"])
			args = string(b)
		}
	}
	if name == "" {
		return nil
	}
	if args == "" {
		args = "{}"
	}
	return &provider.ToolCall{
		ID:   "call_" + util.RandomHex(8),
		Type: "function",
		Function: provider.FuncCall{
			Name:      name,
			Arguments: args,
		},
	}
}

func (l *AgentLoop) handleToolCalls(ctx context.Context, messages []provider.Message, toolCalls []provider.ToolCall) []provider.Message {
	// Notify consumers that tool execution is starting
	if l.onToolCallStart != nil {
		for _, tc := range toolCalls {
			var params map[string]interface{}
			if tc.Function.Arguments != "" {
				json.Unmarshal([]byte(tc.Function.Arguments), &params)
			}
			l.onToolCallStart(tc.Function.Name, params)
		}
	}

	results := l.executeToolsConcurrent(ctx, toolCalls)
	for i, tc := range toolCalls {
		result := results[i]
		tcName := tc.Function.Name
		if result.HasMultimodal() {
			var parts []provider.ContentPart
			parts = append(parts, provider.NewTextPart("Tool result: "+tcName))
			parts = append(parts, result.Parts...)
			msg := provider.Message{Role: "tool", ToolCallID: tc.ID, Name: tcName}
			msg.SetMultimodalContent(parts)
			messages = append(messages, msg)
		} else {
			messages = append(messages, provider.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tcName,
				Content:    result.Text,
			})
		}
	}
	return messages
}

func (l *AgentLoop) executeToolsConcurrent(ctx context.Context, toolCalls []provider.ToolCall) []*tools.ToolResult {
	results := make([]*tools.ToolResult, len(toolCalls))
	var wg sync.WaitGroup
	for i, tc := range toolCalls {
		wg.Add(1)
		go func(idx int, tc provider.ToolCall) {
			defer wg.Done()
			// Skip if context already cancelled.
			if ctx.Err() != nil {
				results[idx] = tools.NewTextResult("Interrupted")
				// Notify consumers that tool call ended (interrupted)
				if l.onToolCallEnd != nil {
					var params map[string]interface{}
					if tc.Function.Arguments != "" {
						json.Unmarshal([]byte(tc.Function.Arguments), &params)
					}
					l.onToolCallEnd(tc.Function.Name, params, "Interrupted")
				}
				return
			}
			results[idx] = l.executeSingleTool(ctx, tc)
			// Notify consumers that tool call ended
			if l.onToolCallEnd != nil {
				var params map[string]interface{}
				if tc.Function.Arguments != "" {
					json.Unmarshal([]byte(tc.Function.Arguments), &params)
				}
				l.onToolCallEnd(tc.Function.Name, params, results[idx].Text)
			}
		}(i, tc)
	}
	wg.Wait()
	return results
}

func (l *AgentLoop) executeSingleTool(ctx context.Context, tc provider.ToolCall) *tools.ToolResult {
	var params map[string]interface{}
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
			slog.Warn("invalid tool call arguments", "name", tc.Function.Name, "error", err, "params", tc.Function.Arguments)
		}
	}
	slog.Info("executing tool", "name", tc.Function.Name, "params", tc.Function.Arguments)
	result, err := l.tools.Execute(ctx, tc.Function.Name, params)
	if err != nil {
		return tools.NewTextResult(fmt.Sprintf("Error: %s", err))
	}
	slog.Info("tool completed", "name", tc.Function.Name, "result_len", len(result.Text))
	return result
}

func (l *AgentLoop) toToolSchemas(defs []provider.ToolSchema) []provider.ToolSchema {
	var result []provider.ToolSchema
	for _, d := range defs {
		result = append(result, provider.ToolSchema{
			Type: d.Type,
			Function: provider.ToolSchemaFunc{
				Name:        d.Function.Name,
				Description: d.Function.Description,
				Parameters:  d.Function.Parameters,
			},
		})
	}
	return result
}

// streamFlusher coalesces streaming deltas so the consumer receives at most
// one update per tick interval, preventing message storms on fast streams.
// Content and ReasoningContent are accumulated values (never deltas).
type streamFlusher struct {
	mu        sync.Mutex
	content   string
	reasoning string
	dirty     bool
	onReply   func(content string, reasoning string)
	ticker    *time.Ticker
	done      chan struct{}
}

func newStreamFlusher(onReply func(content string, reasoning string)) *streamFlusher {
	return &streamFlusher{onReply: onReply}
}

func (f *streamFlusher) start() {
	f.ticker = time.NewTicker(50 * time.Millisecond)
	f.done = make(chan struct{})
	go func() {
		for {
			select {
			case <-f.ticker.C:
				f.flush()
			case <-f.done:
				return
			}
		}
	}()
}

func (f *streamFlusher) stop() {
	if f.ticker != nil {
		f.ticker.Stop()
	}
	if f.done != nil {
		close(f.done)
	}
	f.flush() // emit any pending tail
}

// push records the latest accumulated values; the ticker emits them later.
func (f *streamFlusher) push(content, reasoning string) {
	f.mu.Lock()
	f.content = content
	f.reasoning = reasoning
	f.dirty = true
	f.mu.Unlock()
}

func (f *streamFlusher) flush() {
	f.mu.Lock()
	if !f.dirty {
		f.mu.Unlock()
		return
	}
	c, r := f.content, f.reasoning
	f.dirty = false
	f.mu.Unlock()
	f.onReply(c, r)
}

// LastAssistantReasoning returns the reasoning_content of the last assistant
// message in the conversation ("" when none carries any).
func LastAssistantReasoning(messages []provider.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].ReasoningContent
		}
	}
	return ""
}
