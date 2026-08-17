package main

import (
	"fmt"
	"os"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/ui/app"
	"github.com/user/mini_code/util"
)

func main() {
	// Setup file logger for TUI mode - logs go to file only
	util.SetupDefaultFileLogger()

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %s\n", err)
		os.Exit(1)
	}

	// Create model client
	client, err := provider.NewModelClient(cfg.Model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create model client: %s\n", err)
		os.Exit(1)
	}
	defer client.Close()

	// Create session manager
	mgr, err := session.NewDefaultSessionManager(client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create session manager: %s\n", err)
		os.Exit(1)
	}
	defer mgr.Close()

	// Set tool call callbacks
	mgr.SetToolCallCallbacks(
		// onDecided callback: agent decided to call a tool (not yet executed)
		func(id, name, args string) {
			app.SendToolCallEvent(app.ToolCallDecidedEvent{
				ID:       id,
				ToolName: name,
				Args:     args,
			})
		},
		// onStart callback: tool execution started
		func(id, name string) {
			app.SendToolCallEvent(app.ToolCallStartedEvent{
				ID:       id,
				ToolName: name,
			})
		},
		// onEnd callback: tool execution finished
		func(id, name string, result string) {
			app.SendToolCallEvent(app.ToolCallCompletedEvent{
				ID:       id,
				ToolName: name,
				Result:   result,
			})
		},
		// onReply callback (streaming: content/reasoning are accumulated values)
		func(content, reasoningContent string) {
			app.SendToolCallEvent(app.AssistantReplyEvent{
				Content:          content,
				ReasoningContent: reasoningContent,
			})
		},
	)

	// Run TUI application
	app.Run(client, mgr)
}
