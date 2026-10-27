package plugin

import (
	"fmt"
	"os"
	"path"
	"strings"
)

// BootSpec declares that an appearance plugin also ships a STARTUP ANIMATION.
//
// The startup clip used to be its own subsystem: a separate directory
// (~/.mini_code/boot-anim) with its own manifest (theme.json), its own
// installer, its own settings tab. That was one concept too many — a theme is
// a static HTML bundle with assets, which is exactly what an appearance plugin
// already is. Folding it in means one thing to install, one toggle, one place to
// look:
//
//	plugin.json
//	  {
//	    "id": "boot-mc-access",
//	    "kind": "appearance",
//	    "boot": {"entry": "index.html", "durationMs": 9000, "interactive": false}
//	  }
//
// Everything a theme used to say in theme.json can still be said there: when a
// boot plugin has a theme.json next to it, the host reads the numbers it does
// not find in the boot block from that file. So the plugin repo's existing
// themes keep working after gaining a plugin.json — nothing has to be retyped.
//
// Security does NOT change: the entry is served from the plugin's own directory
// under /bootanim/<id>/... with the theme CSP (connect-src 'none', no nested
// frames) and played in a sandboxed iframe with allow-scripts but no
// allow-same-origin. A boot plugin is therefore the one plugin kind allowed to
// ship .js — and only these files are served with a script-capable policy.
type BootSpec struct {
	// Entry is the HTML document that draws the animation, relative to the
	// plugin directory. Empty means index.html.
	Entry string `json:"entry,omitempty"`
	// Preview is an optional still (svg/png/webp) for the plugin card.
	Preview string `json:"preview,omitempty"`
	// DurationMs is how long the theme's own timeline runs; MaxMs is the hard
	// stop the player enforces (it must not be able to hold the UI hostage).
	DurationMs int `json:"durationMs,omitempty"`
	MaxMs      int `json:"maxMs,omitempty"`
	// Interactive lets the finished clip keep listening to the pointer.
	Interactive bool `json:"interactive,omitempty"`
	// MinViewport is the smallest viewport the clip is worth playing in.
	MinViewport int      `json:"minViewport,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// Boot defaults and limits, mirrored by the player.
const (
	BootEntryDefault = "index.html"
	// BootDurationMin / BootDurationMax bound the declared timeline; anything
	// outside is a mistake (or an attempt to make the overlay impossible to
	// dismiss) and is clamped.
	BootDurationMin = 300
	BootDurationMax = 60000
	// BootMaxMsCeiling is the absolute hard stop the player enforces no matter
	// what a manifest asks for.
	BootMaxMsCeiling = 30000
)

// bootEntryExts are the document types a startup clip may start with.
var bootEntryExts = []string{".html", ".htm"}

// bootPreviewExts are the image types allowed as a card preview.
var bootPreviewExts = []string{".svg", ".png", ".jpg", ".jpeg", ".webp", ".gif"}

// HasBoot reports whether the plugin ships a startup animation.
func (p Plugin) HasBoot() bool { return p.Boot != nil }

// BootEntryFile is the entry document, defaulted and cleaned, exactly as the
// asset route will look it up.
func (p Plugin) BootEntryFile() string {
	if p.Boot == nil {
		return ""
	}
	entry := strings.TrimSpace(p.Boot.Entry)
	if entry == "" {
		entry = BootEntryDefault
	}
	return path.Clean(strings.ReplaceAll(entry, `\`, "/"))
}

// validateBoot enforces the boot contract: a real entry document inside the
// plugin directory, an existing preview when declared, and sane timings.
func (p Plugin) validateBoot() error {
	if p.Boot == nil {
		return nil
	}
	entry := p.BootEntryFile()
	if entry == "" || !hasAnyExt(entry, bootEntryExts...) {
		return fmt.Errorf("boot.entry must be an .html file inside the plugin, got %q", p.Boot.Entry)
	}
	full, err := p.FilePath(entry)
	if err != nil {
		return fmt.Errorf("boot.entry %q: %w", p.Boot.Entry, err)
	}
	if _, err := statFile(full); err != nil {
		return fmt.Errorf("boot.entry %q not found in the plugin directory", entry)
	}
	p.Boot.Entry = entry
	if prev := strings.TrimSpace(p.Boot.Preview); prev != "" {
		if !hasAnyExt(prev, bootPreviewExts...) {
			return fmt.Errorf("boot.preview must be an image (svg/png/webp), got %q", prev)
		}
		full, err := p.FilePath(prev)
		if err != nil {
			return fmt.Errorf("boot.preview %q: %w", prev, err)
		}
		if _, err := statFile(full); err != nil {
			return fmt.Errorf("boot.preview %q not found in the plugin directory", prev)
		}
		p.Boot.Preview = normalizeAssetPath(prev)
	}
	if d := p.Boot.DurationMs; d != 0 {
		if d < BootDurationMin {
			p.Boot.DurationMs = BootDurationMin
		} else if d > BootDurationMax {
			p.Boot.DurationMs = BootDurationMax
		}
	}
	if mx := p.Boot.MaxMs; mx != 0 {
		if mx > BootMaxMsCeiling {
			p.Boot.MaxMs = BootMaxMsCeiling
		}
		if p.Boot.MaxMs < p.Boot.DurationMs {
			p.Boot.MaxMs = p.Boot.DurationMs
		}
	}
	if p.Boot.MinViewport < 0 {
		p.Boot.MinViewport = 0
	}
	if p.Boot.MinViewport > 0 && p.Boot.MinViewport < 240 {
		p.Boot.MinViewport = 240
	}
	return nil
}

// statFile is os.Stat, kept behind a helper so the rest of this file reads the
// same way as the other validators.
func statFile(name string) (os.FileInfo, error) {
	return os.Stat(name)
}
