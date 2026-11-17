package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/util"
)


const (
	shellIdleLimit  = 30 * time.Minute
	shellStartWait  = 15 * time.Second
	shellCdWait     = 10 * time.Second
	shellStderrTail = 60 * time.Millisecond
	shellErrBufCap  = 1 << 20
)

type runKind int

const (
	kindOK      runKind = iota
	kindPreStart
	kindTimeout
	kindCancel
	kindGone
)

type shellOutcome struct {
	kind   runKind
	out    []byte
	stderr []byte
	cwd    string
	code   int
}

type persistentShell struct {
	spawnDir string

	mu       sync.Mutex
	seq      uint64
	cwd      string
	lastUsed time.Time
	started  bool

	cmd      *exec.Cmd
	out      *bufio.Reader
	inW      *os.File
	outR     *os.File
	errR     *os.File
	killTree func()
	waitOnce sync.Once

	applyEnv []string

	errMu   sync.Mutex
	errBuf  []byte
	errFeed func(string)
}


var shellTracker = struct {
	mu   sync.Mutex
	live map[*persistentShell]struct{}
	once sync.Once
}{live: make(map[*persistentShell]struct{})}

func trackShell(ps *persistentShell) {
	shellTracker.once.Do(func() { go shellReaper() })
	shellTracker.mu.Lock()
	shellTracker.live[ps] = struct{}{}
	shellTracker.mu.Unlock()
}

func shellReaper() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for range t.C {
		shellTracker.mu.Lock()
		pss := make([]*persistentShell, 0, len(shellTracker.live))
		for p := range shellTracker.live {
			pss = append(pss, p)
		}
		shellTracker.mu.Unlock()
		for _, p := range pss {
			p.mu.Lock()
			if p.started && time.Since(p.lastUsed) > shellIdleLimit {
				p.teardownLocked()
			}
			p.mu.Unlock()
		}
	}
}


func (ps *persistentShell) start() error {
	if ps.started {
		return nil
	}
	if !persistentSupported() {
		return fmt.Errorf("persistent shell not supported on this platform")
	}
	cmd, inW, outR, errR, release, err := startShellProcess(ps.spawnDir)
	if err != nil {
		return err
	}
	ps.cmd, ps.inW, ps.outR, ps.errR, ps.killTree = cmd, inW, outR, errR, release
	ps.out = bufio.NewReader(outR)
	ps.waitOnce = sync.Once{}
	ps.started = true
	ps.errMu.Lock()
	ps.errBuf, ps.errFeed = nil, nil
	ps.errMu.Unlock()
	go ps.drainStderr()

	guard := time.AfterFunc(shellStartWait, ps.abortResources)
	a, b, d := ps.tokens()
	payload := "@echo off\r\n" + ps.echoLines(a, b, d)
	_, code, _, rerr := ps.exchange(context.Background(), payload, a, b, d, nil)
	guard.Stop()
	if rerr != nil {
		ps.teardownLocked()
		return fmt.Errorf("persistent shell bootstrap failed: %w", rerr)
	}
	ps.lastUsed = time.Now()
	if ps.cwd == "" {
		if ps.spawnDir != "" {
			ps.cwd = ps.spawnDir
		} else if wd, werr := os.Getwd(); werr == nil {
			ps.cwd = wd
		}
	}
	_ = code
	return nil
}

func (ps *persistentShell) abortResources() {
	if ps.inW != nil {
		_ = ps.inW.Close()
	}
	if ps.killTree != nil {
		ps.killTree()
	}
	if ps.cmd != nil && ps.cmd.Process != nil {
		_ = ps.cmd.Process.Kill()
	}
}

func (ps *persistentShell) reap() {
	if ps.cmd != nil {
		ps.waitOnce.Do(func() { _ = ps.cmd.Wait() })
	}
}

func (ps *persistentShell) exitCode() int {
	if ps.cmd != nil && ps.cmd.ProcessState != nil {
		return ps.cmd.ProcessState.ExitCode()
	}
	return -1
}

func (ps *persistentShell) teardownLocked() {
	ps.abortResources()
	ps.reap()
	if ps.outR != nil {
		_ = ps.outR.Close()
	}
	if ps.errR != nil {
		_ = ps.errR.Close()
	}
	ps.cmd, ps.out, ps.inW, ps.outR, ps.errR, ps.killTree = nil, nil, nil, nil, nil, nil
	ps.started = false
}


func (ps *persistentShell) tokens() (a, b, d string) {
	pid, n := os.Getpid(), ps.seq
	return fmt.Sprintf("__MCS1_%d_%d__", pid, n),
		fmt.Sprintf("__MCS2_%d_%d__", pid, n),
		fmt.Sprintf("__MCD_%d_%d__", pid, n)
}

