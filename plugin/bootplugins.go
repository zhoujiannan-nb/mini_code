package plugin

import "sort"

// BootPlugins returns the enabled plugins that ship a startup animation, sorted
// by id.
//
// A startup clip is no longer its own subsystem: it is an appearance plugin that
// also declares a "boot" block (see boot.go). The rule for "which one plays" is
// deliberately boring — the first enabled one by id, unless the config names a
// specific id — because a boot screen is a single, global thing: there is no
// sensible way to show two.
func BootPlugins() []Plugin {
	var out []Plugin
	for _, p := range Load() {
		if p.Boot != nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BootPluginByID finds one enabled plugin that ships a startup animation.
func BootPluginByID(id string) (Plugin, bool) {
	for _, p := range Load() {
		if p.Boot != nil && p.ID == id {
			return p, true
		}
	}
	return Plugin{}, false
}
