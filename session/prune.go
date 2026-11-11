package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dsh-style tool-result pruning (see deepseek-harness compaction-tool-result-pruner):
// an oversized tool result is kept head + tail with the low-value middle
// replaced by a marker. The cut is computed in RUNES so a boundary can never
// split a multi-byte character. The full text optionally spills to a file the
// model can re-read (exec/type) — the pruned copy is what enters the model
// history and the replacement ledger row.
//
// Placement of the pass: the loop prunes each tool result ONCE at completion
// (cheap, model-free, deterministic), instead of waiting for compaction
// pressure like dsh does. The ledger keeps full fidelity: the original full
// tool/result row stays append-only, followed by a compaction/prune shadow
// event pricing the cut and a replacement tool/result row (same call_id,
// "replaces" set) that readers fold over the original.
type pruneConfig struct {
	Threshold int // rune length above which pruning kicks in
	Head      int // runes kept from the front
	Tail      int // runes kept from the end
}

// defaultPrune mirrors dsh's defaults (8192/4096/1024 chars): head carries the
// command echo and structure, tail carries the verdict — errors and exit codes
// land at the end of tool output; the middle is the bulk.
var defaultPrune = pruneConfig{Threshold: 8192, Head: 4096, Tail: 1024}

// pruneToolText cuts s to head + marker + tail. Returns (pruned, true) when
// the text is over threshold; (”, false) otherwise, so callers can tell "no
// work needed" from "pruned". spillPath, when non-empty, is named in the
// marker so the model knows where the full output lives.
func pruneToolText(s string, spillPath string) (string, bool) {
	r := []rune(s)
	if len(r) <= defaultPrune.Threshold {
		return "", false
	}
	omitted := len(r) - defaultPrune.Head - defaultPrune.Tail
	marker := fmt.Sprintf("\n\n[... middle pruned: %d chars omitted", omitted)
	if spillPath != "" {
		marker += "; full output: " + spillPath
	}
	marker += " ...]\n\n"
	var b strings.Builder
	b.Grow(defaultPrune.Head + len(marker) + defaultPrune.Tail)
	b.WriteString(string(r[:defaultPrune.Head]))
	b.WriteString(marker)
	b.WriteString(string(r[len(r)-defaultPrune.Tail:]))
	out := b.String()
	// Same guard dsh enforces: a prune that did not shrink the payload is a bug.
	if len([]rune(out)) >= len(r) {
		return "", false
	}
	return out, true
}

// spillToolText writes the full text to dir/<sessionID>-<callID>.txt and
// returns the path ("" on any failure — spilling is best-effort; the marker
// simply omits the path and the ledger still holds the full row).
func spillToolText(dir, sessionID, callID, text string) string {
	if dir == "" || text == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(dir, sanitizeFileName(sessionID)+"-"+sanitizeFileName(callID)+".txt")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return ""
	}
	return path
}

// sanitizeFileName keeps session/call ids path-safe.
func sanitizeFileName(s string) string {
	return strings.Map(func(c rune) rune {
		switch c {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', ' ':
			return '_'
		}
		return c
	}, s)
}
