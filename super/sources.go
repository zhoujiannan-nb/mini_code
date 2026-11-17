package super

// Source kinds (采集类型注册表). v9 design: a Source is a channel that turns
// the outside world into Trigger events; the runner owns the poll clock, an
// adapter only answers three things — describe itself, probe a config, and
// poll one step (reading the cursor, returning the new one). Everything else
// (routing, tickets, threads) is decided by the decision table in tickets.go,
// so a new kind never touches routing code.
//
// This round ships exactly ONE enabled kind: git-watch (本地 git 仓库分支
// 监控, ls-remote 轻轮询——无公网 webhook,与 dsh 的 Notifications 条件请求
// 同属准实时方案)。其余 kind 在此注册表预留。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Trigger is one source event in its unified contract form:
// Trigger{src_key, kind, actor, text, ts}. text is the 原话 — what gets
// delivered verbatim into the bound thread (or into the ticket).
type Trigger struct {
	SrcKey string `json:"src_key"`
	Kind   string `json:"kind"`
	Actor  string `json:"actor"` // 谁产生的（origin/main 的新提交…）
	Text   string `json:"text"`  // 事件原话
	TS     string `json:"ts"`
}

// KindAdapter is one registered source type.
type KindAdapter interface {
	Name() string
	Label() string // human name for the create-form
	Enabled() bool // false = 注册了但本期未启用（表单里置灰）
	// Probe validates a form config before the source is stored
	// (connectivity check). Return a short human summary on success.
	Probe(ctx context.Context, cfg map[string]interface{}) (string, error)
	// Poll advances one step: returns (event, newCursor, err); event==nil
	// with a new cursor means "baseline moved silently" (首见不回放).
	Poll(ctx context.Context, cfg map[string]interface{}, cursor string) (*Trigger, string, error)
}

var kindRegistry = map[string]KindAdapter{
	"git-watch": &gitWatchKind{},
	// 预留（v9 注册表位,未启用）：timer（一句话定时任务并入源框架）、
	// webhook-poll（云端中转拉取）、rss……注册即接入决策路由,零路由代码。
}

// GetKind resolves a registry key.
func GetKind(name string) (KindAdapter, error) {
	k, ok := kindRegistry[name]
	if !ok {
		return nil, fmt.Errorf("未注册的采集类型: %s", name)
	}
	return k, nil
}

// KindDescriptors feeds the UI create-form (name/label/enabled + fields).
func KindDescriptors() []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(kindRegistry))
	for _, k := range kindRegistry {
		out = append(out, map[string]interface{}{
			"name": k.Name(), "label": k.Label(), "enabled": k.Enabled(),
		})
	}
	return out
}

// --- git-watch ---

type gitWatchKind struct{}

func (k *gitWatchKind) Name() string    { return "git-watch" }
func (k *gitWatchKind) Label() string   { return "Git 分支监控（本地仓库）" }
func (k *gitWatchKind) Enabled() bool   { return true }

type gitWatchCfg struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

func parseGitCfg(cfg map[string]interface{}) (gitWatchCfg, error) {
	var g gitWatchCfg
	b, err := json.Marshal(cfg)
	if err != nil {
		return g, err
	}
	if err := json.Unmarshal(b, &g); err != nil {
		return g, fmt.Errorf("config 解析失败: %w", err)
	}
	g.Path = strings.TrimSpace(g.Path)
	g.Branch = strings.TrimSpace(g.Branch)
	if g.Path == "" {
		return g, fmt.Errorf("path 必填：本地 git 仓库目录（如 D:\\project_ai\\mini_code）")
	}
	st, err := os.Stat(g.Path)
	if err != nil || !st.IsDir() {
		return g, fmt.Errorf("仓库目录不存在: %s", g.Path)
	}
	return g, nil
}

// gitRun runs one git subcommand with a hard deadline (ls-remote touches the
// network; never let a tick hang the runner).
func gitRun(ctx context.Context, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return text, fmt.Errorf("git %s 超时(30s)", strings.Join(args, " "))
		}
		return text, fmt.Errorf("git %s: %v (%s)", strings.Join(args, " "), err, truncate(text, 200))
	}
	return text, nil
}

func (k *gitWatchKind) resolveBranch(g gitWatchCfg) (string, error) {
	if g.Branch != "" {
		return g.Branch, nil
	}
	out, err := gitRun(context.Background(), g.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if out == "HEAD" {
		return "", fmt.Errorf("仓库处于游离 HEAD，请在建源表单里指定 branch")
	}
	return out, nil
}

func (k *gitWatchKind) Probe(ctx context.Context, raw map[string]interface{}) (string, error) {
	g, err := parseGitCfg(raw)
	if err != nil {
		return "", err
	}
	if _, err := gitRun(ctx, g.Path, "rev-parse", "--git-dir"); err != nil {
		return "", fmt.Errorf("%s 不是 git 仓库", g.Path)
	}
	branch, err := func() (string, error) {
		if g.Branch != "" {
			return g.Branch, nil
		}
		out, err := gitRun(ctx, g.Path, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return "", err
		}
		if out == "HEAD" {
			return "", fmt.Errorf("仓库处于游离 HEAD，请显式指定 branch")
		}
		return out, nil
	}()
	if err != nil {
		return "", err
	}
	sha, err := k.lsRemote(ctx, g.Path, branch)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("连通正常：origin/%s 当前 %s", branch, short(sha)), nil
}

// lsRemote reads origin/<branch> without touching the working tree — the
// cheap conditional-read half of the near-realtime design (no fetch, no
// webhook, no public endpoint).
func (k *gitWatchKind) lsRemote(ctx context.Context, dir, branch string) (string, error) {
	out, err := gitRun(ctx, dir, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) < 1 {
		return "", fmt.Errorf("origin 上没有 refs/heads/%s", branch)
	}
	return fields[0], nil
}

func (k *gitWatchKind) Poll(ctx context.Context, raw map[string]interface{}, cursor string) (*Trigger, string, error) {
	g, err := parseGitCfg(raw)
	if err != nil {
		return nil, "", err
	}
	branch, err := k.resolveBranch(g)
	if err != nil {
		return nil, "", err
	}
	sha, err := k.lsRemote(ctx, g.Path, branch)
	if err != nil {
		return nil, "", err
	}
	if cursor == "" {
		// 基线：首见静默（历史事件永不回放,与 dsh Source 语义一致）。
		return nil, sha, nil
	}
	if sha == cursor {
		return nil, "", nil
	}
	tr := &Trigger{
		Kind:  k.Name(),
		Actor: "origin/" + branch,
		Text: fmt.Sprintf("监控发现 origin/%s 出现新提交 %s → %s（本地仓库 %s 的远端引用）。请 fetch 后查看 %s..%s 的差异，按本 thread 工单范围处理；范围外的事项 hand back 给发起方。",
			branch, short(cursor), short(sha), g.Path, short(cursor), short(sha)),
		TS: time.Now().Format(time.RFC3339),
	}
	return tr, sha, nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
