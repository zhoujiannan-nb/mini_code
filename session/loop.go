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
	compactionOn    bool // false = skip the automatic compaction check entirely
	// argAgingOn shrinks successful big write/edit arguments in the history to
	// a summary after each batch (dsh-style per-turn token saving). Initialized
	// from MC_ARG_AGE=1 — off by default, see handleToolCalls for the round-4
	// evidence that grounded qwen loses paths when the payloads disappear.
	argAgingOn bool
	// compactionTurn is set from the moment the compaction instruction is
	// appended until the summary comes back: the next model call IS the
	// summarization turn.
	compactionTurn bool
	// compactionBlocked stops retrying compaction for the rest of this run
	// after the model failed to produce a summary (avoids a compaction loop).
	compactionBlocked bool
	// lastCompactionTokens is the calibrated size right after the last
	// compaction. Compaction only re-arms once the context has grown by a
	// meaningful amount since — otherwise a context that is still over the
	// threshold after a compaction (huge retained tool output, big system
	// prompt) would trigger a compaction turn on EVERY turn, which costs a
	// full re-prefill each time and never converges.
	lastCompactionTokens int
	ctxState             *tools.ContextState // shared context view for context tools (nil = disabled)
	contextRemindersOn   bool                // inject 80%/85% usage notices (90% forces compaction) (nil ctxState-safe)
	onToolCallStart      func(name string, params map[string]interface{})
	onToolCallEnd        func(name string, params map[string]interface{}, result string)
	// onToolOutput receives a streaming tool's output while it runs (UI feed
	// only: nothing that arrives here reaches the model or the history).
	onToolOutput func(name string, chunk string)
	// onToolArgs receives the arguments a tool call is STILL GENERATING (accumulated
	// so far). A 20 KB write_file payload takes minutes to type out and tool_start
	// only fires once the whole reply has arrived, so without this the UI shows
	// nothing while the model is doing its longest job. Display-only.
	onToolArgs       func(name string, argsSoFar string)
	onAssistantReply func(content string, reasoning string)
	onTurn           func(turn int, messages []provider.Message)
	onContextUsage   func(tokens int)
	onUsage          func(promptTokens, cachedTokens, completionTokens int, cacheReported bool, speed float64) // real per-call usage (cache-hit + output-speed visibility)
	// eventSink appends conversation facts to the session event ledger (see
	// events.go). nil = no ledger (tests, or a loop built without a store).
	eventSink func(typ string, turn int, payload map[string]any)
	// spillDir/spillID locate the full-text files behind pruned tool results
	// (prune.go). Empty spillDir = no spilling: pruned markers omit the path
	// and the ledger's full row stays the only escape hatch.
	spillDir string
	spillID  string
	// jobOwner is the session id background work is registered to: a command that
	// outlives its wait belongs to the conversation that started it, and only
	// that conversation may read it or stop it. Empty means this loop has no job
	// runtime, which leaves every async-capable tool on its synchronous path.
	jobOwner string
	// obs is the session's file-observation memory (policy.go). Nil = inert.
	obs *tools.Observation
	// curTurn tags events raised outside the turn-loop body (tool callbacks,
	// the continue guard) with the turn they happened in.
	curTurn int
	gate    ToolGate // runtime permission gate (nil = no gate)

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

	// Edit-churn guard: the round-5 A/B (persona clauses stripped, dsh-style)
	// spent 53 incremental edit_file calls on one file — the "write a
	// complete file in one write call" prompt clause was load-bearing for
	// qwen, so it moves from prompt text into harness code. editTally counts
	// successful edit_file calls per path within the current task; crossing
	// editNudgeAt injects a corrective user message (see tallyFileEdits),
	// and a full write_file to the path resets the count because that is
	// exactly the behavior the nudge asks for.
	editTally  map[string]int
	editNudged map[string]int

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

	// userImages carries image data URLs (data:image/...;base64,...) that
	// are attached to the initial user message as multimodal content parts,
	// so the model sees them on the very first turn. Set via SetUserImages
	// before Run; the loop instance is created per task, so the state is
	// never shared between tasks.
	userImages []string

	// injections returns user messages queued while this task was running
	// (e.g. group-chat messages delivered to a running session). The loop
	// drains it at the start of each turn and appends them as plain user
	// messages, so they reach the model on the next turn without starting a
	// new prompt. Set via SetInjections before Run.
	injections func() []string

	// workerNotice renders the worker notice (worker.go) at the start of a task:
	// the join reminder when the session has no worker, or the latest 公约 when the
	// version this conversation carries is behind. Appended to the tail, never
	// inserted into the middle. Set via SetWorkerNotice before Run.
	workerNotice func([]provider.Message) provider.Message

	// calib overrides the shared token calibrator (token_calib.go) when set
	// (tests); nil = look up the calibrator for this loop's model + toolset.
	calib *tokenCalibrator
	// estBefore is the raw local estimate of the context just sent to the
	// model; the provider's usage report for that call folds it into the
	// calibration.
	estBefore int
}

