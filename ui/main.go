package main

import (
	"encoding/json"
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
		// onStart callback
		func(name string, params map[string]interface{}) {
			args, _ := json.Marshal(params)
			app.SendToolCallEvent(app.ToolCallStartedEvent{
				ToolName: name,
				Args:     string(args),
			})
		},
		// onEnd callback
		func(name string, params map[string]interface{}, result string) {
			app.SendToolCallEvent(app.ToolCallCompletedEvent{
				ToolName: name,
				Result:   result,
			})
		},
		// onReply callback
		func(content string) {
			app.SendToolCallEvent(app.AssistantReplyEvent{
				Content: content,
			})
		},
	)

	// Run TUI application
	app.Run(client, mgr)
}
