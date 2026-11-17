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

type pruneConfig struct {
	Threshold int
	Head      int
	Tail      int
}

const pruneMarkerRoom = 1024

var defaultPrune = pruneConfig{Threshold: 24576, Head: (24576 - pruneMarkerRoom + 1) / 2, Tail: (24576 - pruneMarkerRoom) / 2}

func init() {
	if v := os.Getenv("MINICODE_PRUNE_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 4096 {
			defaultPrune = pruneConfig{Threshold: n, Head: (n - pruneMarkerRoom + 1) / 2, Tail: (n - pruneMarkerRoom) / 2}
		}
	}
}

var pruneExemptTools = map[string]bool{"glob": true, "list_dir": true}

var readTools = map[string]bool{"read": true, "read_file": true}

func pruneExemptCall(name, pathArg, spillDir string) bool {
	if pruneExemptTools[name] {
		return true
	}
	if readTools[name] {
		return isUnderSpillDir(pathArg, spillDir)
	}
	return false
}

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
	if len([]rune(out)) >= len(r) {
		return "", false
	}
	return out, true
}

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

func sanitizeFileName(s string) string {
	return strings.Map(func(c rune) rune {
		switch c {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', ' ':
			return '_'
		}
		return c
	}, s)
}

const spillRetention = 30 * 24 * time.Hour

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
