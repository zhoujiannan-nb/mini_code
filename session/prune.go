package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// dsh-style tool-result pruning (see deepseek-harness compaction-tool-result-pruner):
// an oversized tool result is kept head + tail with the low-value middle
// replaced by a marker. The cut is computed in RUNES so a boundary can never
// split a multi-byte character. The full text optionally spills to a file the
// model can re-read — the pruned copy is what enters the model
// history and the replacement ledger row.
//
// Placement of the pass: the loop prunes each tool result ONCE at completion
// (cheap, model-free, deterministic), instead of waiting for compaction
// pressure like dsh does. The ledger keeps full fidelity: the original full
// tool/result row stays append-only, followed by a compaction/prune shadow
// event pricing the cut and a replacement tool/result row (same call_id,
// "replaces" set) that readers fold over the original.
//
// Budget history: the first pass shipped dsh's PRUNER numbers (8192/4096/1024)
// as the WRITE-TIME policy. That was a mismatch — dsh's write-time budget is
// maxInlineTokens = 12500 est tokens (~50k chars), while 8192/4096/1024 is
// what its pressure-triggered pruner applies much later. At 5120 kept runes
// the pass cut exactly what the model needed to see (file reads, listings,
// spill re-reads) and forced extra recovery turns. The budget now matches
// the "inline ceiling" semantics: cut to Threshold, split head/tail in half
// like dsh's retention.ts.
type pruneConfig struct {
	Threshold int // rune length above which pruning kicks in
	Head      int // runes kept from the front
	Tail      int // runes kept from the end
}

// pruneMarkerRoom is the slack kept between Head+Tail and Threshold so the
// marker itself cannot push the pruned copy back over the threshold (a
// pruned result that still exceeds it would re-prune every pass — the
// idempotency the tests pin). The spill branch of the marker carries a path
// plus the recovery hint, so the slack covers path + prose.
const pruneMarkerRoom = 1024

// defaultPrune: 24576 runes (~6k tokens) inline ceiling, head/tail half-and-half
// of the kept budget. MINICODE_PRUNE_CHARS overrides the ceiling for tuning
// without a rebuild.
var defaultPrune = pruneConfig{Threshold: 24576, Head: (24576 - pruneMarkerRoom + 1) / 2, Tail: (24576 - pruneMarkerRoom) / 2}

func init() {
	if v := os.Getenv("MINICODE_PRUNE_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 4096 {
			defaultPrune = pruneConfig{Threshold: n, Head: (n - pruneMarkerRoom + 1) / 2, Tail: (n - pruneMarkerRoom) / 2}
		}
	}
}

// pruneExemptTools names tools whose results the pass must never cut: they
// already carry their own caps and continuation footers (glob's 100-result
// cap with the full-list spill notice, list_dir's listing budget), so a
// second cut would only break their own recovery contract.
var pruneExemptTools = map[string]bool{"glob": true, "list_dir": true}

// readTools are the content-read tools, and their exemption is PATH-based,
// not name-based. Only a re-read of a spill file must survive intact:
// cutting one would prune the recovery read too (and spawn ANOTHER spill
// file), the read→spill→read loop dsh pins out with its spill-policy
// exemption. A whole-file read of an ordinary project file is just inflow —
// a real game-session ledger showed five exempt-by-name whole-file reads
// alone cost ~50k tokens of attention — so those now pay the same head/tail
// cut as every other tool, with the full text spilling to a file the marker
// names and the original file still re-readable by offset/limit windows.
// Both the dsh name (read) and the historical mc name (read_file) share the
// rule so old ledger rows fold the same way.
var readTools = map[string]bool{"read": true, "read_file": true}

// pruneExemptCall is the call-site check: unconditional for the capped
// listing tools, spill-folder-scoped for the read tools, false for
// everything else. pathArg is the call's file argument ("" when it has none
// or the args do not parse) and spillDir the loop's spill folder.
func pruneExemptCall(name, pathArg, spillDir string) bool {
	if pruneExemptTools[name] {
		return true
	}
	if readTools[name] {
		return isUnderSpillDir(pathArg, spillDir)
	}
	return false
}

// isUnderSpillDir reports whether path is a file inside the spill folder.
// Only absolute paths qualify: the prune marker always names the absolute
// spill path, so a genuine spill re-read arrives absolute while any other
// read keeps the normal cut. Compared case-insensitively (Windows path
// casing) and on a cleaned separator boundary, so "spill_old" is not "spill".
func isUnderSpillDir(path, spillDir string) bool {
	if path == "" || spillDir == "" || !filepath.IsAbs(path) {
		return false
	}
	p, d := filepath.Clean(path), filepath.Clean(spillDir)
	if len(p) <= len(d) || !strings.EqualFold(p[:len(d)], d) {
		return false
	}
	c := p[len(d)]
	return c == filepath.Separator || c == '/' || c == '\\'
}

// toolCallPath pulls the file argument out of a tool call's raw JSON args
// ("" when absent or malformed — malformed args then take the normal cut).
func toolCallPath(args string) string {
	if args == "" {
		return ""
	}
	var p map[string]interface{}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return ""
	}
	return toolPathArg(p)
}

// pruneToolText cuts s to head + marker + tail. Returns (pruned, true) when
// the text is over threshold; ("", false) otherwise, so callers can tell "no
// work needed" from "pruned". spillPath, when non-empty, is named in the
// marker together with the recovery recipe (dsh lesson: a path without the
// "how to read it back" hint turns the recovery cost into guesswork).
func pruneToolText(s string, spillPath string) (string, bool) {
	r := []rune(s)
	if len(r) <= defaultPrune.Threshold {
		return "", false
	}
	omitted := len(r) - defaultPrune.Head - defaultPrune.Tail
	marker := fmt.Sprintf("\n\n[... middle pruned: %d chars omitted", omitted)
	if spillPath != "" {
		marker += "; full output: " + spillPath + " — read it back with read offset/limit windows, or search it with grep ..."
	} else {
		marker += " ..."
	}
	marker += "]\n\n"
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

// spillRetention is how long a spill file survives after it was written
// (dsh's cleanupPeriodDays equivalent). The files are pure copies — the
// ledger rows keep the full text — so deleting an old one only removes the
// model's convenient re-read path, never information the system still has.
const spillRetention = 30 * 24 * time.Hour

// cleanupSpillDir deletes spill files older than spillRetention. Runs once
// per process, off the store constructor, so a busy build/test day does not
// grow the folder without bound.
func cleanupSpillDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-spillRetention)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
