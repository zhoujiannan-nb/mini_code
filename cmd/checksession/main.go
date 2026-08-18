package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type rawMsg struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Func struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
	ToolCallID string `json:"tool_call_id"`
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > n {
		return s[:n] + fmt.Sprintf("... [+%d chars]", len(s)-n)
	}
	return s
}

func main() {
	sessionID := "e53cd83bc01b8ed69200528e"
	all := false
	if len(os.Args) > 1 && os.Args[1] != "" {
		if os.Args[1] == "all" {
			all = true
		} else {
			sessionID = os.Args[1]
		}
	}
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".mini_code", "agent.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		fmt.Println("open db error:", err)
		os.Exit(1)
	}
	defer db.Close()

	var ids []string
	if all {
		rows, err := db.Query("SELECT session_id FROM sessions")
		if err != nil {
			fmt.Println("query error:", err)
			os.Exit(1)
		}
		for rows.Next() {
			var id string
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
	} else {
		ids = []string{sessionID}
	}

	for _, id := range ids {
		auditSession(db, id)
	}
}

func auditSession(db *sql.DB, sessionID string) {
	var msgsStr string
	err := db.QueryRow("SELECT messages FROM sessions WHERE session_id = ?", sessionID).Scan(&msgsStr)
	if err != nil {
		fmt.Println("query error:", sessionID, err)
		return
	}
	var msgs []rawMsg
	if err := json.Unmarshal([]byte(msgsStr), &msgs); err != nil {
		fmt.Println("unmarshal messages error:", sessionID, err)
		return
	}
	fmt.Printf("\n=== session %s: %d messages, %d bytes of JSON ===\n", sessionID, len(msgs), len(msgsStr))

	assistantToolCallIDs := map[string]bool{} // tool_call_id declared by some assistant
	answered := map[string]bool{}             // tool_call_id that got a tool response
	badCount := 0
	for i, m := range msgs {
		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				args := tc.Func.Arguments
				assistantToolCallIDs[tc.ID] = true
				if strings.TrimSpace(args) == "" {
					fmt.Printf("MSG[%02d] assistant tool_call id=%-24s name=%-12s ARGUMENTS EMPTY\n", i, tc.ID, tc.Func.Name)
					badCount++
					continue
				}
				if !json.Valid([]byte(args)) {
					badCount++
					fmt.Printf("MSG[%02d] assistant tool_call id=%-24s name=%-12s INVALID JSON (%d bytes): %s\n", i, tc.ID, tc.Func.Name, len(args), trunc(args, 300))
				}
			}
		case "tool":
			answered[m.ToolCallID] = true
			if !assistantToolCallIDs[m.ToolCallID] {
				fmt.Printf("MSG[%02d] tool msg tool_call_id=%s has NO matching assistant tool_call (orphan)\n", i, m.ToolCallID)
			}
		}
	}
	for i, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if !answered[tc.ID] {
				fmt.Printf("MSG[%02d] assistant tool_call id=%-24s name=%-12s has NO tool response (dangling)\n", i, tc.ID, tc.Func.Name)
			}
		}
	}
	if badCount == 0 {
		fmt.Println("ok: no invalid/empty tool-call arguments")
	}

	if os.Getenv("OUTLINE") != "1" {
		return
	}
	for i, m := range msgs {
		c := string(m.Content)
		if len(c) > 80 {
			c = c[:80] + "..."
		}
		c = strings.ReplaceAll(c, "\n", " ")
		extra := ""
		if len(m.ToolCalls) > 0 {
			names := make([]string, 0)
			for _, tc := range m.ToolCalls {
				names = append(names, tc.Func.Name)
			}
			extra = " tool_calls=" + strings.Join(names, ",")
		}
		if m.ToolCallID != "" {
			extra = " tool_call_id=" + m.ToolCallID
		}
		fmt.Printf("%02d %-9s %s%s\n", i, m.Role, trunc(c, 80), extra)
	}
}