// SetUserImages attaches image data URLs to the initial user message (the
// model sees them on the first turn). Must be called before Run.
func (l *AgentLoop) SetUserImages(images []string) { l.userImages = images }

// SetInjections installs the mid-task user-message drain. Messages queued
// while the task runs are appended to the conversation at the start of the
// next turn (and persisted with it). Must be called before Run.
func (l *AgentLoop) SetInjections(fn func() []string) { l.injections = fn }

// SetWorkerNotice installs the worker-notice provider. The loop renders it once,
// at the start of the task, and appends it when it has something to say (see
// applyWorkerNotice). Must be called before Run.
func (l *AgentLoop) SetWorkerNotice(fn func([]provider.Message) provider.Message) {
	l.workerNotice = fn
}

// SetTokenCalibrator overrides the shared token calibrator (mainly for
// tests). Must be called before Run; nil restores the shared per-model
// calibrator.
func (l *AgentLoop) SetTokenCalibrator(c *tokenCalibrator) { l.calib = c }

func (l *AgentLoop) calibrator() *tokenCalibrator {
	if l.calib != nil {
		return l.calib
	}
	return calibratorFor(l.client.ModelName(), l.toolDefinitions)
}

// calibratedCount estimates the local token count of messages and maps it to
// the provider's token scale via the calibrator. It also remembers the raw
// estimate (estBefore) so the usage report of the LLM call this count
// precedes can fold it into the fit.
func (l *AgentLoop) calibratedCount(messages []provider.Message) int {
	l.estBefore = util.CountMessagesTokens(messages)
	return l.calibrator().apply(l.estBefore)
}

// countMessages is calibratedCount without the estBefore side effect: budget
// math outside the request path must not pollute the sample pair that the next
// usage report is folded into.
func (l *AgentLoop) countMessages(messages []provider.Message) int {
	return l.calibrator().apply(util.CountMessagesTokens(messages))
}

// SetGate installs the runtime permission gate consulted before every tool
// call (role permissions + cautious-mode approval). Must be called before
// Run; a nil gate disables checking.
func (l *AgentLoop) SetGate(g ToolGate) { l.gate = g }

// SetOnToolOutput wires the live output feed of tools that can stream (see
// tools.StreamTool). It is display-only: the model still sees just the final
// tool result, so wiring it changes nothing about what the agent does.
func (l *AgentLoop) SetOnToolOutput(fn func(name, chunk string)) { l.onToolOutput = fn }

// SetOnToolArgs wires the live feed of arguments the model is still generating
// (see argsStreamer). Like the output feed it is display-only: the model and the
// history never see it.
func (l *AgentLoop) SetOnToolArgs(fn func(name, argsSoFar string)) { l.onToolArgs = fn }

// SetContextState installs the shared context state the context tools read
// (context_usage / compact_context). The loop publishes the current usage
// each turn and consumes any pending active-compaction request. Must be
// called before Run; a nil state disables the context tools' live data.
func (l *AgentLoop) SetContextState(state *tools.ContextState) { l.ctxState = state }

// SetContextRemindersOn toggles the context-usage notices
// (injected at 80% / 85% of the usable input budget). Must be called before Run.
func (l *AgentLoop) SetContextRemindersOn(on bool) { l.contextRemindersOn = on }

// SetOnContextUsage installs the per-turn context-usage callback fired after
// the compaction check. The count reflects the context about to be sent to
// the model; the web UI uses it to keep the token bar fresh during long tasks.
func (l *AgentLoop) SetOnContextUsage(fn func(tokens int)) { l.onContextUsage = fn }

// SetOnUsage installs the callback fired after each successful LLM call with
// the provider-reported usage (prompt / cached / completion tokens) and whether
// the provider reported cache-hit information at all. It is the source of the
// cache-hit indicator: cachedTokens is the number of prompt tokens served from
// the provider's KV prefix cache, and cacheReported distinguishes "0% hits"
// from "this deployment does not report cache hits". speed is the average
// output speed of this call in tokens/second (completion tokens over the
// call's wall time; 0 when the provider reported no completion count).
func (l *AgentLoop) SetOnUsage(fn func(promptTokens, cachedTokens, completionTokens int, cacheReported bool, speed float64)) {
	l.onUsage = fn
}

// SetEventSink installs the session event ledger sink. Every conversation
// fact the loop produces (turns, messages, tool calls and results,
// compactions, guard steers, the task outcome) is appended there; agent.log
// keeps only process diagnostics. Must be called before Run.
func (l *AgentLoop) SetEventSink(fn func(typ string, turn int, payload map[string]any)) {
	l.eventSink = fn
}

