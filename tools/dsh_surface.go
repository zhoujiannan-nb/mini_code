package tools

// dsh_surface.go — the dsh tool surface.
//
// The tool NAMES, DESCRIPTIONS and PARAMETER SCHEMAS the model sees are taken
// verbatim from dsh's measured request schema (tools/dsh_tools.json, captured
// from a live dsh session's request/header). Keeping the spec as an embedded
// data file instead of retyping it guarantees byte-for-byte parity: the same
// tool surface is what makes a harness-vs-harness comparison honest.
//
// The implementations underneath remain MiniCode's own. Where dsh's parameter
// names differ from the historical MiniCode ones (file_path vs path,
// old_string vs old_text, timeoutMs vs timeout, workdir vs working_dir), the
// tools accept BOTH: the dsh spelling is the documented surface, the old
// spelling keeps existing sessions and tests working.

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

// dshDesc returns the verbatim dsh description for a tool name ("" when the
// spec does not carry it — callers must not render an empty description into a
// schema, so the registration list is the check that every name exists).
func dshDesc(name string) string {
	if s, ok := dshSpecs()[name]; ok {
		return s.Description
	}
	return ""
}

// dshParams returns the verbatim dsh parameter schema. The map is shared and
// must be treated as read-only by callers.
func dshParams(name string) map[string]interface{} {
	if s, ok := dshSpecs()[name]; ok {
		return s.Parameters
	}
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

// strParam reads the first parameter key present as a non-empty string. This
// is how the dsh spelling (file_path, old_string, ...) and the historical
// MiniCode spelling (path, old_text, ...) both keep working.
func strParam(params map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := params[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
