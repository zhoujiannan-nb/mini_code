package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
	uiclaw "github.com/user/mini_code/ui/app"
	"github.com/user/mini_code/util"
)

func main() {
	util.SetupDefaultFileLogger()

	// Check for --tui flag
	if len(os.Args) > 1 && os.Args[1] == "--tui" {
		runTUI()
		return
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %s\n", err)
		os.Exit(1)
	}

	client, err := provider.NewModelClient(cfg.Model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create model client: %s\n", err)
		os.Exit(1)
	}
	defer client.Close()

	mgr, err := session.NewDefaultSessionManager(client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create session manager: %s\n", err)
		os.Exit(1)
	}
	defer mgr.Close()

	workDir, _ := os.Getwd()
	scanner := bufio.NewScanner(os.Stdin)

	sess, err := mgr.CreateSession("conversation", "build", workDir, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create session: %s\n", err)
		os.Exit(1)
	}

	// Global context: only cancelled by SIGTERM or program exit.
	globalCtx, globalCancel := context.WithCancel(context.Background())
	defer globalCancel()

	// SIGTERM always exits immediately.
	sigTerm := make(chan os.Signal, 1)
	signal.Notify(sigTerm, syscall.SIGTERM)
	go func() {
		<-sigTerm
		globalCancel()
		os.Stdin.Close()
	}()

	// SIGINT channel reused for both prompt and task phases.
	sigInt := make(chan os.Signal, 1)

	fmt.Println("Mini Code ready. Type your task (type 'exit' to quit):")
	fmt.Println("  Ctrl+C during task: interrupt and return to prompt")
	fmt.Println("  Ctrl+C at prompt:   exit")

	for {
		fmt.Print("\n> ")

		// Prompt phase: race scanner input against SIGINT.
		// Ctrl+C at prompt exits the program.
		inputCh := make(chan string, 1)
		go func() {
			if scanner.Scan() {
				inputCh <- scanner.Text()
			}
			close(inputCh)
		}()

		signal.Notify(sigInt, syscall.SIGINT)
		var goal string
		var gotInput bool
		select {
		case <-sigInt:
			fmt.Println("\nBye!")
			return
		case text, ok := <-inputCh:
			signal.Stop(sigInt)
			if !ok {
				return
			}
			gotInput = true
			goal = text
		}

		if !gotInput {
			break
		}

		goal = strings.TrimSpace(goal)
		if goal == "" {
			continue
		}
		if goal == "exit" {
			fmt.Println("Bye!")
			break
		}

		// Task phase: create per-task context.
		// Ctrl+C during task cancels the task but keeps the program alive.
		taskCtx, taskCancel := context.WithCancel(globalCtx)

		signal.Notify(sigInt, syscall.SIGINT)
		go func() {
			<-sigInt
			taskCancel()
		}()

		result, err := sess.Prompt(taskCtx, goal, 0)

		signal.Stop(sigInt)
		taskCancel()

		if result != "" {
			fmt.Printf("\n%s\n", result)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		}

		if taskCtx.Err() != nil {
			fmt.Println("\n[Interrupted]")
		}
	}

	globalCancel()
	os.Stdin.Close()
}

// runTUI starts the TUI application
func runTUI() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %s\n", err)
		os.Exit(1)
	}

	client, err := provider.NewModelClient(cfg.Model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create model client: %s\n", err)
		os.Exit(1)
	}
	defer client.Close()

	mgr, err := session.NewDefaultSessionManager(client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create session manager: %s\n", err)
		os.Exit(1)
	}
	defer mgr.Close()

	uiclaw.Run(client, mgr)
}