// SetSpillDir wires where oversized tool results spill their full text
// (folder + owning session id used to name the file). Must be called before Run.
func (l *AgentLoop) SetSpillDir(dir, sessionID string) {
	l.spillDir = dir
	l.spillID = sessionID
}

// SetJobOwner names the session that background work belongs to. Until it is
// called, tools that could hand their work to the job runtime run synchronously,
// which is the correct fallback for a loop built without a session (tests, the
// one-shot CLI).
func (l *AgentLoop) SetJobOwner(sessionID string) { l.jobOwner = sessionID }

// emitEvent raises one ledger event. Best-effort by contract: the sink
// implementation owns its error handling; the loop never blocks on the ledger.
func (l *AgentLoop) emitEvent(typ string, turn int, payload map[string]any) {
	if l.eventSink != nil {
		l.eventSink(typ, turn, payload)
	}
}

// emitTaskEnd records the outcome of the whole task. Every Run return point
// goes through here, so the ledger always says exactly how a run ended —
// the live UI's resync depends on it.
func (l *AgentLoop) emitTaskEnd(status string, turn int, errMsg string) {
	p := map[string]any{"status": status, "turns": turn}
	if errMsg != "" {
		p["error"] = errMsg
	}
	l.emitEvent(EventTaskEnd, turn, p)
}

// emitAssistantMessage records a committed assistant message. Deltas are not
// events; this is the one truth about what the model said.
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

// buildUserMessage builds the initial user message: plain text, or a
// multimodal message (text part + image parts) when images are attached.
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

// imageURLsOf extracts the image data URLs of a message's content parts
// (nil for a plain text message).
func imageURLsOf(m provider.Message) []string {
	var urls []string
	for _, p := range m.ContentParts {
		if p.Type == "image_url" && p.ImageURL != nil {
			urls = append(urls, p.ImageURL.URL)
		}
	}
	return urls
}

// orphanToolCallNote is the synthetic result written for a tool call that never
// ran. The loop persists the assistant message carrying tool_calls BEFORE
// executing them (so a web refresh mid-tool keeps the reply); if the process
// dies in that window, the stored history ends on a call with no answer, and
// the next task would replay it as-is.
//
// That request is malformed: the OpenAI tool-calling contract requires exactly
// one tool message per tool_call_id, and vLLM/SGLang/DeepSeek answer 400, which
// leaves the session permanently unable to send anything. Repairing only ever
// inserts after the results that already follow the offending reply - which for
// a crashed run is the tail of the history - so the cached prefix is untouched.
const orphanToolCallNote = "Tool not executed: the task was interrupted before this call ran."

// repairOrphanToolCalls appends a synthetic tool result for every tool call
// whose result is missing. It only ever inserts; existing messages are copied
// unchanged, so the request stays a byte-for-byte extension of the cached
// prefix.
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
		// Copy the results that already follow this reply untouched, and append
		// only what is missing after them: existing messages keep their exact
		// position, so nothing that was already cached shifts.
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

// userMessageMatches reports whether two user messages carry the same text
// and the same images — used for the "history already ends with this user
// message" dedup (interrupt recovery must not re-append the message, but a
// re-send with images must still get its images).
// historyEndsWithUserMsg reports whether the persisted history ends with
// userMsg, so interrupt recovery still dedups the goal message.
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
		argAgingOn:       os.Getenv("MC_ARG_AGE") == "1",
		onToolCallStart:  onToolCallStart,
		onToolCallEnd:    onToolCallEnd,
		onAssistantReply: onAssistantReply,
		onTurn:           onTurn,
		pathLocks:        make(map[string]*sync.Mutex),
		nudged:           make(map[string]int),
		altNudged:        make(map[string]int),
		editTally:        make(map[string]int),
		editNudged:       make(map[string]int),
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

// Context-usage notices: two stages, measured against the SAME reference as the
// automatic compaction — the model's usable input budget (InputBudgetFraction).
// 80% nudges, 85% says the loop will do it next turn if you do not.
const (
	usageNoticeStage1    = noticeInputFraction1 // fraction of the usable input budget
	usageNoticeStage2    = noticeInputFraction2 // fraction of the usable input budget
	usageNoticeStage1Pct = 80                   // marker / message label of stage 1
	usageNoticeStage2Pct = 85                   // marker / message label of stage 2
)

// usageNoticeRole is the role of the injected notice. It is a "user" message so
// every chat template honors it (a trailing "system" notice is ignored by some
// templates that only read the first system message). The marker-based dedup is
// role-agnostic, so the role can be changed freely.
const usageNoticeRole = "user"

// usageNoticeMarkerPrefix identifies any usage notice in a message. Exported to
// notices.go so the transcript can label these rows as runtime-injected.
const usageNoticeMarkerPrefix = "__CTX_USAGE_"

