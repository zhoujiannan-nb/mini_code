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

	"github.com/user/mini_code/approval"
	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/super"
	"github.com/user/mini_code/util"
	"github.com/user/mini_code/web"
)

const usage = `Mini Code Agent

Usage:
   mini_code              Windows: desktop shell (native window, double-click);
                          other platforms: web UI + API (default)
   mini_code web          web UI + API (POST /goal, /api/...) in a browser
   mini_code cmd          interactive CLI channel
   mini_code desktop      web UI in a native desktop window (Windows/macOS)

Flags:
  -agent string     agent role for new sessions (default "build")
  -p int            listen port, web/desktop mode only (default: web_port from config)

Desktop mode (Windows/macOS): opens the same UI in an embedded native window
(WebView2 / WKWebView, no browser needed). Closing the window hides it to the
system tray; use the tray menu to re-open or fully quit. The page stays
reachable at http://127.0.0.1:<web_port> for browsers.
`

type runtime struct {
	cfg       *config.AppConfig
	cfgMu     sync.RWMutex
	clients   []*provider.ModelClient
	mgr       *session.SessionManager
	hub       *channels.Hub
	subagents *session.SubagentService
	workers   *session.WorkerService
	superSvc  *super.Service
	approvals *approval.Service
}

func buildModelClients(models []config.ModelConfig) (map[string]*provider.ModelClient, []*provider.ModelClient, error) {
	clients := make(map[string]*provider.ModelClient, len(models))
	all := make([]*provider.ModelClient, 0, len(models))
	for _, mc := range models {
		c, err := provider.NewModelClient(mc)
		if err != nil {
			return nil, nil, fmt.Errorf("model %q: %w", mc.Name, err)
		}
		clients[mc.Name] = c
		all = append(all, c)
	}
	return clients, all, nil
}

func (rt *runtime) closeClients() {
	for _, c := range rt.clients {
		c.Close()
	}
}

func setup(args []string, maxTurns int) (*runtime, error) {
	fs := flag.NewFlagSet("mini_code", flag.ExitOnError)
	agentRole := fs.String("agent", "build", "agent role for new sessions (build)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	clients, allClients, err := buildModelClients(cfg.Models)
	if err != nil {
		return nil, fmt.Errorf("create model client: %w", err)
	}
	mgr, err := session.NewDefaultSessionManager(nil)
	if err != nil {
		for _, c := range allClients {
			c.Close()
		}
		return nil, fmt.Errorf("create session manager: %w", err)
	}
	mgr.SetModels(clients, cfg.DefaultModel)
	mgr.SetCompactionEnabled(cfg.Compaction())
	mgr.SetTitlePolicy(cfg.TitleAutoEnabled(), cfg.TitleModel)

	workDir, _ := os.Getwd()

	hub := channels.NewHub(mgr, *agentRole, workDir, maxTurns)
	hub.Attach(mgr)

	subagents := session.NewSubagentService(mgr)
	mgr.SetSubagents(subagents)
	subagents.OnJobStart = func(childID string, cancel context.CancelFunc) {
		hub.RegisterExternalTask(childID, cancel)
	}
	subagents.OnJobEnd = func(childID string) {
		hub.UnregisterExternalTask(childID)
	}
	subagents.OnJobDone = func(sessionID, content, reasoning string, interrupted bool) {
		am := channels.NewAssistantMessage("", sessionID, channels.ChannelWeb, content)
		am.Reasoning = reasoning
		hub.Submit(am)
		status := channels.StatusTaskDone
		if interrupted {
			status = channels.StatusInterrupted
		}
		hub.Submit(channels.NewStatusMessage("", sessionID, channels.ChannelWeb, status, ""))
	}

	approvals := approval.NewService(hub, mgr)
	mgr.SetApprover(approvals)


	workers := session.NewWorkerService(mgr.GetStore(), mgr)
	mgr.SetWorkers(workers)

	superSvc := super.NewService(mgr.GetStore().DB(), mgr, hub)
	mgr.SetSuperRegistrar(superSvc)
	web.AttachSuperService(superSvc)
	go superSvc.Start(context.Background())

	return &runtime{
		cfg:       cfg,
		clients:   allClients,
		mgr:       mgr,
		hub:       hub,
		subagents: subagents,
		workers:   workers,
		superSvc:  superSvc,
		approvals: approvals,
	}, nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "Error: %s\n", err)
	os.Exit(1)
}

func main() {
	util.SetupDefaultFileLogger()

	args := os.Args[1:]
	mode := ""
	var rest []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		mode = args[0]
		rest = args[1:]
	} else if mode == "" && len(args) > 0 {
		rest = args
	}

	switch mode {
	case "", "web", "server":
		if mode == "" && desktopDefault {
			runDesktop(rest)
			return
		}
		runWeb(rest)
	case "cmd", "cli", "interactive", "chat":
		runInteractive(rest)
	case "desktop", "gui":
		runDesktop(rest)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n\n%s", mode, usage)
		os.Exit(2)
	}
}


func (rt *runtime) Hub() *channels.Hub            { return rt.hub }
func (rt *runtime) Mgr() *session.SessionManager  { return rt.mgr }
func (rt *runtime) Workers() *session.WorkerService { return rt.workers }
func (rt *runtime) Approvals() *approval.Service  { return rt.approvals }

