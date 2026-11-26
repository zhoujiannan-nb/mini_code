package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
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
	client          *provider.ModelClient
	maxTurns        int
	tools           *tools.ToolRegistry
	toolDefinitions []provider.ToolSchema
	compactor       *ContextCompactor
	compactionOn    bool
	argAgingOn bool
	compactionTurn bool
	compactionBlocked bool
	lastCompactionTokens int
	ctxState             *tools.ContextState
	onToolCallStart      func(name string, params map[string]interface{})
	onToolCallEnd        func(name string, params map[string]interface{}, result string)
	onToolOutput func(name string, chunk string)
	onToolArgs       func(name string, argsSoFar string)
	onAssistantReply func(content string, reasoning string)
	onTurn           func(turn int, messages []provider.Message)
	onContextUsage   func(tokens int)
	onUsage          func(promptTokens, cachedTokens, completionTokens int, cacheReported bool, speed float64)
	eventSink func(typ string, turn int, payload map[string]any)
	spillDir string
	spillID  string
	jobOwner string
	obs    *tools.Observation
	obsDir string
	curTurn int
	gate    ToolGate

	pathMu    sync.Mutex
	pathLocks map[string]*sync.Mutex

	repeatSig   string
	repeatCount int
	nudged      map[string]int

	altHistory []string
	altNudged  map[string]int

	truncPrefix    string
	truncContinues int

	emptyRetries int

	userImages []string

	injections func() []string

	workerNotice func([]provider.Message) provider.Message

	calib *tokenCalibrator
	estBefore int
}

func (l *AgentLoop) SetUserImages(images []string) { l.userImages = images }

func (l *AgentLoop) SetInjections(fn func() []string) { l.injections = fn }

func (l *AgentLoop) SetWorkerNotice(fn func([]provider.Message) provider.Message) {
	l.workerNotice = fn
}

func (l *AgentLoop) SetTokenCalibrator(c *tokenCalibrator) { l.calib = c }

func (l *AgentLoop) calibrator() *tokenCalibrator {
	if l.calib != nil {
		return l.calib
	}
	return calibratorFor(l.client.ModelName(), l.toolDefinitions)
}

func (l *AgentLoop) calibratedCount(messages []provider.Message) int {
	l.estBefore = util.CountMessagesTokens(messages)
	return l.calibrator().apply(l.estBefore)
}

const unknownInputBudget = 65536

var unknownWindowWarned sync.Map

func warnUnknownWindowOnce(model string) {
	if _, loaded := unknownWindowWarned.LoadOrStore(model, struct{}{}); !loaded {
		slog.Warn("model context window is not configured: context budgeting assumes a conservative budget, "+
			"so the session will compact earlier than it has to. Set context_window in the model config (or run the probe).",
			"model", model, "assumed_input_budget", unknownInputBudget)
	}
}

func inputBudgetFor(client *provider.ModelClient) int {
	if client == nil {
		return unknownInputBudget
	}
	if b := client.GetMaxInput(); b > 0 {
		return b
	}
	warnUnknownWindowOnce(client.ModelName())
	return unknownInputBudget
}

func (l *AgentLoop) inputBudget() int { return inputBudgetFor(l.client) }

func (l *AgentLoop) countMessages(messages []provider.Message) int {
	return l.calibrator().apply(util.CountMessagesTokens(messages))
}

func (l *AgentLoop) SetGate(g ToolGate) { l.gate = g }

func (l *AgentLoop) SetOnToolOutput(fn func(name, chunk string)) { l.onToolOutput = fn }

func (l *AgentLoop) SetOnToolArgs(fn func(name, argsSoFar string)) { l.onToolArgs = fn }

func (l *AgentLoop) SetContextState(state *tools.ContextState) { l.ctxState = state }

func (l *AgentLoop) SetOnContextUsage(fn func(tokens int)) { l.onContextUsage = fn }

func (l *AgentLoop) SetOnUsage(fn func(promptTokens, cachedTokens, completionTokens int, cacheReported bool, speed float64)) {
	l.onUsage = fn
}