// usageNoticeMarker returns the stable marker embedded in a usage notice of the
// given stage. It is used to detect already-announced stages by scanning the
// context, so no external state is needed and a compaction that summarizes the
// notice naturally re-arms the reminder if usage is still high.
func usageNoticeMarker(pct int) string {
	return fmt.Sprintf("%s%d__", usageNoticeMarkerPrefix, pct)
}

// usageNotice builds the notice for one of the two stages. pct is the share of the
// usable input budget the context has reached; ref is that budget in tokens — the
// same denominator every threshold uses — and compactionAt is the token count where
// the loop compacts on its own. Quoting both means the model never has to guess what
// a bare percentage meant, nor how much room is left before the loop acts.
func usageNotice(pct, tokens, ref, compactionAt int) provider.Message {
	var text string
	if pct >= usageNoticeStage2Pct {
		text = fmt.Sprintf("Context usage is now at %d%% of the usable input budget (%d / %d tokens). The session compacts automatically at %d tokens, so call compact_context NOW to summarize older work (keep only key facts, file paths, decisions, and current progress). If multiple context-usage notices appear, the most recent one is the current state.", pct, tokens, ref, compactionAt)
	} else {
		text = fmt.Sprintf("Context usage is now at %d%% of the usable input budget (%d / %d tokens); the session compacts automatically at %d tokens. Consider calling compact_context to summarize older work. If multiple context-usage notices appear, the most recent one is the current state.", pct, tokens, ref, compactionAt)
	}
	return provider.Message{Role: usageNoticeRole, Content: text + " " + usageNoticeMarker(pct)}
}

// scanUsageNoticeMarkers reports which usage notices (80/85%) have already been
// announced in the context (by their markers).
func scanUsageNoticeMarkers(messages []provider.Message) (hasStage1, hasStage2 bool) {
	for _, m := range messages {
		t := m.GetText()
		if !hasStage1 && strings.Contains(t, usageNoticeMarker(usageNoticeStage1Pct)) {
			hasStage1 = true
		}
		if !hasStage2 && strings.Contains(t, usageNoticeMarker(usageNoticeStage2Pct)) {
			hasStage2 = true
		}
	}
	return hasStage1, hasStage2
}

// maybeInjectUsageReminder appends a context-usage notice when the context crosses
// 80% or 85% of the model's usable input budget and that stage has not been
// announced yet in the current context. Both the reminders and the automatic
// compaction divide by the same budget, so the numbers the model is shown are the
// ones the loop actually acts on. With an unknown window there is no budget to
// measure against, so no notice is injected.
//
// It is cache-friendly: the notice is only ever appended to the end (extending
// the prefix), never inserted or removed from the middle, so the KV prefix cache
// is preserved. tokens is the calibrated (provider-scale) estimate the caller
// already computed.
func (l *AgentLoop) maybeInjectUsageReminder(messages []provider.Message, tokens int) []provider.Message {
	budget := l.client.GetMaxInput()
	limit1 := InputBudgetFraction(budget, usageNoticeStage1)
	limit2 := InputBudgetFraction(budget, usageNoticeStage2)
	if limit2 <= 0 {
		return messages
	}
	// Current stage: 2 (>= second limit), 1 (>= first limit), 0 (below both).
	current, label := 0, 0
	switch {
	case tokens >= limit2:
		current, label = 2, usageNoticeStage2Pct
	case tokens >= limit1:
		current, label = 1, usageNoticeStage1Pct
	}
	if current == 0 {
		return messages
	}
	// Announced stage: the highest stage whose marker is already present.
	hasStage1, hasStage2 := scanUsageNoticeMarkers(messages)
	announced := 0
	if hasStage2 {
		announced = 2
	} else if hasStage1 {
		announced = 1
	}
	if current <= announced {
		return messages
	}
	compactionAt := l.autoCompactLimit()
	l.emitEvent(EventUserMessage, l.curTurn, map[string]any{
		"source": "context_usage", "stage": label, "tokens": tokens,
		"input_budget": budget, "compaction_at": compactionAt,
	})
	return append(messages, usageNotice(label, tokens, budget, compactionAt))
}

// autoCompactLimit is the token count at which the loop compacts on its own: 90% of
// the model's usable input budget, the same denominator the notices quote.
func (l *AgentLoop) autoCompactLimit() int {
	return InputBudgetFraction(l.client.GetMaxInput(), compactInputFraction)
}

// applyWorkerNotice appends the worker notice when there is one: the join reminder,
// or a 公约 newer than the version this conversation already carries. It runs once
// per task, so a task is reminded at most once — if the session does not resolve
// the worker, the next task asks again.
//
// Append-only on purpose: the request stays an exact extension of the previous one
// and keeps hitting the provider's prefix cache. A compaction drops notices (they
// are derived data), so the next task simply appends a fresh one.
func (l *AgentLoop) applyWorkerNotice(messages []provider.Message) []provider.Message {
	if l.workerNotice == nil {
		return messages
	}
	want := l.workerNotice(messages)
	if strings.TrimSpace(want.GetText()) == "" {
		return messages // nothing to say (sub-agent, built-in bucket, already current)
	}
	l.emitEvent(EventUserMessage, 0, map[string]any{"text": want.GetText(), "source": "worker_notice"})
	return append(messages, want)
}

