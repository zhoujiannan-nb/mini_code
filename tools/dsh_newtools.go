package tools


import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)


type todoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

type TodoTool struct {
	mu        sync.Mutex
	items     []todoItem
	onChanged func()
}

func NewTodoTool() *TodoTool { return &TodoTool{} }

func (t *TodoTool) SetOnChanged(f func()) {
	t.mu.Lock()
	t.onChanged = f
	t.mu.Unlock()
}

func (t *TodoTool) Snapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.items) == 0 {
		return ""
	}
	b, err := json.Marshal(t.items)
	if err != nil {
		return ""
	}
	return string(b)
}

func (t *TodoTool) Restore(s string) {
	if s == "" {
		return
	}
	var items []todoItem
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return
	}
	t.mu.Lock()
	t.items = items
	t.mu.Unlock()
}

func (t *TodoTool) Name() string                       { return "todo_write" }
func (t *TodoTool) Description() string                { return dshDesc("todo_write") }
func (t *TodoTool) Parameters() map[string]interface{} { return dshParams("todo_write") }
func (t *TodoTool) IsHidden() bool                     { return false }

func (t *TodoTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	raw, _ := params["todos"].([]interface{})
	items := make([]todoItem, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		content, _ := m["content"].(string)
		status, _ := m["status"].(string)
		if strings.TrimSpace(content) == "" {
			return NewTextResult("Error: invalid todo: `content` must be a non-empty string"), nil
		}
		if seen[content] {
			dup, _ := json.Marshal(content)
			return NewTextResult("Error: invalid todos: duplicate content " + string(dup)), nil
		}
		seen[content] = true
		switch status {
		case "pending", "in_progress", "completed":
		default:
			return NewTextResult(fmt.Sprintf("Error: invalid status %q for todo %q (pending | in_progress | completed)", status, content)), nil
		}
		items = append(items, todoItem{Content: content, Status: status})
	}
	t.mu.Lock()
	t.items = items
	hook := t.onChanged
	t.mu.Unlock()
	if hook != nil {
		hook()
	}

	var p, i, c int
	for _, it := range items {
		switch it.Status {
		case "pending":
			p++
		case "in_progress":
			i++
		case "completed":
			c++
		}
	}
	return NewTextResult(fmt.Sprintf("Updated todo list: %d pending, %d in progress, %d completed.", p, i, c)), nil
}


