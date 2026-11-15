// context_api.go — GET /api/sessions/{id}/context: the read model behind the
// UI's「上下文」drawer (web/ui/js/components/ContextPanel.js). Three views over
// data the session already keeps; nothing new is stored:
//
//	占用/组成  — token counts over the live conversation, split by role. The
//	             tool-definition line is derived the honest way: whatever the
//	             provider billed as prompt tokens but our own count over the
//	             messages cannot explain (system prompt + tool schemas +
//	             framing overhead), so that line is calibrated by the
//	             provider's real number, not a re-estimated JSON dump.
//	累计       — summed from the assistant/message ledger rows' usage payloads
//	             (every call: prompt/completion/cached tokens + whether the
//	             provider reports cache hits at all).
//	事件       — the ledger's compaction/prune rows plus model switches
//	             (task/start rows whose model differs from the previous run's).
//	轮次/步    — the same ledger read as an audit trail: a step is one loop (one
//	             billed assistant call), a round is the stretch between two
//	             compactions. A long task that compacted six times is six closed
//	             rounds plus the one still running, and each round records how
//	             many loops it took, how far it pushed the context, and what the
//	             compaction that ended it gave back.
//
// The companion endpoint GET /api/sessions/{id}/context/detail answers what the
// context is actually made of — the system prompt text, the real tool schemas,
// and the per-message token breakdown (or one message's full text).
//
// Manual compaction was removed as a user-facing action: auto compaction still
// runs in the loop, and this endpoint is where its effect becomes visible.
// The old POST /compact route is gone.
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/util"
)

// ctxSection is one composition segment for the drawer.
type ctxSection struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Tokens int    `json:"tokens"`
}

// ctxEvent is one row of the drawer's event list (compact/prune/switch).
// Round is which「轮」(round between compactions) the event belongs to, stamped
// during the ledger walk — the raw Turn is a per-task loop counter that resets
// on every task, so showing it as 第N轮 was the bug the user caught.
type ctxEvent struct {
	Kind   string `json:"kind"`
	Round  int    `json:"round"`
	Turn   int    `json:"turn"`
	Tokens int    `json:"tokens"`
	Detail string `json:"detail"`
	At     string `json:"at"`
}

// ctxCompactionDone reports whether a compaction ledger row is the real
// end-of-compaction row. The loop writes a phase:"start" heartbeat before the
// work (no before/after keys — it renders as a meaningless "0 → 0" row and,
// worse, used to close the round as a false boundary), then a phase:"done"
// row with the numbers. Rows predating the phase field are judged by their
// keys. Key-presence, never values: a no-progress compaction really reclaims
// 0 (before==after) and must stay, and a genuine reclaim below 1K must not be
// filtered by any size threshold.
func ctxCompactionDone(ev session.EventRecord) bool {
	phase, _ := ev.Payload["phase"].(string)
	if phase != "" {
		return phase == "done"
	}
	_, hasB := ev.Payload["before_tokens"]
	_, hasA := ev.Payload["after_tokens"]
	return hasB && hasA
}

// ctxRound is one「轮」of the drawer's round ledger: the stretch of conversation
// between two compactions (a full round ends when the context is compacted, and
// what follows is the next round). 一次 loop = 一步(step), so Steps counts the
// LLM calls that ran inside this round, and PeakTokens is how far the context
// was pushed before it got reclaimed — the number that explains why the round
// had to be compacted at all.
type ctxRound struct {
	Round      int    `json:"round"`
	Steps      int    `json:"steps"`
	FirstTurn  int    `json:"first_turn"`
	LastTurn   int    `json:"last_turn"`
	PeakTokens int    `json:"peak_tokens"`
	Reclaimed  int    `json:"reclaimed"`
	Compacted  bool   `json:"compacted"`
	At         string `json:"at"`
}

// ctxStats is the drawer's time account over the same ledger: how long the
// model held the session (ModelMs, summed over the per-call call_ms), how long
// the tools did (ToolMs), and the average time to the model's first token
// (TtftAvgMs, 0 when no call reported one — an absent measurement, not a fast
// one). Always present in the response, never null.
type ctxStats struct {
	ModelMs   int64 `json:"model_ms"`
	ToolMs    int64 `json:"tool_ms"`
	TtftAvgMs int64 `json:"ttft_avg_ms"`
}

// ctxTotals sums every provider-reported usage line in the ledger. Uncached
// is derived as Input-Cached and only means something when Reported is true
// (some deployments never report cache hits — that is "unknown", not "miss").
type ctxTotals struct {
	Requests int   `json:"requests"`
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Cached   int64 `json:"cached"`
	Uncached int64 `json:"uncached"`
	Reported bool  `json:"reported"`
}

