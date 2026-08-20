package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file adds "did you mean" recovery hints for the file tools. When the
// model mistypes a path (wrong case, wrong extension, a name that lives one
// directory deeper, a renamed file), the previous error was a bare
// "file not found: X" — the model then burns a full extra turn on list_dir
// just to discover the actual name. Pointing at the closest real names in
// the same directory lets it recover in one step.
//
// All hints are advisory text appended to the tool result on the error path
// only; the success path is byte-for-byte unchanged.

const (
	similarHintCap    = 5    // at most N candidate names per hint
	similarHintMin    = 0.4  // below this Dice score the candidate is noise
	similarHintScanCap = 1000 // only scan the first N directory entries
)

// similarFileNames returns the entry names in dir most similar to name
// (case-insensitive), best first, capped at similarHintCap. It reuses the
// rune-bigram Dice coefficient from closestLineHint: names that share most
// of their bigrams (a typo, a case difference, a version suffix) score high,
// unrelated names score near zero.
func similarFileNames(dir, name string) []string {
	name = strings.ToLower(name)
	if name == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	if len(entries) > similarHintScanCap {
		entries = entries[:similarHintScanCap]
	}
	nameRunes := []rune(name)
	nameBigrams := bigramCount(nameRunes)
	if len(nameBigrams) == 0 {
		return nil
	}
	type cand struct {
		name  string
		score float64
	}
	var cands []cand
	for _, e := range entries {
		en := e.Name()
		enLower := strings.ToLower(en)
		if enLower == name {
			continue // an exact (case-insensitive) match cannot be "similar"
		}
		score := bigramDice(nameBigrams, bigramCount([]rune(enLower)))
		if score >= similarHintMin {
			cands = append(cands, cand{name: en, score: score})
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) > similarHintCap {
		cands = cands[:similarHintCap]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.name
	}
	return out
}

// fileNotFoundHint returns a short "similar names in <dir>" hint for a path
// that does not exist, or "" when the parent directory is missing/empty or
// holds nothing similar. It is meant to be appended to the "file not found"
// error of read_file / edit_file.
func fileNotFoundHint(fp string) string {
	dir := filepath.Dir(fp)
	base := filepath.Base(fp)
	if base == "." || base == "" {
		return ""
	}
	sims := similarFileNames(dir, base)
	if len(sims) == 0 {
		return ""
	}
	return fmt.Sprintf("Hint: similar names in %s: %s (check spelling/extension; use list_dir or glob to see all files).", dir, strings.Join(sims, ", "))
}

// dirNotFileHint returns a short hint for a path that exists but is a
// directory, telling the model how to look inside it, or "" when the entry
// count cannot be determined.
func dirNotFileHint(fp string) string {
	entries, err := os.ReadDir(fp)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("It is a directory with %d entries — use list_dir (or glob) to see its contents.", len(entries))
}
