package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
)

// TestSessionContextAPI covers GET /api/sessions/{id}/context — the read
// model for the「上下文」drawer: composition sections, cumulative totals
// summed from the assistant ledger rows, and the compact/prune/model-switch
// event list. Also pins that the removed manual-compaction route is really
// gone (需求⑦).
func TestSessionContextAPI(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("ctx-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()
	store := be.mgr.GetStore()

	seqs := []struct {
		typ     string
		payload map[string]any
	}{
		{session.EventTaskStart, map[string]any{"goal": "one", "model": "m-a"}},
		{session.EventAssistantMessage, map[string]any{"content": "x", "usage": map[string]any{
			"prompt_tokens": 1000, "completion_tokens": 50, "cached_tokens": 800, "cache_reported": true}}},
		{session.EventTaskStart, map[string]any{"goal": "two", "model": "m-a"}}, // 同模型:不算切换
		{session.EventAssistantMessage, map[string]any{"content": "y", "usage": map[string]any{
			"prompt_tokens": 500, "completion_tokens": 20, "cached_tokens": 0, "cache_reported": true}}},
		{session.EventTaskStart, map[string]any{"goal": "three", "model": "m-b"}}, // 换模型:switch
		{session.EventCompaction, map[string]any{"before_tokens": 2000, "after_tokens": 800, "kind": "auto"}},
		{session.EventCompactionPrune, map[string]any{"call_id": "c1", "name": "exec", "chars_before": 5000, "chars_after": 500}},
	}
	for i, e := range seqs {
		if _, err := store.AppendEvent(id, e.typ, i+1, e.payload); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}

	var resp struct {
		Window   int `json:"window"`
		Tokens   int `json:"tokens"`
		Pct      int `json:"pct"`
		Sections []struct {
			Key    string `json:"key"`
			Label  string `json:"label"`
			Tokens int    `json:"tokens"`
		} `json:"sections"`
		Totals *struct {
			Requests int   `json:"requests"`
			Input    int64 `json:"input"`
			Output   int64 `json:"output"`
			Cached   int64 `json:"cached"`
			Uncached int64 `json:"uncached"`
			Reported bool  `json:"reported"`
		} `json:"totals"`
		Events []struct {
			Kind   string `json:"kind"`
			Turn   int    `json:"turn"`
			Tokens int    `json:"tokens"`
			Detail string `json:"detail"`
		} `json:"events"`
	}
	if err := getJSON(api.URL+"/api/sessions/"+id+"/context", &resp); err != nil {
		t.Fatalf("GET context: %v", err)
	}

	if len(resp.Sections) != 5 {
		t.Fatalf("sections = %d, want 5: %+v", len(resp.Sections), resp.Sections)
	}
	wantKeys := []string{"system", "tools", "user", "assistant", "tool"}
	for i, k := range wantKeys {
		if resp.Sections[i].Key != k || resp.Sections[i].Label == "" {
			t.Fatalf("sections[%d] = %+v, want key %q with a label", i, resp.Sections[i], k)
		}
	}

	if resp.Totals == nil {
		t.Fatal("totals = null, want the summed usage")
	}
	tt := resp.Totals
	if tt.Requests != 2 || tt.Input != 1500 || tt.Output != 70 || tt.Cached != 800 || tt.Uncached != 700 || !tt.Reported {
		t.Fatalf("totals = %+v, want 2/1500/70/800/700/reported", *tt)
	}

	kinds := make([]string, 0, len(resp.Events))
	for _, e := range resp.Events {
		kinds = append(kinds, e.Kind)
	}
	got := strings.Join(kinds, ",")
	if got != "switch,compact,prune" {
		t.Fatalf("event kinds = %q, want \"switch,compact,prune\"", got)
	}
	for _, e := range resp.Events {
		if e.Kind == "compact" && e.Tokens != 1200 {
			t.Fatalf("compact reclaimed %d, want 1200", e.Tokens)
		}
		if e.Kind == "switch" && !strings.Contains(e.Detail, "m-b") {
			t.Fatalf("switch detail = %q, want the new model m-b", e.Detail)
		}
	}

	// 无用量账本的会话:totals 必须是 null(前端显示空态而不是 0 假象)。
	sess2, err := be.mgr.CreateSessionRecord("ctx-empty", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord 2: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := getJSON(api.URL+"/api/sessions/"+sess2.ID()+"/context", &raw); err != nil {
		t.Fatalf("GET context (empty): %v", err)
	}
	if string(raw["totals"]) != "null" {
		t.Fatalf("empty session totals = %s, want null", string(raw["totals"]))
	}
	// 没跑过一步的会话:rounds 是空数组(不是 null),steps 是 0 —— 前端按
	// 「有 rounds 数组就渲染列表」处理,null 会让它炸。
	if string(raw["rounds"]) != "[]" {
		t.Fatalf("empty session rounds = %s, want []", string(raw["rounds"]))
	}
	if string(raw["steps"]) != "0" {
		t.Fatalf("empty session steps = %s, want 0", string(raw["steps"]))
	}

	// 未知会话 → 404。
	r, err := http.Get(api.URL + "/api/sessions/nope/context")
	if err != nil {
		t.Fatalf("GET context 404: %v", err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", r.StatusCode)
	}

	// 手动压缩路由已删(需求⑦)。POST 落在 `GET /` 兜底上 → 405;
	// 无论 404 还是 405,关键是从前这个 POST 不再执行压缩(不会 200)。
	rp, err := http.Post(api.URL+"/api/sessions/"+id+"/compact", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST compact: %v", err)
	}
	rp.Body.Close()
	if rp.StatusCode == http.StatusOK || rp.StatusCode == http.StatusCreated {
		t.Fatalf("POST /compact status = %d, want 404/405 (route removed)", rp.StatusCode)
	}
}

// TestSessionContextRounds covers the round/step ledger of
// GET /api/sessions/{id}/context: one loop (one billed assistant call) is one
// step, and the stretch between two compactions is one round. A long task that
// compacted six times must be auditable — which round, how many loops it took,
// how far it pushed the context, and what its compaction gave back. Also pins
// the exact JSON tags the frontend codes against.
func TestSessionContextRounds(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("rounds-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()
	store := be.mgr.GetStore()

	// One loop = one assistant/message row carrying usage.
	usage := func(prompt, completion, turn int) {
		t.Helper()
		if _, err := store.AppendEvent(id, session.EventAssistantMessage, turn, map[string]any{
			"content": "x",
			"usage": map[string]any{
				"prompt_tokens": prompt, "completion_tokens": completion,
				"cached_tokens": 0, "cache_reported": true,
			},
		}); err != nil {
			t.Fatalf("AppendEvent assistant: %v", err)
		}
	}

	// 第 1 轮:3 步,上下文从 3000 一路顶到 8800,然后被压缩回收 7000。
	usage(3000, 40, 1)
	usage(6000, 55, 2)
	usage(8800, 30, 3)
	if _, err := store.AppendEvent(id, session.EventCompaction, 3, map[string]any{
		"before_tokens": 9000, "after_tokens": 2000, "kind": "auto",
	}); err != nil {
		t.Fatalf("AppendEvent compaction: %v", err)
	}
	// 第 2 轮(仍在进行):压缩后的修剪行 + 2 步。
	if _, err := store.AppendEvent(id, session.EventCompactionPrune, 4, map[string]any{
		"call_id": "c1", "name": "read", "chars_before": 4000, "chars_after": 400,
	}); err != nil {
		t.Fatalf("AppendEvent prune: %v", err)
	}
	usage(2100, 25, 4)
	usage(2400, 20, 5)

	var resp struct {
		Steps  int `json:"steps"`
		Rounds []struct {
			Round      int    `json:"round"`
			Steps      int    `json:"steps"`
			FirstTurn  int    `json:"first_turn"`
			LastTurn   int    `json:"last_turn"`
			PeakTokens int    `json:"peak_tokens"`
			Reclaimed  int    `json:"reclaimed"`
			Compacted  bool   `json:"compacted"`
			At         string `json:"at"`
		} `json:"rounds"`
		Totals *struct {
			Requests int `json:"requests"`
		} `json:"totals"`
	}
	if err := getJSON(api.URL+"/api/sessions/"+id+"/context", &resp); err != nil {
		t.Fatalf("GET context: %v", err)
	}

	if resp.Steps != 5 {
		t.Fatalf("steps = %d, want 5(五次 loop)", resp.Steps)
	}
	if resp.Totals == nil || resp.Totals.Requests != resp.Steps {
		t.Fatalf("totals/requests = %+v, want the same count as steps(%d)", resp.Totals, resp.Steps)
	}
	if len(resp.Rounds) != 2 {
		t.Fatalf("rounds = %d 段, want 2(一次压缩 = 两轮): %+v", len(resp.Rounds), resp.Rounds)
	}
	r1, r2 := resp.Rounds[0], resp.Rounds[1]
	if r1.Round != 1 || r1.Steps != 3 || r1.FirstTurn != 1 || r1.LastTurn != 3 {
		t.Fatalf("round 1 = %+v, want round1/3步/turn1-3", r1)
	}
	if r1.PeakTokens != 8800 {
		t.Fatalf("round 1 peak_tokens = %d, want 8800(本轮把上下文顶到的最大值)", r1.PeakTokens)
	}
	if !r1.Compacted || r1.Reclaimed != 7000 {
		t.Fatalf("round 1 = %+v, want compacted=true reclaimed=7000", r1)
	}
	if r1.At == "" {
		t.Fatal("round 1 at = \"\", want the compaction's created_at")
	}
	if r2.Round != 2 || r2.Steps != 2 || r2.FirstTurn != 4 || r2.LastTurn != 5 {
		t.Fatalf("round 2 = %+v, want round2/2步/turn4-5", r2)
	}
	if r2.Compacted || r2.Reclaimed != 0 {
		t.Fatalf("round 2 = %+v, want the still-running round (compacted=false, reclaimed=0)", r2)
	}
	if r2.PeakTokens != 2400 {
		t.Fatalf("round 2 peak_tokens = %d, want 2400(压缩后重新长到的大小)", r2.PeakTokens)
	}

	// 前端按固定 JSON tag 对接,字段名一个都不能改。
	var raw struct {
		Steps  json.RawMessage              `json:"steps"`
		Rounds []map[string]json.RawMessage `json:"rounds"`
	}
	if err := getJSON(api.URL+"/api/sessions/"+id+"/context", &raw); err != nil {
		t.Fatalf("GET context (raw): %v", err)
	}
	if len(raw.Steps) == 0 {
		t.Fatal("响应缺少 steps 字段")
	}
	if len(raw.Rounds) == 0 {
		t.Fatal("响应缺少 rounds 字段或为空")
	}
	for _, k := range []string{"round", "steps", "first_turn", "last_turn", "peak_tokens", "reclaimed", "compacted", "at"} {
		if _, ok := raw.Rounds[0][k]; !ok {
			t.Fatalf("rounds[0] 缺少契约字段 %q(实际 %v)", k, raw.Rounds[0])
		}
	}
}

// awaitConversation polls until the session's record carries at least n
// messages — a turn is persisted only when its model request completes.
func awaitConversation(t *testing.T, mgr *session.SessionManager, id string, n int) []provider.Message {
	t.Helper()
	for i := 0; i < 200; i++ {
		s, err := mgr.Get(id)
		if err == nil && s != nil {
			if msgs := s.SnapshotMessages(); len(msgs) >= n {
				return msgs
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("会话 %s 在预期时间内没有产生至少 %d 条消息", id, n)
	return nil
}

// ctxGet fetches a URL and returns its status code plus its JSON body as a map
// (nil when the body is not a JSON object). Unlike getJSON it does not fail on
// non-200, so the error cases can assert their codes.
func ctxGet(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	var out map[string]any
	if len(b) > 0 {
		_ = json.Unmarshal(b, &out)
	}
	return r.StatusCode, out
}

// TestSessionContextDetailAPI covers GET /api/sessions/{id}/context/detail —
// the parts of the context the composition view can only count: the system
// prompt text (split into the sections we wrote and the ones this machine
// injected), the real tool schemas, the per-message breakdown, and one
// message's full text (?part=messages&i=N). The conversation is produced by a
// real turn (POST /api/messages → hub → loop → mock model), so what the drawer
// reads back is what actually sits in the context.
func TestSessionContextDetailAPI(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("detail-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()

	r, err := http.Post(api.URL+"/api/messages", "application/json",
		strings.NewReader(`{"session_id":"`+id+`","content":"说一句你好"}`))
	if err != nil {
		t.Fatalf("POST /api/messages: %v", err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/messages status = %d, want 202: %s", r.StatusCode, b)
	}
	msgs := awaitConversation(t, be.mgr, id, 3)
	detail := api.URL + "/api/sessions/" + id + "/context/detail"

	// part=system:会话里第一条 system 消息的全文。
	code, sys := ctxGet(t, detail+"?part=system")
	if code != http.StatusOK {
		t.Fatalf("part=system status = %d, want 200: %v", code, sys)
	}
	if sys["part"] != "system" {
		t.Fatalf("part = %v, want \"system\"", sys["part"])
	}
	sysText, ok := sys["text"].(string)
	if !ok {
		t.Fatalf("part=system 缺少 text 字段: %v", sys)
	}
	if sysText == "" {
		t.Fatal("跑过一轮对话后 text 不应为空(系统提示词真的在上下文里)")
	}
	if _, ok := sys["note"]; !ok {
		t.Fatalf("part=system 缺少 note 字段(可为空串): %v", sys)
	}
	if !strings.Contains(msgs[0].GetText(), sysText) {
		t.Fatal("part=system 的文本不是上下文里的系统提示词")
	}
	// parts:系统提示词的来源细分。分段拼回去必须等于 text(否则抽屉显示的
	// 是另一份提示词),并且至少有一段是本机注入的(工作目录一定在)。
	parts, ok := sys["parts"].([]any)
	if !ok || len(parts) == 0 {
		t.Fatalf("part=system 应有非空 parts 数组: %v", sys)
	}
	var rebuilt []string
	injected := 0
	for i, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("parts[%d] 不是对象: %v", i, p)
		}
		for _, k := range []string{"label", "injected", "tokens", "text"} {
			if _, ok := pm[k]; !ok {
				t.Fatalf("parts[%d] 缺少契约字段 %q: %v", i, k, pm)
			}
		}
		if label, _ := pm["label"].(string); label == "" {
			t.Fatalf("parts[%d].label 为空: %v", i, pm)
		}
		if inj, ok := pm["injected"].(bool); ok && inj {
			injected++
		}
		if tk, ok := pm["tokens"].(float64); !ok || tk < 0 {
			t.Fatalf("parts[%d].tokens = %v", i, pm["tokens"])
		}
		text, _ := pm["text"].(string)
		rebuilt = append(rebuilt, text)
	}
	if strings.Join(rebuilt, "\n") != sysText {
		t.Fatalf("parts 拼回去不等于 text(%d 段 vs %d rune 的整段)", len(rebuilt), len(sysText))
	}
	if injected == 0 {
		t.Fatal("parts 里没有任何 injected=true 的段:工作目录/环境块至少要有一段")
	}

	// part=tools:真实的工具定义(session.ContextPlan,同一真相源)。
	code, tools := ctxGet(t, detail+"?part=tools")
	if code != http.StatusOK {
		t.Fatalf("part=tools status = %d, want 200: %v", code, tools)
	}
	list, ok := tools["tools"].([]any)
	if !ok {
		t.Fatalf("part=tools 缺少 tools 数组: %v", tools)
	}
	if len(list) == 0 {
		t.Fatalf("part=tools 返回空列表(脚手架会话应当有工具定义): %v", tools)
	}
	first, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("tools[0] 不是对象: %v", list[0])
	}
	for _, k := range []string{"name", "description", "tokens"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("tools[0] 缺少契约字段 %q: %v", k, first)
		}
	}
	if name, _ := first["name"].(string); name == "" {
		t.Fatalf("tools[0].name 为空: %v", first)
	}
	if total, ok := tools["total"].(float64); !ok || total <= 0 {
		t.Fatalf("part=tools total = %v, want > 0", tools["total"])
	}

	// part=messages:逐条明细(条数 + tokens + 字符数 + 预览)。
	code, ml := ctxGet(t, detail+"?part=messages")
	if code != http.StatusOK {
		t.Fatalf("part=messages status = %d, want 200: %v", code, ml)
	}
	rows, ok := ml["messages"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("part=messages 缺少 messages 数组或为空: %v", ml)
	}
	row0, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0] 不是对象: %v", rows[0])
	}
	for _, k := range []string{"index", "role", "tokens", "chars", "preview"} {
		if _, ok := row0[k]; !ok {
			t.Fatalf("messages[0] 缺少契约字段 %q: %v", k, row0)
		}
	}
	if role, _ := row0["role"].(string); role == "" {
		t.Fatalf("messages[0].role 为空: %v", row0)
	}
	if tk, ok := row0["tokens"].(float64); !ok || tk <= 0 {
		t.Fatalf("messages[0].tokens = %v, want > 0", row0["tokens"])
	}
	if idx, ok := row0["index"].(float64); !ok || idx != 0 {
		t.Fatalf("messages[0].index = %v, want 0(全量会话里的下标)", row0["index"])
	}
	if got, ok := ml["total"].(float64); !ok || int(got) != len(msgs) {
		t.Fatalf("part=messages total = %v, want %d", ml["total"], len(msgs))
	}
	if got, ok := ml["returned"].(float64); !ok || int(got) != len(rows) {
		t.Fatalf("part=messages returned = %v, want %d", ml["returned"], len(rows))
	}
	if _, ok := ml["truncated"].(bool); !ok {
		t.Fatalf("part=messages 缺少 truncated 布尔字段: %v", ml)
	}
	if _, ok := ml["has_more"].(bool); !ok {
		t.Fatalf("part=messages 缺少 has_more 布尔字段: %v", ml)
	}
	if _, ok := ml["role"].(string); !ok {
		t.Fatalf("part=messages 应回显 role(未过滤时为空串): %v", ml)
	}
	if est, ok := row0["est"].(bool); !ok || est {
		t.Fatalf("短消息的 est = %v (present=%v), want false", row0["est"], ok)
	}

	// part=messages&i=0:单条全文 = 系统提示词本体。
	code, one := ctxGet(t, detail+"?part=messages&i=0")
	if code != http.StatusOK {
		t.Fatalf("part=messages&i=0 status = %d, want 200: %v", code, one)
	}
	if one["part"] != "message" {
		t.Fatalf("part = %v, want \"message\"(单条全文)", one["part"])
	}
	if one["index"] != float64(0) || one["role"] != "system" {
		t.Fatalf("单条全文 = %+v, want index 0 / role system", one)
	}
	if text, _ := one["text"].(string); text != sysText {
		t.Fatal("i=0 的全文与 part=system 的系统提示词不一致")
	}
	if one["truncated"] != false {
		t.Fatalf("短消息的 truncated = %v, want false", one["truncated"])
	}
	if tk, ok := one["tokens"].(float64); !ok || tk <= 0 {
		t.Fatalf("单条全文 tokens = %v, want > 0", one["tokens"])
	}

	// 用户那条消息:全文里能看到用户说了什么。
	userIdx := -1
	for i, m := range msgs {
		if m.Role == "user" {
			userIdx = i
			break
		}
	}
	if userIdx < 0 {
		t.Fatal("会话里没有 user 消息,脚手架失效")
	}
	code, one = ctxGet(t, detail+"?part=messages&i="+strconv.Itoa(userIdx))
	if code != http.StatusOK {
		t.Fatalf("part=messages&i=%d status = %d, want 200: %v", userIdx, code, one)
	}
	if text, _ := one["text"].(string); !strings.Contains(text, "说一句你好") {
		t.Fatalf("i=%d 的全文没有用户原话: %q", userIdx, text)
	}

	// 越界 → 404;不是数字 → 400;未知会话 → 404;part 非法/缺失 → 400。
	if code, _ = ctxGet(t, detail+"?part=messages&i=99999"); code != http.StatusNotFound {
		t.Fatalf("越界 i status = %d, want 404", code)
	}
	if code, _ = ctxGet(t, detail+"?part=messages&i=abc"); code != http.StatusBadRequest {
		t.Fatalf("非数字 i status = %d, want 400", code)
	}
	if code, _ = ctxGet(t, detail+"?part=bogus"); code != http.StatusBadRequest {
		t.Fatalf("part=bogus status = %d, want 400", code)
	}
	if code, _ = ctxGet(t, detail); code != http.StatusBadRequest {
		t.Fatalf("缺少 part status = %d, want 400", code)
	}
	if code, _ = ctxGet(t, api.URL+"/api/sessions/nope/context/detail?part=system"); code != http.StatusNotFound {
		t.Fatalf("未知会话 status = %d, want 404", code)
	}
}

// TestSessionContextRoundMerge covers the「0 步空轮」bug: when auto compaction
// fires twice back to back, no loop ran in between, so the second compaction
// must NOT open a round of its own — it belongs to the round before it, and its
// saving adds up. Those empty rows read as "第2轮 · 0步 · 顶到0K · 压缩−181.5K",
// which looked like a broken ledger.
func TestSessionContextRoundMerge(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("merge-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()
	store := be.mgr.GetStore()

	usage := func(prompt, completion, turn int) {
		t.Helper()
		if _, err := store.AppendEvent(id, session.EventAssistantMessage, turn, map[string]any{
			"content": "x",
			"usage": map[string]any{
				"prompt_tokens": prompt, "completion_tokens": completion,
				"cached_tokens": 0, "cache_reported": true,
			},
		}); err != nil {
			t.Fatalf("AppendEvent assistant: %v", err)
		}
	}
	compact := func(before, after, turn int) {
		t.Helper()
		if _, err := store.AppendEvent(id, session.EventCompaction, turn, map[string]any{
			"before_tokens": before, "after_tokens": after, "kind": "auto",
		}); err != nil {
			t.Fatalf("AppendEvent compaction: %v", err)
		}
	}

	usage(5000, 30, 1)
	usage(6000, 40, 2)
	compact(5000, 4900, 2) // 回收 100
	compact(4900, 4700, 2) // 紧接着再来一次,回收 200 —— 中间没有 loop
	usage(4800, 20, 3)     // 这一段才是下一轮

	var resp struct {
		Steps  int `json:"steps"`
		Rounds []struct {
			Round      int  `json:"round"`
			Steps      int  `json:"steps"`
			PeakTokens int  `json:"peak_tokens"`
			Reclaimed  int  `json:"reclaimed"`
			Compacted  bool `json:"compacted"`
		} `json:"rounds"`
	}
	if err := getJSON(api.URL+"/api/sessions/"+id+"/context", &resp); err != nil {
		t.Fatalf("GET context: %v", err)
	}
	if resp.Steps != 3 {
		t.Fatalf("steps = %d, want 3", resp.Steps)
	}
	if len(resp.Rounds) != 2 {
		t.Fatalf("连续压缩应只算一轮,rounds = %+v", resp.Rounds)
	}
	r1, r2 := resp.Rounds[0], resp.Rounds[1]
	if r1.Round != 1 || r1.Steps != 2 || !r1.Compacted || r1.Reclaimed != 300 {
		t.Fatalf("round1 = %+v, want 2步/compacted/reclaimed=100+200=300", r1)
	}
	if r1.PeakTokens != 6000 {
		t.Fatalf("round1 peak = %d, want 6000", r1.PeakTokens)
	}
	if r2.Round != 2 || r2.Steps != 1 || r2.Compacted || r2.Reclaimed != 0 {
		t.Fatalf("round2 = %+v, want 1步/未压缩/reclaimed=0", r2)
	}
}

// TestSessionContextStats covers the drawer's time account: how long the model
// held the session, how long the tools did, and the average time to the first
// token. Also pins that a pruned tool result (the "replaces" copy) is not timed
// twice, and that stats is an object — never null — even for a session that has
// never run.
func TestSessionContextStats(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("stats-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()
	store := be.mgr.GetStore()

	if _, err := store.AppendEvent(id, session.EventAssistantMessage, 1, map[string]any{
		"content": "x", "usage": map[string]any{
			"prompt_tokens": 1000, "completion_tokens": 10,
			"cache_reported": false, "call_ms": 1200, "ttft_ms": 300,
		},
	}); err != nil {
		t.Fatalf("AppendEvent 1: %v", err)
	}
	if _, err := store.AppendEvent(id, session.EventToolResult, 1, map[string]any{
		"call_id": "c1", "name": "read", "ok": true, "duration_ms": 500, "result": "…",
	}); err != nil {
		t.Fatalf("AppendEvent 2: %v", err)
	}
	// 同一次调用的裁剪替换行:账本规则是「后一行折叠掉原行」,计时不能翻倍。
	if _, err := store.AppendEvent(id, session.EventToolResult, 1, map[string]any{
		"call_id": "c1", "name": "read", "ok": true, "duration_ms": 9999,
		"result": "剪短了", "replaces": "c1",
	}); err != nil {
		t.Fatalf("AppendEvent 3: %v", err)
	}
	if _, err := store.AppendEvent(id, session.EventAssistantMessage, 2, map[string]any{
		"content": "y", "usage": map[string]any{
			"prompt_tokens": 1500, "completion_tokens": 20,
			"cache_reported": false, "call_ms": 800,
		},
	}); err != nil {
		t.Fatalf("AppendEvent 4: %v", err)
	}
	if _, err := store.AppendEvent(id, session.EventToolResult, 2, map[string]any{
		"call_id": "c2", "name": "pwsh", "ok": true, "duration_ms": 250, "result": "ok",
	}); err != nil {
		t.Fatalf("AppendEvent 5: %v", err)
	}

	var resp struct {
		Stats *struct {
			ModelMs   int64 `json:"model_ms"`
			ToolMs    int64 `json:"tool_ms"`
			TtftAvgMs int64 `json:"ttft_avg_ms"`
		} `json:"stats"`
	}
	if err := getJSON(api.URL+"/api/sessions/"+id+"/context", &resp); err != nil {
		t.Fatalf("GET context: %v", err)
	}
	if resp.Stats == nil {
		t.Fatal("stats = null, want 对象(永不为 null)")
	}
	if resp.Stats.ModelMs != 2000 {
		t.Fatalf("model_ms = %d, want 1200+800=2000", resp.Stats.ModelMs)
	}
	if resp.Stats.ToolMs != 750 {
		t.Fatalf("tool_ms = %d, want 500+250=750(replaces 行不重复计时)", resp.Stats.ToolMs)
	}
	if resp.Stats.TtftAvgMs != 300 {
		t.Fatalf("ttft_avg_ms = %d, want 300(只对测到 TTFT 的那次求平均)", resp.Stats.TtftAvgMs)
	}

	// 从没跑过的会话:stats 仍是零值对象,前端不用判空。
	fresh, err := be.mgr.CreateSessionRecord("stats-empty", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord 2: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := getJSON(api.URL+"/api/sessions/"+fresh.ID()+"/context", &raw); err != nil {
		t.Fatalf("GET context (empty): %v", err)
	}
	if got := string(raw["stats"]); got != `{"model_ms":0,"tool_ms":0,"ttft_avg_ms":0}` {
		t.Fatalf("空会话 stats = %s, want 零值对象", got)
	}
}

// TestSessionContextMessagesPaging covers the paged, fast message view: a page
// is bounded by &limit (default 50, clamped to 1..200), older pages come from
// &before, &role filters one role, and has_more says whether an older page
// exists. Oversized rows are estimated rather than counted rune by rune, and
// say so with est=true.
func TestSessionContextMessagesPaging(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("page-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	// 直接给会话塞历史:分页逻辑只读 SnapshotMessages,不需要真跑 60 轮。
	rec := sess.Record()
	msgs := make([]provider.Message, 0, 62)
	msgs = append(msgs, provider.Message{Role: "system", Content: "系统提示词"})
	for i := 1; i <= 60; i++ {
		role := "assistant"
		if i%2 == 1 {
			role = "user"
		}
		msgs = append(msgs, provider.Message{Role: role, Content: fmt.Sprintf("第%d条消息", i)})
	}
	rec.Messages = msgs
	d := api.URL + "/api/sessions/" + sess.ID() + "/context/detail"

	// 默认页 = 最新 50 条,更前面还有。
	_, p := ctxGet(t, d+"?part=messages")
	if int(p["total"].(float64)) != 61 {
		t.Fatalf("total = %v, want 61", p["total"])
	}
	rows := p["messages"].([]any)
	if len(rows) != 50 || int(p["returned"].(float64)) != 50 {
		t.Fatalf("默认页 returned = %v/%d 条, want 50", p["returned"], len(rows))
	}
	if p["has_more"] != true {
		t.Fatalf("默认页 has_more = %v, want true", p["has_more"])
	}
	if first := rows[0].(map[string]any)["index"]; first != float64(11) {
		t.Fatalf("默认页第一条 index = %v, want 11(升序返回)", first)
	}
	if last := rows[49].(map[string]any)["index"]; last != float64(60) {
		t.Fatalf("默认页最后一条 index = %v, want 60", last)
	}
	if role, _ := p["role"].(string); role != "" {
		t.Fatalf("未过滤时 role = %q, want 空串", role)
	}

	// 更早一页:before=11 只要 index<11 的那段,扫到头 → 没有更前面了。
	_, p = ctxGet(t, d+"?part=messages&before=11")
	rows = p["messages"].([]any)
	if len(rows) != 11 || p["has_more"] != false {
		t.Fatalf("before=11 页 = %d 条/has_more=%v, want 11/false", len(rows), p["has_more"])
	}
	if rows[0].(map[string]any)["index"] != float64(0) {
		t.Fatalf("before=11 页首条 = %v, want index 0", rows[0])
	}

	// role 过滤 + 小页:倒着扫够数就停,has_more 如实。
	_, p = ctxGet(t, d+"?part=messages&role=user&limit=5")
	rows = p["messages"].([]any)
	if len(rows) != 5 || p["has_more"] != true || p["role"] != "user" {
		t.Fatalf("role=user&limit=5 → %d 条/has_more=%v/role=%v, want 5/true/user", len(rows), p["has_more"], p["role"])
	}
	wantIdx := []float64{51, 53, 55, 57, 59}
	for i, want := range wantIdx {
		row := rows[i].(map[string]any)
		if row["index"] != want || row["role"] != "user" {
			t.Fatalf("rows[%d] = %+v, want index %v role user", i, row, want)
		}
	}

	// limit 夹到 1..200:要得再多也只有 61 条,要 0 条也给 1 条。
	if _, p = ctxGet(t, d+"?part=messages&limit=1000"); len(p["messages"].([]any)) != 61 {
		t.Fatalf("limit=1000 应返回全部 61 条: %v", p["returned"])
	}
	if _, p = ctxGet(t, d+"?part=messages&limit=0"); len(p["messages"].([]any)) != 1 {
		t.Fatalf("limit=0 应夹成 1 条: %v", p["returned"])
	}
	if code, _ := ctxGet(t, d+"?part=messages&limit=abc"); code != http.StatusBadRequest {
		t.Fatalf("非数字 limit status = %d, want 400", code)
	}
	if code, _ := ctxGet(t, d+"?part=messages&before=abc"); code != http.StatusBadRequest {
		t.Fatalf("非数字 before status = %d, want 400", code)
	}

	// 超长消息:按前缀外推,est=true;普通消息 est=false。
	big, err := be.mgr.CreateSessionRecord("est-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord 2: %v", err)
	}
	bigRec := big.Record()
	bigRec.Messages = []provider.Message{
		{Role: "system", Content: "系统提示词"},
		{Role: "tool", Content: strings.Repeat("长输出", 12000)}, // 36000 rune,远超外推阈值
	}
	_, bp := ctxGet(t, api.URL+"/api/sessions/"+big.ID()+"/context/detail?part=messages")
	brows := bp["messages"].([]any)
	if len(brows) != 2 {
		t.Fatalf("超长会话应有 2 条: %v", bp["returned"])
	}
	small := brows[0].(map[string]any)
	huge := brows[1].(map[string]any)
	if small["est"] != false {
		t.Fatalf("短消息 est = %v, want false", small["est"])
	}
	if huge["est"] != true {
		t.Fatalf("超长消息 est = %v, want true", huge["est"])
	}
	if chars := huge["chars"]; chars != float64(36000) {
		t.Fatalf("超长消息 chars = %v, want 36000(rune 数,不是字节)", chars)
	}
	if tk := huge["tokens"].(float64); tk <= 0 {
		t.Fatalf("超长消息 tokens = %v, want > 0", huge["tokens"])
	}
	// 全文视图:token 仍是精确计数(不受列表外推影响),文本按 32000 rune 截断。
	_, one := ctxGet(t, api.URL+"/api/sessions/"+big.ID()+"/context/detail?part=messages&i=1")
	if one["part"] != "message" {
		t.Fatalf("超长消息全文 part = %v, want message", one["part"])
	}
	if one["truncated"] != true {
		t.Fatalf("36000 rune 的全文 truncated = %v, want true(上限 32000 rune)", one["truncated"])
	}
	if txt, _ := one["text"].(string); utf8.RuneCountInString(txt) != 32000 {
		t.Fatalf("截断后文本长度 = %d rune, want 32000", utf8.RuneCountInString(txt))
	}
	if exact, ok := one["tokens"].(float64); !ok || exact <= 0 {
		t.Fatalf("全文 tokens = %v, want > 0", one["tokens"])
	}
}

// TestSessionContextCompactionNoise pins the fix for the「一轮里成对压缩事件」
// complaint: the loop writes a phase:"start" heartbeat before doing the work
// (no before/after keys — it rendered as a noise「auto压缩 0 → 0」row and,
// worse, closed the round as a false boundary). The read model must drop it
// entirely: one real compaction = one event row, and an orphan start (task
// cancelled before done) must not split a round. A no-progress done row
// (before==after, a REAL zero reclaim) must survive.
func TestSessionContextCompactionNoise(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("noise-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()
	store := be.mgr.GetStore()
	add := func(typ string, turn int, p map[string]any) {
		t.Helper()
		if _, err := store.AppendEvent(id, typ, turn, p); err != nil {
			t.Fatalf("AppendEvent %s: %v", typ, err)
		}
	}

	add(session.EventAssistantMessage, 1, map[string]any{"content": "x",
		"usage": map[string]any{"prompt_tokens": 9000, "completion_tokens": 30, "cache_reported": false}})
	// 一次真实压缩 = start 心跳 + done 真行:抽屉里只能看到一条事件,轮不能被劈开。
	add(session.EventCompaction, 1, map[string]any{"phase": "start", "kind": "auto", "tokens": 9000})
	add(session.EventCompaction, 1, map[string]any{"phase": "done", "kind": "auto",
		"before_tokens": 9000, "after_tokens": 8800}) // 真回收 200(<1K,必须保留)
	add(session.EventAssistantMessage, 2, map[string]any{"content": "y",
		"usage": map[string]any{"prompt_tokens": 8800, "completion_tokens": 10, "cache_reported": false}})
	// 孤立 start(压缩前任务取消):不是事件,更不是轮边界。
	add(session.EventCompaction, 2, map[string]any{"phase": "start", "kind": "auto", "tokens": 8800})
	// 无进展的真 done:before==after,回收确实是 0,真事件保留。
	add(session.EventCompaction, 2, map[string]any{"phase": "done", "kind": "auto",
		"before_tokens": 8800, "after_tokens": 8800})

	var resp struct {
		Events []struct {
			Kind   string `json:"kind"`
			Round  int    `json:"round"`
			Tokens int    `json:"tokens"`
			Detail string `json:"detail"`
		} `json:"events"`
		Rounds []struct {
			Round     int  `json:"round"`
			Steps     int  `json:"steps"`
			Compacted bool `json:"compacted"`
			Reclaimed int  `json:"reclaimed"`
		} `json:"rounds"`
	}
	if err := getJSON(api.URL+"/api/sessions/"+id+"/context", &resp); err != nil {
		t.Fatalf("GET context: %v", err)
	}
	if len(resp.Events) != 2 {
		t.Fatalf("压缩事件行 = %d(%+v), want 2(两条真 done,start 心跳全隐藏)", len(resp.Events), resp.Events)
	}
	if resp.Events[0].Kind != "compact" || resp.Events[0].Round != 1 || resp.Events[0].Tokens != 200 {
		t.Fatalf("events[0] = %+v, want compact/round1/回收200(<1K 真回收不能被过滤)", resp.Events[0])
	}
	if resp.Events[1].Round != 2 {
		t.Fatalf("events[1].round = %d, want 2(第二条 done 属于第二轮)", resp.Events[1].Round)
	}
	// 两轮:第 1 轮(1步,压缩回收200)+ 第 2 轮(1步,无进展压缩收尾,回收0)。
	if len(resp.Rounds) != 2 {
		t.Fatalf("rounds = %+v, want 2 轮(孤立 start 不得新开一轮)", resp.Rounds)
	}
	if resp.Rounds[0].Round != 1 || resp.Rounds[0].Steps != 1 || !resp.Rounds[0].Compacted || resp.Rounds[0].Reclaimed != 200 {
		t.Fatalf("round1 = %+v, want 1步/compacted/200", resp.Rounds[0])
	}
	if resp.Rounds[1].Round != 2 || resp.Rounds[1].Steps != 1 || resp.Rounds[1].Reclaimed != 0 {
		t.Fatalf("round2 = %+v, want 1步/回收0", resp.Rounds[1])
	}
}

// TestSessionTrajectory covers GET /api/sessions/{id}/trajectory: the ledger
// as a dsh-style step stream — task rows carry the user's goal, assistant
// rows carry the model's text (or its tool-call names), one row per tool call
// folds its result onto it (prune replacements too), compaction done rows
// advance the round. Pages are bounded and walk toward the past via &before.
func TestSessionTrajectory(t *testing.T) {
	be := newTestBackend(t)
	api := httptest.NewServer(NewServer(be))
	defer api.Close()

	sess, err := be.mgr.CreateSessionRecord("traj-test", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	id := sess.ID()
	store := be.mgr.GetStore()
	add := func(typ string, turn int, p map[string]any) {
		t.Helper()
		if _, err := store.AppendEvent(id, typ, turn, p); err != nil {
			t.Fatalf("AppendEvent %s: %v", typ, err)
		}
	}

	add(session.EventTaskStart, 0, map[string]any{"goal": "第一条用户任务,带一点长度来看截断", "model": "m-a"})
	add(session.EventToolCall, 1, map[string]any{"call_id": "c1", "name": "read", "args": `{"file_path":"D:\\某文件.txt"}`})
	add(session.EventToolResult, 1, map[string]any{"call_id": "c1", "name": "read", "ok": true, "duration_ms": 420, "result": "文件内容第一行"})
	add(session.EventToolResult, 1, map[string]any{"call_id": "c1", "name": "read", "ok": true, "duration_ms": 9999, "result": "剪短后的内容", "replaces": "c1"})
	add(session.EventAssistantMessage, 1, map[string]any{"content": "",
		"tool_calls": []any{map[string]any{"id": "c2", "name": "pwsh", "args": "{}"}},
		"usage": map[string]any{"prompt_tokens": 500, "completion_tokens": 25, "cache_reported": false, "call_ms": 1500}})
	add(session.EventCompaction, 1, map[string]any{"phase": "start", "kind": "auto"}) // 噪声行不得出现在轨迹里
	add(session.EventCompaction, 1, map[string]any{"phase": "done", "kind": "auto",
		"before_tokens": 205200, "after_tokens": 13400})
	add(session.EventTaskStart, 0, map[string]any{"goal": "第二条任务", "model": "m-a"})
	add(session.EventAssistantMessage, 1, map[string]any{"content": "第二轮回话的第一行\n第二行被截掉",
		"usage": map[string]any{"prompt_tokens": 300, "completion_tokens": 8, "cache_reported": false}})

	code, resp := ctxGet(t, api.URL+"/api/sessions/"+id+"/trajectory")
	if code != http.StatusOK {
		t.Fatalf("trajectory status = %d, want 200: %v", code, resp)
	}
	rows, ok := resp["rows"].([]any)
	if !ok {
		t.Fatalf("rows 缺数组: %v", resp)
	}
	if resp["has_more"] != false {
		t.Fatalf("has_more = %v, want false(未分页)", resp["has_more"])
	}
	if resp["steps"] != float64(2) || resp["rounds"] != float64(2) {
		t.Fatalf("steps/rounds = %v/%v, want 2/2", resp["steps"], resp["rounds"])
	}
	// 期望:task, tool(c1 折叠 result+replacement), assistant([工具调用] pwsh),
	// compact, task, assistant(文本首行)。start 噪声与重复 result 都被吃掉。
	if len(rows) != 6 {
		t.Fatalf("rows = %d 行, want 6: %+v", len(rows), rows)
	}
	kindAt := func(i int) string { return rows[i].(map[string]any)["kind"].(string) }
	for i, want := range []string{"task", "tool", "assistant", "compact", "task", "assistant"} {
		if k := kindAt(i); k != want {
			t.Fatalf("rows[%d].kind = %q, want %q", i, k, want)
		}
	}
	r0 := rows[0].(map[string]any)
	if title, _ := r0["title"].(string); !strings.Contains(title, "第一条用户任务") {
		t.Fatalf("task title = %q, want 用户目标开头", title)
	}
	r1 := rows[1].(map[string]any)
	if title, _ := r1["title"].(string); !strings.Contains(title, "read") || !strings.Contains(title, "file_path") {
		t.Fatalf("tool title = %q, want 名字+参数摘要", title)
	}
	if sub, _ := r1["sub"].(string); !strings.Contains(sub, "剪短后的内容") {
		t.Fatalf("tool sub = %q, want 折叠后的替换行摘要", sub)
	}
	if d := r1["dur_ms"]; d != nil && d.(float64) == 9999 {
		t.Fatalf("replaces 行不该覆盖计时: dur_ms = %v", d)
	}
	r2 := rows[2].(map[string]any)
	if title, _ := r2["title"].(string); !strings.Contains(title, "[工具调用] pwsh") {
		t.Fatalf("纯工具调用回合 title = %q, want [工具调用] pwsh", title)
	}
	if tk := r2["tokens"]; tk != float64(25) {
		t.Fatalf("assistant tokens = %v, want 25(completion_tokens)", tk)
	}
	r3 := rows[3].(map[string]any)
	if title, _ := r3["title"].(string); !strings.Contains(title, "205.2K → 13.4K") {
		t.Fatalf("compact title = %q", title)
	}
	if r3["round"] != float64(1) || rows[4].(map[string]any)["round"] != float64(2) {
		t.Fatalf("压缩前后的 round = %v/%v, want 1/2", r3["round"], rows[4].(map[string]any)["round"])
	}
	r5 := rows[5].(map[string]any)
	if title, _ := r5["title"].(string); title != "第二轮回话的第一行" {
		t.Fatalf("多行文本 title = %q, want 压平到首行", title)
	}
	for _, k := range []string{"seq", "kind", "round", "title"} {
		if _, ok := r5[k]; !ok {
			t.Fatalf("rows 缺契约字段 %q: %v", k, r5)
		}
	}

	// 分页:limit=2 → 最新两行 + has_more;before=<最早拿到的 seq> 走回头页。
	_, p := ctxGet(t, api.URL+"/api/sessions/"+id+"/trajectory?limit=2")
	prows := p["rows"].([]any)
	if len(prows) != 2 || p["has_more"] != true {
		t.Fatalf("limit=2 → %d 行/has_more=%v, want 2/true", len(prows), p["has_more"])
	}
	oldest := prows[0].(map[string]any)["seq"].(float64)
	_, p2 := ctxGet(t, fmt.Sprintf("%s/api/sessions/%s/trajectory?limit=2&before=%.0f", api.URL, id, oldest))
	p2rows := p2["rows"].([]any)
	if len(p2rows) != 2 || p2["has_more"] != true {
		t.Fatalf("回头页 = %d 行/has_more=%v, want 2/true", len(p2rows), p2["has_more"])
	}
	if p2rows[1].(map[string]any)["seq"].(float64) >= oldest {
		t.Fatal("回头页的 seq 必须严格早于 before")
	}

	// 参数错误与未知会话。
	if code, _ = ctxGet(t, api.URL+"/api/sessions/"+id+"/trajectory?limit=abc"); code != http.StatusBadRequest {
		t.Fatalf("非数字 limit status = %d, want 400", code)
	}
	if code, _ = ctxGet(t, api.URL+"/api/sessions/"+id+"/trajectory?before=x"); code != http.StatusBadRequest {
		t.Fatalf("非数字 before status = %d, want 400", code)
	}
	if code, _ = ctxGet(t, api.URL+"/api/sessions/nope/trajectory"); code != http.StatusNotFound {
		t.Fatalf("未知会话 status = %d, want 404", code)
	}
}