func (l *AgentLoop) Run(ctx context.Context, systemPrompt, userMessage string, history []provider.Message) (*LoopResult, error) {
	// A compaction instruction as the last stored message means the previous
	// run was cut off in the middle of a summarization turn. Resume that turn
	// instead of answering a new message on top of a stale instruction.
	resumeCompaction := len(history) > 0 && IsCompactRequest(history[len(history)-1])

	userMsg := l.buildUserMessage(userMessage)
	var messages []provider.Message
	if len(history) > 0 {
		messages = append(messages, history...)
		if len(history) == 0 || history[0].Role != "system" {
			messages = append([]provider.Message{{Role: "system", Content: systemPrompt}}, messages...)
		}
		// Only append user message if history doesn't already end with it
		// (interrupt recovery must not re-append the same message). While
		// resuming a compaction turn nothing is appended: the instruction must
		// stay the last message so the request keeps extending the cached
		// prefix.
		if !resumeCompaction && (len(messages) == 0 || !historyEndsWithUserMsg(messages, userMsg)) {
			messages = append(messages, userMsg)
		}
	} else {
		messages = []provider.Message{
			{Role: "system", Content: systemPrompt},
			userMsg,
		}
	}

	// A run that died between persisting a tool call and appending its result
	// leaves the restored history unanswerable; close those calls before the
	// first request of this task (see repairOrphanToolCalls).
	messages = repairOrphanToolCalls(messages)

	if resumeCompaction {
		l.compactionTurn = true
		slog.Info("resuming in-flight compaction turn", "messages", len(messages))
	} else {
		// Anywhere else a stale instruction would only confuse the model.
		messages = StripCompactRequests(messages)
		// Worker notice: the join reminder or a newer 公约, once per task — appended
		// as a plain user message, never as a rewritten prefix.
		messages = l.applyWorkerNotice(messages)
	}

	for turn := 1; turn <= l.maxTurns; turn++ {
		l.curTurn = turn
		l.emitEvent(EventTurnStart, turn, nil)

		// Check for interrupt before each turn
		if ctx.Err() != nil {
			l.emitTaskEnd("interrupted", turn-1, "cancelled before turn")
			return &LoopResult{Interrupted: true, Messages: messages, Turns: turn - 1}, nil
		}

		// Mid-task user injections: messages queued while this task is
		// running (e.g. group-chat messages for a busy member) reach the
		// model on this turn. Appended as plain user messages at the tail
		// and persisted by persistTurn below.
		if l.injections != nil {
			for _, t := range l.injections() {
				if strings.TrimSpace(t) == "" {
					continue
				}
				messages = append(messages, provider.Message{Role: "user", Content: t})
				l.emitEvent(EventUserMessage, turn, map[string]any{"text": t, "source": "queued"})
			}
		}

		// ---- Compaction: the next model call becomes the summarization turn.
		//
		// The conversation is no longer re-serialized and shipped to a separate
		// summarizer call. Instead one user message (the compaction
		// instruction) is appended to the live context, so the request stays an
		// exact extension of the previous one — everything before the new
		// message is byte-identical and keeps hitting the provider's KV prefix
		// cache — and the model itself answers with the summary. The context is
		// then restarted from [system][summary][verbatim tail] (RebuildContext).
		// Estimate the context about to be sent: first locally (raw cl100k
		// proxy), then calibrated onto the provider's own token scale (see
		// tokenCalibrator). The calibrated count drives the auto-compaction
		// threshold and the usage notices; the raw estimate is remembered so
		// the provider's usage report for this very call can refine the
		// calibration afterwards.
		tokens := l.calibratedCount(messages)
		if l.compactionTurn {
			// The instruction is already the last message; this call answers it.
		} else if !l.compactionBlocked {
			need, instruction := false, ""
			// Model-initiated (compact_context): always honored, regardless of
			// the automatic-compaction toggle, with the model's own instruction
			// as the compaction prompt.
			if l.ctxState != nil {
				if pending, instr := l.ctxState.TakePendingCompaction(); pending {
					need, instruction = true, instr
				}
			}
			// Passive (automatic) compaction uses the fixed prompt. The second
			// condition is the re-arm guard: only compact again once the
			// context has grown by at least recompactGrowTokens since the last
			// compaction (see lastCompactionTokens).
			if !need && l.compactionOn {
				maxInput := l.client.GetMaxInput()
				// 90% of the usable input budget (autoCompactLimit), not a fraction of
				// the raw context window: how much output was reserved must not move
				// the point where the session compacts.
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
				// Persist before the call: a crash mid-turn leaves the
				// instruction as the last message and Run() resumes it.
				l.persistTurn(turn, messages)
				tokens = l.calibratedCount(messages)
			}
		}
		if ctx.Err() != nil {
			l.emitTaskEnd("interrupted", turn, "cancelled before compaction turn")
			return &LoopResult{Interrupted: true, Messages: messages, Turns: turn}, nil
		}
		// Publish the current context usage (reflects the context about to be
		// sent to the model) to the context tools and to the UI token bar.
		if l.ctxState != nil || l.onContextUsage != nil {
			if l.ctxState != nil {
				l.ctxState.SetUsage(tokens, l.client.GetMaxInput(), l.autoCompactLimit())
			}
			if l.onContextUsage != nil {
				l.onContextUsage(tokens)
			}
		}
		// Context usage notices: when the context crosses 80% / 85% of the model's
		// usable input budget (compaction itself is forced at 90%), append a notice to
		// the tail. Cache-friendly: it only extends the prefix, never edits the
		// middle, so the KV prefix cache is preserved.
		// Skipped on a compaction turn: telling the model to call
		// compact_context while it is writing the summary is counter-productive.
		if l.contextRemindersOn && !l.compactionTurn {
			messages = l.maybeInjectUsageReminder(messages, tokens)
		}
		l.persistTurn(turn, messages)

		// call LLM (streaming: chunks are forwarded to the UI via throttled callback)
		var flusher *streamFlusher
		if l.onAssistantReply != nil {
			flusher = newStreamFlusher(l.onAssistantReply)
			flusher.start()
		}
		// Decode-phase clock: first non-empty token → stream end (TTFT
		// excluded, dsh-style). Content/ReasoningContent are accumulated
		// values, so a retry restarting from empty shows up as a length
		// shrink and resets the clock to the new attempt's first token.
		var firstTokAt time.Time
		var lastLen int
		callStart := time.Now()
		// Arguments the model is still typing get forwarded too, so a long
		// write_file is visible while it is being generated (nil-safe).
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
			flusher.stop() // emit any tail and release the ticker before leaving this turn
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
			l.emitTaskEnd("failed", turn, resp.Error)
			return &LoopResult{Success: false, Messages: messages, Turns: turn, Error: resp.Error}, nil
		}

		// Report the provider's real usage (cache-hit visibility). cachedTokens
		// is the prompt tokens served from the KV prefix cache; 0 means the
		// provider did not report cache hits (e.g. SGLang without
		// --enable-cache-report), which we surface as "unknown", not a miss.
		// usageEvent carries this call's provider-reported usage into the
		// assistant/message ledger events below (nil when the provider
		// reported nothing).
		var usageEvent map[string]any
		if u := resp.Usage; u != nil {
			cached := u.CachedTokens()
			reported := u.CacheReported()
			// Output speed of the DECODE phase (tokens/s): completion tokens
			// over first-non-empty-token → stream end, TTFT excluded (dsh
			// style). Falls back to the full call span when the provider
			// never delivered a content chunk (e.g. non-streaming); 0 when
			// no completion count is reported (the UI keeps its live
			// estimate or the previous value).
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
			}
			if l.onUsage != nil {
				l.onUsage(u.PromptTokens, cached, u.CompletionTokens, reported, speed)
			}
			// Fold this call's (estimate, reported prompt_tokens) pair into
			// the calibration so the next turn's threshold decisions are
			// judged on the provider's token scale.
			if u.PromptTokens > 0 {
				l.calibrator().update(l.estBefore, u.PromptTokens)
			}
		}

		// Was this call the summarization turn? Clear the flag here so every
		// branch below (and any later turn) starts from a normal state.
		wasCompactionTurn := l.compactionTurn
		l.compactionTurn = false

		// Compaction turn: this reply is the summary, not an answer to the
		// user. Restart the context from it and keep the task running. Tool
		// calls on this turn are discarded — the instruction forbids them, and
		// executing tools from a summary request would derail the task.
		if wasCompactionTurn {
			summary := ExtractSummary(resp.Content, resp.ReasoningContent)
			// Budget arithmetic runs on the calibrated count — the same number
			// the next turn's threshold decision uses — so the rebuilt context
			// really does land below the threshold.
			est := l.countMessages
			overhead := RebuildOverhead(messages, summary, l.toolDefinitions, est)
			rebuildBudget := CompactKeepBudget(l.client.GetMaxInput(), overhead)
			newMsgs, ok := RebuildContextEst(messages, summary, CompactKeepMessages, rebuildBudget, est)
			if !ok {
				slog.Warn("compaction turn produced no usable summary; continuing uncompacted",
					"turn", turn, "dropped_tool_calls", len(resp.ToolCalls), "content_chars", len(resp.Content))
				messages = StripCompactRequests(messages)
				// Do not retry for the rest of this run: a model that will not
				// summarize should not burn the turn budget trying.
				l.compactionBlocked = true
				l.persistTurn(turn, messages)
				continue
			}
			after := l.countMessages(newMsgs)
			// No-progress guard: if this compaction did not leave the context
			// smaller than the previous one, stop compacting for this run.
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
				l.ctxState.SetUsage(after, l.client.GetMaxInput(), l.autoCompactLimit())
			}
			if l.onContextUsage != nil {
				l.onContextUsage(after)
			}
			l.persistTurn(turn, messages)
			continue
		}

		// parse response
		parsed := l.parseResponse(resp)

		if parsed.toolCallError {
			l.resetTruncation()
			messages = append(messages, provider.Message{Role: "assistant", Content: parsed.toolCallErrorContent, ReasoningContent: resp.ReasoningContent})
			l.emitAssistantMessage(turn, parsed.toolCallErrorContent, resp.ReasoningContent, nil, usageEvent)
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
			l.emitAssistantMessage(turn, parsed.content, resp.ReasoningContent, parsed.toolCalls, usageEvent)
			// Persist the reply BEFORE tool execution: a long tool call used
			// to leave this message unpersisted, so a web refresh mid-tool
			// lost it from the view (history only re-renders the store).
			l.persistTurn(turn, messages)
			messages = l.handleToolCalls(ctx, messages, parsed.toolCalls)
			// Stall detection: if the model keeps repeating the exact same
			// tool call, inject a corrective nudge so it changes strategy
			// instead of burning the turn budget on the same action.
			messages = l.nudgeIfStuck(messages, parsed.toolCalls)
			if ctx.Err() != nil {
				l.emitTaskEnd("interrupted", turn, "cancelled during tool execution")
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
			l.emitAssistantMessage(turn, parsed.content, resp.ReasoningContent, nil, usageEvent)
			messages = append(messages, provider.Message{Role: "user", Content: emptyRetryNotice})
			l.persistTurn(turn, messages)
			continue
		}

		// pure text response -> done. Fold in the earlier truncated chunks
		// of this same answer (if any) so the caller receives the complete
		// text, not just the final tail.
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

	// A text response with no tool calls ends the task (the pure-text
	// path in Run). There used to be a shouldTerminate flag keyed on
	// finish_reason=="stop" here; nothing ever read it, so it was dead
	// weight: narration-only stops are now handled by the continue guard
	// (loop_continue.go) right before the task closes.
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
	// Notify consumers that tool execution is starting, and record the call
	// itself in the ledger (the result arrives later, keyed by call_id).
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
	// dsh-style argument aging: a successful big write/edit no longer needs its
	// payload in the history — the file on disk is the copy of record. Shrink
	// the assistant message's arguments to a summary so every later turn stops
	// re-sending them (the loop's per-turn token saving for creative tasks).
	//
	// Off by default (MC_ARG_AGE=1 to enable): the round-4 real A/B runs showed
	// qwen losing path grounding once the payloads vanished from history — it
	// hallucinated "_work_20260511_..." directories and ballooned to 50-70 turn
	// failures on exactly the tasks the mechanism was meant to speed up. The
	// mechanism itself works (ledger keeps full args, events fire), so keep it
	// switchable for future models that hold grounding better.
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
	messages = l.tallyFileEdits(toolCalls, results, messages)
	return messages
}