type goalRecord struct {
	ID            string `json:"goal_id"`
	Revision      int    `json:"revision"`
	Objective     string `json:"objective"`
	MaxGoalRounds int    `json:"max_goal_rounds,omitempty"`
	Status        string `json:"status"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

const DefaultGoalMaxRounds = 8

const MinBlockedRounds = 3

type GoalSnapshot struct {
	Cur    *goalRecord `json:"cur"`
	Rounds int         `json:"rounds"`
}

type GoalStore struct {
	mu        sync.Mutex
	cur       *goalRecord
	rounds    int
	autoRound bool
	onChanged func()
}

func NewGoalStore() *GoalStore { return &GoalStore{} }

func (s *GoalStore) SetOnChanged(f func()) {
	s.mu.Lock()
	s.onChanged = f
	s.mu.Unlock()
}

func (s *GoalStore) fire() {
	s.mu.Lock()
	f := s.onChanged
	s.mu.Unlock()
	if f != nil {
		f()
	}
}

func (s *GoalStore) SetAutoRound(auto bool) {
	s.mu.Lock()
	s.autoRound = auto
	s.mu.Unlock()
}

func (s *GoalStore) View() (*goalRecord, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.rounds
}

func (s *GoalStore) MaxRounds() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return 0
	}
	if s.cur.MaxGoalRounds > 0 {
		return s.cur.MaxGoalRounds
	}
	return DefaultGoalMaxRounds
}

func (s *GoalStore) BeginRound() {
	s.mu.Lock()
	s.rounds++
	f := s.onChanged
	s.mu.Unlock()
	if f != nil {
		f()
	}
}

func (s *GoalStore) Snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return ""
	}
	b, err := json.Marshal(GoalSnapshot{Cur: s.cur, Rounds: s.rounds})
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *GoalStore) Restore(str string) {
	if str == "" {
		return
	}
	var snap GoalSnapshot
	if err := json.Unmarshal([]byte(str), &snap); err != nil {
		return
	}
	s.mu.Lock()
	s.cur = snap.Cur
	s.rounds = snap.Rounds
	s.mu.Unlock()
}

func goalJSON(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

type CreateGoalTool struct{ store *GoalStore }

func NewCreateGoalTool(s *GoalStore) *CreateGoalTool { return &CreateGoalTool{store: s} }

func (t *CreateGoalTool) Name() string                       { return "create_goal" }
func (t *CreateGoalTool) Description() string                { return dshDesc("create_goal") }
func (t *CreateGoalTool) Parameters() map[string]interface{} { return dshParams("create_goal") }
func (t *CreateGoalTool) IsHidden() bool                     { return false }

func (t *CreateGoalTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	objective := strParam(params, "objective")
	if strings.TrimSpace(objective) == "" {
		return NewTextResult("Error: missing required parameter 'objective'"), nil
	}
	t.store.mu.Lock()
	if t.store.cur != nil && (t.store.cur.Status == "active") {
		g := t.store.cur
		t.store.mu.Unlock()
		return NewTextResult("Error: a goal is already active (" + g.ID + "). Use update_goal to change it."), nil
	}
	rounds := 0
	if v, ok := params["max_goal_rounds"].(float64); ok {
		rounds = int(v)
	}
	g := &goalRecord{
		ID:            fmt.Sprintf("goal-%d", time.Now().UnixNano()),
		Revision:      1,
		Objective:     objective,
		MaxGoalRounds: rounds,
		Status:        "active",
	}
	t.store.cur = g
	t.store.rounds = 0
	t.store.mu.Unlock()
	t.store.fire()
	return NewTextResult(goalJSON(g)), nil
}

type GetGoalTool struct{ store *GoalStore }

func NewGetGoalTool(s *GoalStore) *GetGoalTool { return &GetGoalTool{store: s} }

func (t *GetGoalTool) Name() string                       { return "get_goal" }
func (t *GetGoalTool) Description() string                { return dshDesc("get_goal") }
func (t *GetGoalTool) Parameters() map[string]interface{} { return dshParams("get_goal") }
func (t *GetGoalTool) IsHidden() bool                     { return false }

func (t *GetGoalTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	g, rounds := t.store.View()
	if g == nil {
		return NewTextResult("No goal exists for this session."), nil
	}
	return NewTextResult(goalJSON(struct {
		goalRecord
		GoalRounds int `json:"goal_rounds_delivered"`
	}{*g, rounds})), nil
}

type UpdateGoalTool struct{ store *GoalStore }

func NewUpdateGoalTool(s *GoalStore) *UpdateGoalTool { return &UpdateGoalTool{store: s} }

func (t *UpdateGoalTool) Name() string                       { return "update_goal" }
func (t *UpdateGoalTool) Description() string                { return dshDesc("update_goal") }
func (t *UpdateGoalTool) Parameters() map[string]interface{} { return dshParams("update_goal") }
func (t *UpdateGoalTool) IsHidden() bool                     { return false }

func (t *UpdateGoalTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	id := strParam(params, "goal_id")
	action := strParam(params, "action")
	rev, _ := params["revision"].(float64)

	t.store.mu.Lock()
	auto := t.store.autoRound
	rounds := t.store.rounds
	t.store.mu.Unlock()
	switch action {
	case "edit", "pause", "resume":
		if auto {
			return NewTextResult(fmt.Sprintf("Error: action %q requires a direct top-level human request; this is an automatic continuation round. Keep working on the goal, or end the round.", action)), nil
		}
	case "blocked":
		if auto && rounds < MinBlockedRounds {
			return NewTextResult(fmt.Sprintf("Error: blocked is rejected before %d automatic rounds have been tried (this goal has had %d). Keep working or end the round; the driver will continue.", MinBlockedRounds, rounds)), nil
		}
	}

	t.store.mu.Lock()
	g := t.store.cur
	if g == nil {
		t.store.mu.Unlock()
		return NewTextResult("Error: no goal exists for this session. create_goal first."), nil
	}
	if id != g.ID {
		t.store.mu.Unlock()
		return NewTextResult("Error: goal_id does not match the current goal. Call get_goal."), nil
	}
	if int(rev) != g.Revision {
		t.store.mu.Unlock()
		return NewTextResult(fmt.Sprintf("Error: stale revision %d (current is %d). Call get_goal.", int(rev), g.Revision)), nil
	}
	switch action {
	case "edit":
		if obj := strParam(params, "objective"); obj != "" {
			g.Objective = obj
		}
		if v, ok := params["max_goal_rounds"].(float64); ok {
			g.MaxGoalRounds = int(v)
		}
	case "pause":
		if g.Status != "active" {
			t.store.mu.Unlock()
			return NewTextResult("Error: only an active goal can be paused."), nil
		}
		g.Status = "paused"
	case "resume":
		if g.Status != "paused" {
			t.store.mu.Unlock()
			return NewTextResult("Error: only a paused goal can be resumed."), nil
		}
		g.Status = "active"
	case "complete":
		g.Status = "completed"
	case "blocked":
		reason := strParam(params, "blocked_reason")
		if reason == "" {
			t.store.mu.Unlock()
			return NewTextResult("Error: action blocked requires blocked_reason."), nil
		}
		g.Status = "blocked"
		g.BlockedReason = reason
	default:
		t.store.mu.Unlock()
		return NewTextResult("Error: unknown action (edit | pause | resume | complete | blocked)"), nil
	}
	g.Revision++
	t.store.mu.Unlock()
	t.store.fire()
	return NewTextResult(goalJSON(g)), nil
}


type WebFetchTool struct{}

func NewWebFetchTool() *WebFetchTool { return &WebFetchTool{} }

func (t *WebFetchTool) Name() string                       { return "web_fetch" }
func (t *WebFetchTool) Description() string                { return dshDesc("web_fetch") }
func (t *WebFetchTool) Parameters() map[string]interface{} { return dshParams("web_fetch") }
func (t *WebFetchTool) IsHidden() bool                     { return false }

const (
	webFetchMaxBody      = 5000000
	webFetchMaxText      = 200000
	webFetchMaxURLChars  = 2048
	webFetchTruncationFooter = "\n\n(Content truncated. Fetch a more specific URL or section for the full text.)"
)

var (
	reHTMLScript  = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(?:script|style)>`)
	reHTMLTag     = regexp.MustCompile(`<[^>]+>`)
	reWSqueeze    = regexp.MustCompile(`[ \t\r\f]+`)
	reBlankLine   = regexp.MustCompile(`\n{3,}`)
)

