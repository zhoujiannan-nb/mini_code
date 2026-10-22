package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Startup animation ("boot anim") configuration.
//
// The animation itself is not compiled in: themes are HTML bundles that live in
// a directory (default ~/.mini_code/boot-anim/<id>/) and are served to a
// sandboxed iframe that covers the UI while the app starts. This struct only
// records which theme to play, when to play it and how fast.
//
// Because a nested struct has no way to tell "unset" from "explicitly false", an
// all-zero value (no theme, no mode, no speed, no dir) means "never configured"
// and is filled with the defaults by applyDefaults. Once the settings page
// writes it back it always carries a theme + mode + speed, so a deliberate
// Enabled:false survives a reload.
type BootAnimConfig struct {
	// Enabled is the master switch. Even when on, nothing plays until an
	// actually-installed theme is selected (the host checks the directory).
	Enabled bool `json:"enabled"`

	// Theme is the theme id to play. It doubles as the directory name under
	// Dir, so it is restricted to the same character set the contract allows.
	Theme string `json:"theme"`

	// Mode is when to play: BootAnimModeSession (once per app session),
	// BootAnimModeAlways (every launch/window) or BootAnimModeDaily (once per
	// calendar day). It is a *policy* enforced by the host player, not a hint.
	Mode string `json:"mode"`

	// Speed multiplies the theme's own timeline (0.35-3). 0 means unset and is
	// normalized to 1. Themes that ignore the parameter simply play at their
	// own pace; the host hard-stops at maxMs regardless.
	Speed float64 `json:"speed"`

	// Name is the identity string the theme shows (empty = the theme's own
	// default / the app name). Kept short so themes can lay it out.
	Name string `json:"name,omitempty"`

	// Interactive lets a theme keep listening to the pointer after its intro
	// (the contract's "interactive" flag). Off by default: an intro that stays
	// clickable over the whole UI is more obstacle than feature.
	Interactive bool `json:"interactive"`

	// Mute silences a theme's voice/sound effects. False (the default) lets the
	// theme play audio: the player gives the iframe allow="autoplay" so an intro
	// can speak without a prior click, and a theme must still finish the
	// animation when playback is blocked.
	Mute bool `json:"mute"`

	// Dir is the theme directory. Empty = ~/.mini_code/boot-anim (see DirPath).
	// Users can point it anywhere (e.g. a git checkout of the plugin repo).
	Dir string `json:"dir,omitempty"`

	// Repo is the GitHub "owner/name" the online catalogue is read from.
	// Empty = DefaultBootAnimRepo.
	Repo string `json:"repo,omitempty"`
}

// Playback modes for BootAnimConfig.Mode.
const (
	BootAnimModeSession = "session" // once per browser session (new tab plays again)
	BootAnimModeAlways  = "always"  // every time the page loads
	BootAnimModeDaily   = "daily"   // once per calendar day
)

const (
	// DefaultBootAnimRepo is the plugin catalogue (index.json) source.
	DefaultBootAnimRepo = "zhoujiannan-nb/mini_code-plugins"
	// DefaultBootAnimTheme is played when the user has not picked one. It is
	// just a name: it only takes effect once that theme is installed.
	DefaultBootAnimTheme = "mc-tech-archive"
	// DefaultBootAnimSpeed / limits for the speed multiplier.
	DefaultBootAnimSpeed = 1.0
	MinBootAnimSpeed     = 0.35
	MaxBootAnimSpeed     = 3.0
)

// DefaultBootAnimConfig is what an unconfigured (or older) config gets.
// Enabled is on: the animation stays invisible until a theme is installed, so
// there is nothing to surprise anyone, and the first install then works without
// a second trip through the settings page.
func DefaultBootAnimConfig() BootAnimConfig {
	return BootAnimConfig{
		Enabled: true,
		Theme:   DefaultBootAnimTheme,
		Mode:    BootAnimModeSession,
		Speed:   DefaultBootAnimSpeed,
	}
}

// Normalize clamps every field to a usable value. Called on load (so a
// hand-edited config never breaks the player) and before a web-driven save.
func (b *BootAnimConfig) Normalize() {
	switch b.Mode {
	case BootAnimModeSession, BootAnimModeAlways, BootAnimModeDaily:
	default:
		b.Mode = BootAnimModeSession
	}
	if b.Speed <= 0 {
		b.Speed = DefaultBootAnimSpeed
	}
	b.Speed = clampBootAnimSpeed(b.Speed)
	b.Name = strings.TrimSpace(b.Name)
	if len(b.Name) > 48 {
		b.Name = b.Name[:48]
	}
	b.Theme = strings.TrimSpace(b.Theme)
	b.Dir = strings.TrimSpace(b.Dir)
	b.Repo = strings.TrimSpace(strings.Trim(strings.TrimSpace(b.Repo), "/"))
	if b.Repo == "" {
		b.Repo = DefaultBootAnimRepo
	}
}

