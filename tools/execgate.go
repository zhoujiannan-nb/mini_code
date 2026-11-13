package tools

import (
	"os"
	"regexp"
	"strings"
)

// This file is the exec "discipline gate": a hard counterpart to the rules the
// system prompt states (inspect files with the dedicated tools, never sleep on
// background work). duel3 showed the model talks itself into sleep-waiting and
// dir-walking despite the prompt wording, so the clearly-identifiable shapes
// are refused at exec time with a pointer to the right tool. Deliberately
// conservative: only unambiguous shapes are listed, and anything with a
// plausible legitimate reading (findstr in a pipe, ping to a real host) stays
// allowed. MC_EXEC_GATE=0 disables the whole gate (the dangerousPatterns block
// in shell.go is separate and always on).

var execGateEnabled = func() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("MC_EXEC_GATE")))
	return v != "0" && v != "off" && v != "false"
}()

type disciplineRule struct {
	pat    *regexp.Regexp
	reason string
}

var disciplineRules = []disciplineRule{
	// --- never sleep on background work: jobs notify, job_output waits ---
	{regexp.MustCompile(`(?i)\btimeout\s+/t\b`), "waiting with `timeout /t`: background exec jobs notify you when they finish, and job_output(wait:true) waits for one properly"},
	{regexp.MustCompile(`(?i)\bstart-sleep\b`), "waiting with Start-Sleep: background exec jobs notify you when they finish; use job_output(wait:true) if you are genuinely blocked on one"},
	{regexp.MustCompile(`(?i)(?:^|[;&|(]\s*)ping\s+-n\s+\d+\s+(?:localhost|127\.0\.0\.1|0\.0\.0\.0)`), "using ping as a sleep: background exec jobs notify you when they finish; use job_output(wait:true)"},
	{regexp.MustCompile(`(?i)(?:^|[;&|(]\s*)sleep\s+\d`), "sleeping to wait: background exec jobs notify you when they finish; use job_output(wait:true)"},
	// --- inspect text files with read_file, not the shell ---
	{regexp.MustCompile(`(?i)(?:^|[;&|]\s*)(?:type|cat|more)\s+\S`), "shell file viewing (type/cat/more): use read_file (offset/limit for ranges)"},
	{regexp.MustCompile(`(?i)(?:^|[;&|]\s*)(?:head|tail|sed)\s+\S`), "shell file slicing (head/tail/sed): use read_file with offset/limit"},
	// --- discover files with glob/list_dir, not recursive shell walking ---
	{regexp.MustCompile(`(?i)\bdir\b[^|&\n]*/s\b`), "recursive dir /s tree-walking: use glob (name patterns) or list_dir (one level, recursive:true)"},
	{regexp.MustCompile(`(?i)\bfor\s+/r\b`), "`for /r` tree-walking: use glob or list_dir with recursive:true"},
	{regexp.MustCompile(`(?i)\bwhere\s+/r\b`), "`where /r` tree-walking: use glob"},
	// --- search file contents with search_files, not recursive findstr ---
	{regexp.MustCompile(`(?i)\bfindstr\b[^|&\n]*/s\b`), "recursive findstr content search: use search_files"},
}

// disciplineDeny returns the refusal message for commands that match the
// prompt-discipline patterns the harness enforces, or "" when the command is
// fine. Advisory only for the model: it may re-issue with the named tool.
func disciplineDeny(command string) string {
	if !execGateEnabled {
		return ""
	}
	for _, r := range disciplineRules {
		if r.pat.MatchString(command) {
			return r.reason
		}
	}
	return ""
}
