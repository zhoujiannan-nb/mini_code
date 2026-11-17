package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)


const spoolDirName = "mini_code_spool"

func spoolWrite(prefix, text string) string {
	if text == "" {
		return ""
	}
	dir := filepath.Join(os.TempDir(), spoolDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	sweepSpoolDir(dir)
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.txt", prefix, time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return ""
	}
	return path
}

const spoolRetention = 30 * 24 * time.Hour

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
