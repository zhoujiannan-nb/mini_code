package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
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

	// Per-path locks serialize batched tool calls that touch the same file
	// path, so one turn can never interleave two writes/edits of the same
	// file (lost update), read a file while another call rewrites it (stale
	// read), or interleave a copy/move creating or removing a path with
	// another call using that same path. A call locks every path it touches
	// (see fileLockKeys), in sorted order to avoid deadlock. Calls on
	// disjoint paths still run in parallel.
	pathMu    sync.Mutex
	pathLocks map[string]*sync.Mutex

	// Stall detection: when the model re-issues the exact same tool call
	// batch (the set of name + normalized-argument calls, order-independent)
	// on consecutive turns, it is stuck in a loop and will burn the turn
	// budget without progress — including the pattern where the same pair
	// (or set) of failing calls is re-issued over and over, possibly
	// shuffled. nudgeIfStuck tracks the current streak and injects a
	// corrective message once (or twice, escalating) per repeated
	// signature.
	repeatSig   string
	repeatCount int
	nudged      map[string]int

	// Alternating stall detection: a model that ping-pongs between two
	// DISTINCT tool calls (A, B, A, B, ...) is stuck just like an exact
	// repeater — identical arguments re-issued with nothing in between
	// produce identical results, so no new information ever arrives.
	// altHistory keeps the recent batch signatures (window of
	// altHistoryWindow); altNudged caps the nudges per alternating pair.
	altHistory []string
	altNudged  map[string]int

	// Truncation handling: a response cut off by the output token limit
	// (finish_reason="length") is not a complete answer. truncPrefix
	// accumulates the consecutive truncated text chunks of the same answer
	// so the caller receives the full text; truncContinues counts how many
	// times the loop has asked the model to continue (bounded by
	// maxTruncContinues so a persistently oversized answer degrades to
	// best-effort output instead of looping forever).
	truncPrefix    string
	truncContinues int

	// Empty-response handling: a text response with no content and no tool
	// calls is not an answer — ending the task on it would hand the caller
	// an empty reply and silently lose the work. emptyRetries counts the
	// consecutive empty responses of the current answer; the loop nudges
	// the model to take a concrete next step, bounded by maxEmptyRetries
	// so a persistently empty model fails the task with a clear error
	// instead of burning the turn budget.
	emptyRetries int
}

// maxTruncContinues bounds how many times the loop asks the model to
// continue a response that was cut off by the output token limit.
const maxTruncContinues = 3

// maxEmptyRetries bounds how many consecutive empty responses the loop
// tolerates before failing the task with a clear error.
const maxEmptyRetries = 3

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
		pathLocks:        make(map[string]*sync.Mutex),
		nudged:           make(map[string]int),
		altNudged:        make(map[string]int),
	}
}

