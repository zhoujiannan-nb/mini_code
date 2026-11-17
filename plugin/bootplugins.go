package plugin


func BootPluginByID(id string) (Plugin, bool) {
	for _, p := range Load() {
		if p.Boot != nil && p.ID == id {
			return p, true
		}
	}
	return Plugin{}, false
}
