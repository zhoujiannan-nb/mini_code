package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)


const waitSegmentMax = 4

const waitInterceptSec = 20

var (
	rePingWait = regexp.MustCompile(`^ping\s+(?:-n|-i)\s+(\d+)`)
	reTimeoutWait = regexp.MustCompile(`^timeout\s+/t\s+(\d+)`)
	reStartSleepWait = regexp.MustCompile(`(?i)^powershell\s+.*?start-sleep\s+(?:-seconds|-s)\s+(\d+)`)
	reStartSleepBare = regexp.MustCompile(`(?i)^start-sleep\s+(?:-seconds|-s)\s+(\d+)`)
	rePSCmdlet = regexp.MustCompile(`(?i);\s*(get|set|dir|type|write|start|stop|remove|new|invoke|select|where|foreach)\b|&\s*[a-z0-9_.\\/-]+\.(exe|cmd|bat|ps1)\b`)
	reWaitNoise = regexp.MustCompile(`(?i)(2>&1|>\s*nul|&\s*echo\s.*)`)
)

func pureWaitSeconds(command string) int {
	total := 0
	parts := strings.FieldsFunc(command, func(r rune) bool {
		return r == '&' || r == ';' || r == '\n' || r == '|'
	})
	if len(parts) == 0 || len(parts) > waitSegmentMax {
		return 0
	}
	slept := false
	for _, raw := range parts {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(s), "cd ") {
			continue
		}
		low := strings.ToLower(reWaitNoise.ReplaceAllString(s, ""))
		low = strings.TrimSpace(low)
		if low == "" || strings.HasPrefix(low, "echo ") {
			continue
		}
		var m []string
		switch {
		case strings.HasPrefix(low, "ping "):
			m = rePingWait.FindStringSubmatch(low)
		case strings.HasPrefix(low, "timeout "):
			m = reTimeoutWait.FindStringSubmatch(low)
		case strings.HasPrefix(low, "start-sleep"):
			m = reStartSleepBare.FindStringSubmatch(low)
		case strings.HasPrefix(low, "powershell"):
			if rePSCmdlet.MatchString(s) {
				return 0
			}
			m = reStartSleepWait.FindStringSubmatch(s)
		}
		if len(m) < 2 {
			return 0
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			return 0
		}
		total += n
		slept = true
	}
	if !slept {
		return 0
	}
	return total
}

func waitLoopNote(seconds int) string {
	return fmt.Sprintf(
		"Command skipped: it only sleeps (%ds) and waiting by sleeping is the one thing that cannot speed anything up.\n"+
			"Instead: keep working and let the notification arrive (job_list shows what is still running, and you are told when each one finishes), "+
			"or wait inside a tool that returns the answer — job_output(job_id=..., wait=true, timeout_ms=120000).\n"+
			"If you are waiting for something this session does not own (another process, a build server), run the check you were waiting for now, "+
			"and run it again if the result is not there yet.", seconds)
}
