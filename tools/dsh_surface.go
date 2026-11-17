package tools


import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed dsh_tools.json
var dshToolsJSON []byte

type dshToolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

var (
	dshSpecOnce sync.Once
	dshSpecMap  map[string]dshToolSpec
)

func dshSpecs() map[string]dshToolSpec {
	dshSpecOnce.Do(func() {
		dshSpecMap = map[string]dshToolSpec{}
		var list []dshToolSpec
		if err := json.Unmarshal(dshToolsJSON, &list); err == nil {
			for _, s := range list {
				dshSpecMap[s.Name] = s
			}
		}
	})
	return dshSpecMap
}

func dshDesc(name string) string {
	if s, ok := dshSpecs()[name]; ok {
		return s.Description
	}
	return ""
}

func dshParams(name string) map[string]interface{} {
	if s, ok := dshSpecs()[name]; ok {
		return s.Parameters
	}
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

func strParam(params map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := params[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
