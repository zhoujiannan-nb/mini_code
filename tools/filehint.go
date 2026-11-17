package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)


const (
	similarHintCap    = 5
	similarHintMin    = 0.4
	similarHintScanCap = 1000
)

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
			continue
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
	return fmt.Sprintf("Hint: similar names in %s: %s (check spelling/extension; use glob to see all files).", dir, strings.Join(sims, ", "))
}

func dirNotFileHint(fp string) string {
	entries, err := os.ReadDir(fp)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("It is a directory with %d entries — use glob with the pattern \"%s/*\" to see its contents.", len(entries), filepath.ToSlash(fp))
}
