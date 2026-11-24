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

type ctxSection struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Tokens int    `json:"tokens"`
}

type ctxEvent struct {
	Kind   string `json:"kind"`
	Round  int    `json:"round"`
	Turn   int    `json:"turn"`
	Tokens int    `json:"tokens"`
	Detail string `json:"detail"`
	At     string `json:"at"`
}

func ctxCompactionDone(ev session.EventRecord) bool {
	phase, _ := ev.Payload["phase"].(string)
	if phase != "" {
		return phase == "done"
	}
	_, hasB := ev.Payload["before_tokens"]
	_, hasA := ev.Payload["after_tokens"]
	return hasB && hasA
}

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

type ctxStats struct {
	ModelMs   int64 `json:"model_ms"`
	ToolMs    int64 `json:"tool_ms"`
	TtftAvgMs int64 `json:"ttft_avg_ms"`
}

type ctxTotals struct {
	Requests int   `json:"requests"`
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Cached   int64 `json:"cached"`
	Uncached int64 `json:"uncached"`
	Reported bool  `json:"reported"`
}

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
	cur := ctxRound{Round: 1}
	open := false
	for _, ev := range rows {
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
			cur.Steps++
			if peak := int(ctxNum(usage["prompt_tokens"])); peak > cur.PeakTokens {
				cur.PeakTokens = peak
			}
			stats.ModelMs += ctxNum(usage["call_ms"])
			if ttft := ctxNum(usage["ttft_ms"]); ttft > 0 {
				ttftSum += ttft
				ttftN++
			}
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
			how, _ := ev.Payload["kind"].(string)
			if how == "" {
				how = "auto"
			}
			evts = append(evts, ctxEvent{
				Kind: "compact", Round: cur.Round, Turn: ev.Turn, Tokens: int(back),
				Detail: fmt.Sprintf("%s压缩:%s → %s tokens", how, kish(before), kish(after)),
				At:     ev.CreatedAt,
			})
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
			if _, dup := ev.Payload["replaces"]; dup {
				continue
			}
			stats.ToolMs += ctxNum(ev.Payload["duration_ms"])
		case session.EventTaskStart:
			model, _ := ev.Payload["model"].(string)
			if model == "" {
				continue
			}
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
	if open {
		rounds = append(rounds, cur)
	}
	if len(evts) > 400 {
		evts = evts[len(evts)-400:]
	}
	if len(rounds) > 200 {
		rounds = rounds[len(rounds)-200:]
	}
	if ttftN > 0 {
		stats.TtftAvgMs = ttftSum / ttftN
	}
	return tot, evts, rounds, tot.Requests, stats
}

func kish(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

type ctxCountRow struct {
	total                          int
	sys, user, asst, tool          int
	est                            bool
}

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

var ctxCountsCache sync.Map

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

	counts := ctxCachedCounts(id, all)
	tokens := sess.CalibratedTokenCount()
	window := s.be.Mgr().ContextWindowFor(rec.ModelKey)
	pct := 0
	if window > 0 {
		pct = tokens * 100 / window
		if pct > 999 {
			pct = 999
		}
	}

	toolsT := 0
	if _, defs, perr := sess.ContextPlan(); perr == nil {
		for _, d := range defs {
			toolsT += ctxToolTokens(d)
		}
	} else if lu := sess.LastUsage(); lu != nil {
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
		totals = nil
	}
	if stats == nil {
		stats = &ctxStats{}
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
		"est":      counts.est,
		"pct":      pct,
		"sections": sections,
		"totals":   totals,
		"stats":    stats,
		"events":   events,
		"rounds":   rounds,
		"steps":    steps,
	})
}

const (
	ctxMsgPageSize     = 50
	ctxMsgPageMax      = 200
	ctxMsgScanCap      = 2000
	ctxPreviewRunes    = 400
	ctxDetailTextLimit = 32000
	ctxEstimateRunes   = 16000
)

type ctxToolDetail struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tokens      int    `json:"tokens"`
}

type ctxMessageBrief struct {
	Index   int    `json:"index"`
	Role    string `json:"role"`
	Tokens  int    `json:"tokens"`
	Chars   int    `json:"chars"`
	Preview string `json:"preview"`
	Est     bool   `json:"est"`
}

type ctxSystemPart struct {
	Label    string `json:"label"`
	Injected bool   `json:"injected"`
	Tokens   int    `json:"tokens"`
	Text     string `json:"text"`
}

func ctxToolTokens(def provider.ToolSchema) int {
	text := def.Function.Name + "\n" + def.Function.Description
	if b, err := json.Marshal(def); err == nil {
		text = string(b)
	}
	return util.CountMessagesTokens([]provider.Message{{Role: "tool", Content: text}})
}

func ctxMsgTokens(m provider.Message) (tokens int, est bool) {
	text := m.GetText()
	n := utf8.RuneCountInString(text)
	if n <= ctxEstimateRunes {
		return util.CountMessagesTokens([]provider.Message{m}), false
	}
	head := m
	head.Content = string([]rune(text)[:ctxEstimateRunes])
	head.ContentParts = nil
	headT := util.CountMessagesTokens([]provider.Message{head})
	perRune := float64(headT) / float64(ctxEstimateRunes)
	return headT + int(perRune*float64(n-ctxEstimateRunes)), true
}

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
			resp["tools"] = []ctxToolDetail{}
			resp["note"] = "无法读取该会话的工具定义:" + perr.Error()
		}
		writeJSON(w, http.StatusOK, resp)

	case "messages":
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
				hasMore = true
				break
			}
			scanned++
			if roleQ != "" && all[i].Role != roleQ {
				continue
			}
			if len(pick) >= limit {
				hasMore = true
				break
			}
			pick = append(pick, i)
		}
		rows := make([]ctxMessageBrief, 0, len(pick))
		for i := len(pick) - 1; i >= 0; i-- {
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
			"truncated": hasMore,
			"messages":  rows,
		})

	default:
		writeErr(w, http.StatusBadRequest, `part must be "system", "tools" or "messages"`)
	}
}


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

func ctxTrajRows(rows []session.EventRecord) []ctxTrajRow {
	var out []ctxTrajRow
	byCall := map[string]int{}
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
				continue
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
