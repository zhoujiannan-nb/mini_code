package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Sleeping to wait is the one habit that survives every timeout mechanism,
// because it is not a timeout mechanism — it is the model refusing to end a turn
// while something is still running. In the run before this check, the six tasks
// contained fifteen pure sleeps (`ping -n 40`, `timeout /t 60`,
// `powershell Start-Sleep -Seconds 45`, `& echo waited`), worth roughly ten
// minutes of wall clock — more than the whole win the job machinery had just
// bought, spent on staring at nothing.
//
// A pure wait is intercepted rather than warned about, and that is a deliberate
// asymmetry: the command has no observable effect, so skipping it cannot break a
// plan, while running it costs the wait AND leaves the model in the habit. Any
// command that also *looks* at something (`type`, `dir`, a test run) is left
// completely alone — "sleep then check" is a polling loop, and polling is a
// behavior to measure, not a command to veto.

// waitSegmentMax is how many chained segments a command may have and still be
// considered a pure wait. Above this it is doing something else along the way.
const waitSegmentMax = 4

// waitInterceptSec is the shortest total sleep worth interrupting for. Under it,
// a sleep is a settle-the-races pause in front of a check, and the interception
// would cost more than the wait.
const waitInterceptSec = 20

var (
	// rePingWait matches `ping -n 40 127.0.0.1`, the classic Windows sleep.
	rePingWait = regexp.MustCompile(`^ping\s+(?:-n|-i)\s+(\d+)`)
	// reTimeoutWait matches `timeout /t 60 /nobreak`.
	reTimeoutWait = regexp.MustCompile(`^timeout\s+/t\s+(\d+)`)
	// reStartSleepWait matches `Start-Sleep -Seconds 45` (bare or inside a
	// powershell -Command), and nothing else in the same segment.
	reStartSleepWait = regexp.MustCompile(`(?i)^powershell\s+.*?start-sleep\s+(?:-seconds|-s)\s+(\d+)`)
	reStartSleepBare = regexp.MustCompile(`(?i)^start-sleep\s+(?:-seconds|-s)\s+(\d+)`)
	// reStartSleepHasWork rejects a powershell one-liner that sleeps and then
	// does something (Get-ChildItem, a native command): that is a check, not a wait.
	rePSCmdlet = regexp.MustCompile(`(?i);\s*(get|set|dir|type|write|start|stop|remove|new|invoke|select|where|foreach)\b|&\s*[a-z0-9_.\\/-]+\.(exe|cmd|bat|ps1)\b`)
	// reWaitNoise is what may hang off a wait segment without making it a real
	// command: redirection and an echo of "I waited". The timeout /t and /nobreak
	// flags deliberately stay, because the matcher for that segment needs them.
	reWaitNoise = regexp.MustCompile(`(?i)(2>&1|>\s*nul|&\s*echo\s.*)`)
)

// pureWaitSeconds returns how long a command sleeps, or 0 when it is not a pure
// wait. A command counts as pure only when every chained segment is a sleep or an
// echo, which is what keeps "sleep then look at the result" running untouched.
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
		// `cd /d X` alone in a chain is still nobody's work: the model that
		// changes directory and sleeps was only repositioning for the sleep.
		if strings.HasPrefix(strings.ToLower(s), "cd ") {
			continue
		}
		low := strings.ToLower(reWaitNoise.ReplaceAllString(s, ""))
		low = strings.TrimSpace(low)
		if low == "" || strings.HasPrefix(low, "echo ") {
			continue // no-op
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
				return 0 // it sleeps AND looks: that is a poll, let it run
			}
			m = reStartSleepWait.FindStringSubmatch(s)
		}
		if len(m) < 2 {
			return 0 // some segment does real work
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

// waitLoopNote is the refusal text: why nothing ran, what to do instead, in the
// order the model can act on. It names job_output's wait because a model that
// genuinely must wait should wait inside a tool that returns the result.
func waitLoopNote(seconds int) string {
	return fmt.Sprintf(
		"Command skipped: it only sleeps (%ds) and waiting by sleeping is the one thing that cannot speed anything up.\n"+
			"Instead: keep working and let the notification arrive (job_list shows what is still running, and you are told when each one finishes), "+
			"or wait inside a tool that returns the answer — job_output(job_id=..., wait=true, timeout_ms=120000).\n"+
			"If you are waiting for something this session does not own (another process, a build server), run the check you were waiting for now, "+
			"and run it again if the result is not there yet.", seconds)
}
