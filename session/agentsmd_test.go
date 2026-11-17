package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)


func stubGlobalInstructions(t *testing.T, files ...string) {
	t.Helper()
	old := userGlobalInstructionFiles
	userGlobalInstructionFiles = func() []string { return files }
	t.Cleanup(func() { userGlobalInstructionFiles = old })
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentsMdChainOrderAndStop(t *testing.T) {
	stubGlobalInstructions(t)
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "above", "AGENTS.md"), "outside the repo")
	repo := filepath.Join(tmp, "above", "repo")
	mustWrite(t, filepath.Join(repo, "AGENTS.md"), "root rules")
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(repo, "pkg")
	mustWrite(t, filepath.Join(pkg, "AGENTS.md"), "pkg rules")
	mustWrite(t, filepath.Join(pkg, "CLAUDE.md"), "claude fallback")

	got := agentsInstructionsSection(filepath.Join(pkg, "deep"))
	if got == "" {
		t.Fatal("no section built")
	}
	if strings.Contains(got, "outside the repo") {
		t.Error("the chain must stop at the repository root")
	}
	if strings.Contains(got, "claude fallback") {
		t.Error("AGENTS.md must win over CLAUDE.md in the same directory")
	}
	r, p := strings.Index(got, "root rules"), strings.Index(got, "pkg rules")
	if r < 0 || p < 0 || r > p {
		t.Fatalf("root-to-cwd order broken: root=%d pkg=%d\n%s", r, p, got)
	}
}

func TestAgentsMdGlobalsFirst(t *testing.T) {
	tmp := t.TempDir()
	global := filepath.Join(tmp, "global-AGENTS.md")
	mustWrite(t, global, "global rules")
	stubGlobalInstructions(t, global)
	wd := filepath.Join(tmp, "proj")
	mustWrite(t, filepath.Join(wd, "AGENTS.md"), "project rules")
	got := agentsInstructionsSection(wd)
	g, p := strings.Index(got, "global rules"), strings.Index(got, "project rules")
	if g < 0 || p < 0 || g > p {
		t.Fatalf("globals must come first: global=%d project=%d\n%s", g, p, got)
	}
}

func TestAgentsMdTruncateAtRuneBoundary(t *testing.T) {
	stubGlobalInstructions(t)
	tmp := t.TempDir()
	body := strings.Repeat("规", instructionFileCap)
	mustWrite(t, filepath.Join(tmp, "AGENTS.md"), body)
	got := readInstructionFile(filepath.Join(tmp, "AGENTS.md"))
	if !strings.Contains(got, "truncated at 65536 bytes") {
		t.Fatal("oversized file not truncated")
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a rune")
	}
	if strings.Contains(got, "\n\n"+body) {
		t.Fatal("the whole file came through")
	}
}

func TestAgentsMdNothingEmpty(t *testing.T) {
	stubGlobalInstructions(t)
	if got := agentsInstructionsSection(t.TempDir()); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
