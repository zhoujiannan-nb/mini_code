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

// Persistent shell session for the exec tool (dsh's "persistent bash/pwsh"
// equivalent, adapted to the cmd.exe dialect the whole persona teaches).
//
// Why: the one-shot path pays a full cmd.exe cold start on EVERY exec call
// (~0.5s bare, seconds on boxes where AV hooks image creation), and a
// verification loop of a dozen calls makes inline self-checks feel expensive
// — measured on the A/B range, that tax pushed the model toward "write a
// script file, run the file" scaffolding loops while the same model on a
// persistent shell verified everything inline in a handful of calls. One
// long-lived cmd.exe driven over stdin removes the per-call process cost
// and gives the model real shell state (cwd, env vars) across calls.
//
// Protocol (the same shape for bootstrap, working_dir cd, and model
// commands):
//
//	<command lines>                 each line gets " < NUL" so a stdin-hungry
//	                                program cannot swallow the lines below
//	echo __MCD_<pid>_<seq>__%CD%              reports the shell's cwd
//	echo __MCS1_<pid>_<seq>__%ERRORLEVEL%__MCS2_<pid>_<seq>__   end + code
//
// The reply is read line by line until a line carries MCS1 <int> MCS2; a
// command output that ended without a trailing newline merges into the
// sentinel line and is cut at the marker. Tokens embed pid+sequence and the
// code must parse as an integer between them, so output that merely looks
// like a marker cannot end the exchange early.
//
// Failure model: anything that wedges the shell (timeout kill, user cancel,
// the model running `exit`, a REPL eating the pipe) tears it down and the
// next call transparently starts a fresh one — only shell state is ever
// lost, and the result text says so. Commands that must not run this way
// (stdin `input`, batch `for %x` loops, text the OEM code page cannot
// encode, a shell that fails to boot twice) fall back to the intact
// one-shot cmd.exe path. A/B escape hatch: MINICODE_NO_PERSISTENT_SHELL=1.

const (
	shellIdleLimit  = 30 * time.Minute
	shellStartWait  = 15 * time.Second
	shellCdWait     = 10 * time.Second
	shellStderrTail = 60 * time.Millisecond
	shellErrBufCap  = 1 << 20
)

type runKind int

const (
	kindOK      runKind = iota // normal completion; code is the command's ERRORLEVEL
	kindPreStart               // failed before the command could run (one-shot retry is safe)
	kindTimeout                // ctx deadline; shell killed and reset
	kindCancel                 // ctx canceled (user stop); shell killed and reset
	kindGone                   // shell process died mid-command (e.g. model ran `exit`)
)

type shellOutcome struct {
	kind   runKind
	out    []byte // command stdout bytes, bookkeeping lines removed
	stderr []byte // bytes the shell wrote to stderr during this command
	cwd    string // shell cwd after the command ("" if unknown)
	code   int
}

type persistentShell struct {
	spawnDir string

	mu       sync.Mutex // serializes commands: one command at a time per shell
	seq      uint64
	cwd      string
	lastUsed time.Time
	started  bool

	cmd      *exec.Cmd
	out      *bufio.Reader
	inW      *os.File // our end of the stdin pipe (write)
	outR     *os.File // our end of the stdout pipe (read)
	errR     *os.File // our end of the stderr pipe (read)
	killTree func()   // releases the job object (kills the whole tree)
	waitOnce sync.Once

	// applyEnv is the shell state the pool computed for this lease: `set` lines
	// that bring a fresh or recycled cmd.exe up to the session's environment. It
	// runs once, before the command, and is cleared whether or not it succeeded.
	applyEnv []string

	// stderr arrives asynchronously; errMu guards the per-command snapshot
	// and the live-feed hook (set only while a command runs).
	errMu   sync.Mutex
	errBuf  []byte
	errFeed func(string)
}

// ---- idle tracker: reaps shells of abandoned sessions ----

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

// ---- process lifecycle ----

// start spawns cmd.exe and runs the bootstrap so the first real command
// sees a clean protocol stream. Caller holds ps.mu.
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

	// With redirected stdin cmd.exe consumes commands like a batch stream;
	// /Q already starts it with echo off, this is belt-and-braces in case a
	// registry AutoRun flipped it on (echoed command lines would pollute the
	// stream). The exchange drains any banner text, so boot noise never
	// reaches the model. Hard guard: a machine whose cmd.exe hangs on an
	// autorun profile must fail the START, not the session.
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

// abortResources releases OS resources (stdin pipe, job, process) without
// touching ps fields, so the watchdog goroutine can call it while runLocked
// holds ps.mu. Idempotent: double closes/kills just return ignored errors.
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