func (ps *persistentShell) echoLines(a, b, d string) string {
	return "echo " + d + "%CD%\r\necho " + a + "%ERRORLEVEL%" + b + "\r\n"
}

func (ps *persistentShell) exchange(ctx context.Context, payload, a, b, d string, feed func([]byte)) (content []byte, code int, cwd string, rerr error) {
	ps.wire(">> " + payload)
	if _, rerr = ps.inW.Write([]byte(payload)); rerr != nil {
		return nil, -1, "", rerr
	}
	var buf bytes.Buffer
	tokA, tokB, tokD := []byte(a), []byte(b), []byte(d)
	for {
		line, err := ps.out.ReadBytes('\n')
		if len(line) > 0 {
			ps.wire("<< " + strings.TrimRight(string(line), "\r\n"))
			l := bytes.TrimRight(line, "\r\n")
			if i := bytes.Index(l, tokA); i >= 0 {
				after := l[i+len(tokA):]
				if j := bytes.Index(after, tokB); j >= 0 {
					if c, cerr := strconv.Atoi(strings.TrimSpace(string(after[:j]))); cerr == nil {
						if i > 0 {
							buf.Write(l[:i])
						}
						return buf.Bytes(), c, cwd, nil
					}
				}
			}
			if j := bytes.Index(l, tokD); j >= 0 {
				if j > 0 {
					buf.Write(l[:j])
				}
				cwd = strings.TrimSpace(string(l[j+len(tokD):]))
				continue
			}
			buf.Write(l)
			buf.WriteByte('\n')
			if feed != nil {
				feed(line)
			}
		}
		if err != nil {
			return buf.Bytes(), -1, cwd, err
		}
		if ctx != nil && ctx.Err() != nil {
			return buf.Bytes(), -1, cwd, ctx.Err()
		}
	}
}


type persistentReport struct {
	out         shellOutcome
	preCwd      string
	startFailed bool
	cdFailed    bool
}

