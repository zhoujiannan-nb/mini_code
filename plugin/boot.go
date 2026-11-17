package plugin

import (
	"fmt"
	"os"
	"path"
	"strings"
)

type BootSpec struct {
	Entry string `json:"entry,omitempty"`
	Preview string `json:"preview,omitempty"`
	DurationMs int `json:"durationMs,omitempty"`
	MaxMs      int `json:"maxMs,omitempty"`
	Interactive bool `json:"interactive,omitempty"`
	MinViewport int      `json:"minViewport,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

const (
	BootEntryDefault = "index.html"
	BootDurationMin = 300
	BootDurationMax = 60000
	BootMaxMsCeiling = 30000
)

var bootEntryExts = []string{".html", ".htm"}

var bootPreviewExts = []string{".svg", ".png", ".jpg", ".jpeg", ".webp", ".gif"}

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

func statFile(name string) (os.FileInfo, error) {
	return os.Stat(name)
}
