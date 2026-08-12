package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/user/uclaw/config"
	"github.com/user/uclaw/provider"
	"github.com/user/uclaw/session"
	"github.com/user/uclaw/util"
)

type GoalRequest struct {
	Goal string `json:"goal"`
}

type GoalResponse struct {
	Data  string `json:"data"`
	Error string `json:"error,omitempty"`
}

func main() {
	port := flag.Int("p", 8080, "server listen port")
	flag.Parse()

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

	mux := http.NewServeMux()
	mux.HandleFunc("/goal", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req GoalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if req.Goal == "" {
			http.Error(w, "goal is required", http.StatusBadRequest)
			return
		}

		slog.Info("server received goal", "goal", req.Goal[:min(80, len(req.Goal))])

		workDir, _ := os.Getwd()
		result, err := mgr.RunTask(ctx, req.Goal, req.Goal, "build", workDir, 0)

		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			slog.Error("task failed", "error", err)
			json.NewEncoder(w).Encode(GoalResponse{Error: err.Error()})
			return
		}

		json.NewEncoder(w).Encode(GoalResponse{Data: result})
	})

	addr := fmt.Sprintf(":%d", *port)
	slog.Info("starting server", "port", *port)
	fmt.Printf("UClaw Server listening on %s\n", addr)

	server := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		slog.Info("shutting down server")
		server.Close()
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "Server error: %s\n", err)
		os.Exit(1)
	}
}