// reap waits for the (killed or exited) shell process exactly once.
func (ps *persistentShell) reap() {
	if ps.cmd != nil {
		ps.waitOnce.Do(func() { _ = ps.cmd.Wait() })
	}
}

// exitCode reports the OS exit status of the dead shell process; call after
// reap and before teardownLocked nils ps.cmd.
func (ps *persistentShell) exitCode() int {
	if ps.cmd != nil && ps.cmd.ProcessState != nil {
		return ps.cmd.ProcessState.ExitCode()
	}
	return -1
}

// teardownLocked kills whatever is running and forgets the process; the
// next start() rebuilds everything. Caller holds ps.mu. Idempotent.
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

// ---- protocol ----

func (ps *persistentShell) tokens() (a, b, d string) {
	pid, n := os.Getpid(), ps.seq
	return fmt.Sprintf("__MCS1_%d_%d__", pid, n),
		fmt.Sprintf("__MCS2_%d_%d__", pid, n),
		fmt.Sprintf("__MCD_%d_%d__", pid, n)
}

// echoLines builds the two bookkeeping lines every exchange appends: the
// current directory report, then the end marker carrying ERRORLEVEL.
func (ps *persistentShell) echoLines(a, b, d string) string {
	return "echo " + d + "%CD%\r\necho " + a + "%ERRORLEVEL%" + b + "\r\n"
}

