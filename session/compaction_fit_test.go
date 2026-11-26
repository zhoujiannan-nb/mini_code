package session

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
)

func TestFitCompactionRequestShrinksToolPayloads(t *testing.T) {
	client := newClientWithBudget(t, "http://unused", 8192, 32768)
	defer client.Close()
	bigOut := strings.Repeat("tool output line ", 4000)
	bigArgs := `{"file_path":"a.txt","content":"` + strings.Repeat("x", 30000) + `"}`
	req := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "work"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "w1", Type: "function", Function: provider.FuncCall{Name: "write", Arguments: bigArgs}},
		}},
		{Role: "tool", Content: "ok", ToolCallID: "w1", Name: "write"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "e1", Type: "function", Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"run"}`}},
		}},
		{Role: "tool", Content: bigOut, ToolCallID: "e1", Name: "exec"},
		{Role: "assistant", Content: "recent"},
		BuildCompactRequest(""),
	}
	est := defaultTokenEstimator
	before := est(req)
	out := fitCompactionRequest(req, client, est, nil)
	if est(out) >= before {
		t.Fatalf("request should shrink: %d -> %d", before, est(out))
	}
	if out[0].GetText() != "SYSTEM" {
		t.Fatal("system must stay intact")
	}
	if !IsCompactRequest(out[len(out)-1]) {
		t.Fatal("the compaction instruction must stay last")
	}
	if out[len(out)-2].GetText() != "recent" {
		t.Fatal("the newest real message must stay intact")
	}
	prunedTool, agedArgs := false, false
	for _, m := range out {
		if m.Role == "tool" && strings.Contains(m.GetText(), "middle pruned") {
			prunedTool = true
		}
		for _, tc := range m.ToolCalls {
			if strings.Contains(tc.Function.Arguments, "aged out") {
				agedArgs = true
			}
		}
	}
	if !prunedTool || !agedArgs {
		t.Fatalf("expected pruned tool result (%v) and aged write args (%v)", prunedTool, agedArgs)
	}
}

func TestSummarizeRequestFitsWindowAndCapsOutput(t *testing.T) {
	var gotMaxTokens int
	var promptChars int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			MaxTokens int `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		gotMaxTokens = req.MaxTokens
		for _, m := range req.Messages {
			promptChars += len(m.Content)
		}
		sseText(w, "## Goal\nsummarized")
	}))
	defer srv.Close()
	client := newClientWithBudget(t, srv.URL, 8192, 32768)
	defer client.Close()

	bigOut := strings.Repeat("kernel benchmark output ", 3000)
	msgs := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "work"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "e1", Type: "function", Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"bench"}`}},
		}},
		{Role: "tool", Content: bigOut, ToolCallID: "e1", Name: "exec"},
		{Role: "assistant", Content: "r1"},
		{Role: "assistant", Content: "r2"},
		{Role: "assistant", Content: "r3"},
	}
	c := NewContextCompactor(client)
	out, err := c.SummarizeAndRebuild(context.Background(), msgs, testToolDefs(), "")
	if err != nil {
		t.Fatalf("SummarizeAndRebuild: %v", err)
	}
	if len(out) == 0 || !strings.Contains(out[1].GetText(), "<previous-summary>") {
		t.Fatal("rebuild should carry the summary")
	}
	if gotMaxTokens <= 0 || gotMaxTokens > 8192 {
		t.Fatalf("summary call must cap max_tokens (got %d)", gotMaxTokens)
	}
	if promptChars >= len(bigOut)/2 {
		t.Fatalf("the oversized tool output should have been pruned before sending: prompt %d chars vs original tool %d", promptChars, len(bigOut))
	}
}
