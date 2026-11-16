package plugin

// (BootPlugins, the "all enabled boot plugins sorted by id" listing, used to
// live here: nothing ever listed them, because a boot screen is single-select
// and the one caller below resolves it by id.)

// BootPluginByID finds one enabled plugin that ships a startup animation.
func BootPluginByID(id string) (Plugin, bool) {
	for _, p := range Load() {
		if p.Boot != nil && p.ID == id {
			return p, true
		}
	}
	return Plugin{}, false
}
