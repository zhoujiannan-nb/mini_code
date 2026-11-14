package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// agentsmd.go — project instruction files (dsh packages/context/
// agent-instructions): AGENTS.md along the ancestor chain from the
// repository root down to the working directory, plus the user-global
// files. MiniCode loads them into the STATIC system prompt (the deviation
// from dsh's hot re-splice is deliberate and recorded in the ledger): the
// section is byte-stable while the files are untouched, so the provider's
// prefix cache survives, and edits take effect from the next task on —
// same refresh behaviour as pluginsSection.

// instructionFileCap is dsh's per-file load limit (65536 bytes). A bigger
// file is truncated at a rune boundary; a runaway AGENTS.md must not evict
// the conversation.
const instructionFileCap = 65536

// instructionFileNames are the per-directory instruction files, in
// precedence order: AGENTS.md wins, CLAUDE.md is the compatibility fallback.
var instructionFileNames = []string{"AGENTS.md", "CLAUDE.md"}

// userGlobalInstructionFiles lists the per-user instruction files, dsh's
// ~/.agents/AGENTS.md included. A variable so tests can point the loader at
// a fake home instead of the machine running the tests.
var userGlobalInstructionFiles = func() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".agents", "AGENTS.md"),
		filepath.Join(home, ".mini_code", "AGENTS.md"),
	}
}

// agentsInstructionsSection renders the whole instruction region, or "" when
// no file exists. Order is least specific first — user globals, then the
// ancestor chain from the repository root down to the working directory —
// so the instructions closest to the code are read last and win.
func agentsInstructionsSection(workDir string) string {
	var bodies []string
	for _, p := range userGlobalInstructionFiles() {
		if b := readInstructionFile(p); b != "" {
			bodies = append(bodies, b)
		}
	}
	for _, p := range projectInstructionChain(workDir) {
		if b := readInstructionFile(p); b != "" {
			bodies = append(bodies, b)
		}
	}
	if len(bodies) == 0 {
		return ""
	}
	return "\n# Project Instructions\n\nInstruction files for this workspace, least specific first; where they conflict, the file closest to the working directory wins.\n\n" +
		strings.Join(bodies, "\n\n") + "\n"
}

// projectInstructionChain walks up from workDir collecting each directory's
// instruction file, and stops after the first directory holding a .git
// entry (dsh's repository-root rule: a submodule walk-up stops at its own
// repo, and a bare directory stops at the filesystem root). The result is
// reversed into root-to-cwd order.
func projectInstructionChain(workDir string) []string {
	if workDir == "" {
		return nil
	}
	var chain []string
	dir := workDir
	for {
		for _, name := range instructionFileNames {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				chain = append(chain, p)
				break // one instruction file per directory, AGENTS.md wins
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break // this directory is a repository root: the chain starts here
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// readInstructionFile returns the labelled body of one file: capped at
// instructionFileCap, cut on a rune boundary, empty for missing or blank
// files (a file of nothing but whitespace is not an instruction).
func readInstructionFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > instructionFileCap {
		cut := instructionFileCap
		for cut > 0 && !utf8.RuneStart(b[cut]) {
			cut--
		}
		b = append(b[:cut:cut], []byte("\n…[truncated at 65536 bytes]")...)
	}
	body := strings.TrimSpace(string(b))
	if body == "" {
		return ""
	}
	return fmt.Sprintf("## %s\n\n%s", filepath.ToSlash(path), body)
}