func (l *AgentLoop) SetEventSink(fn func(typ string, turn int, payload map[string]any)) {
	l.eventSink = fn
}

func (l *AgentLoop) SetSpillDir(dir, sessionID string) {
	l.spillDir = dir
	l.spillID = sessionID
}

func (l *AgentLoop) SetJobOwner(sessionID string) { l.jobOwner = sessionID }

func (l *AgentLoop) emitEvent(typ string, turn int, payload map[string]any) {
	if l.eventSink != nil {
		l.eventSink(typ, turn, payload)
	}
}

func (l *AgentLoop) emitTaskEnd(status string, turn int, errMsg string) {
	p := map[string]any{"status": status, "turns": turn}
	if errMsg != "" {
		p["error"] = errMsg
	}
	l.emitEvent(EventTaskEnd, turn, p)
}

func (l *AgentLoop) emitAssistantMessage(turn int, content, reasoning string, toolCalls []provider.ToolCall, usage map[string]any) {
	p := map[string]any{"content": content, "reasoning_chars": len(reasoning)}
	if len(toolCalls) > 0 {
		calls := make([]map[string]any, 0, len(toolCalls))
		for _, tc := range toolCalls {
			calls = append(calls, map[string]any{"id": tc.ID, "name": tc.Function.Name, "args": tc.Function.Arguments})
		}
		p["tool_calls"] = calls
	}
	if usage != nil {
		p["usage"] = usage
	}
	l.emitEvent(EventAssistantMessage, turn, p)
}

func (l *AgentLoop) buildUserMessage(text string) provider.Message {
	if len(l.userImages) == 0 {
		return provider.Message{Role: "user", Content: text}
	}
	var parts []provider.ContentPart
	if text != "" {
		parts = append(parts, provider.NewTextPart(text))
	}
	for _, u := range l.userImages {
		parts = append(parts, provider.NewImagePart(u))
	}
	m := provider.Message{Role: "user"}
	m.SetMultimodalContent(parts)
	return m
}

func imageURLsOf(m provider.Message) []string {
	var urls []string
	for _, p := range m.ContentParts {
		if p.Type == "image_url" && p.ImageURL != nil {
			urls = append(urls, p.ImageURL.URL)
		}
	}
	return urls
}

const orphanToolCallNote = "Tool not executed: the task was interrupted before this call ran."

func repairOrphanToolCalls(messages []provider.Message) []provider.Message {
	answered := make(map[string]bool, len(messages))
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}
	var names []string
	out := make([]provider.Message, 0, len(messages)+2)
	for i := 0; i < len(messages); i++ {
		m := messages[i]
		out = append(out, m)
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		j := i + 1
		for j < len(messages) && messages[j].Role == "tool" {
			out = append(out, messages[j])
			j++
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == "" || answered[tc.ID] {
				continue
			}
			out = append(out, provider.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    orphanToolCallNote,
			})
			answered[tc.ID] = true
			names = append(names, tc.Function.Name)
		}
		i = j - 1
	}
	if len(names) == 0 {
		return messages
	}
	slog.Warn("repaired tool calls left without a result by an interrupted run",
		"count", len(names), "tools", strings.Join(names, ","))
	return out
}

func historyEndsWithUserMsg(messages []provider.Message, userMsg provider.Message) bool {
	if len(messages) == 0 {
		return false
	}
	return userMessageMatches(messages[len(messages)-1], userMsg)
}

func userMessageMatches(last, cur provider.Message) bool {
	if last.Role != "user" || cur.Role != "user" {
		return false
	}
	if last.GetText() != cur.GetText() {
		return false
	}
	li, ci := imageURLsOf(last), imageURLsOf(cur)
	if len(li) != len(ci) {
		return false
	}
	for i := range li {
		if li[i] != ci[i] {
			return false
		}
	}
	return true
}

const maxTruncContinues = 3

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
		compactionOn:     true,
		argAgingOn:       os.Getenv("MC_ARG_AGE") == "1",
		onToolCallStart:  onToolCallStart,
		onToolCallEnd:    onToolCallEnd,
		onAssistantReply: onAssistantReply,
		onTurn:           onTurn,
		pathLocks:        make(map[string]*sync.Mutex),
		nudged:           make(map[string]int),
		altNudged:        make(map[string]int),
	}
}