// IsZero reports whether b has never been configured, i.e. it is the zero value
// an older config.json — or a UI that knows nothing about boot animation —
// unmarshals to.
func (b BootAnimConfig) IsZero() bool {
	return !b.Enabled && b.Theme == "" && b.Mode == "" && b.Speed == 0 && b.Dir == "" && b.Repo == "" && b.Name == "" && !b.Interactive && !b.Mute
}

// applyBootAnimDefaults fills an unconfigured boot-anim block with defaults and
// then normalizes whatever was configured.
func applyBootAnimDefaults(cfg *AppConfig) {
	if cfg.BootAnim.IsZero() {
		cfg.BootAnim = DefaultBootAnimConfig()
	}
	cfg.BootAnim.Normalize()
}

// EffectiveRepo is the GitHub "owner/name" to read the catalogue from.
func (b BootAnimConfig) EffectiveRepo() string {
	if r := strings.TrimSpace(b.Repo); r != "" {
		return r
	}
	return DefaultBootAnimRepo
}

// DirPath is the directory themes are loaded from: Dir when set (with a leading
// ~ expanded), otherwise ~/.mini_code/boot-anim. It does not create the
// directory: a missing directory just means "no theme installed".
func (b BootAnimConfig) DirPath() (string, error) {
	if d := strings.TrimSpace(b.Dir); d != "" {
		return expandHomePath(d)
	}
	base, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "boot-anim"), nil
}

// expandHomePath resolves a leading "~" / "~\" to the user's home directory so
// a config file written on one machine can name a home-relative path.
func expandHomePath(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return filepath.Clean(p), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot expand ~ in %q: %w", p, err)
	}
	rest := strings.TrimLeft(strings.TrimPrefix(p, "~"), "/\\")
	if rest == "" {
		return filepath.Clean(home), nil
	}
	return filepath.Clean(filepath.Join(home, rest)), nil
}

// bootAnimIDPattern is the theme id (and therefore directory name) allowed by
// the theme contract: lowercase letters, digits and dashes, starting with a
// letter or digit. It keeps an id safe to use in a URL path and as a path
// segment — no "..", no separators, no leading dash.
var bootAnimIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidBootAnimID reports whether s can be used as a theme id / theme dir name.
func ValidBootAnimID(s string) bool { return bootAnimIDPattern.MatchString(s) }

// bootAnimRepoPattern is a GitHub "owner/name" (no slashes beyond the one, each
// component starting with an alphanumeric so it can never be "." or "..") so it
// can be pasted into a raw.githubusercontent URL path.
var bootAnimRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,38}/[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

// ValidBootAnimRepo reports whether s is an "owner/name" pair.
func ValidBootAnimRepo(s string) bool { return bootAnimRepoPattern.MatchString(s) }

// Validate checks a boot-anim block before it is written to disk. The values are
// normalized first (Normalize), so anything still out of range here is a
// hand-edited mistake worth reporting instead of silently fixing.
func (b BootAnimConfig) Validate() error {
	if b.IsZero() {
		// Unset (an older config, or a caller that only touches the model list):
		// nothing to check — the defaults get filled on load.
		return nil
	}
	switch b.Mode {
	case BootAnimModeSession, BootAnimModeAlways, BootAnimModeDaily:
	default:
		return fmt.Errorf("boot_anim.mode must be %s, %s or %s", BootAnimModeSession, BootAnimModeAlways, BootAnimModeDaily)
	}
	if b.Speed < MinBootAnimSpeed || b.Speed > MaxBootAnimSpeed {
		return fmt.Errorf("boot_anim.speed must be between %g and %g", MinBootAnimSpeed, MaxBootAnimSpeed)
	}
	if b.Theme != "" && !ValidBootAnimID(b.Theme) {
		return fmt.Errorf("boot_anim.theme must be a theme id (lowercase letters, digits and dashes)")
	}
	if b.Repo != "" && !ValidBootAnimRepo(b.Repo) {
		return fmt.Errorf(`boot_anim.repo must look like "owner/name"`)
	}
	return nil
}

// SameBootAnim reports whether two boot-anim blocks configure the same thing.
// Used by the save path to keep its "nothing changed" short circuit.
func SameBootAnim(a, b BootAnimConfig) bool {
	return a.Enabled == b.Enabled && a.Theme == b.Theme && a.Mode == b.Mode &&
		a.Speed == b.Speed && a.Name == b.Name && a.Interactive == b.Interactive &&
		a.Mute == b.Mute && a.Dir == b.Dir && a.Repo == b.Repo
}

func clampBootAnimSpeed(v float64) float64 {
	if v < MinBootAnimSpeed {
		return MinBootAnimSpeed
	}
	if v > MaxBootAnimSpeed {
		return MaxBootAnimSpeed
	}
	return v
}