// exchange writes payload and reads lines until the end marker, feeding raw
// output lines to feed (may be nil). On a read/write error the partial
// output is returned together with the error; the caller maps it through
// ctx to tell timeout/cancel from an unexpected shell death.
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
						// Output without a trailing newline merged into the
						// marker line: the prefix is real content.
						if i > 0 {
							buf.Write(l[:i])
						}
						return buf.Bytes(), c, cwd, nil
					}
				}
				// else: output merely shaped like a marker; keep it as content
			}
			if j := bytes.Index(l, tokD); j >= 0 {
				// the cwd report (possibly merged onto an unterminated output line)
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

// ---- command execution ----

// persistentReport is the result of one exec-tool call attempted on the
// persistent shell.
type persistentReport struct {
	out         shellOutcome
	preCwd      string // shell cwd before the command (for the cwd-change note)
	startFailed bool   // the shell never came up; command untouched, one-shot retry safe
	cdFailed    bool   // working_dir could not be entered (out.code is the cd's code)
}

// exec runs one tool call: ensure the shell, optionally cd to working_dir,
// then the command itself, all under one timeout budget.
func (ps *persistentShell) exec(ctx context.Context, command, cwd string, timeoutSec int, sink func(string)) persistentReport {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.started && time.Since(ps.lastUsed) > shellIdleLimit {
		ps.teardownLocked() // stale shell: reset rather than trust a possibly invalid cwd
	}
	if !ps.started {
		if err := ps.start(); err != nil {
			return persistentReport{startFailed: true, out: shellOutcome{kind: kindPreStart}}
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	preCwd := ps.cwd

	// Replay the session's shell state before anything else. A shell that was
	// just spawned (or just restarted after a kill) has the environment the
	// process started with, not the one the model built up call by call; and a
	// second shell for a concurrent command has neither. Replaying the `set`
	// diff is what lets "the session keeps its state" stay true across more than
	// one process. Failure here means the command never ran, so the caller is
	// still free to run it some other way.
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
			// The shell died on something as trivial as a cd, moments after
			// (re)start: treat as a start failure and let the one-shot path
			// run the command instead.
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

// runLocked runs one protocol exchange (model command, or one internal
// line when line != ""). Caller holds ps.mu and has a live shell.
func (ps *persistentShell) runLocked(ctx context.Context, command, line string, sink func(string)) (shellOutcome, error) {
	var outF, errF *outputFeeder
	if sink != nil {
		// Tee into the UI while the full bytes still land in the snapshot:
		// same coalescing feeders the one-shot path uses, so the live view
		// (including the [stderr] marking) does not change with the engine.
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
		payload = strings.TrimRight(line, "\r\n") + "\r\n" // internal commands verbatim
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

	// Let in-flight stderr land in the snapshot: the program had already
	// exited before cmd echoed the marker, so its bytes are only a scheduler
	// hop away.
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

// ---- exec tool integration ----

// Close releases the session's shells. Even without it nothing leaks — the job
// object kills each shell when the process exits, and the idle reaper catches
// abandoned sessions — but Close frees the shells (and their hold on the working
// directory) right away. Background work is NOT canceled here: whoever owns the
// job registry cancels it first, so a killed command gets a verdict instead of a
// vanished process. Idempotent; safe before any command ever ran.
func (t *ExecTool) Close() error {
	if p := t.takePool(); p != nil {
		p.close()
	}
	return nil
}

// poolFor lazily builds the session's shell pool. Per-ExecTool means
// per-session, and the pool — not any one shell — is what holds the session's
// cwd and environment.
func (t *ExecTool) poolFor() *shellPool {
	t.psMu.Lock()
	defer t.psMu.Unlock()
	if t.pool == nil {
		t.pool = newShellPool(t.workingDir)
	}
	return t.pool
}

// takePool detaches the pool so Close can tear it down without racing a command
// that is still using it.
func (t *ExecTool) takePool() *shellPool {
	t.psMu.Lock()
	p := t.pool
	t.pool = nil
	t.psMu.Unlock()
	return p
}

// run leases the shell and runs one command on it. The lease carries the
// environment replay (applied by exec, before the command) and the directory the
// session believes it is in, so a command never has to care which cmd.exe — or
// whether a brand-new one — is carrying it.
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
	// Read the environment back only when the command spelled `set`: that is the
	// only way the session's state can have moved, and the read costs a round
	// trip. Anything else leaves the pool's belief untouched, which is correct
	// precisely because nothing could have changed it.
	if rep.out.kind == kindOK && reSetUse.MatchString(command) {
		l.readEnv(ctx)
	}
	return rep
}

// readEnv snapshots the leased shell's environment through the same protocol the
// command itself uses. `set` with no arguments lists NAME=VALUE lines; it is a
// cmd.exe internal, so it costs no process spawn.
func (l *shellLease) readEnv(ctx context.Context) {
	rep := l.slot.ps.exec(ctx, "set", "", int(shellCdWait/time.Second), nil)
	if rep.out.kind != kindOK {
		return
	}
	// The shell writes its environment in the OEM code page, like any other
	// output: decode on the way in so the value replayed into the next shell
	// round-trips through the encoder the same way command output does.
	l.noteEnv([]byte(util.DecodeToUTF8(rep.out.out)))
}

// persistentWanted reports whether this call may run on the persistent shell at
// all. Each rejection is a property of the command, not of the shell: stdin
// payload (there the stdin IS the command channel), a batch `for %x` loop (which
// needs the interactive spelling), text the OEM code page cannot encode.
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

// noteShellFailure records a shell that could not be brought up. Two failures in
// a row mean this machine will not boot the persistent shell (an AutoRun profile
// that hangs, a locked-down cmd.exe), so stop paying for the attempt.
func (t *ExecTool) noteShellFailure() {
	t.psMu.Lock()
	t.psFailures++
	if t.psFailures >= 2 {
		t.noPersistentS = true
	}
	t.psMu.Unlock()
}

// persistentOutcome is what an attempt on the persistent shell ended up with:
// the command's model-facing text, or the decision to run the call some other
// way instead.
type persistentOutcome struct {
	text     string
	fallback bool // nothing ran on the shell; the one-shot path may take it
	// bad marks a text that starts with "Error:" — the caller's job status
	// follows from it, so the classification is made once, here.
	bad bool
	// outcome is the raw shell result, for callers that need the exit code
	// rather than the prose (the job runtime settles on it).
	outcome shellOutcome
	ran     bool // the command reached the shell
}

// runOnShell runs one exec call on a leased shell and renders it the way the
// model has always seen it. timeoutSec is the SHELL's own deadline: the
// synchronous path passes the call's timeout and gets the classic
// kill-and-reset text, while the job path passes the background limit and does
// its own waiting on the outside.
func (t *ExecTool) runOnShell(ctx context.Context, command, cwd string, timeoutSec int, sink func(string)) persistentOutcome {
	if cwd != "" {
		// Same up-front check as the one-shot path, same wording, so the
		// model's recovery behavior does not depend on which engine ran.
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
		// The command never ran: the one-shot path can still take it, and cannot
		// double-execute anything.
		t.noteShellFailure()
		return persistentOutcome{fallback: true}
	}
	out := rep.out
	if out.kind != kindOK {
		poison = true // whatever it was, this cmd.exe is not trustworthy again
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
		note = "cwd: " + out.cwd // tell the model where the cd landed; it steers blind otherwise
	}
	res.text = buildExecResult(command, out.out, out.stderr, out.code, note)
	return res
}

// ---- command text munging ----

// reForLoopVar rejects inline batch for-loops: `for %f in (...)` works when
// typed but needs %%f when cmd.exe reads redirected input, which is exactly
// how the persistent shell consumes commands. Falling back to the one-shot
// path (cmd /C, interactive spelling) keeps the common model idiom working.
var reForLoopVar = regexp.MustCompile(`(?i)^\s*for\s+%[a-z]\b`)

// persistentSafeInline reports whether the command may run inside the
// persistent shell at all.
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

// buildModelPayload turns the model's command into the stdin payload: one
// input line per shell line, each NUL-fed; the bookkeeping lines are
// appended by the caller.
func buildModelPayload(command string) string {
	var b strings.Builder
	for _, ln := range strings.Split(command, "\n") {
		b.WriteString(insertNulStdin(strings.TrimRight(ln, "\r")))
		b.WriteString("\r\n")
	}
	return b.String()
}

// insertNulStdin appends ` < NUL` to the stdin-reading head of every
// top-level clause of a command line. The persistent shell's stdin IS the
// command channel, so any program that reads stdin must instead see NUL/EOF
// — exactly what the one-shot path gives it via Go's nil-stdin-devnull
// rule. Without this, `more` or a REPL would eat the sentinel lines and
// wedge the protocol until timeout.
//
// The redirect is only applied to clauses whose head is an EXTERNAL program
// (first token contains a path separator or a dot, e.g. `python`,
// `node.exe`, `.\tool.exe`). cmd.exe implements a `<` redirect for its own
// internal commands (cd, set, mkdir, echo, ...) by running them in a forked
// child shell — measured on the probe, `cd x < NUL` then moves the CHILD's
// directory and the persistent shell never moves, which would silently
// destroy the cwd/env persistence the feature exists for. Internal commands
// never read stdin anyway, so they need nothing.
//
// Lines that already redirect stdin (`<`) or use caret continuations are
// left alone; those commands either do not read stdin or fall to the
// timeout-reset safety net.
func insertNulStdin(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.Contains(t, "<") || strings.Contains(t, "^") {
		return line
	}
	clauses := splitTopAmp(t)
	for i, cl := range clauses {
		if strings.TrimSpace(cl) == "" {
			continue // from "&&"/"||": the empty clause between the two &
		}
		segs := splitTop(cl, '|')
		if strings.ContainsAny(segs[0], "()") {
			continue // parenthesised group: placement gets tricky, hands off
		}
		if skipNulForHead(cl) {
			continue
		}
		segs[0] = withNul(segs[0])
		clauses[i] = strings.Join(segs, "|")
	}
	return strings.Join(clauses, "&")
}

// reSetPrompt matches `set /p VAR=prompt` — the one internal command that
// reads stdin. It must get the NUL redirect (it reads, so it would eat the
// sentinel); the forked-child side effect (the variable landing in the
// child and being lost) matches the one-shot path, where the same redirect
// against NUL leaves the variable just as undefined.
var reSetPrompt = regexp.MustCompile(`(?i)^\s*set\s+/p\b`)

// skipNulForHead reports whether a clause head is a cmd internal that
// never reads stdin — those must not be redirected, because cmd.exe honors
// a redirect on an internal command by forking a child shell, which would
// silently strand any state the command sets (cd landing in the child...).
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

// internalCmdNames is the conservative core of cmd.exe's (extension)
// builtins: words that are never real program files on PATH, so a clause
// head spelled exactly like one of them is definitely internal and definitely
// not a stdin reader (the sole exception, set /p, is handled above). Words
// outside the list default to the NUL redirect — the cost if one turns out
// to be an unlisted internal (pushd, subst...) is that it runs in a child
// and keeps no state, which is exactly what the one-shot path would have
// done; the cost of MISSING an external stdin reader (more.com, python,
// sort.exe, findstr...) would be a wedged protocol, so that side gets the
// safe default.
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

// splitTopAmp splits on clause-joining ampersands while leaving the ones
// that belong to a redirection operator (`2>&1`, `<&`) alone. && still
// splits into "a", "", "b" the way splitTop would.
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
				continue // part of >& or <& redirection, not a clause join
			}
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// withNul appends the NUL redirect ahead of any trailing whitespace, so the
// original separators survive the split/join round trip.
func withNul(seg string) string {
	t := strings.TrimRight(seg, " \t")
	return t + " < NUL" + seg[len(t):]
}

// splitTop splits on a byte that appears outside double quotes.
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

// ---- stderr plumbing ----

// drainStderr copies the shell's stderr pipe into the per-command snapshot
// and the live feed until the pipe closes (shell teardown). cmd.exe's own
// diagnostics go to stdout; the external programs' stderr — the part the
// model needs labelled — flows through here.
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

// ---- helpers ----

// psWire file, set from MINICODE_PS_WIRE for protocol debugging.
var (
	psWireOnce sync.Once
	psWireFile *os.File
)

// wire dumps one protocol line (payload or reply) when debugging is on.
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
