// Mini Code — single binary, channel-driven.
//
// One binary serves all channels: the web UI/API (default) and DingTalk.
// All message sources produce messages into the message hub (package
// channels); the hub resolves the owning session of each message (creating
// a new session when the message belongs to none, keyed on
// session_id / session_key), runs the agent loop, and publishes the
// produced messages back to every channel. Every completed turn is
// persisted to the database for interrupt recovery.
//
// Usage:
//
//	mini_code           web UI + API on the configured port (default)
//	mini_code cmd       interactive CLI channel
//	mini_code dingtalk  DingTalk stream channel only (no web)
//
// The DingTalk channel runs automatically alongside whichever channel is
// requested whenever it is configured; it can also be (re)started live from
// the web settings page.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/dingtalk"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/util"
	"github.com/user/mini_code/web"
)

const usage = `Mini Code Agent

Usage:
  mini_code              web UI + API (POST /goal, /api/...) — default
  mini_code cmd          interactive CLI channel
  mini_code dingtalk     DingTalk stream channel only (no web)

Flags:
  -agent string     agent role for new sessions (default "build")
  -p int            listen port, web mode only (default: web_port from config)

The DingTalk channel starts automatically in any mode when it is
configured (dingtalk.enabled + app_key + app_secret in ~/.mini_code/config.json),
and can be started/stopped live from the web settings page.
`

// dtController manages the DingTalk channel's lifecycle so it can be
// started, stopped and restarted at runtime (e.g. from the web settings).
type dtController struct {
	mu      sync.Mutex
	hub     *channels.Hub
	cancel  context.CancelFunc
	running bool
}

func newDTController(hub *channels.Hub) *dtController {
	return &dtController{hub: hub}
}

// apply starts, stops or restarts the DingTalk channel to match cfg.
// It returns a short human-readable outcome for the UI.
func (d *dtController) apply(cfg config.DingTalkConfig) string {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !cfg.Enabled || cfg.AppKey == "" || cfg.AppSecret == "" {
		if d.running {
			d.cancel()
			d.running = false
			slog.Info("dingtalk channel stopped")
			return "已停止"
		}
		return "未启用"
	}
	if d.running {
		d.cancel()
		d.running = false
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := dingtalk.New(cfg, d.hub)
	d.cancel = cancel
	d.running = true
	slog.Info("dingtalk channel starting", "app_key", cfg.AppKey[:min(4, len(cfg.AppKey))]+"***")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("dingtalk channel panicked, contained", "panic", r)
				d.mu.Lock()
				d.running = false
				d.mu.Unlock()
			}
		}()
		if err := client.Start(ctx); err != nil && ctx.Err() == nil {
			slog.Error("dingtalk channel stopped", "err", err)
			d.mu.Lock()
			d.running = false
			d.mu.Unlock()
		}
	}()
	return "已启动"
}

func (d *dtController) stop() {
	d.mu.Lock()
	if d.running {
		d.cancel()
		d.running = false
	}
	d.mu.Unlock()
}

func (d *dtController) isRunning() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// runtime bundles everything a mode needs and implements web.Backend.
type runtime struct {
	cfg *config.AppConfig
	cfgMu sync.RWMutex
	client *provider.ModelClient
	mgr    *session.SessionManager
	hub    *channels.Hub
	dt     *dtController
}

// setup loads config, the model client, the session manager and the message
// hub. maxTurns is the per-task turn budget (0 = loop default).
func setup(args []string, maxTurns int) (*runtime, error) {
	fs := flag.NewFlagSet("mini_code", flag.ExitOnError)
	agentRole := fs.String("agent", "build", "agent role for new sessions (build/explore/generate)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	client, err := provider.NewModelClient(cfg.Model)
	if err != nil {
		return nil, fmt.Errorf("create model client: %w", err)
	}
	mgr, err := session.NewDefaultSessionManager(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("create session manager: %w", err)
	}
	mgr.SetCompactionEnabled(cfg.Model.Compaction())

	workDir, _ := os.Getwd()

	hub := channels.NewHub(mgr, *agentRole, workDir, maxTurns)
	hub.Attach(mgr)
	return &runtime{
		cfg:    cfg,
		client: client,
		mgr:    mgr,
		hub:    hub,
		dt:     newDTController(hub),
	}, nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "Error: %s\n", err)
	os.Exit(1)
}

