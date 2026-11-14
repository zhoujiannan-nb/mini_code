package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// spool.go — the "where the complete list was saved" side of the dsh
// search/glob promise: when a result is capped, the FULL list goes to a file
// and the model-visible text names the path, so nothing gathered is lost,
// just not inline (same discipline as the session-side prune spill, but the
// tools package cannot see the session store, and temp-space with a mtime
// sweep is honest for copies the ledger still holds in full).

// spoolDirName nests the files under the temp dir instead of littering it.
const spoolDirName = "mini_code_spool"

// spoolWrite puts text in the spool dir and returns the path ("" on any
// failure — the capped result simply omits the path, exactly like a failed
// prune spill).
func spoolWrite(prefix, text string) string {
	if text == "" {
		return ""
	}
	dir := filepath.Join(os.TempDir(), spoolDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	sweepSpoolDir(dir) // best-effort age cleanup, cheap (the dir stays small)
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.txt", prefix, time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return ""
	}
	return path
}

// spoolRetention is how long a spool file survives. These are convenience
// copies of data the model can always re-derive by re-running the search,
// so old ones are pure garbage.
const spoolRetention = 30 * 24 * time.Hour

// sweepSpoolDir deletes spool files older than the retention. Called on
// write (the dir holds at most a handful of files, so walking it is free)
// rather than on a timer.
func sweepSpoolDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-spoolRetention)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
