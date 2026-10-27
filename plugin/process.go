package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/tools"
)

// Process plugins (kind 1: "程序") are long-lived companion programs that
// mini_code starts, keeps alive and talks to — a python service, a local
// model server, a browser driver daemon. They are NOT tools: they contribute no
// schema to the prompt. They differ from a tool plugin (kind 2, "工具") in
// exactly that way, and the distinction matters for the prefix cache:
//
//	tool plugin   — appends a schema to the prompt prefix (cache-affecting)
//	process plugin — contributes NO prompt text at all (cache-invisible)
//
// A process plugin declares itself in plugin.json under "process":
//
//	"process": {
//	  "command": "python",
//	  "args": ["server.py"],
//	  "autostart": true,
//	  "restart": "on-failure",
//	  "health_check": {"type": "http", "url": "http://127.0.0.1:8080/healthz"},
//	  "ready_timeout_seconds": 30,
//	  "expose": {
//	    "tool": true,          // give the model a plugin_proc__<id> control tool
//	    "endpoint": "http://127.0.0.1:8080"
//	  }
//	}
//
// "Expose" is what the user meant by "可以被查到": the model can list running
// plugins, read their status and their declared endpoint. The control tool is
// appended AFTER the core tools like any plugin tool, so an installed process
// plugin never shifts the cached core segment.
type ProcessSpec struct {
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Env       []string `json:"env,omitempty"`
	Autostart bool     `json:"autostart,omitempty"`
	// Restart is "always", "on-failure" (default) or "never".
	Restart string `json:"restart,omitempty"`
	// ReadyTimeoutSeconds bounds how long WaitReady waits (default 20).
	ReadyTimeoutSeconds int `json:"ready_timeout_seconds,omitempty"`
	// HealthCheck is optional; without it "ready" means "still running".
	HealthCheck *HealthCheck `json:"health_check,omitempty"`
	Expose      *ProcessExpose `json:"expose,omitempty"`
	// Description is what the model sees in the control tool's output.
	Description string `json:"description,omitempty"`
}

// HealthCheck polls one of three trivial probes until the plugin answers.
type HealthCheck struct {
	// Type is "http" (GET the URL expecting 2xx), "tcp" (dial the URL's host)
	// or "process" (just "still alive").
	Type string `json:"type,omitempty"`
	URL  string `json:"url,omitempty"`
}

// ProcessExpose declares what the plugin tells mini_code about itself.
type ProcessExpose struct {
	// Tool registers a plugin_proc__<id> control tool the model may call.
	Tool bool `json:"tool,omitempty"`
	// Endpoint is an http(s) URL the model is told about (documentation only:
	// mini_code never proxies it, so no credential crosses the boundary).
	Endpoint string `json:"endpoint,omitempty"`
}

