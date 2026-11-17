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

type ProcessSpec struct {
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Env       []string `json:"env,omitempty"`
	Autostart bool     `json:"autostart,omitempty"`
	Restart string `json:"restart,omitempty"`
	ReadyTimeoutSeconds int `json:"ready_timeout_seconds,omitempty"`
	HealthCheck *HealthCheck `json:"health_check,omitempty"`
	Expose      *ProcessExpose `json:"expose,omitempty"`
	Description string `json:"description,omitempty"`
}

type HealthCheck struct {
	Type string `json:"type,omitempty"`
	URL  string `json:"url,omitempty"`
}

type ProcessExpose struct {
	Tool bool `json:"tool,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

type ProcState struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Dir         string `json:"dir"`
	State       string `json:"state"`
	PID         int    `json:"pid,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	Restarts    int    `json:"restarts,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Description string `json:"description,omitempty"`
	Command string `json:"command"`
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`
}

const (
	procStateStopped  = "stopped"
	procStateStarting = "starting"
	procStateRunning  = "running"
	procStateFailed   = "failed"

	procDefaultReadyTimeout = 20 * time.Second
	procMaxReadyTimeout     = 300 * time.Second
	procMaxLogLines = 200
	procRestartBackoff    = 1 * time.Second
	procRestartBackoffMax = 30 * time.Second
)

type runningProc struct {
	spec   ProcessSpec
	dir    string
	cmd    *exec.Cmd
	state  string
	pid    int
	start  time.Time
	errMsg string
	restarts int
	stop chan struct{}
	logtail []string
}

type ProcManager struct {
	mu      sync.Mutex
	procs   map[string]*runningProc
	loadFn  func() []Plugin
	started bool
}

func NewProcManager(loadFn func() []Plugin) *ProcManager {
	if loadFn == nil {
		loadFn = Load
	}
	return &ProcManager{procs: make(map[string]*runningProc), loadFn: loadFn}
}

func processPlugins(all []Plugin) []Plugin {
	out := make([]Plugin, 0, len(all))
	for _, p := range all {
		if p.Process != nil {
			out = append(out, p)
		}
	}
	return out
}

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

func (m *ProcManager) Start(id string) error {
	p, ok := m.find(id)
	if !ok {
		return fmt.Errorf("插件 %q 未安装或未启用", id)
	}
	return m.startPlugin(p, false)
}

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
		return nil
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
	cmd.Env = append(os.Environ(), append([]string{
		"PYTHONIOENCODING=utf-8",
		"PYTHONUTF8=1",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
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

	go m.drain(p.ID, "out", stdout)
	go m.drain(p.ID, "err", stderr)

	slog.Info("plugin process started", "id", p.ID, "pid", cmd.Process.Pid, "cmd", spec.Command)

	go m.waitExit(p.ID, cmd)
	go m.waitReady(p.ID)
	return nil
}

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
		return
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

func (m *ProcManager) find(id string) (Plugin, bool) {
	for _, p := range m.loadFn() {
		if p.ID == id {
			return p, true
		}
	}
	return Plugin{}, false
}

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

func (m *ProcManager) drain(id, which string, r io.ReadCloser) {
	defer r.Close()
	sc := bufio.NewScanner(r)
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

type ProcTool struct {
	pluginID    string
	name        string
	description string
	stateFn     func() ProcState
	logFn       func() []string
	restartFn   func() error
	endpoint    string
}

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

var _ = filepath.Join
