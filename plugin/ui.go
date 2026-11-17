package plugin

import (
	"fmt"
	"path"
	"strings"
)


type UISpec struct {
	ID string `json:"id,omitempty"`
	Slots []SlotSpec `json:"slots,omitempty"`
	Motion []MotionSpec `json:"motion,omitempty"`
	Accent string `json:"accent,omitempty"`
}

type SlotSpec struct {
	Slot string `json:"slot"`
	HTML string `json:"html"`
	Height int `json:"height,omitempty"`
}

type MotionSpec struct {
	Event string `json:"event"`
	Class string `json:"class"`
}

var validSlots = map[string]bool{
	"app-top":        true,
	"sidebar-top":    true,
	"sidebar-bottom": true,
	"composer-bar":   true,
	"header-right":   true,
	"overlay":        true,
}

var validMotionEvents = map[string]bool{
	"send":    true,
	"reply":   true,
	"error":   true,
	"tool":    true,
	"session": true,
}

func (p Plugin) validateUI() error {
	ui := p.UI
	if ui == nil && len(p.Panels) == 0 {
		return nil
	}
	if ui == nil {
		ui = &UISpec{}
		for _, pn := range p.Panels {
			ui.Slots = append(ui.Slots, SlotSpec{Slot: pn.Slot, HTML: pn.HTML})
		}
		p.UI = ui
	}
	for i, s := range ui.Slots {
		slot := strings.TrimSpace(s.Slot)
		if slot == "" {
			return fmt.Errorf("ui.slots[%d] has no slot name", i)
		}
		if !validSlots[slot] {
			return fmt.Errorf("ui.slots[%d] unknown slot %q (known: %s)", i, slot, strings.Join(slotNames(), ", "))
		}
		if strings.TrimSpace(s.HTML) == "" {
			return fmt.Errorf("ui.slots[%d] (%s) has no html file", i, slot)
		}
		if !hasAnyExt(s.HTML, ".html", ".svg", ".htm") {
			return fmt.Errorf("ui.slots[%d] (%s) must be .html or .svg, got %q", i, slot, s.HTML)
		}
		if _, err := p.FilePath(s.HTML); err != nil {
			return err
		}
		if s.Height < 0 || s.Height > 600 {
			return fmt.Errorf("ui.slots[%d] (%s) height must be 0–600, got %d", i, slot, s.Height)
		}
		if s.Height > 0 && s.Height < 16 {
			return fmt.Errorf("ui.slots[%d] (%s) height must be at least 16, got %d", i, slot, s.Height)
		}
	}
	for i, mo := range ui.Motion {
		if !validMotionEvents[strings.ToLower(strings.TrimSpace(mo.Event))] {
			return fmt.Errorf("ui.motion[%d] unknown event %q (known: send, reply, error, tool, session)", i, mo.Event)
		}
		if strings.TrimSpace(mo.Class) == "" {
			return fmt.Errorf("ui.motion[%d] has no class", i)
		}
	}
	if ui.Accent != "" && !isHexColor(ui.Accent) {
		return fmt.Errorf("ui.accent %q is not a #rgb / #rrggbb colour", ui.Accent)
	}
	return nil
}

func (p Plugin) DeclaresSlotHTML(rel string) bool {
	want := normalizeAssetPath(rel)
	if want == "" || p.UI == nil {
		return false
	}
	for _, s := range p.UI.Slots {
		if normalizeAssetPath(s.HTML) == want {
			return true
		}
	}
	return false
}

func normalizeAssetPath(rel string) string {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, `\`, "/"))
	rel = strings.TrimPrefix(rel, "/")
	if i := strings.IndexByte(rel, '?'); i >= 0 {
		rel = rel[:i]
	}
	return path.Clean(rel)
}

type PanelSpec struct {
	Slot string `json:"slot"`
	HTML string `json:"html"`
}

func hasAnyExt(name string, exts ...string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if i := strings.IndexByte(lower, '?'); i >= 0 {
		lower = lower[:i]
	}
	for _, e := range exts {
		if strings.HasSuffix(lower, e) {
			return true
		}
	}
	return false
}

func isHexColor(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "#") {
		return false
	}
	body := s[1:]
	if len(body) != 3 && len(body) != 6 && len(body) != 8 {
		return false
	}
	for _, c := range body {
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !ok {
			return false
		}
	}
	return true
}

func SlotNames() []string { return slotNames() }

func slotNames() []string {
	out := make([]string, 0, len(validSlots))
	for s := range validSlots {
		out = append(out, s)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
