package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/user/mini_code/tools"
)

// The observation hook has two halves, and they answer different questions:
//
//   - before a mutation runs, the write-before-read gate (checkWriteBeforeRead)
//     refuses to touch a file whose contents this session cannot prove it saw;
//   - after a call runs, the outcome is folded back into the session's file
//     memory (noteToolOutcome) so a read authorises the next edit and an edit
//     makes its own result the new truth.
//
// The names below are the tools that take a single "path" argument. exec is
// deliberately absent: a command's side effects on the tree are unknowable here
// without diffing, so the gate neither trusts nor blames it — the fingerprint
// check catches its damage the next time a file tool touches that path.

// observedReads are the calls that count as having seen a path exists.
var observedReads = map[string]bool{
	"read_file":    true,
	"readimg":      true,
	"list_dir":     true,
	"glob":         true,
	"search_files": true,
}

// contentReads are the reads whose result is the file's TEXT: only these
// authorise a later overwrite. A directory listing proves a name exists, and a
// search hit proves one line matches — neither is knowing what an overwrite
// would destroy.
var contentReads = map[string]bool{
	"read_file": true,
	"readimg":   true,
}

// observedMutates are the calls that change a path, and therefore the ones whose
// blindness is worth recording and whose preconditions are worth enforcing.
var observedMutates = map[string]bool{
	"write_file":  true,
	"edit_file":   true,
	"delete_file": true,
	"copy_file":   true,
	"move_file":   true,
}

// readGateEnv disables the write-before-read gate for a process
// (MC_SKIP_READ_GATE=1). It exists for the same reason the tool-ablation switch
// does: a gate that blocks work is a claim that must be measurable, and the only
// honest way to measure "does this gate cost more than it saves" is to be able
// to turn it off without editing code.
const readGateEnv = "MC_SKIP_READ_GATE"

var (
	readGateOnce     sync.Once
	readGateDisabled bool
)

func readGateOff() bool {
	readGateOnce.Do(func() {
		readGateDisabled = strings.TrimSpace(os.Getenv(readGateEnv)) == "1"
	})
	return readGateDisabled
}

// SetObservation installs the session's file-observation memory and the working
// directory the tools resolve relative paths against (the gate has to key paths
// the same way the tools do, or "src\\a.go" read and "src/a.go" written would
// look like two different files). Must be called before Run; nil leaves the hook
// inert.
func (l *AgentLoop) SetObservation(o *tools.Observation, workDir string) {
	l.obs = o
	l.obsDir = workDir
}

// obsPath resolves a tool's path argument the way the file tools do, so the gate
// and the memory agree on identity.
func (l *AgentLoop) obsPath(raw string) string {
	if raw == "" || filepath.IsAbs(raw) || l.obsDir == "" {
		return raw
	}
	return filepath.Join(l.obsDir, raw)
}

// checkWriteBeforeRead returns a refusal message when this call would modify an
// existing file the session has not read (or has read only before it changed on
// disk). An empty return means the call may proceed.
//
// Two exemptions, both deliberate:
//   - append mode cannot destroy anything, and it is the documented way to
//     build a large file in chunks — the file's existing content is not being
//     replaced, so there is nothing to have read;
//   - creating a new file is not a blind overwrite (tools.Observation.NeedsRead
//     already returns false for a path that does not exist).
func (l *AgentLoop) checkWriteBeforeRead(name string, params map[string]interface{}) string {
	if l.obs == nil || readGateOff() || !observedMutates[name] {
		return ""
	}
	if name == "write_file" {
		if appendMode, _ := params["append"].(bool); appendMode {
			return ""
		}
	}
	path, _ := params["path"].(string)
	if path == "" {
		return ""
	}
	need, reason := l.obs.NeedsRead(l.obsPath(path))
	if !need {
		return ""
	}
	return fmt.Sprintf("Error: refusing to modify a file the session has not read: %s. Read it first (call read_file on %q; one page is enough for a large file), then retry. "+
		"write_file with append=true is allowed without a read. Do not guess at the file's content and do not work around this by using the shell.",
		reason, path)
}

// observePolicy raises the ledger row for a mutation and records nothing else:
// the gate above is what decides, this is what reports.
func (l *AgentLoop) observePolicy(callID, name string, params map[string]interface{}) {
	if l.obs == nil || !observedMutates[name] {
		return
	}
	path, _ := params["path"].(string)
	if path == "" {
		return
	}
	l.emitEvent(EventPolicyObserve, l.curTurn, map[string]any{
		"call_id": callID,
		"tool":    name,
		"path":    path,
		"blind":   l.obs.Blind(l.obsPath(path)),
	})
}

// isToolFailure reports whether a tool's text starts with one of the failure
// prefixes the file tools use. "Warning:" is a failure for this purpose: the
// edit warning means nothing was written.
func isToolFailure(text string) bool {
	return strings.HasPrefix(text, "Error") || text == "Interrupted" || strings.HasPrefix(text, "Warning:")
}

// noteToolOutcome folds one completed call into the session's file memory:
// a successful read authorises a later write, a successful write makes its own
// result the new truth, and a failed call changes nothing.
//
// Recording the write is what keeps the gate from demanding a redundant re-read
// after every edit — the model produced those bytes, so it knows them.
func (l *AgentLoop) noteToolOutcome(name string, params map[string]interface{}, result *tools.ToolResult) {
	if l.obs == nil || result == nil || result.HasMultimodal() && !contentReads[name] {
		return
	}
	text := result.Text
	if isToolFailure(text) {
		return
	}
	switch {
	case contentReads[name]:
		if path, _ := params["path"].(string); path != "" {
			l.obs.NoteContentRead(l.obsPath(path))
		}
	case observedReads[name]:
		if path, _ := params["path"].(string); path != "" {
			l.obs.NoteRead(l.obsPath(path))
		}
	case observedMutates[name]:
		if path, _ := params["path"].(string); path != "" {
			l.obs.NoteWrite(l.obsPath(path))
		}
		// copy_file / move_file also create the destination.
		if dst, _ := params["destination"].(string); dst != "" {
			l.obs.NoteWrite(l.obsPath(dst))
		}
	}
}