func (rt *runtime) liveConfig() *config.AppConfig {
	rt.cfgMu.RLock()
	defer rt.cfgMu.RUnlock()
	cp := *rt.cfg
	return &cp
}

func (rt *runtime) Status() web.StatusInfo {
	cfg := rt.liveConfig()
	def := cfg.DefaultModelCfg()
	providerName, modelName, ctxWindow := "", "", 0
	if def != nil {
		providerName, modelName, ctxWindow = def.Provider, def.ModelName, def.ContextWindow
	}
	models := make([]web.ModelInfo, 0, len(cfg.Models))
	for _, mc := range cfg.Models {
		models = append(models, web.ModelInfo{
			Name:          mc.Name,
			ModelName:     mc.ModelName,
			Provider:      mc.Provider,
			ContextWindow: mc.ContextWindow,
		})
	}
	return web.StatusInfo{
		WebPort:       cfg.WebPort,
		Provider:      providerName,
		ModelName:     modelName,
		ContextWindow: ctxWindow,
		Models:        models,
		DefaultModel:  cfg.DefaultModel,
		Compaction:    cfg.Compaction(),
	}
}

func (rt *runtime) GetConfig() *config.AppConfig {
	return rt.liveConfig()
}

func (rt *runtime) SaveConfig(cfg *config.AppConfig) (*web.SaveResult, error) {
	old := rt.liveConfig()

	oldModelsJSON, _ := json.Marshal(struct {
		Models       []config.ModelConfig `json:"models"`
		DefaultModel string               `json:"default_model"`
	}{old.Models, old.DefaultModel})
	newModelsJSON, _ := json.Marshal(struct {
		Models       []config.ModelConfig `json:"models"`
		DefaultModel string               `json:"default_model"`
	}{cfg.Models, cfg.DefaultModel})
	modelsChanged := string(oldModelsJSON) != string(newModelsJSON)

	if !modelsChanged && old.WebPort == cfg.WebPort && old.Compaction() == cfg.Compaction() &&
		old.TitleAutoEnabled() == cfg.TitleAutoEnabled() && old.TitleModel == cfg.TitleModel &&
		old.UserAvatar == cfg.UserAvatar && old.AgentAvatar == cfg.AgentAvatar &&
		config.SameBootAnim(old.BootAnim, cfg.BootAnim) {
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

	if old.Compaction() != cfg.Compaction() {
		rt.mgr.SetCompactionEnabled(cfg.Compaction())
		if cfg.Compaction() {
			notes = append(notes, "上下文自动压缩已开启")
		} else {
			notes = append(notes, "上下文自动压缩已关闭")
		}
	}
	if old.TitleAutoEnabled() != cfg.TitleAutoEnabled() || old.TitleModel != cfg.TitleModel {
		rt.mgr.SetTitlePolicy(cfg.TitleAutoEnabled(), cfg.TitleModel)
		if !cfg.TitleAutoEnabled() {
			notes = append(notes, "会话自动标题已关闭(保留首句截断的标题)")
		} else {
			label := cfg.TitleModel
			if label == "" {
				label = "沿用会话自己的模型"
			}
			notes = append(notes, "会话自动标题已开启(标题模型:"+label+")")
		}
	}
	if modelsChanged {
		clients, allClients, err := buildModelClients(cfg.Models)
		if err != nil {
			return nil, fmt.Errorf("配置已保存,但切换模型失败(当前仍用旧模型): %w", err)
		}
		rt.mgr.SetModels(clients, cfg.DefaultModel)
		rt.closeClients()
		rt.clients = allClients
		result.ModelSwapped = true
		notes = append(notes, "模型配置已立即生效(进行中的任务完成后切换)")
		slog.Info("model registry rebuilt", "models", len(clients), "default", cfg.DefaultModel)
	}
	if old.WebPort != cfg.WebPort {
		notes = append(notes, "web 端口将在下次启动时生效")
	}
	if !config.SameBootAnim(old.BootAnim, cfg.BootAnim) {
		notes = append(notes, "开机动画设置已保存,下次打开窗口时生效")
	}

	result.Message = "配置已保存"
	result.Notes = notes
	return result, nil
}


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
	defer rt.closeClients()
	defer rt.mgr.Close()
	if port == 0 {
		port = rt.liveConfig().WebPort
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go rt.hub.Run(ctx)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
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

func runInteractive(args []string) {
	rt, err := setup(args, 0)
	if err != nil {
		fatal(err)
	}
	defer rt.closeClients()
	defer rt.mgr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.hub.Run(ctx)

	out := rt.hub.Subscribe(channels.ChannelCLI)
	terminal := make(chan channels.Message, 8)
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

			msg := channels.NewUserMessage(channels.ChannelCLI, goal)
			msg.SessionID = currentSession
			rt.hub.Submit(msg)
			fmt.Print("  (running, Ctrl+C to interrupt)")

			select {
			case <-sigInt:
				if currentSession != "" {
					rt.hub.CancelTask(currentSession)
				}
				<-terminal
			case m := <-terminal:
				if m.SessionID != "" {
					currentSession = m.SessionID
				}
			}
		}
	}
}