func fileLockKeys(args string) []string {
	if args == "" {
		return nil
	}
	var p struct {
		FilePath    string `json:"file_path"`
		Path        string `json:"path"`
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var keys []string
	for _, raw := range []string{p.FilePath, p.Path, p.Source, p.Destination} {
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

func (l *AgentLoop) persistTurn(turn int, messages []provider.Message) {
	if l.onTurn != nil {
		l.onTurn(turn, messages)
	}
}

func (l *AgentLoop) autoCompactLimit() int {
	return InputBudgetFraction(l.inputBudget(), compactInputFraction)
}

func (l *AgentLoop) applyWorkerNotice(messages []provider.Message) []provider.Message {
	if l.workerNotice == nil {
		return messages
	}
	want := l.workerNotice(messages)
	if strings.TrimSpace(want.GetText()) == "" {
		return messages
	}
	l.emitEvent(EventUserMessage, 0, map[string]any{"text": want.GetText(), "source": "worker_notice"})
	return append(messages, want)
}

func (l *AgentLoop) Run(ctx context.Context, systemPrompt, userMessage string, history []provider.Message) (*LoopResult, error) {
	resumeCompaction := len(history) > 0 && IsCompactRequest(history[len(history)-1])

	userMsg := l.buildUserMessage(userMessage)
	var messages []provider.Message
	if len(history) > 0 {
		messages = append(messages, history...)
		if len(history) == 0 || history[0].Role != "system" {
			messages = append([]provider.Message{{Role: "system", Content: systemPrompt}}, messages...)
		}
		if !resumeCompaction && (len(messages) == 0 || !historyEndsWithUserMsg(messages, userMsg)) {
			messages = append(messages, userMsg)
		}
	} else {
		messages = []provider.Message{
			{Role: "system", Content: systemPrompt},
			userMsg,
		}
	}

	messages = repairOrphanToolCalls(messages)

	if resumeCompaction {
		l.compactionTurn = true
		slog.Info("resuming in-flight compaction turn", "messages", len(messages))
	} else {
		messages = StripCompactRequests(messages)
		messages = l.applyWorkerNotice(messages)
	}

	for turn := 1; turn <= l.maxTurns; turn++ {
		l.curTurn = turn
		l.emitEvent(EventTurnStart, turn, nil)

		if ctx.Err() != nil {
			l.emitTaskEnd("interrupted", turn-1, "cancelled before turn")
			return &LoopResult{Interrupted: true, Messages: messages, Turns: turn - 1}, nil
		}

		if l.injections != nil {
			for _, t := range l.injections() {
				if strings.TrimSpace(t) == "" {
					continue
				}
				messages = append(messages, provider.Message{Role: "user", Content: t})
				l.emitEvent(EventUserMessage, turn, map[string]any{"text": t, "source": "queued"})
			}
		}

		tokens := l.calibratedCount(messages)
		if l.compactionTurn {
		} else if !l.compactionBlocked {
			need, instruction := false, ""
			if l.ctxState != nil {
				if pending, instr := l.ctxState.TakePendingCompaction(); pending {
					need, instruction = true, instr
				}
			}
			if !need && l.compactionOn {
				maxInput := l.inputBudget()
				if l.compactor.ShouldCompactCount(tokens, l.autoCompactLimit()) &&
					tokens >= l.lastCompactionTokens+recompactGrowTokens(maxInput) {
					need = true
				}
			}
			if need {
				l.emitEvent(EventCompaction, turn, map[string]any{
					"kind": "auto", "phase": "start", "tokens": tokens,
					"agent_instruction": instruction != "",
				})
				messages = append(messages, BuildCompactRequest(instruction))
				l.compactionTurn = true
				l.persistTurn(turn, messages)
				tokens = l.calibratedCount(messages)
			}
		}
		if ctx.Err() != nil {
			l.emitTaskEnd("interrupted", turn, "cancelled before compaction turn")
			return &LoopResult{Interrupted: true, Messages: messages, Turns: turn}, nil
		}
		if l.ctxState != nil || l.onContextUsage != nil {
			if l.ctxState != nil {
				l.ctxState.SetUsage(tokens, l.inputBudget(), l.autoCompactLimit())
			}
			if l.onContextUsage != nil {
				l.onContextUsage(tokens)
			}
		}
		l.persistTurn(turn, messages)

		var flusher *streamFlusher
		if l.onAssistantReply != nil {
			flusher = newStreamFlusher(l.onAssistantReply)
			flusher.start()
		}
		var firstTokAt time.Time
		var lastLen int
		callStart := time.Now()
		argFeed := newArgsStreamer(l.onToolArgs)
		resp, err := l.chatWithRetry(ctx, turn, messages, l.toToolSchemas(l.toolDefinitions), func(chunk provider.StreamChunk) {
			argFeed.push(chunk.ToolCalls)
			n := len(chunk.Content) + len(chunk.ReasoningContent)
			if n < lastLen {
				firstTokAt = time.Time{}
				lastLen = 0
			}
			if firstTokAt.IsZero() && n > 0 {
				firstTokAt = time.Now()
			}
			if n > lastLen {
				lastLen = n
			}
			if flusher != nil {
				flusher.push(chunk.Content, chunk.ReasoningContent)
			}
		})
		if flusher != nil {
			flusher.stop()
		}
		if err != nil {
			if ctx.Err() != nil {
				l.emitTaskEnd("interrupted", turn, "cancelled during LLM call")
				return &LoopResult{Interrupted: true, Messages: messages, Turns: turn}, nil
			}
			l.emitTaskEnd("failed", turn, err.Error())
			return &LoopResult{Success: false, Messages: messages, Turns: turn, Error: err.Error()}, nil
		}
		if resp.Error != "" {
			if isContextOverflowText(resp.Error) && l.shrinkForOverflow(messages) {
				l.emitEvent(EventCompaction, turn, map[string]any{"kind": "overflow-shrink", "phase": "done"})
				l.persistTurn(turn, messages)
				continue
			}
			l.emitTaskEnd("failed", turn, resp.Error)
			return &LoopResult{Success: false, Messages: messages, Turns: turn, Error: resp.Error}, nil
		}

		var usageEvent map[string]any
		if u := resp.Usage; u != nil {
			cached := u.CachedTokens()
			reported := u.CacheReported()
			speed := 0.0
			if u.CompletionTokens > 0 {
				dec := time.Since(callStart)
				if !firstTokAt.IsZero() {
					dec = time.Since(firstTokAt)
				}
				if sec := dec.Seconds(); sec > 0 {
					speed = float64(u.CompletionTokens) / sec
				}
			}
			usageEvent = map[string]any{
				"prompt_tokens": u.PromptTokens, "cached_tokens": cached,
				"cache_reported": reported, "completion_tokens": u.CompletionTokens,
				"reasoning_tokens": u.ReasoningTokens, "speed_tok_s": speed,
				"call_ms": time.Since(callStart).Milliseconds(),
			}
			if !firstTokAt.IsZero() {
				if ttft := firstTokAt.Sub(callStart).Milliseconds(); ttft >= 0 {
					usageEvent["ttft_ms"] = ttft
				}
			}
			if l.onUsage != nil {
				l.onUsage(u.PromptTokens, cached, u.CompletionTokens, reported, speed)
			}
			if u.PromptTokens > 0 {
				l.calibrator().update(l.estBefore, u.PromptTokens)
			}
		}

		wasCompactionTurn := l.compactionTurn
		l.compactionTurn = false

		if wasCompactionTurn {
			summary := ExtractSummary(resp.Content, resp.ReasoningContent)
			est := l.countMessages
			overhead := RebuildOverhead(messages, summary, l.toolDefinitions, est)
			rebuildBudget := CompactKeepBudget(l.inputBudget(), overhead)
			newMsgs, ok := RebuildContextEst(messages, summary, CompactKeepMessages, rebuildBudget, est)
			if !ok {
				slog.Warn("compaction turn produced no usable summary; continuing uncompacted",
					"turn", turn, "dropped_tool_calls", len(resp.ToolCalls), "content_chars", len(resp.Content))
				messages = StripCompactRequests(messages)
				l.compactionBlocked = true
				l.persistTurn(turn, messages)
				continue
			}
			after := l.countMessages(newMsgs)
			if l.lastCompactionTokens > 0 && after >= l.lastCompactionTokens {
				slog.Warn("compaction made no progress, disabling further compaction for this run",
					"turn", turn, "after", after, "previous_after", l.lastCompactionTokens)
				l.compactionBlocked = true
			}
			l.lastCompactionTokens = after
			l.emitEvent(EventCompaction, turn, map[string]any{
				"kind": "auto", "phase": "done",
				"before_tokens": tokens, "after_tokens": after,
				"before_msgs": len(messages), "after_msgs": len(newMsgs),
				"summary_chars": len(summary),
			})
			messages = newMsgs
			if l.ctxState != nil {
				l.ctxState.SetUsage(after, l.inputBudget(), l.autoCompactLimit())
			}
			if l.onContextUsage != nil {
				l.onContextUsage(after)
			}
			l.persistTurn(turn, messages)
			continue
		}

		parsed := l.parseResponse(resp)

		if parsed.toolCallError {
			l.resetTruncation()
			messages = append(messages, provider.Message{Role: "assistant", Content: parsed.toolCallErrorContent, ReasoningContent: resp.ReasoningContent})
			l.emitAssistantMessage(turn, parsed.toolCallErrorContent, resp.ReasoningContent, nil, usageEvent)
			messages = append(messages, provider.Message{Role: "user", Content: "The above tool call format is incorrect. Use structured tool_calls with valid JSON."})
			l.persistTurn(turn, messages)
			continue
		}

		if resp.FinishReason == "length" && len(parsed.toolCalls) == 0 {
			l.truncPrefix += parsed.content
			messages = append(messages, provider.Message{Role: "assistant", Content: parsed.content, ReasoningContent: resp.ReasoningContent})
			l.emitAssistantMessage(turn, parsed.content, resp.ReasoningContent, nil, usageEvent)
			if l.truncContinues >= maxTruncContinues {
				slog.Warn("truncated-response continuation limit reached, returning best-effort content", "turn", turn)
				l.persistTurn(turn, messages)
				l.emitTaskEnd("completed", turn, "truncated-response continuation limit")
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
				l.emitTaskEnd("interrupted", turn, "cancelled before tool execution")
				return &LoopResult{Interrupted: true, Messages: messages, Turns: turn}, nil
			}
			for i := range parsed.toolCalls {
				if parsed.toolCalls[i].ID == "" {
					parsed.toolCalls[i].ID = "call_" + util.RandomHex(8)
				}
			}
			if parsed.content != "" && l.onAssistantReply != nil {
				l.onAssistantReply(parsed.content, resp.ReasoningContent)
			}
			messages = append(messages, provider.Message{
				Role:             "assistant",
				Content:          parsed.content,
				ReasoningContent: resp.ReasoningContent,
				ToolCalls:        parsed.toolCalls,
			})
			l.emitAssistantMessage(turn, parsed.content, resp.ReasoningContent, parsed.toolCalls, usageEvent)
			l.persistTurn(turn, messages)
			messages = l.handleToolCalls(ctx, messages, parsed.toolCalls)
			messages = l.nudgeIfStuck(messages, parsed.toolCalls)
			if ctx.Err() != nil {
				l.emitTaskEnd("interrupted", turn, "cancelled during tool execution")
				return &LoopResult{Interrupted: true, Messages: messages, Turns: turn}, nil
			}
			l.persistTurn(turn, messages)
			continue
		}

		if strings.TrimSpace(parsed.content) == "" && l.truncPrefix == "" {
			if l.emptyRetries >= maxEmptyRetries {
				slog.Warn("empty-response retry limit reached, failing task", "turn", turn)
				l.persistTurn(turn, messages)
				return &LoopResult{Success: false, Messages: messages, Turns: turn, Error: fmt.Sprintf("model returned an empty response %d times in a row; task ended without a result", l.emptyRetries)}, nil
			}
			l.emptyRetries++
			messages = append(messages, provider.Message{Role: "assistant", Content: parsed.content, ReasoningContent: resp.ReasoningContent})
			l.emitAssistantMessage(turn, parsed.content, resp.ReasoningContent, nil, usageEvent)
			messages = append(messages, provider.Message{Role: "user", Content: emptyRetryNotice})
			l.persistTurn(turn, messages)
			continue
		}

		finalContent := l.truncPrefix + parsed.content
		l.resetTruncation()
		messages = append(messages, provider.Message{Role: "assistant", Content: parsed.content, ReasoningContent: resp.ReasoningContent})
		l.emitAssistantMessage(turn, parsed.content, resp.ReasoningContent, nil, usageEvent)
		l.persistTurn(turn, messages)
		l.emitTaskEnd("completed", turn, "")
		return &LoopResult{Success: true, Content: finalContent, Messages: messages, Turns: turn}, nil
	}

	l.persistTurn(l.maxTurns, messages)
	l.emitTaskEnd("failed", l.maxTurns, fmt.Sprintf("max turns reached (%d)", l.maxTurns))
	return &LoopResult{Success: false, Messages: messages, Turns: l.maxTurns, Error: fmt.Sprintf("max turns reached (%d)", l.maxTurns)}, nil
}

type parsedResponse struct {
	content              string
	toolCalls            []provider.ToolCall
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

	for i := range toolCalls {
		args := strings.TrimSpace(toolCalls[i].Function.Arguments)
		if args == "" {
			toolCalls[i].Function.Arguments = "{}"
		} else if !json.Valid([]byte(args)) {
			msg := "Tool call arguments are not valid JSON. Retry with well-formed JSON arguments."
			if resp.FinishReason == "length" {
				msg = "The response hit the output token limit, so the tool call arguments were cut off mid-JSON. The payload was too large for a single call: split the work into smaller steps (e.g. write the file in several smaller chunks) and retry."
			}
			return parsedResponse{toolCallError: true, toolCallErrorContent: msg}
		}
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
	for _, tc := range toolCalls {
		var params map[string]interface{}
		if tc.Function.Arguments != "" {
			json.Unmarshal([]byte(tc.Function.Arguments), &params)
		}
		if l.onToolCallStart != nil {
			l.onToolCallStart(tc.Function.Name, params)
		}
		l.emitEvent(EventToolCall, l.curTurn, map[string]any{
			"call_id": tc.ID, "name": tc.Function.Name, "args": tc.Function.Arguments,
		})
	}

	results := l.executeToolsConcurrent(ctx, toolCalls)
	if l.argAgingOn {
		for i, tc := range toolCalls {
			text := results[i].Text
			if results[i].HasMultimodal() || strings.HasPrefix(text, "Error:") || text == "Interrupted" {
				continue
			}
			rep, ok := ageOutCallArgs(tc.Function.Name, tc.Function.Arguments)
			if !ok {
				continue
			}
			if ageCallArgs(messages, tc.ID, rep) {
				l.emitEvent(EventCompactionArgAge, l.curTurn, map[string]any{
					"call_id": tc.ID, "name": tc.Function.Name,
					"chars_before": len([]rune(tc.Function.Arguments)), "chars_after": len([]rune(rep)),
				})
			}
		}
	}
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

func (l *AgentLoop) resetTruncation() {
	l.truncPrefix = ""
	l.truncContinues = 0
	l.emptyRetries = 0
}

func batchSignature(calls []provider.ToolCall) string {
	sigs := make([]string, len(calls))
	for i, tc := range calls {
		sigs[i] = toolCallSignature(tc)
	}
	sort.Strings(sigs)
	return strings.Join(sigs, "\x01")
}

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
	l.altHistory = append(l.altHistory, sig)
	if len(l.altHistory) > altHistoryWindow {
		l.altHistory = l.altHistory[len(l.altHistory)-altHistoryWindow:]
	}

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

const altHistoryWindow = 6

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
			if ctx.Err() != nil {
				results[idx] = tools.NewTextResult("Interrupted")
				if l.onToolCallEnd != nil {
					var params map[string]interface{}
					if tc.Function.Arguments != "" {
						json.Unmarshal([]byte(tc.Function.Arguments), &params)
					}
					l.onToolCallEnd(tc.Function.Name, params, "Interrupted")
				}
				l.emitEvent(EventToolResult, l.curTurn, map[string]any{
					"call_id": tc.ID, "name": tc.Function.Name, "ok": false, "error": "interrupted",
				})
				return
			}
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
			toolStart := time.Now()
			results[idx] = l.executeSingleTool(ctx, tc)
			if l.onToolCallEnd != nil {
				var params map[string]interface{}
				if tc.Function.Arguments != "" {
					json.Unmarshal([]byte(tc.Function.Arguments), &params)
				}
				l.onToolCallEnd(tc.Function.Name, params, results[idx].Text)
			}
			text := results[idx].Text
			payload := map[string]any{
				"call_id": tc.ID, "name": tc.Function.Name,
				"ok":          !strings.HasPrefix(text, "Error:"),
				"duration_ms": time.Since(toolStart).Milliseconds(),
				"result":      text,
			}
			if !results[idx].HasMultimodal() && !pruneExemptCall(tc.Function.Name, toolCallPath(tc.Function.Arguments), l.spillDir) {
				if before := len([]rune(text)); before > defaultPrune.Threshold {
					spillPath := spillToolText(l.spillDir, l.spillID, tc.ID, text)
					if pruned, ok := pruneToolText(text, spillPath); ok {
						fullRow := map[string]any{}
						for k, v := range payload {
							fullRow[k] = v
						}
						l.emitEvent(EventToolResult, l.curTurn, fullRow)
						l.emitEvent(EventCompactionPrune, l.curTurn, map[string]any{
							"call_id": tc.ID, "name": tc.Function.Name,
							"chars_before": before, "chars_after": len([]rune(pruned)),
							"spill": spillPath,
						})
						payload["result"] = pruned
						payload["replaces"] = tc.ID
						text = pruned
						results[idx].Text = pruned
					}
				}
			}
			l.emitEvent(EventToolResult, l.curTurn, payload)
		}(i, tc)
	}
	wg.Wait()
	return results
}

func (l *AgentLoop) executeSingleTool(ctx context.Context, tc provider.ToolCall) (result *tools.ToolResult) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("tool execution panicked, contained", "name", tc.Function.Name, "panic", r,
				"args", tc.Function.Arguments, "stack", string(debug.Stack()))
			result = tools.NewTextResult(fmt.Sprintf("Error: tool %q crashed (panic: %v). The task continues; analyze the cause and try a different approach.", tc.Function.Name, r))
		}
	}()
	var params map[string]interface{}
	var argsErr error
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
			argsErr = err
			slog.Warn("invalid tool call arguments", "name", tc.Function.Name, "error", err, "params", tc.Function.Arguments)
		}
	}
	if argsErr != nil {
		return tools.NewTextResult(fmt.Sprintf("Error: Invalid arguments for tool %q: %s", tc.Function.Name, argsErr))
	}
	if tool := l.tools.Get(tc.Function.Name); tool != nil {
		if params == nil {
			params = map[string]interface{}{}
		}
		tools.NormalizeAndCoerceArgs(tool.Parameters(), params)
		if verr := tools.ValidateToolArgs(tc.Function.Name, tool.Parameters(), params); verr != nil {
			return tools.NewTextResult("Error: " + verr.Error())
		}
	}
	defer func() { l.noteToolOutcome(tc.Function.Name, params, result) }()
	if l.gate != nil {
		if allow, denyMsg := l.gate(ctx, tc.Function.Name, params); !allow {
			return tools.NewTextResult(denyMsg)
		}
	}
	if denyMsg := l.checkWriteBeforeRead(tc.Function.Name, params); denyMsg != "" {
		return tools.NewTextResult(denyMsg)
	}
	l.observePolicy(tc.ID, tc.Function.Name, params)
	if at, ok := l.tools.Get(tc.Function.Name).(tools.AsyncTool); ok && l.jobOwner != "" {
		var sink func(string)
		if l.onToolOutput != nil {
			sink = func(chunk string) { l.onToolOutput(tc.Function.Name, chunk) }
		}
		if call, aerr := at.StartAsync(ctx, l.jobOwner, params, sink); aerr == nil && call != nil && call.Job != nil {
			if call.Settled {
				return tools.NewTextResult(call.Job.Output(true))
			}
			return tools.NewTextResult(call.Note)
		}
	}
	var err error
	if st, ok := l.tools.Get(tc.Function.Name).(tools.StreamTool); ok && l.onToolOutput != nil {
		result, err = st.ExecuteStreamed(ctx, params, func(chunk string) {
			l.onToolOutput(tc.Function.Name, chunk)
		})
	} else {
		result, err = l.tools.Execute(ctx, tc.Function.Name, params)
	}
	if err != nil {
		return tools.NewTextResult(fmt.Sprintf("Error: %s", err))
	}
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

