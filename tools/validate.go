package tools

// Generic tool-argument validation at the dispatch boundary.
//
// dsh validates every model tool call against the tool's own JSON schema
// BEFORE the tool runs: normalize optional nulls, coerce primitives to the
// declared types, then check required/type/enum/additionalProperties, and on
// failure the model receives
//     Error: Validation failed for tool "edit":
//       - new_string: must have required property 'new_string'
//
//     Received arguments:
//     { ... }
// (measured from dsh's validation.js: validateToolArguments + formatValidationPath).
// MiniCode advertised the same schemas but never enforced them, so a call
// with a misspelled key reached Execute where a missing value silently became
// the zero value — the mechanism behind a measured content-loss incident. This
// file is the missing dispatch-layer rule; it is schema-driven and tool-agnostic.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// validationIssue is one schema violation, already path-formatted the way
// dsh's formatValidationPath does: dots instead of slashes, the required
// property's name appended to its parent path, and "root" for an unpathed
// error at the top level.
type validationIssue struct {
	path    string
	message string
}

// NormalizeAndCoerceArgs applies the two pre-check transforms dsh runs before
// schema validation, in place on params:
//  1. null on a property that is NOT in the schema's required list is removed
//     (models emit null for "absent" on optional keys; the tool treats an
//     absent key the same way);
//  2. primitives are coerced to the declared schema type when losslessly
//     representable ("5000" → 5000, 5 → "5", "true" → true). A string that is
//     not a number stays a string and then fails the check, exactly like
//     typebox's Value.Convert.
func NormalizeAndCoerceArgs(schema map[string]interface{}, params map[string]interface{}) {
	normalizeOptionalNulls(schema, params)
	coerceByJSONSchema(schema, params)
}

func normalizeOptionalNulls(schema map[string]interface{}, value interface{}) {
	if schema == nil {
		return
	}
	if arr, ok := value.([]interface{}); ok {
		if items, ok := schema["items"].(map[string]interface{}); ok {
			for _, el := range arr {
				normalizeOptionalNulls(items, el)
			}
		}
		return
	}
	obj, ok := value.(map[string]interface{})
	if !ok {
		return
	}
	props, _ := schema["properties"].(map[string]interface{})
	if props == nil {
		return
	}
	required := requiredSet(schema)
	for k, v := range obj {
		if v == nil && !required[k] {
			delete(obj, k)
			continue
		}
		if sub, ok := props[k].(map[string]interface{}); ok {
			normalizeOptionalNulls(sub, v)
		}
	}
}

func coerceByJSONSchema(schema map[string]interface{}, value interface{}) interface{} {
	if schema == nil {
		return value
	}
	switch want := schema["type"].(type) {
	case string:
		switch want {
		case "object":
			if obj, ok := value.(map[string]interface{}); ok {
				if props, ok := schema["properties"].(map[string]interface{}); ok {
					for k, v := range obj {
						if sub, ok := props[k].(map[string]interface{}); ok {
							obj[k] = coerceByJSONSchema(sub, v)
						}
					}
				}
			}
		case "array":
			if arr, ok := value.([]interface{}); ok {
				if items, ok := schema["items"].(map[string]interface{}); ok {
					for i, el := range arr {
						arr[i] = coerceByJSONSchema(items, el)
					}
				}
			}
		default:
			return coercePrimitive(value, want)
		}
	case []interface{}:
		// Union: convert to the first declared type the value already matches,
		// else leave it for the check to judge.
		if types, ok := toStringList(want); ok {
			for _, t := range types {
				if matchesJSONType(value, t) {
					return value
				}
			}
			return coercePrimitive(value, types[0])
		}
	}
	return value
}

// coercePrimitive converts a value to the declared scalar type only when it
// is losslessly representable; anything else passes through unchanged so the
// validation step reports the mismatch.
func coercePrimitive(value interface{}, want string) interface{} {
	switch want {
	case "string":
		switch v := value.(type) {
		case float64:
			if v == float64(int64(v)) {
				return strconv.FormatInt(int64(v), 10)
			}
			return strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(v)
		}
	case "number":
		if s, ok := value.(string); ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				return f
			}
		}
	case "integer":
		switch v := value.(type) {
		case float64:
			if v == float64(int64(v)) {
				return v
			}
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f == float64(int64(f)) {
				return f
			}
		}
	case "boolean":
		if s, ok := value.(string); ok {
			switch s {
			case "true":
				return true
			case "false":
				return false
			}
		}
	}
	return value
}