// ProcState is the observable state of one process plugin.
type ProcState struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Dir         string `json:"dir"`
	State       string `json:"state"` // stopped | starting | running | failed
	PID         int    `json:"pid,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	Restarts    int    `json:"restarts,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Description string `json:"description,omitempty"`
	// Command is shown so the user can see exactly what would be run.
	Command string `json:"command"`
	// UptimeSeconds is computed at read time (0 when not running).
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`
}

const (
	procStateStopped  = "stopped"
	procStateStarting = "starting"
	procStateRunning  = "running"
	procStateFailed   = "failed"

	procDefaultReadyTimeout = 20 * time.Second
	procMaxReadyTimeout     = 300 * time.Second
	// procMaxLogLines bounds the in-memory tail kept per plugin.
	procMaxLogLines = 200
	// procRestartBackoff is the wait before a restart attempt; it doubles up to
	// procRestartBackoffMax so a plugin that crashes on startup cannot spin the
	// CPU.
	procRestartBackoff    = 1 * time.Second
	procRestartBackoffMax = 30 * time.Second
)

// runningProc is the supervisor entry for one started plugin.
type runningProc struct {
	spec   ProcessSpec
	dir    string
	cmd    *exec.Cmd
	state  string
	pid    int
	start  time.Time
	errMsg string
	// restarts counts automatic restarts (not manual starts).
	restarts int
	// stop is closed to tell the supervisor loop not to restart.
	stop chan struct{}
	// logtail holds the last procMaxLogLines lines of stdout+stderr.
	logtail []string
}

// ProcManager supervises every process plugin. There is one per process; the
// web server owns it.
type ProcManager struct {
	mu      sync.Mutex
	procs   map[string]*runningProc
	loadFn  func() []Plugin
	started bool
}

// NewProcManager builds a manager. loadFn is called to re-read the plugin
// directory (a fresh read means a hot-added plugin is startable without a
// restart of mini_code).
func NewProcManager(loadFn func() []Plugin) *ProcManager {
	if loadFn == nil {
		loadFn = Load
	}
	return &ProcManager{procs: make(map[string]*runningProc), loadFn: loadFn}
}

// processPlugins filters the enabled plugins that declare a process spec.
func processPlugins(all []Plugin) []Plugin {
	out := make([]Plugin, 0, len(all))
	for _, p := range all {
		if p.Process != nil {
			out = append(out, p)
		}
	}
	return out
}

// StartAutostart launches every process plugin with autostart: true. It is
// called once when the server starts; a failure in one plugin is logged and
// does not stop the others (or the server).
func (m *ProcManager) StartAutostart() {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	for _, p := range processPlugins(m.loadFn()) {
		if p.Process == nil || !p.Process.Autostart {
			continue
		}
		if err := m.Start(p.ID); err != nil {
			slog.Warn("plugin process: autostart failed", "id", p.ID, "err", err)
		}
	}
}

// Start launches one process plugin by id. Starting an already running plugin
// is a no-op.
func (m *ProcManager) Start(id string) error {
	p, ok := m.find(id)
	if !ok {
		return fmt.Errorf("插件 %q 未安装或未启用", id)
	}
	return m.startPlugin(p, false)
}

// startPlugin is the shared path for a manual start and an automatic restart.
func (m *ProcManager) startPlugin(p Plugin, isRestart bool) error {
	if p.Process == nil {
		return fmt.Errorf("插件 %q 没有 process 配置", p.ID)
	}
	spec := *p.Process
	if strings.TrimSpace(spec.Command) == "" {
		return fmt.Errorf("插件 %q 的 process.command 为空", p.ID)
	}

	m.mu.Lock()
	if cur, ok := m.procs[p.ID]; ok && (cur.state == procStateRunning || cur.state == procStateStarting) {
		m.mu.Unlock()
		return nil // already up
	}
	rp := &runningProc{
		spec:  spec,
		dir:   p.Dir,
		state: procStateStarting,
		start: time.Now(),
		stop:  make(chan struct{}),
	}
	if cur, ok := m.procs[p.ID]; ok {
		rp.restarts = cur.restarts
		rp.logtail = cur.logtail
	}
	if isRestart {
		rp.restarts++
	}
	m.procs[p.ID] = rp
	m.mu.Unlock()

	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = p.Dir
	hideWindow(cmd)
	// Same UTF-8 child environment a plugin tool gets: a python helper that
	// prints non-ASCII must not die on a GBK console code page.
	cmd.Env = append(os.Environ(), append([]string{
		"PYTHONIOENCODING=utf-8",
		"PYTHONUTF8=1",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		// The child learns where it lives and which plugin it is, so a reusable
		// script can be dropped into several plugins unmodified.
		"MINICODE_PLUGIN_ID=" + p.ID,
		"MINICODE_PLUGIN_DIR=" + p.Dir,
	}, spec.Env...)...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.fail(p.ID, "无法创建 stdout 管道: "+err.Error())
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		m.fail(p.ID, "无法创建 stderr 管道: "+err.Error())
		return err
	}
	if err := cmd.Start(); err != nil {
		m.fail(p.ID, "启动失败: "+err.Error())
		return fmt.Errorf("启动 %s 失败: %w", spec.Command, err)
	}

	m.mu.Lock()
	rp.cmd = cmd
	rp.pid = cmd.Process.Pid
	rp.state = procStateRunning
	rp.errMsg = ""
	m.mu.Unlock()

	// Drain both pipes; without a reader a chatty plugin fills the pipe buffer
	// and blocks forever on write.
	go m.drain(p.ID, "out", stdout)
	go m.drain(p.ID, "err", stderr)

	slog.Info("plugin process started", "id", p.ID, "pid", cmd.Process.Pid, "cmd", spec.Command)

	// WaitReady runs in the background so Start() never blocks the caller (an
	// autostart loop or an HTTP handler). The state moves running → running or
	// running → failed inside.
	go m.waitExit(p.ID, cmd)
	go m.waitReady(p.ID)
	return nil
}

// waitExit reaps the child and decides whether to restart.
func (m *ProcManager) waitExit(id string, cmd *exec.Cmd) {
	err := cmd.Wait()

	m.mu.Lock()
	rp, ok := m.procs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	select {
	case <-rp.stop:
		// A deliberate stop: the caller already set the state.
		m.mu.Unlock()
		return
	default:
	}
	restart := strings.ToLower(strings.TrimSpace(rp.spec.Restart))
	backoff := procRestartBackoff
	if n := rp.restarts; n > 0 {
		backoff = procRestartBackoff << uint(min(n, 5))
		if backoff > procRestartBackoffMax {
			backoff = procRestartBackoffMax
		}
	}
	if err != nil {
		rp.state = procStateFailed
		rp.errMsg = "进程退出: " + err.Error()
	} else {
		rp.state = procStateStopped
		rp.errMsg = "进程正常退出"
	}
	pid := rp.pid
	rp.pid = 0
	wantRestart := (restart == "always") || (restart == "on-failure" && err != nil) || restart == ""
	if restart == "never" {
		wantRestart = false
	}
	m.mu.Unlock()

	slog.Info("plugin process exited", "id", id, "pid", pid, "err", err, "restart", wantRestart)
	if !wantRestart {
		return
	}
	go func() {
		time.Sleep(backoff)
		m.mu.Lock()
		cur, ok := m.procs[id]
		stopped := false
		if ok {
			select {
			case <-cur.stop:
				stopped = true
			default:
			}
		}
		m.mu.Unlock()
		if !ok || stopped {
			return
		}
		p, found := m.find(id)
		if !found {
			return
		}
		slog.Info("plugin process restarting", "id", id, "after", backoff.String())
		if err := m.startPlugin(p, true); err != nil {
			slog.Warn("plugin process: restart failed", "id", id, "err", err)
		}
	}()
}

// waitReady polls the health check (or just checks liveness) and records the
// outcome in the state.
func (m *ProcManager) waitReady(id string) {
	m.mu.Lock()
	rp, ok := m.procs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	spec := rp.spec
	m.mu.Unlock()

	timeout := time.Duration(spec.ReadyTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = procDefaultReadyTimeout
	}
	if timeout > procMaxReadyTimeout {
		timeout = procMaxReadyTimeout
	}
	if spec.HealthCheck == nil || strings.EqualFold(spec.HealthCheck.Type, "process") || spec.HealthCheck.URL == "" {
		return // "running" is all we can say
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		_, alive := m.procs[id]
		m.mu.Unlock()
		if !alive {
			return
		}
		if probeHealth(spec.HealthCheck) {
			slog.Info("plugin process ready", "id", id)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	m.mu.Lock()
	if cur, ok := m.procs[id]; ok && cur.state == procStateRunning {
		cur.state = procStateFailed
		cur.errMsg = fmt.Sprintf("健康检查 %d 秒内未通过(%s)", int(timeout.Seconds()), spec.HealthCheck.URL)
	}
	m.mu.Unlock()
	slog.Warn("plugin process health check timed out", "id", id, "url", spec.HealthCheck.URL)
}

// Stop terminates one plugin process (and suppresses its restart).
func (m *ProcManager) Stop(id string) error {
	m.mu.Lock()
	rp, ok := m.procs[id]
	if !ok || rp.cmd == nil {
		m.mu.Unlock()
		return fmt.Errorf("插件 %q 没有在运行", id)
	}
	select {
	case <-rp.stop:
	default:
		close(rp.stop)
	}
	cmd := rp.cmd
	rp.cmd = nil
	rp.pid = 0
	rp.state = procStateStopped
	rp.errMsg = ""
	m.mu.Unlock()

	if cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil {
			return fmt.Errorf("结束进程失败: %w", err)
		}
	}
	slog.Info("plugin process stopped", "id", id)
	return nil
}

// StopAll terminates every managed process (server shutdown).
func (m *ProcManager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.procs))
	for id, rp := range m.procs {
		if rp.cmd != nil {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		if err := m.Stop(id); err != nil {
			slog.Debug("plugin process: stop failed", "id", id, "err", err)
		}
	}
}

// States returns the state of every installed, enabled process plugin — the
// ones that are running as well as the ones that are not. That is what the
// settings page and the model's control tool read.
func (m *ProcManager) States() []ProcState {
	var out []ProcState
	for _, p := range processPlugins(m.loadFn()) {
		if p.Process == nil {
			continue
		}
		out = append(out, m.StateOf(p))
	}
	return out
}

// StateOf reports one plugin, whether or not it was ever started.
func (m *ProcManager) StateOf(p Plugin) ProcState {
	st := ProcState{
		ID:      p.ID,
		Name:    p.Name,
		Dir:     p.Dir,
		State:   procStateStopped,
		Command: strings.TrimSpace(p.Process.Command + " " + strings.Join(p.Process.Args, " ")),
	}
	if p.Process.Description != "" {
		st.Description = p.Process.Description
	}
	if p.Process.Expose != nil {
		st.Endpoint = p.Process.Expose.Endpoint
	}
	m.mu.Lock()
	if rp, ok := m.procs[p.ID]; ok {
		st.State = rp.state
		st.PID = rp.pid
		st.LastError = rp.errMsg
		st.Restarts = rp.restarts
		if !rp.start.IsZero() {
			st.StartedAt = rp.start.Unix()
			if rp.state == procStateRunning || rp.state == procStateStarting {
				st.UptimeSeconds = int64(time.Since(rp.start).Seconds())
			}
		}
	}
	m.mu.Unlock()
	return st
}

// LogTail returns the last lines the plugin printed (stdout+stderr, interleaved
// by arrival). Handy for diagnosing a plugin that fails its health check.
func (m *ProcManager) LogTail(id string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	rp, ok := m.procs[id]
	if !ok {
		return nil
	}
	out := make([]string, len(rp.logtail))
	copy(out, rp.logtail)
	return out
}

// isRunning reports whether the plugin is currently up (used by the control
// tool to phrase its answer).
func (m *ProcManager) isRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	rp, ok := m.procs[id]
	return ok && rp.state == procStateRunning
}

// find looks the plugin up in the directory listing.
func (m *ProcManager) find(id string) (Plugin, bool) {
	for _, p := range m.loadFn() {
		if p.ID == id {
			return p, true
		}
	}
	return Plugin{}, false
}

// fail records a start failure.
func (m *ProcManager) fail(id, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rp, ok := m.procs[id]
	if !ok {
		return
	}
	rp.state = procStateFailed
	rp.errMsg = msg
	rp.pid = 0
}

// drain reads one pipe and appends to the plugin's log tail.
func (m *ProcManager) drain(id, which string, r io.ReadCloser) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	// A plugin may print a long line (a JSON dump); the default 64KB token
	// limit would stop the reader and block the child on a full pipe.
	sc.Buffer(make([]byte, 0, 8*1024), 256*1024)
	for sc.Scan() {
		line := sanitizeUTF8(sc.Text())
		m.mu.Lock()
		if rp, ok := m.procs[id]; ok {
			rp.logtail = append(rp.logtail, which+": "+line)
			if len(rp.logtail) > procMaxLogLines {
				rp.logtail = rp.logtail[len(rp.logtail)-procMaxLogLines:]
			}
		}
		m.mu.Unlock()
	}
}

// probeHealth runs one health check, returning true when the plugin answered.
func probeHealth(hc *HealthCheck) bool {
	if hc == nil || hc.URL == "" {
		return true
	}
	switch strings.ToLower(hc.Type) {
	case "tcp":
		return probeTCP(hc.URL)
	case "", "http":
		return probeHTTP(hc.URL)
	case "process":
		return true
	default:
		return probeHTTP(hc.URL)
	}
}

// ProcTool is the control tool a process plugin exposes to the model when
// expose.tool is true. It answers three questions — is it up, what is it, how
// do I restart it — without ever proxying the plugin's own protocol: mini_code
// has no idea what a given plugin speaks, and inventing a proxy would be the
// "everything is a plugin" trap.
type ProcTool struct {
	pluginID    string
	name        string
	description string
	stateFn     func() ProcState
	logFn       func() []string
	restartFn   func() error
	endpoint    string
}

// NewProcTool builds the control tool for one process plugin.
func NewProcTool(p Plugin, m *ProcManager) *ProcTool {
	name := "plugin_proc__" + strings.ReplaceAll(p.ID, "-", "_")
	desc := "Report the status of the mini_code process plugin " + p.ID
	if p.Process != nil && p.Process.Description != "" {
		desc += " (" + p.Process.Description + ")"
	}
	desc += ". Returns JSON: state, pid, uptime, restarts, last_error, endpoint and the recent log tail. " +
		"Use it to check whether the companion program is up before telling the user a feature is unavailable."
	return &ProcTool{
		pluginID:    p.ID,
		name:        name,
		description: desc,
		stateFn:     func() ProcState { return m.StateOf(p) },
		logFn:       func() []string { return m.LogTail(p.ID) },
		restartFn:   func() error { return m.Start(p.ID) },
		endpoint:    func() string {
			if p.Process != nil && p.Process.Expose != nil {
				return p.Process.Expose.Endpoint
			}
			return ""
		}(),
	}
}

func (t *ProcTool) Name() string        { return t.name }
func (t *ProcTool) Description() string { return t.description }
func (t *ProcTool) IsHidden() bool      { return false }

func (t *ProcTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"restart": map[string]interface{}{
				"type":        "boolean",
				"description": "Start the companion program if it is not running (default false).",
			},
			"logs": map[string]interface{}{
				"type":        "integer",
				"description": "How many trailing log lines to include (default 20, max 200).",
			},
		},
		"required": []string{},
	}
}

func (t *ProcTool) Execute(_ context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	if b, ok := params["restart"].(bool); ok && b && t.restartFn != nil {
		_ = t.restartFn()
	}
	n := 20
	if v, ok := params["logs"]; ok {
		if f, ok := toInt(v); ok {
			n = f
		}
	}
	if n < 0 {
		n = 0
	}
	if n > procMaxLogLines {
		n = procMaxLogLines
	}
	st := t.stateFn()
	payload := map[string]interface{}{
		"plugin":      st.ID,
		"state":       st.State,
		"pid":         st.PID,
		"uptime_sec":  st.UptimeSeconds,
		"restarts":    st.Restarts,
		"command":     st.Command,
		"endpoint":    st.Endpoint,
		"description": st.Description,
	}
	if st.LastError != "" {
		payload["last_error"] = st.LastError
	}
	if n > 0 && t.logFn != nil {
		logs := t.logFn()
		if len(logs) > n {
			logs = logs[len(logs)-n:]
		}
		if len(logs) > 0 {
			payload["log_tail"] = logs
		}
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return tools.NewTextResult("Error: cannot encode plugin state: " + err.Error()), nil
	}
	return tools.NewTextResult(string(b)), nil
}

// toInt accepts the several shapes a JSON number arrives in.
func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case string:
		var i int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &i); err == nil {
			return i, true
		}
	}
	return 0, false
}

// ensure filepath is referenced (used by the dir computation above and kept
// here so the import stays honest if the supervisor is trimmed).
var _ = filepath.Join