func main() {
	util.SetupDefaultFileLogger()

	args := os.Args[1:]
	mode := "" // no argument = web (default)
	var rest []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		mode = args[0]
		rest = args[1:]
	} else if mode == "" && len(args) > 0 {
		// Flags only (e.g. "mini_code -p 7501"): web mode with the flags.
		rest = args
	}

	switch mode {
	case "", "web", "server":
		runWeb(rest)
	case "cmd", "cli", "interactive", "chat":
		runInteractive(rest)
	case "dingtalk":
		runDingTalk(rest)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n\n%s", mode, usage)
		os.Exit(2)
	}
}

// --- web.Backend ---

func (rt *runtime) Hub() *channels.Hub          { return rt.hub }
func (rt *runtime) Mgr() *session.SessionManager { return rt.mgr }

func (rt *runtime) liveConfig() *config.AppConfig {
	rt.cfgMu.RLock()
	defer rt.cfgMu.RUnlock()
	cp := *rt.cfg
	return &cp
}

func (rt *runtime) Status() web.StatusInfo {
	cfg := rt.liveConfig()
	return web.StatusInfo{
		WebPort:       cfg.WebPort,
		Provider:      cfg.Model.Provider,
		ModelName:     cfg.Model.ModelName,
		ContextWindow: cfg.Model.ContextWindow,
		Compaction:    cfg.Model.Compaction(),
		DTEnabled:     cfg.DingTalk.Enabled,
		DTRunning:     rt.dt.isRunning(),
	}
}

func (rt *runtime) GetConfig() *config.AppConfig {
	return rt.liveConfig()
}

// SaveConfig persists cfg to disk and hot-applies the changed parts:
// model config swaps the provider client, DingTalk config restarts the
// channel. The web port takes effect on next start.
func (rt *runtime) SaveConfig(cfg *config.AppConfig) (*web.SaveResult, error) {
	old := rt.liveConfig()

	oldModelJSON, _ := json.Marshal(old.Model)
	newModelJSON, _ := json.Marshal(cfg.Model)
	modelChanged := string(oldModelJSON) != string(newModelJSON)

	oldDTJSON, _ := json.Marshal(old.DingTalk)
	newDTJSON, _ := json.Marshal(cfg.DingTalk)
	dtChanged := string(oldDTJSON) != string(newDTJSON)

	if !modelChanged && !dtChanged && old.WebPort == cfg.WebPort {
		return &web.SaveResult{Message: "配置没有变化"}, nil
	}

	path, err := config.GetConfigPath()
	if err != nil {
		return nil, err
	}
	if err := config.SaveConfig(cfg, path); err != nil {
		return nil, err
	}
	rt.cfgMu.Lock()
	*rt.cfg = *cfg
	rt.cfgMu.Unlock()

	result := &web.SaveResult{}
	notes := []string{}

	if old.Model.Compaction() != cfg.Model.Compaction() {
		rt.mgr.SetCompactionEnabled(cfg.Model.Compaction())
		if cfg.Model.Compaction() {
			notes = append(notes, "上下文自动压缩已开启")
		} else {
			notes = append(notes, "上下文自动压缩已关闭")
		}
	}
	if modelChanged {
		nc, err := provider.NewModelClient(cfg.Model)
		if err != nil {
			return nil, fmt.Errorf("配置已保存,但切换模型失败(当前仍用旧模型): %w", err)
		}
		rt.mgr.SetClient(nc)
		rt.client = nc
		result.ModelSwapped = true
		notes = append(notes, "模型配置已立即生效(进行中的任务完成后切换)")
		slog.Info("model client swapped", "model", cfg.Model.ModelName)
	}
	if dtChanged {
		outcome := rt.dt.apply(cfg.DingTalk)
		result.DingTalk = outcome
		notes = append(notes, "钉钉渠道"+outcome)
	}
	if old.WebPort != cfg.WebPort {
		notes = append(notes, "web 端口将在下次启动时生效")
	}

	result.Message = "配置已保存"
	result.Notes = notes
	return result, nil
}

// --- channels ---

// runWeb starts the web UI + API (and the DingTalk channel when configured).
func runWeb(args []string) {
	var port int
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-p":
			if i+1 < len(args) {
				i++
				if v, err := strconv.Atoi(args[i]); err == nil {
					port = v
				}
			}
		case strings.HasPrefix(args[i], "-p="):
			if v, err := strconv.Atoi(strings.TrimPrefix(args[i], "-p=")); err == nil {
				port = v
			}
		default:
			rest = append(rest, args[i])
		}
	}

	rt, err := setup(rest, 0)
	if err != nil {
		fatal(err)
	}
	defer rt.client.Close()
	defer rt.mgr.Close()
	if port == 0 {
		port = rt.liveConfig().WebPort
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go rt.hub.Run(ctx)
	rt.dt.apply(rt.liveConfig().DingTalk)
	defer rt.dt.stop()

	addr := fmt.Sprintf(":%d", port)
	slog.Info("starting web channel", "port", port)
	fmt.Printf("Mini Code 已启动: http://127.0.0.1:%d (Ctrl+C 停止)\n", port)

	httpServer := &http.Server{Addr: addr, Handler: web.NewServer(rt)}
	go func() {
		<-ctx.Done()
		slog.Info("shutting down web channel")
		httpServer.Close()
	}()
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		fatal(err)
	}
}