// editNudgeAt is the number of separate successful edits to one file that
// triggers the first churn nudge; every further editNudgeAt edits escalate
// once more (two nudges max per file per task).
const editNudgeAt = 4

// tallyFileEdits implements the edit-churn guard: count successful edit_file
// calls per target path, nudge toward a single complete write_file once a
// file accumulates edits, and reset the count when the model does rewrite
// the file whole. Nudges are injected as user messages right after the tool
// results, the same channel nudgeIfStuck uses.
func (l *AgentLoop) tallyFileEdits(calls []provider.ToolCall, results []*tools.ToolResult, messages []provider.Message) []provider.Message {
	if l.editTally == nil {
		l.editTally = make(map[string]int)
		l.editNudged = make(map[string]int)
	}
	nudgePath, nudgeN, nudgeLevel := "", 0, 0
	for i, tc := range calls {
		if results[i].HasMultimodal() {
			continue
		}
		text := results[i].Text
		if strings.HasPrefix(text, "Error:") || text == "Interrupted" {
			continue
		}
		p := callPathArg(tc.Function.Arguments)
		if p == "" {
			continue
		}
		switch tc.Function.Name {
		case "edit_file":
			l.editTally[p]++
			n := l.editTally[p]
			if n >= editNudgeAt && n%editNudgeAt == 0 {
				if k := l.editNudged[p]; k < 2 {
					l.editNudged[p] = k + 1
					nudgePath, nudgeN, nudgeLevel = p, n, k+1
				}
			}
		case "write_file":
			// A full rewrite is exactly what the nudge would ask for.
			delete(l.editTally, p)
		}
	}
	if nudgePath == "" {
		return messages
	}
	var msg string
	if nudgeLevel == 1 {
		msg = fmt.Sprintf("This file (%s) has now been edited %d separate times this task. If the changes are cumulative, stop stacking small patches: produce the complete current content with one write_file call instead.", nudgePath, nudgeN)
	} else {
		msg = fmt.Sprintf("This file (%s) has now been edited %d times. Do not keep patching it piece by piece: rewrite it whole with write_file, or finish the task with what you have.", nudgePath, nudgeN)
	}
	slog.Warn("edit churn detected, nudging model", "path", nudgePath, "edits", nudgeN, "level", nudgeLevel)
	return append(messages, provider.Message{Role: "user", Content: msg})
}