func isContextOverflowText(text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range []string{"context_length_exceeded", "maximum context length", "context length", "context window", "exceeds the context"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func (l *AgentLoop) shrinkForOverflow(messages []provider.Message) bool {
	est := defaultTokenEstimator
	budget := inputBudgetFor(l.client)
	if budget <= 0 {
		return false
	}
	budget = budget * 3 / 4
	before := est(messages)
	keep := CompactKeepMessages
	start := 0
	if len(messages) > 0 && messages[0].Role == "system" {
		start = 1
	}
	end := len(messages) - keep
	if end > len(messages) {
		end = len(messages)
	}
	for i := end - 1; i >= start && est(messages) > budget; i-- {
		m := &messages[i]
		switch m.Role {
		case "tool":
			if pruned, ok := pruneToolText(m.GetText(), ""); ok {
				m.SetTextContent(pruned)
			}
		case "assistant":
			for j := range m.ToolCalls {
				tc := &m.ToolCalls[j]
				if rep, ok := ageOutCallArgs(tc.Function.Name, tc.Function.Arguments); ok {
					tc.Function.Arguments = rep
				}
			}
		}
	}
	for i := end - 1; i >= start && est(messages) > budget; i-- {
		m := &messages[i]
		if m.Role == "tool" && est([]provider.Message{*m}) > 512 {
			t := truncateToBudget(*m, 512, est)
			m.SetTextContent(t.GetText())
		}
	}
	for i := end - 1; i >= start && est(messages) > budget; i-- {
		m := &messages[i]
		switch m.Role {
		case "user":
			if est([]provider.Message{*m}) > 1024 {
				t := truncateToBudget(*m, 1024, est)
				m.SetTextContent(t.GetText())
			}
		case "assistant":
			if len(m.ToolCalls) == 0 && est([]provider.Message{*m}) > 1024 {
				t := truncateToBudget(*m, 1024, est)
				m.SetTextContent(t.GetText())
			}
		}
	}
	shrunk := est(messages) < before
	if shrunk {
		slog.Warn("context overflow: shrunk history in place", "before", before, "after", est(messages), "budget", budget)
	}
	return shrunk
}

func (l *AgentLoop) chatWithRetry(ctx context.Context, turn int, messages []provider.Message, schemas []provider.ToolSchema, onChunk func(provider.StreamChunk)) (*provider.ChatResponse, error) {
	const maxAttempts = 3
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
		wait := time.Duration(2*attempt-1) * time.Second
		slog.Warn("LLM call failed, retrying", "turn", turn, "attempt", attempt, "wait_s", int(wait.Seconds()), "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, lastErr
}

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
	f.flush()
}

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

func LastAssistantReasoning(messages []provider.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].ReasoningContent
		}
	}
	return ""
}
