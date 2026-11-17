package tools

import (
	"os"
	"regexp"
	"strings"
)


func execGateEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("MC_EXEC_GATE")))
	return v != "0" && v != "off" && v != "false"
}

type disciplineRule struct {
	pat    *regexp.Regexp
	reason string
}

var disciplineRules = []disciplineRule{
	{regexp.MustCompile(`(?i)\btimeout\s+/t\b`), "waiting with `timeout /t`: background exec jobs notify you when they finish, and job_output(wait:true) waits for one properly"},
	{regexp.MustCompile(`(?i)\bstart-sleep\b`), "waiting with Start-Sleep: background exec jobs notify you when they finish; use job_output(wait:true) if you are genuinely blocked on one"},
	{regexp.MustCompile(`(?i)(?:^|[;&|(]\s*)ping\s+-n\s+\d+\s+(?:localhost|127\.0\.0\.1|0\.0\.0\.0)`), "using ping as a sleep: background exec jobs notify you when they finish; use job_output(wait:true)"},
	{regexp.MustCompile(`(?i)(?:^|[;&|(]\s*)sleep\s+\d`), "sleeping to wait: background exec jobs notify you when they finish; use job_output(wait:true)"},
	{regexp.MustCompile(`(?i)(?:^|[;&|]\s*)(?:type|cat|more)\s+\S`), "shell file viewing (type/cat/more): use read (offset/limit for ranges)"},
	{regexp.MustCompile(`(?i)(?:^|[;&|]\s*)(?:head|tail|sed)\s+\S`), "shell file slicing (head/tail/sed): use read with offset/limit"},
	{regexp.MustCompile(`(?i)\bdir\b[^|&\n]*/s\b`), "recursive dir /s tree-walking: use glob (name patterns)"},
	{regexp.MustCompile(`(?i)\bfor\s+/r\b`), "`for /r` tree-walking: use glob"},
	{regexp.MustCompile(`(?i)\bwhere\s+/r\b`), "`where /r` tree-walking: use glob"},
	{regexp.MustCompile(`(?i)\bfindstr\b[^|&\n]*/s\b`), "recursive findstr content search: use grep"},
}

func disciplineDeny(command string) string {
	if !execGateEnabled() {
		return ""
	}
	for _, r := range disciplineRules {
		if r.pat.MatchString(command) {
			return r.reason
		}
	}
	return ""
}