// ValidateToolArgs checks params against the tool's advertised JSON-schema
// (the same map Parameters() returns) and, when anything is wrong, returns an
// error whose text is byte-comparable with dsh's validation failure:
// a per-issue bullet list plus the arguments as received. Callers run
// NormalizeAndCoerceArgs first so coercion never shows up as an error.
func ValidateToolArgs(name string, schema map[string]interface{}, params interface{}) error {
	issues := checkValue("", schema, params)
	if len(issues) == 0 {
		return nil
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "Validation failed for tool %q:\n", name)
	for _, is := range issues {
		p := is.path
		if p == "" {
			p = "root"
		}
		fmt.Fprintf(&b, "  - %s: %s\n", p, is.message)
	}
	b.WriteString("\nReceived arguments:\n")
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(params); err != nil {
		fmt.Fprint(&b, "undefined")
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

func checkValue(path string, schema map[string]interface{}, value interface{}) []validationIssue {
	if schema == nil {
		return nil
	}
	var issues []validationIssue

	if want, ok := schemaType(schema); ok && !matchesAnyType(value, want) {
		return append(issues, validationIssue{pathOrRoot(path), "must be " + strings.Join(want, ", ")})
	}
	if allowed, ok := enumValues(schema); ok && !inEnum(value, allowed) {
		// The declared type already matched (the check above returned
		// otherwise), so this is the ajv "enum" keyword failure.
		return append(issues, validationIssue{pathOrRoot(path), "must be equal to one of the allowed values"})
	}

	switch v := value.(type) {
	case map[string]interface{}:
		props, _ := schema["properties"].(map[string]interface{})
		for _, r := range requiredList(schema) {
			if _, present := v[r]; !present {
				issues = append(issues, validationIssue{joinPath(path, r), fmt.Sprintf("must have required property '%s'", r)})
			}
		}
		if additional, present := schema["additionalProperties"]; present {
			if b, isBool := additional.(bool); isBool && !b {
				for k := range v {
					if props == nil {
						if _, declared := props[k]; !declared {
							issues = append(issues, validationIssue{pathOrRoot(path), "must NOT have additional properties"})
							break
						}
					} else if _, declared := props[k]; !declared {
						issues = append(issues, validationIssue{pathOrRoot(path), "must NOT have additional properties"})
						break
					}
				}
			}
		}
		for k, sub := range props {
			val, present := v[k]
			if !present {
				continue
			}
			subSchema, ok := sub.(map[string]interface{})
			if !ok {
				continue
			}
			issues = append(issues, checkValue(joinPath(path, k), subSchema, val)...)
		}
	case []interface{}:
		if items, ok := schema["items"].(map[string]interface{}); ok {
			for i, el := range v {
				issues = append(issues, checkValue(joinPath(path, strconv.Itoa(i)), items, el)...)
			}
		}
	}
	return issues
}

func pathOrRoot(p string) string {
	if p == "" {
		return "root"
	}
	return p
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

func schemaType(schema map[string]interface{}) ([]string, bool) {
	switch t := schema["type"].(type) {
	case string:
		return []string{t}, true
	case []interface{}:
		return toStringList(t)
	}
	return nil, false
}

func toStringList(list []interface{}) ([]string, bool) {
	out := make([]string, 0, len(list))
	for _, el := range list {
		s, ok := el.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, len(out) > 0
}

func requiredList(schema map[string]interface{}) []string {
	switch r := schema["required"].(type) {
	case []string:
		return r
	case []interface{}:
		out, ok := toStringList(r)
		if ok {
			return out
		}
	}
	return nil
}

func requiredSet(schema map[string]interface{}) map[string]bool {
	set := map[string]bool{}
	for _, r := range requiredList(schema) {
		set[r] = true
	}
	return set
}

func enumValues(schema map[string]interface{}) ([]interface{}, bool) {
	e, ok := schema["enum"].([]interface{})
	return e, ok && len(e) > 0
}

func inEnum(value interface{}, allowed []interface{}) bool {
	for _, a := range allowed {
		if jsonEqual(a, value) {
			return true
		}
	}
	return false
}

func jsonEqual(a, b interface{}) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case nil:
		return b == nil
	}
	return false
}

func matchesAnyType(value interface{}, types []string) bool {
	for _, t := range types {
		if matchesJSONType(value, t) {
			return true
		}
	}
	return false
}

// matchesJSONType is the JSON-domain type test (decoded payloads use
// float64 for every number; Go-native test values may carry int kinds).
func matchesJSONType(value interface{}, t string) bool {
	switch t {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		return isJSONNumber(value)
	case "integer":
		f, ok := toFloat(value)
		return ok && f == float64(int64(f))
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]interface{})
		return ok
	case "array":
		_, ok := value.([]interface{})
		return ok
	case "null":
		return value == nil
	}
	return true // unknown schema type: not enforceable, stay permissive
}

func isJSONNumber(value interface{}) bool {
	_, ok := toFloat(value)
	return ok
}

func toFloat(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}