// fileLockKeys returns the serialization keys for a tool call: the cleaned
// file paths the call touches, taken from its "path", "source" and
// "destination" parameters (whichever are present). read_file / write_file /
// edit_file / delete_file carry "path"; move_file and copy_file carry
// "source" and "destination". Locking every touched path — not just one —
// is what keeps a batched turn from interleaving a copy/move that creates or
// removes a path with another call that reads or writes that same path.
// It returns nil when the call carries no file path at all.
func fileLockKeys(args string) []string {
	if args == "" {
		return nil
	}
	var p struct {
		Path        string `json:"path"`
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var keys []string
	for _, raw := range []string{p.Path, p.Source, p.Destination} {
		if raw == "" {
			continue
		}
		k := filepath.Clean(raw)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys
}

// pathLock returns (creating if needed) the mutex guarding one file path.
func (l *AgentLoop) pathLock(key string) *sync.Mutex {
	l.pathMu.Lock()
	defer l.pathMu.Unlock()
	m, ok := l.pathLocks[key]
	if !ok {
		m = &sync.Mutex{}
		l.pathLocks[key] = m
	}
	return m
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
		resp, err := l.chatWithRetry(ctx, turn, messages, l.toToolSchemas(l.toolDefinitions), func(chunk provider.StreamChunk) {
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
			l.resetTruncation()
			messages = append(messages, provider.Message{Role: "assistant", Content: parsed.toolCallErrorContent, ReasoningContent: resp.ReasoningContent})
			messages = append(messages, provider.Message{Role: "user", Content: "The above tool call format is incorrect. Use structured tool_calls with valid JSON."})
			l.persistTurn(turn, messages)
			continue
		}

		// Truncation handling: a text response cut off by the output token
		// limit is not a complete answer — ending the task here would hand
		// the user a truncated reply. Ask the model to continue exactly
		// where it stopped; the chunks are concatenated into the final
		// answer (see the pure-text path below).
		if resp.FinishReason == "length" && len(parsed.toolCalls) == 0 {
			l.truncPrefix += parsed.content
			messages = append(messages, provider.Message{Role: "assistant", Content: parsed.content, ReasoningContent: resp.ReasoningContent})
			if l.truncContinues >= maxTruncContinues {
				slog.Warn("truncated-response continuation limit reached, returning best-effort content", "turn", turn)
				l.persistTurn(turn, messages)
				return &LoopResult{Success: true, Content: l.truncPrefix, Messages: messages, Turns: turn}, nil
			}
			l.truncContinues++
			messages = append(messages, provider.Message{Role: "user", Content: "Your previous response was cut off by the output token limit before it finished. Continue exactly from where you stopped. Do not repeat the text you already wrote and do not restart the answer."})
			l.persistTurn(turn, messages)
			continue
		}

		if len(parsed.toolCalls) > 0 {
			l.resetTruncation()
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
			// Stall detection: if the model keeps repeating the exact same
			// tool call, inject a corrective nudge so it changes strategy
			// instead of burning the turn budget on the same action.
			messages = l.nudgeIfStuck(messages, parsed.toolCalls)
			if ctx.Err() != nil {
				slog.Info("agent loop interrupted during tool execution", "turn", turn)
				return &LoopResult{Interrupted: true, Messages: messages, Turns: turn}, nil
			}
			// Turn complete: persist the conversation for interrupt recovery.
			l.persistTurn(turn, messages)
			continue
		}

		// Empty-response guard: a text response with no content and no tool
		// calls is not an answer — ending the task here would hand the
		// caller an empty reply (e.g. /goal returns {"data":""}) and the
		// work is silently lost. Nudge the model to take a concrete next
		// step instead of terminating; the retry is bounded (see
		// maxEmptyRetries) so a persistently empty model fails the task
		// with a clear error rather than looping until the turn budget.
		if strings.TrimSpace(parsed.content) == "" && l.truncPrefix == "" {
			if l.emptyRetries >= maxEmptyRetries {
				slog.Warn("empty-response retry limit reached, failing task", "turn", turn)
				l.persistTurn(turn, messages)
				return &LoopResult{Success: false, Messages: messages, Turns: turn, Error: fmt.Sprintf("model returned an empty response %d times in a row; task ended without a result", l.emptyRetries)}, nil
			}
			l.emptyRetries++
			messages = append(messages, provider.Message{Role: "assistant", Content: parsed.content, ReasoningContent: resp.ReasoningContent})
			messages = append(messages, provider.Message{Role: "user", Content: "Your previous response was empty. Continue the task: take the next concrete step (a tool call) or answer the user directly."})
			l.persistTurn(turn, messages)
			continue
		}

		// pure text response -> done. Fold in the earlier truncated chunks
		// of this same answer (if any) so the caller receives the complete
		// text, not just the final tail.
		finalContent := l.truncPrefix + parsed.content
		l.resetTruncation()
		messages = append(messages, provider.Message{Role: "assistant", Content: parsed.content, ReasoningContent: resp.ReasoningContent})
		l.persistTurn(turn, messages)
		return &LoopResult{Success: true, Content: finalContent, Messages: messages, Turns: turn}, nil
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
			msg := "Tool call arguments are not valid JSON. Retry with well-formed JSON arguments."
			if resp.FinishReason == "length" {
				// The arguments were cut off by the output token limit, not
				// merely mistyped: retrying the same oversized payload will
				// truncate again. Point the model at the actual fix.
				msg = "The response hit the output token limit, so the tool call arguments were cut off mid-JSON. The payload was too large for a single call: split the work into smaller steps (e.g. write the file in several smaller chunks) and retry."
			}
			return parsedResponse{toolCallError: true, toolCallErrorContent: msg}
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

// toolCallSignature returns a stable identity for a tool call: the tool name
// plus its arguments normalized to canonical JSON (keys sorted, whitespace
// dropped) so that semantically identical calls compare equal even when the
// model emits them with different key order or spacing.
func toolCallSignature(tc provider.ToolCall) string {
	args := strings.TrimSpace(tc.Function.Arguments)
	if args != "" {
		var v interface{}
		if json.Unmarshal([]byte(args), &v) == nil {
			if b, err := json.Marshal(v); err == nil {
				args = string(b)
			}
		}
	}
	return tc.Function.Name + "\x00" + args
}

// resetTruncation clears the truncated-text continuation state and the
// empty-response retry count. It is called whenever the model produces a
// non-truncated response (a tool call, a failed tool call, or a complete
// non-empty text answer): the truncated chunks from earlier are then either
// already folded into the final answer or abandoned in favor of tool work,
// so they must not leak into a later, unrelated answer; likewise, any
// progress means the empty-response streak is over.
func (l *AgentLoop) resetTruncation() {
	l.truncPrefix = ""
	l.truncContinues = 0
	l.emptyRetries = 0
}

// batchSignature returns an order-independent identity for a whole batch of
// tool calls: each call's canonical signature (see toolCallSignature),
// sorted and joined by a separator. Re-issuing the same set of calls on
// consecutive turns — in the same order or shuffled — produces the same
// signature, so an alternating stall (the same pair of failing calls over
// and over) is detected exactly like a single-call stall. A one-call batch
// has the same signature as the call itself, so single-call behavior is
// unchanged.
func batchSignature(calls []provider.ToolCall) string {
	sigs := make([]string, len(calls))
	for i, tc := range calls {
		sigs[i] = toolCallSignature(tc)
	}
	sort.Strings(sigs)
	return strings.Join(sigs, "\x01")
}

// batchToolNames renders the unique tool names of a batch in first-seen
// order, e.g. "exec" or "exec + read_file", for the nudge message.
func batchToolNames(calls []provider.ToolCall) string {
	seen := make(map[string]bool, len(calls))
	var names []string
	for _, tc := range calls {
		if !seen[tc.Function.Name] {
			seen[tc.Function.Name] = true
			names = append(names, tc.Function.Name)
		}
	}
	return strings.Join(names, " + ")
}

// nudgeIfStuck tracks whether the model is stuck in a stall loop and, when
// detected, appends a corrective user message telling it to change strategy.
// Two stall shapes are caught, each with at most two escalating nudges:
//
//  1. Exact-repeat stall: the same tool call batch re-issued on consecutive
//     turns. The batch identity is order-independent (see batchSignature),
//     so both "the same call again" and "the same set of calls, shuffled"
//     are caught. A streak of 3 triggers the first nudge; 5 the second.
//
//  2. Alternating stall: two DISTINCT batches ping-ponged turn after turn
//     (A, B, A, B, A, B). Identical arguments with nothing in between
//     produce identical results, so the model is re-reading the same
//     information and making no progress. A strict period-2 window of
//     altHistoryWindow turns triggers the first nudge; if the alternation
//     continues, a second, firmer one follows.
//
// Any different batch (or a turn without tool calls) resets the streaks —
// genuinely new work is progress, not a stall.
func (l *AgentLoop) nudgeIfStuck(messages []provider.Message, calls []provider.ToolCall) []provider.Message {
	if len(calls) == 0 {
		l.repeatSig = ""
		l.repeatCount = 0
		l.altHistory = nil
		return messages
	}
	sig := batchSignature(calls)
	if sig == l.repeatSig {
		l.repeatCount++
	} else {
		l.repeatSig = sig
		l.repeatCount = 1
	}
	// Keep the recent window for alternating-stall detection.
	l.altHistory = append(l.altHistory, sig)
	if len(l.altHistory) > altHistoryWindow {
		l.altHistory = l.altHistory[len(l.altHistory)-altHistoryWindow:]
	}

	// Stall 1: the exact same batch on consecutive turns.
	if l.repeatCount >= 3 {
		n := l.nudged[sig]
		if n < 2 {
			l.nudged[sig] = n + 1
			toolNames := batchToolNames(calls)
			var msg string
			if n == 0 {
				msg = fmt.Sprintf("You have now executed the exact same tool call(s) (%s) %d times in a row with no progress. Stop repeating it: analyze the result you already received and take a different action — change the arguments, use a different tool, or finish the task with what you already know.", toolNames, l.repeatCount)
			} else {
				msg = fmt.Sprintf("You are STILL repeating the same tool call(s) (%s). This is a loop and it will not succeed. Do NOT call them again: either fix the underlying problem with a genuinely different approach, or answer the task directly with the information you already have.", toolNames)
			}
			slog.Warn("repeated tool call batch detected, nudging model", "tools", toolNames, "streak", l.repeatCount, "nudge", n+1)
			return append(messages, provider.Message{Role: "user", Content: msg})
		}
	}

	// Stall 2: strict alternation between two distinct batches.
	if pair, ok := alternatingPair(l.altHistory); ok {
		n := l.altNudged[pair]
		if n < 2 {
			l.altNudged[pair] = n + 1
			h := l.altHistory[len(l.altHistory)-altHistoryWindow:]
			descA, descB := callDesc(h[0]), callDesc(h[1])
			var msg string
			if n == 0 {
				msg = fmt.Sprintf("You are alternating between two tool calls — %s and %s — turn after turn. Re-issuing the same calls with the same arguments produces the same results: no new information, no progress. Stop the alternation: analyze the results you already have, then take a genuinely different action (change the arguments, use a different tool, or finish the task with what you already know).", descA, descB)
			} else {
				msg = fmt.Sprintf("You are STILL alternating between %s and %s. This loop will not succeed. Do NOT call either of them again: fix the underlying problem with a genuinely different approach, or answer the task directly with the information you already have.", descA, descB)
			}
			slog.Warn("alternating tool call stall detected, nudging model", "call_a", descA, "call_b", descB, "nudge", n+1)
			return append(messages, provider.Message{Role: "user", Content: msg})
		}
	}
	return messages
}

// altHistoryWindow is the number of recent batch signatures kept for
// alternating-stall detection. A strict A,B,A,B,A,B window (six turns, three
// full alternations) is unambiguous: with nothing in between, the repeated
// calls return the same results every time, so the model cannot be making
// progress. Shorter alternations (e.g. reading two files back and forth once
// or twice) stay below the threshold and are treated as normal work.
const altHistoryWindow = 6

// alternatingPair reports whether the recent signature window forms a strict
// period-2 alternation of two distinct calls (A,B,A,B,A,B) and returns a
// canonical, order-independent key for the pair.
func alternatingPair(hist []string) (string, bool) {
	if len(hist) < altHistoryWindow {
		return "", false
	}
	h := hist[len(hist)-altHistoryWindow:]
	if h[0] == h[1] {
		return "", false
	}
	for i := 2; i < len(h); i++ {
		if h[i] != h[i-2] {
			return "", false
		}
	}
	a, b := h[0], h[1]
	if b < a {
		a, b = b, a
	}
	return a + "\x01" + b, true
}

// callDesc renders a short human-readable description of a call signature
// (see toolCallSignature): the tool name, plus its arguments truncated to
// 80 chars when non-empty.
func callDesc(sig string) string {
	name, args, _ := strings.Cut(sig, "\x00")
	if args == "" {
		return name
	}
	if len(args) > 80 {
		args = args[:80] + "…"
	}
	return name + "(" + args + ")"
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
			// Serialize calls that touch the same file path: a batched turn
			// with two edits of one file must not interleave (lost update /
			// stale read), and a copy/move that creates or removes a path
			// must not interleave with another call reading or writing that
			// same path. Different paths keep full parallelism. All keys of
			// a call are locked in sorted order so two calls sharing some
			// keys can never deadlock.
			if keys := fileLockKeys(tc.Function.Arguments); len(keys) > 0 {
				sort.Strings(keys)
				for _, k := range keys {
					l.pathLock(k).Lock()
				}
				defer func() {
					for i := len(keys) - 1; i >= 0; i-- {
						l.pathLock(keys[i]).Unlock()
					}
				}()
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

func (l *AgentLoop) executeSingleTool(ctx context.Context, tc provider.ToolCall) (result *tools.ToolResult) {
	// Panic containment: a bug in one tool (index out of range, nil deref,
	// a bad model-supplied argument) must fail that single call, not crash
	// the whole agent process. The model sees a normal tool error and can
	// retry with a different approach; the task and the server keep running.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("tool execution panicked, contained", "name", tc.Function.Name, "panic", r)
			result = tools.NewTextResult(fmt.Sprintf("Error: tool %q crashed (panic: %v). The task continues; analyze the cause and try a different approach.", tc.Function.Name, r))
		}
	}()
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

// chatWithRetry wraps ChatStream with bounded retries for transient
// transport-level failures (connection reset, stream dropped mid-response,
// server timeout). Retrying is safe and idempotent: the assistant message is
// appended to the conversation only after a fully successful response, so a
// failed attempt leaves no state behind and the same request can simply be
// re-sent. API-level errors (resp.Error) are NOT retried here — the client
// has already retried those, and context-length errors must fail fast.
func (l *AgentLoop) chatWithRetry(ctx context.Context, turn int, messages []provider.Message, schemas []provider.ToolSchema, onChunk func(provider.StreamChunk)) (*provider.ChatResponse, error) {
	const maxAttempts = 3 // initial call + 2 retries
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := l.client.ChatStream(ctx, messages, schemas, onChunk)
		if err == nil {
			if attempt > 1 {
				slog.Info("LLM call recovered after transient failure", "turn", turn, "attempt", attempt)
			}
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == maxAttempts {
			break
		}
		wait := time.Duration(2*attempt-1) * time.Second // 1s, then 3s
		slog.Warn("LLM call failed, retrying", "turn", turn, "attempt", attempt, "wait_s", int(wait.Seconds()), "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, lastErr
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
