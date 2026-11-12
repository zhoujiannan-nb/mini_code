package session

import (
	"github.com/user/mini_code/tools"
)

// The observation hook: what the ledger learns about files the session mutates
// without having looked at them. See tools/policy.go for why this measures and
// never blocks; this file is only the wiring that knows the tool names.
//
// The names below are the tools that take a single "path" argument. exec is
// deliberately absent: a command's side effects on the tree are unknowable here
// without diffing, so pretending otherwise would put a number in the ledger that
// nobody should trust.

// observedReads are the calls that count as having seen a path.
var observedReads = map[string]bool{
	"read_file":    true,
	"readimg":      true,
	"list_dir":     true,
	"glob":         true,
	"search_files": true,
}

// observedMutates are the calls that change a path, and therefore the ones whose
// blindness is worth recording.
var observedMutates = map[string]bool{
	"write_file":  true,
	"edit_file":   true,
	"delete_file": true,
	"copy_file":   true,
	"move_file":   true,
}

// SetObservation installs the session's file-observation memory. Must be called
// before Run; nil leaves the hook inert.
func (l *AgentLoop) SetObservation(o *tools.Observation) { l.obs = o }

// observePolicy records one call's effect on the session's file memory, and
// raises the ledger row when a mutation lands on a file nobody looked at.
//
// Reads are noted before the call runs and the blind check is done before the
// mutation: after write_file the file exists either way, and "was it there and
// unread?" is the question being asked.
func (l *AgentLoop) observePolicy(callID, name string, params map[string]interface{}) {
	if l.obs == nil {
		return
	}
	path, _ := params["path"].(string)
	if path == "" {
		return
	}
	switch {
	case observedReads[name]:
		l.obs.NoteRead(path)
	case observedMutates[name]:
		l.emitEvent(EventPolicyObserve, l.curTurn, map[string]any{
			"call_id": callID,
			"tool":    name,
			"path":    path,
			"blind":   l.obs.Blind(path),
		})
	}
}