func (t *WebFetchTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	url := strParam(params, "url")
	if strings.TrimSpace(url) == "" {
		return NewTextResult("Error: invalid arguments: url must be a non-empty string"), nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return NewTextResult("Error: url must be an absolute http(s) URL"), nil
	}
	if len(url) > webFetchMaxURLChars {
		return NewTextResult(fmt.Sprintf("Error: invalid arguments: url must be at most %d characters", webFetchMaxURLChars)), nil
	}
	if u, perr := urlpkg.Parse(url); perr == nil && u.User != nil {
		return NewTextResult("Error: invalid arguments: url must not embed credentials"), nil
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return NewTextResult("Error: " + err.Error()), nil
	}
	req.Header.Set("User-Agent", "mini-code/1.0 (+https://github.com/user/mini_code)")
	resp, err := client.Do(req)
	if err != nil {
		return NewTextResult("Error: fetch failed: " + err.Error()), nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBody))
	if err != nil {
		return NewTextResult("Error: read failed: " + err.Error()), nil
	}
	text := string(body)
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "html") || looksHTML(text) {
		text = htmlToText(text)
	}
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > webFetchMaxText {
		text = string(r[:webFetchMaxText]) + webFetchTruncationFooter
	}
	if text == "" {
		text = "(empty body)"
	}
	return NewTextResult(fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, resp.Status, text)), nil
}

func looksHTML(s string) bool {
	h := strings.ToLower(s)
	if len(h) > 4096 {
		h = h[:4096]
	}
	return strings.Contains(h, "<html") || strings.Contains(h, "<!doctype html") || strings.Contains(h, "<body")
}

func htmlToText(s string) string {
	s = reHTMLScript.ReplaceAllString(s, " ")
	s = reHTMLTag.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ").Replace(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = reWSqueeze.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = strings.Join(lines, "\n")
	s = reBlankLine.ReplaceAllString(s, "\n\n")
	return s
}


type WorkflowTool struct{}

func NewWorkflowTool() *WorkflowTool { return &WorkflowTool{} }

func (t *WorkflowTool) Name() string                       { return "workflow" }
func (t *WorkflowTool) Description() string                { return dshDesc("workflow") }
func (t *WorkflowTool) Parameters() map[string]interface{} { return dshParams("workflow") }
func (t *WorkflowTool) IsHidden() bool                     { return false }

func (t *WorkflowTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	return NewTextResult("Error: this build does not run workflow scripts. Delegate the work with one or more subagent calls instead."), nil
}


type WorkingDirHost interface {
	SetWorkingDir(dir string)
}

type WorkingDirectoryTool struct {
	mu   sync.Mutex
	dir  string
	host WorkingDirHost
}

func NewWorkingDirectoryTool(dir string) *WorkingDirectoryTool {
	return &WorkingDirectoryTool{dir: dir}
}

func (t *WorkingDirectoryTool) SetExecHost(h WorkingDirHost) {
	t.mu.Lock()
	t.host = h
	t.mu.Unlock()
}

func (t *WorkingDirectoryTool) SetDir(dir string) {
	if dir == "" {
		return
	}
	t.mu.Lock()
	t.dir = dir
	t.mu.Unlock()
}

func (t *WorkingDirectoryTool) Name() string                       { return "working_directory" }
func (t *WorkingDirectoryTool) Description() string                { return dshDesc("working_directory") }
func (t *WorkingDirectoryTool) Parameters() map[string]interface{} { return dshParams("working_directory") }
func (t *WorkingDirectoryTool) IsHidden() bool                     { return false }

func (t *WorkingDirectoryTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cd := strParam(params, "cd")
	if cd == "" {
		return NewTextResult(t.dir), nil
	}
	target := cd
	if !filepath.IsAbs(target) {
		target = filepath.Join(t.dir, target)
	}
	target = filepath.Clean(target)
	fi, err := os.Stat(target)
	if err != nil || !fi.IsDir() {
		return NewTextResult(fmt.Sprintf("Error: %q is not an existing directory.", cd)), nil
	}
	t.dir = target
	if t.host != nil {
		t.host.SetWorkingDir(target)
	}
	return NewTextResult(target), nil
}