// ctxNum reads a JSON-round-tripped number (float64 from the DB, int from a
// freshly written in-memory map).
func ctxNum(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

// ctxAllEvents pages through the whole ledger (ListEvents caps a page at 5000).
func ctxAllEvents(store *session.SessionStore, id string) []session.EventRecord {
	var out []session.EventRecord
	after := int64(0)
	for {
		rows, err := store.ListEvents(id, after, 5000)
		if err != nil || len(rows) == 0 {
			return out
		}
		out = append(out, rows...)
		after = rows[len(rows)-1].Seq
		if len(rows) < 5000 {
			return out
		}
	}
}

// contextLedger walks the ledger once and builds five read models over it: the
// cumulative totals, the drawer's event list, the round ledger (a round is the
// stretch between two compactions, a step is one LLM call), the total step
// count and the time account. pruneRows caps how many individual prune/argage
// rows the event list keeps (they fire per tool result and would flood it);
// totals and rounds are never capped per row — the event list ends at 400 rows
// and the round list at 200 rounds, both trimmed at the old end, because a
// session that compacted five hundred times is exactly the audit trail somebody
// opens the drawer for.
func contextLedger(rows []session.EventRecord) (*ctxTotals, []ctxEvent, []ctxRound, int, *ctxStats) {
	tot := &ctxTotals{}
	stats := &ctxStats{}
	var evts []ctxEvent
	var rounds []ctxRound
	pruneRows := 0
	lastModel := ""
	haveModel := false
	var ttftSum int64
	var ttftN int64
	// The round under construction. Rows arrive in seq order (ListEvents sorts
	// by seq), so a compaction is the last thing that happens to its round.
	cur := ctxRound{Round: 1}
	open := false // has the running round seen any row at all?
	for _, ev := range rows {
		// Round span/last-activity: every row counts, rows without a turn just
		// do not move the turn window.
		open = true
		if ev.Turn > 0 {
			if cur.FirstTurn == 0 {
				cur.FirstTurn = ev.Turn
			}
			cur.LastTurn = ev.Turn
		}
		if ev.CreatedAt != "" {
			cur.At = ev.CreatedAt
		}
		switch ev.Type {
		case session.EventAssistantMessage:
			usage, _ := ev.Payload["usage"].(map[string]interface{})
			if usage == nil {
				continue
			}
			tot.Requests++
			tot.Input += ctxNum(usage["prompt_tokens"])
			tot.Output += ctxNum(usage["completion_tokens"])
			if reported, _ := usage["cache_reported"].(bool); reported {
				tot.Reported = true
				tot.Cached += ctxNum(usage["cached_tokens"])
			}
			// One billed usage row = one loop = one step of the running round,
			// and its prompt_tokens is how far the context had grown by then:
			// the round's peak is what forced the compaction that ends it.
			cur.Steps++
			if peak := int(ctxNum(usage["prompt_tokens"])); peak > cur.PeakTokens {
				cur.PeakTokens = peak
			}
			// Time account: this call's wall-clock, and its time to first token
			// when the stream had one (rows written before call_ms existed just
			// do not contribute — the average over what is there stays honest).
			stats.ModelMs += ctxNum(usage["call_ms"])
			if ttft := ctxNum(usage["ttft_ms"]); ttft > 0 {
				ttftSum += ttft
				ttftN++
			}
		case session.EventCompaction:
			// The phase:"start" heartbeat is neither an event nor a round
			// boundary — skip it. (Old sessions full of "0 → 0" noise rows
			// heal retroactively: the filter runs on read.)
			if !ctxCompactionDone(ev) {
				continue
			}
			before := ctxNum(ev.Payload["before_tokens"])
			after := ctxNum(ev.Payload["after_tokens"])
			back := before - after
			if back < 0 {
				back = 0
			}
			how, _ := ev.Payload["kind"].(string)
			if how == "" {
				how = "auto"
			}
			evts = append(evts, ctxEvent{
				Kind: "compact", Round: cur.Round, Turn: ev.Turn, Tokens: int(back),
				Detail: fmt.Sprintf("%s压缩:%s → %s tokens", how, kish(before), kish(after)),
				At:     ev.CreatedAt,
			})
			// A compaction with no loop behind it is not a round of its own:
			// the loop had not run yet, so nothing new had grown. That happens
			// when compaction fires twice back to back (the second pass finds
			// more to cut right away) — folding the reclaim into the previous
			// round keeps the list honest: one round, both savings, no
			// "0 steps · peak 0 · −181.5K" rows that read like a bug.
			if cur.Steps == 0 && len(rounds) > 0 {
				last := &rounds[len(rounds)-1]
				last.Reclaimed += int(back)
				last.Compacted = true
				if ev.CreatedAt != "" {
					last.At = ev.CreatedAt
				}
				if cur.FirstTurn > 0 && last.FirstTurn == 0 {
					last.FirstTurn = cur.FirstTurn
				}
				if cur.LastTurn > 0 {
					last.LastTurn = cur.LastTurn
				}
				open = false
				continue
			}
			// A compaction closes its round: the round is now history, it
			// carries the tokens this compaction gave back, and everything
			// after it belongs to the next one.
			cur.Compacted = true
			cur.Reclaimed = int(back)
			rounds = append(rounds, cur)
			cur = ctxRound{Round: cur.Round + 1}
			open = false
		case session.EventCompactionPrune, session.EventCompactionArgAge:
			if pruneRows >= 150 {
				continue
			}
			pruneRows++
			cb := ctxNum(ev.Payload["chars_before"])
			ca := ctxNum(ev.Payload["chars_after"])
			name, _ := ev.Payload["name"].(string)
			kind := "prune"
			what := "修剪工具输出"
			if ev.Type == session.EventCompactionArgAge {
				kind = "prune"
				what = "老化大参数调用"
			}
			evts = append(evts, ctxEvent{
				Kind: kind, Round: cur.Round, Turn: ev.Turn,
				Detail: fmt.Sprintf("%s %s:%s → %s 字符", what, name, kish(cb), kish(ca)),
				At:     ev.CreatedAt,
			})
		case session.EventToolResult:
			// Tool time. A pruned result is written twice — the full row, then
			// a replacement copy with "replaces" set — and the ledger's rule is
			// that readers fold the replacement over the original, so only the
			// original is timed. Without this the pruned calls would count twice.
			if _, dup := ev.Payload["replaces"]; dup {
				continue
			}
			stats.ToolMs += ctxNum(ev.Payload["duration_ms"])
		case session.EventTaskStart:
			model, _ := ev.Payload["model"].(string)
			if model == "" {
				continue
			}
			// The first run establishes the baseline; only a later run with a
			// different model is a switch worth showing.
			if !haveModel || model == lastModel {
				haveModel = true
				lastModel = model
				continue
			}
			lastModel = model
			evts = append(evts, ctxEvent{
				Kind: "switch", Round: cur.Round, Detail: "模型 → " + model, At: ev.CreatedAt,
			})
		}
	}
	if tot.Reported {
		tot.Uncached = tot.Input - tot.Cached
		if tot.Uncached < 0 {
			tot.Uncached = 0
		}
	}
	// The round still open when the ledger ends is the current one (and when
	// nothing was ever compacted it is the only one): it is reported with
	// Compacted=false and Reclaimed=0, i.e. still running.
	if open {
		rounds = append(rounds, cur)
	}
	// Drawer shows the newest first; cap the whole list at 400 rows.
	if len(evts) > 400 {
		evts = evts[len(evts)-400:]
	}
	// The round list is trimmed at the old end too, but a round keeps the
	// number it had from the session start, so a trimmed ledger still says
	// which round of 250 the drawer is looking at.
	if len(rounds) > 200 {
		rounds = rounds[len(rounds)-200:]
	}
	// Average TTFT over the calls that measured one; 0 means "never measured"
	// (a non-streaming provider, or a ledger written before the field existed),
	// which the drawer shows as "—" rather than as an instant first token.
	if ttftN > 0 {
		stats.TtftAvgMs = ttftSum / ttftN
	}
	// Total steps = total LLM calls: a step is exactly one billed assistant
	// round-trip, so the two counters are the same fact counted once.
	return tot, evts, rounds, tot.Requests, stats
}

// kish formats a token/char count for the one-line event text.
func kish(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// handleSessionContext answers GET /api/sessions/{id}/context.
// ctxCountRow is one pass of the composition count over the live conversation.
type ctxCountRow struct {
	total                          int
	sys, user, asst, tool          int
	est                            bool // any row priced by the bounded estimate
}

// ctxCountComposition prices every message with the SAME bounded estimator the
// detail list uses (ctxMsgTokens): oversized rows are extrapolated from a
// prefix instead of paying a full-BPE pass over a 2 MB tool dump. That single
// change is what makes opening the drawer on a heavy session take milliseconds
// instead of seconds. The per-message work is embarrassingly parallel; the
// tiktoken registry lookup is mutex-guarded and encoding itself is read-only,
// so fan-out is safe once the encoder exists (web.go warms it at boot).
func ctxCountComposition(all []provider.Message) ctxCountRow {
	n := len(all)
	var out ctxCountRow
	if n == 0 {
		return out
	}
	workers := 1
	if n > 60 {
		workers = 4
	}
	per := (n + workers - 1) / workers
	parts := make([]ctxCountRow, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo := w * per
		hi := lo + per
		if lo >= n {
			break
		}
		if hi > n {
			hi = n
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			var p ctxCountRow
			for _, m := range all[lo:hi] {
				t, est := ctxMsgTokens(m)
				p.total += t
				switch m.Role {
				case "system":
					p.sys += t
				case "user":
					p.user += t
				case "assistant":
					p.asst += t
				case "tool":
					p.tool += t
				}
				p.est = p.est || est
			}
			parts[w] = p
		}(w, lo, hi)
	}
	wg.Wait()
	for _, p := range parts {
		out.total += p.total
		out.sys += p.sys
		out.user += p.user
		out.asst += p.asst
		out.tool += p.tool
		out.est = out.est || p.est
	}
	return out
}

// ctxCountsCache keeps the last composition count per session, keyed by a
// fingerprint of the snapshot (message count + every message's byte size,
// including tool-call arguments — the things compaction/pruning rewrite).
// Same fingerprint ⇒ same answer: an idle drawer costs nothing to reopen, and
// a running session's every change still busts it honestly. Entries are tiny;
// the map is hard-capped because this is a single-user server.
var ctxCountsCache sync.Map // id -> *ctxCountCacheEntry

type ctxCountCacheEntry struct {
	fp  string
	row ctxCountRow
}

func ctxCountFingerprint(all []provider.Message) string {
	var bytes int64
	for _, m := range all {
		bytes += int64(len(m.Content))
		for _, p := range m.ContentParts {
			bytes += int64(len(p.Text))
		}
		for _, tc := range m.ToolCalls {
			bytes += int64(len(tc.Function.Arguments)) + int64(len(tc.Function.Name))
		}
	}
	return strconv.Itoa(len(all)) + ":" + strconv.FormatInt(bytes, 10)
}

func ctxCachedCounts(id string, all []provider.Message) ctxCountRow {
	fp := ctxCountFingerprint(all)
	if v, ok := ctxCountsCache.Load(id); ok {
		if e := v.(*ctxCountCacheEntry); e.fp == fp {
			return e.row
		}
	}
	row := ctxCountComposition(all)
	n := 0
	ctxCountsCache.Range(func(_, _ any) bool { n++; return n < 129 })
	if n >= 128 {
		ctxCountsCache.Clear()
	}
	ctxCountsCache.Store(id, &ctxCountCacheEntry{fp: fp, row: row})
	return row
}

func (s *server) handleSessionContext(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.be.Mgr().Get(id)
	if err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	rec := sess.Record()
	all := sess.SnapshotMessages()

	// 占用:与聊天页底栏同一把尺子;超长消息走有界估算(est=true),首开不再
	// 被 MB 级工具输出的全量 BPE 拖死,同快照重开命中缓存直接回。
	counts := ctxCachedCounts(id, all)
	tokens := counts.total
	window := s.be.Mgr().ContextWindowFor(rec.ModelKey)
	pct := 0
	if window > 0 {
		pct = tokens * 100 / window
		if pct > 999 {
			pct = 999
		}
	}

	// 组成:逐条按 role 归类;工具定义按「计费 prompt - 可解释消息」倒推,
	// 首轮没有计费数(未调过 LLM)时该行给 0,前端自然显示 ≈0。
	toolsT := 0
	if lu := sess.LastUsage(); lu != nil {
		if billed := int(ctxNum(lu["prompt"])); billed > counts.total {
			toolsT = billed - counts.total
		}
	}
	sections := []ctxSection{
		{Key: "system", Label: "系统提示词", Tokens: counts.sys},
		{Key: "tools", Label: "工具定义", Tokens: toolsT},
		{Key: "user", Label: "用户消息", Tokens: counts.user},
		{Key: "assistant", Label: "助手回复", Tokens: counts.asst},
		{Key: "tool", Label: "工具结果", Tokens: counts.tool},
	}

	totals, events, rounds, steps, stats := contextLedger(ctxAllEvents(s.be.Mgr().GetStore(), id))
	if totals.Requests == 0 {
		totals = nil // no LLM call yet: the drawer shows its empty state
	}
	if stats == nil {
		stats = &ctxStats{} // the time account is always an object, never null
	}
	if events == nil {
		events = []ctxEvent{}
	}
	if rounds == nil {
		rounds = []ctxRound{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"window":   window,
		"tokens":   tokens,
		"est":      counts.est, // true=含外推估算(前端在占用数前加 ≈)
		"pct":      pct,
		"sections": sections,
		"totals":   totals,
		"stats":    stats,
		"events":   events,
		"rounds":   rounds,
		"steps":    steps,
	})
}

// Caps for the detail endpoint. The drawer asks for one part at a time, and each
// answer is bounded, so opening the detail of a 3000-message conversation is a
// bounded response — and a bounded amount of tiktoken work — rather than the
// whole history.
const (
	ctxMsgPageSize     = 50    // part=messages 默认页大小
	ctxMsgPageMax      = 200   // 客户端能要的最大页
	ctxMsgScanCap      = 2000  // role 过滤时往回扫的行数上限(防扫穿整个大会话)
	ctxPreviewRunes    = 400   // preview length in that list
	ctxDetailTextLimit = 32000 // full text of one message (part=messages&i=N)
	ctxEstimateRunes   = 16000 // 比这更长的消息按前缀计数外推(est=true)
)

// ctxToolDetail is one tool-definition row of the detail view. Tokens is the
// schema's own cost (what the request pays for it), not a share of the
// subtraction-derived「工具定义」line the composition view shows.
type ctxToolDetail struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tokens      int    `json:"tokens"`
}

// ctxMessageBrief is one message row of the detail view. Tokens is exact unless
// Est says otherwise (oversized rows are extrapolated to keep the drawer fast).
type ctxMessageBrief struct {
	Index   int    `json:"index"`
	Role    string `json:"role"`
	Tokens  int    `json:"tokens"`
	Chars   int    `json:"chars"`
	Preview string `json:"preview"`
	Est     bool   `json:"est"`
}

// ctxSystemPart is one section of the system prompt: what it is, whether it was
// injected from this machine (working directory, AGENTS.md, plugins, OS block)
// or comes from the code/role, and what it costs on its own. Tokens is the
// section's own text — the sum is close to but not equal to the system
// message's whole-message count, which also carries per-message framing.
type ctxSystemPart struct {
	Label    string `json:"label"`
	Injected bool   `json:"injected"`
	Tokens   int    `json:"tokens"`
	Text     string `json:"text"`
}

// ctxToolTokens prices one tool schema the way the prompt pays for it: the
// schema travels to the provider as JSON, so the JSON is what gets counted.
// A schema that refuses to marshal falls back to its name + description — the
// drawer must never be the thing that takes a request down.
func ctxToolTokens(def provider.ToolSchema) int {
	text := def.Function.Name + "\n" + def.Function.Description
	if b, err := json.Marshal(def); err == nil {
		text = string(b)
	}
	return util.CountMessagesTokens([]provider.Message{{Role: "tool", Content: text}})
}

// ctxMsgTokens prices one message for the list view and says whether the number
// is an estimate. tiktoken over a 2 MB tool result costs more than the drawer's
// whole round trip, so a message longer than ctxEstimateRunes is counted on its
// first ctxEstimateRunes runes and extrapolated by length (est=true): an
// approximate number that arrives instantly beats an exact one that doesn't.
// The full-text view (&i=N) always counts exactly.
func ctxMsgTokens(m provider.Message) (tokens int, est bool) {
	text := m.GetText()
	n := utf8.RuneCountInString(text)
	if n <= ctxEstimateRunes {
		return util.CountMessagesTokens([]provider.Message{m}), false
	}
	// 只有正文换成前缀:role / name / reasoning / tool_calls 这些骨架照数。
	// 图片在超长消息里不计入估算(est 已标明这是估算,不是账单)。
	head := m
	head.Content = string([]rune(text)[:ctxEstimateRunes])
	head.ContentParts = nil
	headT := util.CountMessagesTokens([]provider.Message{head})
	perRune := float64(headT) / float64(ctxEstimateRunes)
	return headT + int(perRune*float64(n-ctxEstimateRunes)), true
}

// ctxSplitSystem lines the live plan's prompt sections up against the system
// prompt as it sits in the conversation. Sections are matched in order (the
// joined prompt puts one newline between them, so the cursor only walks
// forward). aligned=false means the stored copy and today's plan are not the
// same bytes — a plugin or an AGENTS.md edited mid-conversation, or a
// compaction that rewrote the head — and the caller says so instead of
// pretending the breakdown describes the stored text.
func ctxSplitSystem(stored string, plan []session.PromptPart) (items []ctxSystemPart, aligned bool) {
	items = make([]ctxSystemPart, 0, len(plan))
	joined := make([]string, 0, len(plan))
	cursor := 0
	for _, p := range plan {
		items = append(items, ctxSystemPart{
			Label: p.Label, Injected: p.Injected,
			Text: p.Text, Tokens: util.EstimateTokens(p.Text),
		})
		joined = append(joined, p.Text)
		if p.Text == "" {
			continue
		}
		rel := strings.Index(stored[cursor:], p.Text)
		if rel < 0 {
			return items, false
		}
		cursor += rel + len(p.Text)
	}
	return items, strings.Join(joined, "\n") == stored
}

// ctxPreviewText is one message's list preview: the first ctxPreviewRunes runes
// of its text (runes, never bytes — this text is CJK, and a byte cut would slice
// a character in half). An assistant row that carries only tool calls has no
// text at all, so it gets a marker: an empty row would read as a bug.
func ctxPreviewText(m provider.Message) string {
	text := m.GetText()
	if text == "" && len(m.ToolCalls) > 0 {
		names := make([]string, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			n := tc.Function.Name
			if n == "" {
				n = "?"
			}
			names = append(names, n)
		}
		text = "[工具调用] " + strings.Join(names, ", ")
	}
	if r := []rune(text); len(r) > ctxPreviewRunes {
		return string(r[:ctxPreviewRunes])
	}
	return text
}

// handleSessionContextDetail answers
// GET /api/sessions/{id}/context/detail?part=system|tools|messages[&i=N]:
// the parts of the context the composition view can only count, never show.
//
//	?part=system   the system prompt as it sits in the conversation (the first
//	               role=="system" message — the text that is actually in the
//	               context and being billed, not a re-rendered guess), split
//	               into its sections: which are ours, which the machine injected
//	?part=tools    the real tool schemas for this session, via Session.ContextPlan
//	               (the same preparePromptTools path a turn uses)
//	?part=messages a page of the per-message token/char breakdown: newest
//	               &limit rows (default 50, max 200), older pages with
//	               &before=<index>, one role with &role=<role>; has_more tells
//	               the client there is an older page
//	?part=messages&i=N the full text of message N (rune-capped)
func (s *server) handleSessionContextDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.be.Mgr().Get(id)
	if err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	all := sess.SnapshotMessages()

	switch r.URL.Query().Get("part") {
	case "system":
		text := ""
		for _, m := range all {
			if m.Role == "system" {
				text = m.GetText()
				break
			}
		}
		note := ""
		items := []ctxSystemPart{}
		if text == "" {
			note = "会话还没有系统提示词(还没有发起过对话)"
		} else {
			// 分段用当前计划(同一个 BuildSystemPromptParts),再回到上下文里那份
			// 存量文本上逐段定位:抽屉看的是「真正在计费的那一份」的构成。
			plan, _, perr := sess.ContextPlan()
			if perr != nil {
				note = "分段读取失败,只显示整段:" + perr.Error()
			} else {
				var aligned bool
				items, aligned = ctxSplitSystem(text, plan)
				if !aligned {
					note = "分段来自当前提示词计划:会话开始后注入内容有变化,与上下文里那一份不完全一致"
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"part": "system", "text": text, "note": note, "parts": items,
		})

	case "tools":
		tools := []ctxToolDetail{}
		total := 0
		_, defs, perr := sess.ContextPlan()
		for _, d := range defs {
			n := ctxToolTokens(d)
			total += n
			tools = append(tools, ctxToolDetail{
				Name: d.Function.Name, Description: d.Function.Description, Tokens: n,
			})
		}
		resp := map[string]interface{}{"part": "tools", "tools": tools, "total": total}
		if perr != nil {
			// No plan (unknown agent role, broken config): answer empty-and-explain
			// rather than a 500 over a drawer panel.
			resp["tools"] = []ctxToolDetail{}
			resp["note"] = "无法读取该会话的工具定义:" + perr.Error()
		}
		writeJSON(w, http.StatusOK, resp)

	case "messages":
		// &i=N asks for one message in full instead of the list.
		if v := r.URL.Query().Get("i"); v != "" {
			idx, cerr := strconv.Atoi(v)
			if cerr != nil {
				writeErr(w, http.StatusBadRequest, "i 必须是整数")
				return
			}
			if idx < 0 || idx >= len(all) {
				writeErr(w, http.StatusNotFound,
					fmt.Sprintf("消息序号 %d 越界(共 %d 条)", idx, len(all)))
				return
			}
			m := all[idx]
			text := m.GetText()
			truncated := false
			if runes := []rune(text); len(runes) > ctxDetailTextLimit {
				text = string(runes[:ctxDetailTextLimit])
				truncated = true
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"part": "message", "index": idx, "role": m.Role,
				"tokens": util.CountMessagesTokens([]provider.Message{m}),
				"text":   text, "truncated": truncated,
			})
			return
		}
		// 分页列表:从 before-1 往回扫,收满 limit 条为止(role 非空时只收该角色)。
		// 第一遍只比 role、不算 token —— 几百条 tiktoken 才是这个接口真正的开销;
		// 第二遍只为真正要返回的那几条计价。index 仍是全量下标,可直接 &i= 取全文。
		roleQ := r.URL.Query().Get("role")
		limit := ctxMsgPageSize
		if v := r.URL.Query().Get("limit"); v != "" {
			n, cerr := strconv.Atoi(v)
			if cerr != nil {
				writeErr(w, http.StatusBadRequest, "limit 必须是整数")
				return
			}
			limit = n
		}
		if limit < 1 {
			limit = 1
		}
		if limit > ctxMsgPageMax {
			limit = ctxMsgPageMax
		}
		before := len(all)
		if v := r.URL.Query().Get("before"); v != "" {
			n, cerr := strconv.Atoi(v)
			if cerr != nil {
				writeErr(w, http.StatusBadRequest, "before 必须是整数")
				return
			}
			if n < 0 {
				n = 0
			}
			if n > len(all) {
				n = len(all)
			}
			before = n
		}
		pick := make([]int, 0, limit)
		hasMore := false
		for i, scanned := before-1, 0; i >= 0; i-- {
			if scanned >= ctxMsgScanCap {
				// 往回扫的预算用尽:更前面无法证明没有,按「还有」处理。
				hasMore = true
				break
			}
			scanned++
			if roleQ != "" && all[i].Role != roleQ {
				continue
			}
			if len(pick) >= limit {
				hasMore = true // 这一条就是没进本页的那条
				break
			}
			pick = append(pick, i)
		}
		rows := make([]ctxMessageBrief, 0, len(pick))
		for i := len(pick) - 1; i >= 0; i-- { // 倒着扫,正着给
			m := all[pick[i]]
			tk, est := ctxMsgTokens(m)
			rows = append(rows, ctxMessageBrief{
				Index:   pick[i],
				Role:    m.Role,
				Tokens:  tk,
				Chars:   utf8.RuneCountInString(m.GetText()),
				Preview: ctxPreviewText(m),
				Est:     est,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"part": "messages", "total": len(all), "returned": len(rows),
			"has_more": hasMore, "role": roleQ,
			"truncated": hasMore, // 旧字段名,与 has_more 同义,留一轮兼容
			"messages":  rows,
		})

	default:
		writeErr(w, http.StatusBadRequest, `part must be "system", "tools" or "messages"`)
	}
}

// ---------------- 轨迹(trajectory) ----------------
//
// GET /api/sessions/{id}/trajectory?before=<seq>&limit=N — the dsh-style step
// timeline: one row per interesting ledger event, newest page first, older
// pages behind &before. Rows never carry whole payloads: titles and sub-lines
// are rune-clipped summaries, so even a 5000-event ledger answers in one page.

// ctxTrajRow is one trajectory row. Kind drives the row's tag color in the
// drawer; Round is the audit-round number (same stamping as ctxEvent).
type ctxTrajRow struct {
	Seq    int64  `json:"seq"`
	Kind   string `json:"kind"`
	Round  int    `json:"round"`
	Title  string `json:"title"`
	Sub    string `json:"sub"`
	Tokens int64  `json:"tokens,omitempty"`
	DurMs  int64  `json:"dur_ms,omitempty"`
	At     string `json:"at,omitempty"`
}

const (
	trajPageDefault = 300
	trajPageMax     = 800
)

// ctxClip flattens text to one line and clips it to n runes (CJK-safe).
func ctxClip(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ctxTrajRows turns the raw ledger into trajectory rows, in ledger order.
// A tool call opens its row; its result row folds back onto it (and the
// prune-replacement copy folds too), so the drawer shows one honest line per
// call. Compaction rows use the same real-row filter as the audit ledger.
func ctxTrajRows(rows []session.EventRecord) []ctxTrajRow {
	var out []ctxTrajRow
	byCall := map[string]int{} // call_id -> row index
	round := 1
	for _, ev := range rows {
		switch ev.Type {
		case session.EventTaskStart:
			goal, _ := ev.Payload["goal"].(string)
			model, _ := ev.Payload["model"].(string)
			sub := model
			if n := ctxNum(ev.Payload["images"]); n > 0 {
				if sub != "" {
					sub += " · "
				}
				sub += fmt.Sprintf("%d 图", n)
			}
			out = append(out, ctxTrajRow{
				Seq: ev.Seq, Kind: "task", Round: round,
				Title: "任务: " + ctxClip(goal, 120), Sub: sub, At: ev.CreatedAt,
			})
		case session.EventAssistantMessage:
			content, _ := ev.Payload["content"].(string)
			usage, _ := ev.Payload["usage"].(map[string]interface{})
			title := ctxClip(content, 200)
			if title == "" {
				// A pure tool-call commit: name the calls instead of an empty row.
				names := []string{}
				if calls, ok := ev.Payload["tool_calls"].([]interface{}); ok {
					for _, c := range calls {
						cm, _ := c.(map[string]interface{})
						n, _ := cm["name"].(string)
						if n != "" {
							names = append(names, n)
						}
					}
				}
				if len(names) > 0 {
					title = "[工具调用] " + strings.Join(names, ", ")
				}
			}
			if title == "" {
				continue // nothing sayable (empty content, no calls)
			}
			var tk, dur int64
			if usage != nil {
				tk = ctxNum(usage["completion_tokens"])
				dur = ctxNum(usage["call_ms"])
			}
			out = append(out, ctxTrajRow{
				Seq: ev.Seq, Kind: "assistant", Round: round,
				Title: title, Tokens: tk, DurMs: dur, At: ev.CreatedAt,
			})
		case session.EventToolCall:
			name, _ := ev.Payload["name"].(string)
			args, _ := ev.Payload["args"].(string)
			row := ctxTrajRow{
				Seq: ev.Seq, Kind: "tool", Round: round,
				Title: name + " " + ctxClip(args, 160), At: ev.CreatedAt,
			}
			if id, _ := ev.Payload["call_id"].(string); id != "" {
				byCall[id] = len(out)
			}
			out = append(out, row)
		case session.EventToolResult:
			name, _ := ev.Payload["name"].(string)
			result, _ := ev.Payload["result"].(string)
			ok, _ := ev.Payload["ok"].(bool)
			status := "✓"
			if !ok {
				status = "✗"
			}
			sub := status + " " + ctxClip(result, 300)
			dur := ctxNum(ev.Payload["duration_ms"])
			id, _ := ev.Payload["call_id"].(string)
			_, isReplaces := ev.Payload["replaces"]
			if i, seen := byCall[id]; seen {
				// Fold the result onto its call row. A prune replacement
				// refreshes the summary but must not re-time the call.
				out[i].Sub = sub
				if dur > 0 && !isReplaces {
					out[i].DurMs = dur
				}
				continue
			}
			out = append(out, ctxTrajRow{
				Seq: ev.Seq, Kind: "tool", Round: round,
				Title: name, Sub: sub, DurMs: dur, At: ev.CreatedAt,
			})
		case session.EventCompaction:
			if !ctxCompactionDone(ev) {
				continue
			}
			before := ctxNum(ev.Payload["before_tokens"])
			after := ctxNum(ev.Payload["after_tokens"])
			back := before - after
			if back < 0 {
				back = 0
			}
			out = append(out, ctxTrajRow{
				Seq: ev.Seq, Kind: "compact", Round: round,
				Title: fmt.Sprintf("auto压缩 %s → %s tokens", kish(before), kish(after)),
				Tokens: back, At: ev.CreatedAt,
			})
			round++
		case session.EventCompactionPrune, session.EventCompactionArgAge:
			cb := ctxNum(ev.Payload["chars_before"])
			ca := ctxNum(ev.Payload["chars_after"])
			name, _ := ev.Payload["name"].(string)
			what := "修剪工具输出"
			if ev.Type == session.EventCompactionArgAge {
				what = "老化大参数调用"
			}
			out = append(out, ctxTrajRow{
				Seq: ev.Seq, Kind: "prune", Round: round,
				Title: fmt.Sprintf("%s %s:%s → %s 字符", what, name, kish(cb), kish(ca)),
				At: ev.CreatedAt,
			})
		}
	}
	return out
}

// handleSessionTrajectory answers GET /api/sessions/{id}/trajectory: one page
// of trajectory rows (default the newest 300, max 800), plus the step/round
// summary the drawer header shows. &before=<seq> walks toward the past.
func (s *server) handleSessionTrajectory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.be.Mgr().Get(id)
	if err != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "session not found: "+id)
		return
	}
	limit := trajPageDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		n, cerr := strconv.Atoi(v)
		if cerr != nil {
			writeErr(w, http.StatusBadRequest, "limit 必须是整数")
			return
		}
		limit = n
	}
	if limit < 1 {
		limit = 1
	}
	if limit > trajPageMax {
		limit = trajPageMax
	}
	before := int64(0)
	hasBefore := false
	if v := r.URL.Query().Get("before"); v != "" {
		n, cerr := strconv.ParseInt(v, 10, 64)
		if cerr != nil {
			writeErr(w, http.StatusBadRequest, "before 必须是整数")
			return
		}
		before, hasBefore = n, true
	}

	allRows := ctxAllEvents(s.be.Mgr().GetStore(), id)
	all := ctxTrajRows(allRows)
	end := len(all)
	if hasBefore {
		for end > 0 && all[end-1].Seq >= before {
			end--
		}
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	page := all[start:end]
	if page == nil {
		page = []ctxTrajRow{}
	}
	_, _, rounds, steps, _ := contextLedger(allRows)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"rows":     page,
		"has_more": start > 0,
		"steps":    steps,
		"rounds":   len(rounds),
	})
}