// callPathArg returns the "path" argument of a tool call's JSON arguments,
// or "" when the arguments do not parse or carry no path.
func callPathArg(args string) string {
	var v struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(args), &v) == nil {
		return v.Path
	}
	return ""
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
				l.emitEvent(EventToolResult, l.curTurn, map[string]any{
					"call_id": tc.ID, "name": tc.Function.Name, "ok": false, "error": "interrupted",
				})
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
			toolStart := time.Now()
			results[idx] = l.executeSingleTool(ctx, tc)
			// Notify consumers that tool call ended
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
			// dsh-style tool-result pruning: over-budget output spills its
			// full text to a file, and the ledger keeps full fidelity —
			// original full row, then a compaction/prune shadow event
			// pricing the cut, then a pruned replacement row (same call_id,
			// "replaces" set) that readers fold over the original. The model
			// history carries only the pruned copy: that is the per-turn
			// token saving this pass exists for. Multimodal results are left
			// alone (their parts are not plain text).
			if !results[idx].HasMultimodal() && !pruneExempt(tc.Function.Name) {
				if before := len([]rune(text)); before > defaultPrune.Threshold {
					spillPath := spillToolText(l.spillDir, l.spillID, tc.ID, text)
					if pruned, ok := pruneToolText(text, spillPath); ok {
						fullRow := map[string]any{} // clone: the sink may keep this map
						for k, v := range payload {
							fullRow[k] = v
						}
						l.emitEvent(EventToolResult, l.curTurn, fullRow) // full row stays
						l.emitEvent(EventCompactionPrune, l.curTurn, map[string]any{
							"call_id": tc.ID, "name": tc.Function.Name,
							"chars_before": before, "chars_after": len([]rune(pruned)),
							"spill": spillPath,
						})
						payload["result"] = pruned
						payload["replaces"] = tc.ID
						text = pruned
						results[idx].Text = pruned // model history gets the pruned copy
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
	// Panic containment: a bug in one tool (index out of range, nil deref,
	// a bad model-supplied argument) must fail that single call, not crash
	// the whole agent process. The model sees a normal tool error and can
	// retry with a different approach; the task and the server keep running.
	defer func() {
		if r := recover(); r != nil {
			// The stack goes in the log, not just the value: three runs of
			// bench data contained recovered nil derefs in exec, and with only
			// "panic: invalid memory address" on disk there was nothing to
			// reproduce from. A contained panic that cannot be diagnosed is a
			// bug that will be paid for again by whichever session hits it.
			slog.Error("tool execution panicked, contained", "name", tc.Function.Name, "panic", r,
				"args", tc.Function.Arguments, "stack", string(debug.Stack()))
			result = tools.NewTextResult(fmt.Sprintf("Error: tool %q crashed (panic: %v). The task continues; analyze the cause and try a different approach.", tc.Function.Name, r))
		}
	}()
	var params map[string]interface{}
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
			slog.Warn("invalid tool call arguments", "name", tc.Function.Name, "error", err, "params", tc.Function.Arguments)
		}
	}
	// Runtime permission gate: role permissions + cautious-mode approval.
	// A denied call never reaches the tool; the model gets the deny message
	// as the tool result.
	if l.gate != nil {
		if allow, denyMsg := l.gate(ctx, tc.Function.Name, params); !allow {
			return tools.NewTextResult(denyMsg)
		}
	}
	// What the ledger learns about file mutations relative to what the session
	// read. Observation only: this has never blocked a call and does not now.
	l.observePolicy(tc.ID, tc.Function.Name, params)
	// A tool whose work can outlive the call goes to the job runtime: the work is
	// started under a context this turn does not own, and the call either returns
	// with the answer or hands over a job id. An error here means nothing was
	// started, so falling through to the ordinary path below cannot run anything
	// twice — that is the whole of the contract between the two paths.
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
	// A tool that reports output while it runs gets a sink when someone is
	// listening; everyone else goes through the registry unchanged. The result is
	// the same either way, so the model's view never depends on the live feed.
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
