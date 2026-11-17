package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)


const instructionFileCap = 65536

var instructionFileNames = []string{"AGENTS.md", "CLAUDE.md"}

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
				break
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
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
