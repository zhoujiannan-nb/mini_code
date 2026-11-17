package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type BootAnimConfig struct {
	Enabled bool `json:"enabled"`

	Theme string `json:"theme"`

	Mode string `json:"mode"`

	Name string `json:"name,omitempty"`

	Interactive bool `json:"interactive"`

	Mute bool `json:"mute"`

	Dir string `json:"dir,omitempty"`

	Repo string `json:"repo,omitempty"`
}

const (
	BootAnimModeSession = "session"
	BootAnimModeAlways  = "always"
	BootAnimModeDaily   = "daily"
)

const (
	DefaultBootAnimRepo = "zhoujiannan-nb/mini_code-plugins"
	DefaultBootAnimTheme = "mc-access"
)

func DefaultBootAnimConfig() BootAnimConfig {
	return BootAnimConfig{
		Enabled: true,
		Theme:   DefaultBootAnimTheme,
		Mode:    BootAnimModeSession,
	}
}

func (b *BootAnimConfig) Normalize() {
	switch b.Mode {
	case BootAnimModeSession, BootAnimModeAlways, BootAnimModeDaily:
	default:
		b.Mode = BootAnimModeSession
	}
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

func (b BootAnimConfig) IsZero() bool {
	return !b.Enabled && b.Theme == "" && b.Mode == "" && b.Dir == "" && b.Repo == "" && b.Name == "" && !b.Interactive && !b.Mute
}

func applyBootAnimDefaults(cfg *AppConfig) {
	if cfg.BootAnim.IsZero() {
		cfg.BootAnim = DefaultBootAnimConfig()
	}
	cfg.BootAnim.Normalize()
}

func (b BootAnimConfig) EffectiveRepo() string {
	if r := strings.TrimSpace(b.Repo); r != "" {
		return r
	}
	return DefaultBootAnimRepo
}

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

var bootAnimIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func ValidBootAnimID(s string) bool { return bootAnimIDPattern.MatchString(s) }

var bootAnimRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,38}/[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

func ValidBootAnimRepo(s string) bool { return bootAnimRepoPattern.MatchString(s) }

func (b BootAnimConfig) Validate() error {
	if b.IsZero() {
		return nil
	}
	switch b.Mode {
	case BootAnimModeSession, BootAnimModeAlways, BootAnimModeDaily:
	default:
		return fmt.Errorf("boot_anim.mode must be %s, %s or %s", BootAnimModeSession, BootAnimModeAlways, BootAnimModeDaily)
	}
	if b.Theme != "" && !ValidBootAnimID(b.Theme) {
		return fmt.Errorf("boot_anim.theme must be a theme id (lowercase letters, digits and dashes)")
	}
	if b.Repo != "" && !ValidBootAnimRepo(b.Repo) {
		return fmt.Errorf(`boot_anim.repo must look like "owner/name"`)
	}
	return nil
}

func SameBootAnim(a, b BootAnimConfig) bool {
	return a.Enabled == b.Enabled && a.Theme == b.Theme && a.Mode == b.Mode &&
		a.Name == b.Name && a.Interactive == b.Interactive &&
		a.Mute == b.Mute && a.Dir == b.Dir && a.Repo == b.Repo
}
