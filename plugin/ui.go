package plugin

import (
	"fmt"
	"path"
	"strings"
)

// The third kind: DIY front-end. A plugin may change what the app looks like
// without touching the app's own code, through independent surfaces that a theme
// cannot reach on its own:
//
//	styles      plain CSS, injected after the built-in sheets (already existed)
//	wallpaper   a background image for the message column (already existed)
//	ui.slots    small HTML fragments injected into named holes in the page
//	ui.motion   declarative CSS animations the app triggers on its own events
//	ui.accent   one accent colour, without shipping a stylesheet
//
// Every surface is data, never code: no plugin JavaScript is ever served (the
// asset route refuses .js on purpose, see web/plugins.go). That is what makes it
// safe to download a skin from a stranger's repo — and it is also why the format
// is small.
//
//	"ui": {
//	  "slots": [{"slot": "composer-bar", "html": "bar.html"}],
//	  "motion": [{"event": "send", "class": "mc-shake"}],
//	  "accent": "#6be3b0"
//	}
//
// A desktop pet is deliberately NOT part of this contract. It cannot be done
// well from inside the web page (a page cannot draw outside its own window), and
// faking it inside the window is a toy. It needs its own companion process, which
// is exactly what the kind-1 (process) plugins provide — so a pet will be built
// on top of that seam later rather than bolted into the renderer.

// UISpec is the third-kind declaration block ("ui" in plugin.json).
type UISpec struct {
	// ID is an optional stable name for the appearance bundle ("night-skin").
	// It defaults to the plugin id.
	ID string `json:"id,omitempty"`
	// Slots injects small HTML fragments into named holes in the page.
	Slots []SlotSpec `json:"slots,omitempty"`
	// Motion declares CSS animations triggered by app events.
	Motion []MotionSpec `json:"motion,omitempty"`
	// Accent recolours the app without shipping a stylesheet.
	Accent string `json:"accent,omitempty"`
}

// SlotSpec injects one HTML fragment into a named slot. Slots are the only
// extension point the page itself knows about; a host that does not know a slot
// name rejects the plugin at load time (rather than silently dropping the
// fragment, which is the worst answer for someone building a skin).
//
// The fragment is rendered inside a sandboxed <iframe> with no script and no
// same-origin access. That is deliberate: arbitrary HTML from a downloaded skin
// must not be able to call the app's own API with the page's credentials, so the
// isolation is structural instead of depending on a sanitiser being perfect.
type SlotSpec struct {
	Slot string `json:"slot"`
	HTML string `json:"html"`
	// Height is the iframe height in CSS pixels (default 56, 16–600). A plain
	// iframe cannot report its own height across origins, so the plugin says it.
	Height int `json:"height,omitempty"`
}

// MotionSpec binds a host event to a CSS class the host animates.
type MotionSpec struct {
	// Event is one of: send, reply, error, tool, session.
	Event string `json:"event"`
	// Class is a CSS animation-name the skin defines.
	Class string `json:"class"`
}

// validSlots is the closed set of holes the page provides. A plugin naming an
// unknown slot is rejected loudly at load time rather than silently ignored:
// the usual cause is a typo, and silently dropping the fragment is the worst
// possible answer for someone building a skin.
var validSlots = map[string]bool{
	"app-top":        true, // above the session list, full width
	"sidebar-top":    true, // top of the session sidebar
	"sidebar-bottom": true, // bottom of the session sidebar
	"composer-bar":   true, // a strip under the message composer
	"header-right":   true, // right side of the title bar
	"overlay":        true, // floating layer over the whole app (badges, credits)
}

// validMotionEvents is the closed set of app events a skin may hook.
var validMotionEvents = map[string]bool{
	"send":    true, // the user submitted a message
	"reply":   true, // the assistant finished a reply
	"error":   true, // a task or a request failed
	"tool":    true, // a tool step started
	"session": true, // the active session changed
}

// validateUI enforces the third-kind contract.
func (p Plugin) validateUI() error {
	ui := p.UI
	if ui == nil && len(p.Panels) == 0 {
		return nil
	}
	if ui == nil {
		// Legacy "panels": lift them into the modern shape.
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
		// A slot fragment may only be HTML/SVG: no .js, no reference outside
		// the plugin directory.
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

// DeclaresSlotHTML reports whether rel is exactly one of the slot fragments the
// manifest declares. normalizeAssetPath turns both sides into the form the asset
// route sees (forward slashes, no query).
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

// normalizeAssetPath turns a manifest-relative path into the comparable form the
// asset route sees (forward slashes, no query).
func normalizeAssetPath(rel string) string {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, `\`, "/"))
	rel = strings.TrimPrefix(rel, "/")
	if i := strings.IndexByte(rel, '?'); i >= 0 {
		rel = rel[:i]
	}
	return path.Clean(rel)
}

// PanelSpec is the legacy flat slot declaration (kept for compatibility).
type PanelSpec struct {
	Slot string `json:"slot"`
	HTML string `json:"html"`
}

// hasAnyExt reports whether name ends with one of the given extensions.
func hasAnyExt(name string, exts ...string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	// Strip an optional ?media= or ?v= query the same way styles allow.
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

// isHexColor accepts #rgb, #rrggbb and #rrggbbaa.
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

// SlotNames returns the sorted list of slots the page provides. Exported so the
// plugin centre can document them and a plugin author can check a name without
// reading the source.
func SlotNames() []string { return slotNames() }

// slotNames returns the sorted list of known slots (for error messages).
func slotNames() []string {
	out := make([]string, 0, len(validSlots))
	for s := range validSlots {
		out = append(out, s)
	}
	// Small fixed list; sort for a stable message.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
