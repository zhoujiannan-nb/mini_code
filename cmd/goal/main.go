package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/user/uclaw/config"
	"github.com/user/uclaw/provider"
	"github.com/user/uclaw/session"
	"github.com/user/uclaw/util"
)

func main() {
	goal := flag.String("goal", "", "single goal to execute")
	flag.Parse()

	if *goal == "" {
		fmt.Fprintf(os.Stderr, "Error: -goal flag is required\n")
		fmt.Fprintf(os.Stderr, "Usage: ucode-goal -goal \"your task description\"\n")
		os.Exit(1)
	}

	util.SetupDefaultFileLogger()

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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	workDir, _ := os.Getwd()
	fmt.Printf("Executing goal: %s\n\n", *goal)

	result, err := mgr.RunTask(ctx, *goal, *goal, "build", workDir, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	fmt.Println(result)
}