// runInteractive is the stdin/stdout CLI. It produces user messages into
// the hub and consumes the produced messages for its channel.
func runInteractive(args []string) {
	rt, err := setup(args, 0)
	if err != nil {
		fatal(err)
	}
	defer rt.client.Close()
	defer rt.mgr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.hub.Run(ctx)
	rt.dt.apply(rt.liveConfig().DingTalk)
	defer rt.dt.stop()

	out := rt.hub.Subscribe(channels.ChannelCLI)
	terminal := make(chan channels.Message, 8) // final assistant/error of a task
	go func() {
		for m := range out {
			switch m.Kind {
			case channels.KindToolStart:
				fmt.Printf("  \x1b[2m⚙ %s %s\x1b[0m\n", m.ToolName, m.ToolArgs)
			case channels.KindToolEnd:
				res := strings.ReplaceAll(m.ToolResult, "\n", " ")
				if len([]rune(res)) > 200 {
					res = string([]rune(res)[:200]) + "…"
				}
				fmt.Printf("  \x1b[2m✓ %s\x1b[0m\n", res)
			case channels.KindAssistant:
				if m.Content != "" {
					fmt.Printf("\n%s\n", m.Content)
				}
				select {
				case terminal <- m:
				default:
				}
			case channels.KindError:
				fmt.Fprintf(os.Stderr, "\nError: %s\n", m.Content)
				select {
				case terminal <- m:
				default:
				}
			}
		}
	}()

	sigInt := make(chan os.Signal, 1)
	sigTerm := make(chan os.Signal, 1)
	signal.Notify(sigInt, syscall.SIGINT)
	signal.Notify(sigTerm, syscall.SIGTERM)
	defer signal.Stop(sigInt)
	defer signal.Stop(sigTerm)

	scanner := bufio.NewScanner(os.Stdin)
	currentSession := ""

	fmt.Println("Mini Code ready. Type your task (type 'exit' to quit):")
	fmt.Println("  Ctrl+C during task: interrupt the current task")
	fmt.Println("  Ctrl+C at prompt:   exit")

	for {
		fmt.Print("\n> ")
		inputCh := make(chan string, 1)
		go func() {
			if scanner.Scan() {
				inputCh <- scanner.Text()
			} else {
				close(inputCh)
			}
		}()

		select {
		case <-sigTerm:
			return
		case <-sigInt:
			fmt.Println("\nBye!")
			return
		case text, ok := <-inputCh:
			if !ok {
				return
			}
			goal := strings.TrimSpace(text)
			if goal == "" {
				continue
			}
			if goal == "exit" {
				fmt.Println("Bye!")
				return
			}

			// Produce the message into the hub. An empty SessionID means
			// "no session yet" — the hub creates one on first contact and
			// every later message of this REPL continues it.
			msg := channels.NewUserMessage(channels.ChannelCLI, goal)
			msg.SessionID = currentSession
			rt.hub.Submit(msg)
			fmt.Print("  (running, Ctrl+C to interrupt)")

			select {
			case <-sigInt:
				if currentSession != "" {
					rt.hub.CancelTask(currentSession)
				}
				<-terminal // wait for the interrupted task to settle
			case m := <-terminal:
				if m.SessionID != "" {
					currentSession = m.SessionID
				}
			}
		}
	}
}

// runDingTalk starts the DingTalk stream channel only (no web).
func runDingTalk(args []string) {
	rt, err := setup(args, 0)
	if err != nil {
		fatal(err)
	}
	defer rt.client.Close()
	defer rt.mgr.Close()

	dt := rt.liveConfig().DingTalk
	if !dt.Enabled || dt.AppKey == "" || dt.AppSecret == "" {
		fatal(fmt.Errorf("dingtalk channel not configured: set \"dingtalk\" (enabled, app_key, app_secret) in ~/.mini_code/config.json or via the web settings"))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go rt.hub.Run(ctx)
	defer rt.dt.stop()

	outcome := rt.dt.apply(dt)
	if outcome == "未启用" {
		fatal(fmt.Errorf("dingtalk channel could not be started: check app_key/app_secret"))
	}
	fmt.Println("Mini Code DingTalk channel starting (Ctrl+C to stop)...")
	<-ctx.Done()
}