func (ps *persistentShell) exec(ctx context.Context, command, cwd string, timeoutSec int, sink func(string)) persistentReport {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.started && time.Since(ps.lastUsed) > shellIdleLimit {
		ps.teardownLocked()
	}
	if !ps.started {
		if err := ps.start(); err != nil {
			return persistentReport{startFailed: true, out: shellOutcome{kind: kindPreStart}}
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	preCwd := ps.cwd

	if lines := ps.applyEnv; len(lines) > 0 {
		ps.applyEnv = nil
		envCtx, envCancel := context.WithTimeout(ctx, shellCdWait)
		st, _ := ps.runLocked(envCtx, "", strings.Join(lines, " && "), nil)
		envCancel()
		if st.kind != kindOK {
			return persistentReport{startFailed: true, preCwd: preCwd}
		}
	}

	if cwd != "" && !sameDir(ps.cwd, cwd) {
		cdLine := `cd /d "` + filepath.Clean(cwd) + `"`
		cdCtx, cdCancel := context.WithTimeout(ctx, shellCdWait)
		cd, _ := ps.runLocked(cdCtx, "", cdLine, nil)
		cdCancel()
		if cd.kind == kindPreStart || cd.kind == kindTimeout || cd.kind == kindCancel || cd.kind == kindGone {
			return persistentReport{startFailed: true, preCwd: preCwd}
		}
		if cd.code != 0 {
			return persistentReport{out: cd, preCwd: preCwd, cdFailed: true}
		}
		preCwd = ps.cwd
	}

	out, _ := ps.runLocked(runCtx, command, "", sink)
	return persistentReport{out: out, preCwd: preCwd}
}

func (ps *persistentShell) runLocked(ctx context.Context, command, line string, sink func(string)) (shellOutcome, error) {
	var outF, errF *outputFeeder
	if sink != nil {
		outF = newOutputFeeder(ctx, sink, "")
		errF = newOutputFeeder(ctx, sink, "[stderr] ")
		defer outF.close()
		defer errF.close()
	}
	ps.errMu.Lock()
	ps.errBuf = nil
	if errF != nil {
		ps.errFeed = func(s string) { _, _ = errF.Write([]byte(s)) }
	}
	ps.errMu.Unlock()

	ps.seq++
	a, b, d := ps.tokens()
	var payload string
	if line != "" {
		payload = strings.TrimRight(line, "\r\n") + "\r\n"
	} else {
		payload = buildModelPayload(command)
	}
	payload += ps.echoLines(a, b, d)
	enc, ok := encodeShellInput(payload)
	if !ok {
		return shellOutcome{kind: kindPreStart}, nil
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			ps.abortResources()
		case <-done:
		}
	}()

	var feed func([]byte)
	if outF != nil {
		feed = func(bb []byte) { _, _ = outF.Write(bb) }
	}
	content, code, cwd, rerr := ps.exchange(ctx, string(enc), a, b, d, feed)
	close(done)

	time.Sleep(shellStderrTail)
	ps.errMu.Lock()
	stderr := append([]byte(nil), ps.errBuf...)
	ps.errFeed = nil
	ps.errMu.Unlock()

	res := shellOutcome{out: content, stderr: stderr, cwd: cwd, code: code}
	if rerr != nil {
		ps.abortResources()
		ps.reap()
		res.code = ps.exitCode()
		switch {
		case ctx.Err() == context.DeadlineExceeded:
			res.kind = kindTimeout
		case ctx.Err() == context.Canceled:
			res.kind = kindCancel
		default:
			res.kind = kindGone
		}
		ps.teardownLocked()
		return res, rerr
	}
	if cwd != "" {
		ps.cwd = cwd
	}
	ps.lastUsed = time.Now()
	res.kind = kindOK
	return res, nil
}


func (t *ExecTool) Close() error {
	if p := t.takePool(); p != nil {
		p.close()
	}
	return nil
}

func (t *ExecTool) poolFor() *shellPool {
	t.psMu.Lock()
	defer t.psMu.Unlock()
	if t.pool == nil {
		t.pool = newShellPool(t.workingDir)
	}
	return t.pool
}

func (t *ExecTool) takePool() *shellPool {
	t.psMu.Lock()
	p := t.pool
	t.pool = nil
	t.psMu.Unlock()
	return p
}

func (l *shellLease) run(ctx context.Context, command, cwd string, timeoutSec int, sink func(string)) persistentReport {
	ps := l.slot.ps
	if len(l.lines) > 0 {
		ps.mu.Lock()
		ps.applyEnv = l.lines
		ps.mu.Unlock()
		l.lines = nil
	}
	rep := ps.exec(ctx, command, l.targetCwd(cwd), timeoutSec, sink)
	l.setCwd(rep.out.cwd)
	if rep.out.kind == kindOK && reSetUse.MatchString(command) {
		l.readEnv(ctx)
	}
	return rep
}

func (l *shellLease) readEnv(ctx context.Context) {
	rep := l.slot.ps.exec(ctx, "set", "", int(shellCdWait/time.Second), nil)
	if rep.out.kind != kindOK {
		return
	}
	l.noteEnv([]byte(util.DecodeToUTF8(rep.out.out)))
}

func (t *ExecTool) persistentWanted(params map[string]interface{}, command string) bool {
	t.psMu.Lock()
	off := t.noPersistentS
	t.psMu.Unlock()
	if off || !persistentSupported() {
		return false
	}
	if in, _ := params["input"].(string); in != "" {
		return false
	}
	return persistentSafeInline(command)
}

func (t *ExecTool) noteShellFailure() {
	t.psMu.Lock()
	t.psFailures++
	if t.psFailures >= 2 {
		t.noPersistentS = true
	}
	t.psMu.Unlock()
}

type persistentOutcome struct {
	text     string
	fallback bool
	bad bool
	outcome shellOutcome
	ran     bool
}

func (t *ExecTool) runOnShell(ctx context.Context, command, cwd string, timeoutSec int, sink func(string)) persistentOutcome {
	if cwd != "" {
		if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
			return persistentOutcome{bad: true, text: fmt.Sprintf("Error: working_dir %q does not exist or is not a directory. Create it first (e.g. mkdir) or omit working_dir to run in the default directory.", cwd)}
		}
	}
	lease, err := t.poolFor().lease(ctx)
	if err != nil {
		return persistentOutcome{bad: true, text: "Error: the command was interrupted before it could start."}
	}
	poison := false
	defer func() { lease.release(poison) }()

	rep := lease.run(ctx, command, cwd, timeoutSec, sink)
	if rep.startFailed {
		t.noteShellFailure()
		return persistentOutcome{fallback: true}
	}
	out := rep.out
	if out.kind != kindOK {
		poison = true
	}

	res := persistentOutcome{outcome: out, ran: true}
	switch {
	case rep.cdFailed:
		res.bad = true
		res.text = fmt.Sprintf("Error: working_dir %q could not be entered (the shell reported exit code %d). It may have been deleted or renamed; pass a different working_dir or omit it.", cwd, out.code)
		return res
	case out.kind == kindTimeout:
		res.bad = true
		res.text = fmt.Sprintf("Error: Command timed out after %ds\nNote: the command's process tree was killed and the persistent shell restarted; any cd/set that command had done are gone.", timeoutSec)
		return res
	case out.kind == kindCancel:
		res.bad = true
		res.text = "Error: the command was interrupted; the persistent shell was restarted to recover (cd/set changes made by it, if any, are gone)."
		return res
	}
	note := ""
	if out.kind == kindGone {
		note = "Note: the persistent shell process itself exited during this command (e.g. an `exit` command closed it); the exit code above is the shell's own. A fresh shell starts on the next call."
	} else if out.cwd != "" && !sameDir(rep.preCwd, out.cwd) {
		note = "cwd: " + out.cwd
	}
	if note != "" {
		res.text = buildExecResult(out.out, out.stderr, out.code, "", note)
	} else {
		res.text = buildExecResult(out.out, out.stderr, out.code, "")
	}
	return res
}


