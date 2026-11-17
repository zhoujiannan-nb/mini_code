package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/user/mini_code/tools"
)


var observedReads = map[string]bool{
	"read":         true,
	"read_file":    true,
	"read_image":   true,
	"readimg":      true,
	"list_dir":     true,
	"glob":         true,
	"grep":         true,
	"search_files": true,
}

var contentReads = map[string]bool{
	"read":       true,
	"read_file":  true,
	"read_image": true,
	"readimg":    true,
}

var observedMutates = map[string]bool{
	"write":       true,
	"write_file":  true,
	"edit":        true,
	"edit_file":   true,
	"delete_file": true,
	"copy_file":   true,
	"move_file":   true,
}

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

func (l *AgentLoop) SetObservation(o *tools.Observation, workDir string) {
	l.obs = o
	l.obsDir = workDir
}

func (l *AgentLoop) obsPath(raw string) string {
	if raw == "" || filepath.IsAbs(raw) || l.obsDir == "" {
		return raw
	}
	return filepath.Join(l.obsDir, raw)
}

func toolPathArg(params map[string]interface{}) string {
	if p, _ := params["file_path"].(string); p != "" {
		return p
	}
	p, _ := params["path"].(string)
	return p
}

func (l *AgentLoop) checkWriteBeforeRead(name string, params map[string]interface{}) string {
	if l.obs == nil || readGateOff() || !observedMutates[name] {
		return ""
	}
	if name == "write_file" || name == "write" {
		if appendMode, _ := params["append"].(bool); appendMode {
			return ""
		}
	}
	path := toolPathArg(params)
	if path == "" {
		return ""
	}
	need, reason := l.obs.NeedsRead(l.obsPath(path))
	if !need {
		return ""
	}
	return fmt.Sprintf("Error: refusing to modify a file the session has not read: %s. Read it first (call read on %q; one page is enough for a large file), then retry. "+
		"write with append=true is allowed without a read. Do not guess at the file's content and do not work around this by using the shell.",
		reason, path)
}

func (l *AgentLoop) observePolicy(callID, name string, params map[string]interface{}) {
	if l.obs == nil || !observedMutates[name] {
		return
	}
	path := toolPathArg(params)
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

func isToolFailure(text string) bool {
	return strings.HasPrefix(text, "Error") || text == "Interrupted" || strings.HasPrefix(text, "Warning:")
}

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
		if path := toolPathArg(params); path != "" {
			l.obs.NoteContentRead(l.obsPath(path))
		}
	case observedReads[name]:
		if path := toolPathArg(params); path != "" {
			l.obs.NoteRead(l.obsPath(path))
		}
	case observedMutates[name]:
		if path := toolPathArg(params); path != "" {
			l.obs.NoteWrite(l.obsPath(path))
		}
		if dst, _ := params["destination"].(string); dst != "" {
			l.obs.NoteWrite(l.obsPath(dst))
		}
	}
}
