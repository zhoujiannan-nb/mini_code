package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// examplesRoot is the shipped sample plugins.
func examplesRoot() string {
	return filepath.Join("..", "examples", "plugins")
}

// TestShippedExamplesLoad is the guard that keeps the examples honest: every
// directory under examples/plugins must pass the same validation a downloaded
// plugin does. An example that cannot load is worse than no example, because it
// teaches the wrong contract.
func TestShippedExamplesLoad(t *testing.T) {
	root := examplesRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("no examples directory: %v", err)
	}
	var seen int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		p, err := LoadDir(dir, e.Name())
		if err != nil {
			t.Errorf("example %q does not load: %v", e.Name(), err)
			continue
		}
		seen++
		// The manifest id has to match the directory: that is what the asset
		// route and the installer both rely on.
		if p.ID != e.Name() {
			t.Errorf("example %q declares id %q (must match the directory)", e.Name(), p.ID)
		}
		// And it must classify into exactly one of the three categories.
		switch p.Category() {
		case CategoryProcess, CategoryTool, CategoryAppearance:
		default:
			t.Errorf("example %q has category %q", e.Name(), p.Category())
		}
	}
	if seen < 3 {
		t.Fatalf("expected at least 3 examples (one per category), found %d", seen)
	}
}

// TestExamplesCoverAllThreeCategories: the whole point of the sample set is one
// working plugin per kind, so a missing category is a real gap.
func TestExamplesCoverAllThreeCategories(t *testing.T) {
	entries, err := os.ReadDir(examplesRoot())
	if err != nil {
		t.Skipf("no examples directory: %v", err)
	}
	got := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := LoadDir(filepath.Join(examplesRoot(), e.Name()), e.Name())
		if err != nil {
			continue
		}
		if _, ok := got[p.Category()]; !ok {
			got[p.Category()] = p.ID
		}
	}
	for _, want := range []string{CategoryProcess, CategoryTool, CategoryAppearance} {
		if got[want] == "" {
			t.Errorf("no example covers the %q category", want)
		}
	}
}

// TestExampleProcessDemoIsSelfContained: the process example must not depend on
// anything outside its own directory (no absolute path to another repo, which is
// how the old web-search example was wired and why it could never ship).
func TestExampleProcessDemoIsSelfContained(t *testing.T) {
	dir := filepath.Join(examplesRoot(), "process-demo")
	p, err := LoadDir(dir, "process-demo")
	if err != nil {
		t.Skipf("no process example: %v", err)
	}
	if p.Process == nil {
		t.Fatal("the process example declares no process")
	}
	for _, a := range p.Process.Args {
		if filepath.IsAbs(a) {
			t.Errorf("argument %q is an absolute path; the example must be self-contained", a)
		}
	}
	if p.Process.HealthCheck == nil {
		t.Error("the process example should declare a health check (that is the interesting part)")
	}
	if p.Process.Expose == nil || !p.Process.Expose.Tool {
		t.Error("the process example should expose itself so the model can query it")
	}
}

// TestExampleSlotHTMLReferencesDeclaredFile: the shell fragment a skin ships has
// to be the very file the manifest declares, because the asset route only serves
// declared fragments.
func TestExampleSlotHTMLReferencesDeclaredFile(t *testing.T) {
	dir := filepath.Join(examplesRoot(), "skin-aurora")
	p, err := LoadDir(dir, "skin-aurora")
	if err != nil {
		t.Skipf("no appearance example: %v", err)
	}
	if p.UI == nil || len(p.UI.Slots) != 1 {
		t.Fatal("the appearance example should declare exactly one slot")
	}
	sl := p.UI.Slots[0]
	if !p.DeclaresSlotHTML(sl.HTML) {
		t.Errorf("DeclaresSlotHTML(%q) = false", sl.HTML)
	}
	// An undeclared file in the same directory is not servable.
	if p.DeclaresSlotHTML("bar2.html") {
		t.Error("an undeclared html file must not be servable")
	}
	full, err := p.FilePath(sl.HTML)
	if err != nil {
		t.Fatalf("slot html path: %v", err)
	}
	if _, err := os.Stat(full); err != nil {
		t.Errorf("slot html %q is missing on disk", sl.HTML)
	}
}