var reForLoopVar = regexp.MustCompile(`(?i)^\s*for\s+%[a-z]\b`)

func persistentSafeInline(command string) bool {
	if reForLoopVar.MatchString(command) {
		return false
	}
	if strings.IndexByte(command, 0) >= 0 {
		return false
	}
	_, ok := encodeShellInput(command)
	return ok
}

func buildModelPayload(command string) string {
	var b strings.Builder
	for _, ln := range strings.Split(command, "\n") {
		b.WriteString(insertNulStdin(strings.TrimRight(ln, "\r")))
		b.WriteString("\r\n")
	}
	return b.String()
}

func insertNulStdin(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.Contains(t, "<") || strings.Contains(t, "^") {
		return line
	}
	clauses := splitTopAmp(t)
	for i, cl := range clauses {
		if strings.TrimSpace(cl) == "" {
			continue
		}
		segs := splitTop(cl, '|')
		if strings.ContainsAny(segs[0], "()") {
			continue
		}
		if skipNulForHead(cl) {
			continue
		}
		segs[0] = withNul(segs[0])
		clauses[i] = strings.Join(segs, "|")
	}
	return strings.Join(clauses, "&")
}

var reSetPrompt = regexp.MustCompile(`(?i)^\s*set\s+/p\b`)

func skipNulForHead(clause string) bool {
	c := strings.TrimSpace(clause)
	if reSetPrompt.MatchString(c) {
		return false
	}
	head := c
	if i := strings.IndexAny(head, " \t"); i >= 0 {
		head = head[:i]
	}
	return internalCmdNames[strings.ToLower(strings.Trim(head, "\""))]
}

var internalCmdNames = map[string]bool{
	"cd": true, "chdir": true, "dir": true, "echo": true, "set": true,
	"setlocal": true, "endlocal": true, "title": true, "prompt": true,
	"call": true, "exit": true, "cls": true, "path": true, "ver": true,
	"verify": true, "date": true, "time": true, "type": true, "copy": true,
	"move": true, "ren": true, "rename": true, "del": true, "erase": true,
	"mkdir": true, "md": true, "rmdir": true, "rd": true, "color": true,
	"rem": true, "shift": true, "start": true, "assoc": true,
	"ftype": true, "goto": true, "if": true, "for": true, "break": true,
}

func splitTopAmp(s string) []string {
	var parts []string
	inQ := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQ = !inQ
		case c == '&' && !inQ:
			prev := byte(' ')
			if i > 0 {
				prev = s[i-1]
			}
			var next byte
			if i+1 < len(s) {
				next = s[i+1]
			}
			if prev == '>' || prev == '<' || next == '>' || next == '<' {
				continue
			}
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

func withNul(seg string) string {
	t := strings.TrimRight(seg, " \t")
	return t + " < NUL" + seg[len(t):]
}

func splitTop(s string, sep byte) []string {
	var parts []string
	inQ := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			inQ = !inQ
		case c == sep && !inQ:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}


func (ps *persistentShell) drainStderr() {
	rd := bufio.NewReader(ps.errR)
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			ps.errMu.Lock()
			if len(ps.errBuf) < shellErrBufCap {
				ps.errBuf = append(ps.errBuf, line...)
			}
			f := ps.errFeed
			ps.errMu.Unlock()
			if f != nil {
				f(string(line))
			}
		}
		if err != nil {
			return
		}
	}
}


var (
	psWireOnce sync.Once
	psWireFile *os.File
)

func (ps *persistentShell) wire(s string) {
	psWireOnce.Do(func() {
		if p := os.Getenv("MINICODE_PS_WIRE"); p != "" {
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err == nil {
				psWireFile = f
			}
		}
	})
	if psWireFile != nil {
		fmt.Fprintln(psWireFile, s)
	}
}

func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func isASCIIStr(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
