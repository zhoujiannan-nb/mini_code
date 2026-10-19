package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ModelParams is what ProbeModelParams learned about a served model. Every
// field is optional: a zero value means "the deployment did not report it",
// and the caller must then leave the config field empty so the request omits
// it entirely (a modern framework's own default is then used). Found lists the
// fields that were actually reported, so the UI can say what it filled and
// what it could not.
type ModelParams struct {
	ModelName     string  `json:"model_name,omitempty"`
	ContextWindow int     `json:"context_window,omitempty"`
	MaxTokens     int     `json:"max_tokens,omitempty"`
	Temperature   float64 `json:"temperature,omitempty"`
	TopP          float64 `json:"top_p,omitempty"`
	TopK          int     `json:"top_k,omitempty"`

	Found  []string `json:"found,omitempty"`
	Notes  []string `json:"notes,omitempty"`
	Models []string `json:"models,omitempty"`
}

// found reports whether a field name is in the Found set.
func (p *ModelParams) found(key string) bool {
	for _, k := range p.Found {
		if k == key {
			return true
		}
	}
	return false
}

func (p *ModelParams) mark(key string) {
	if !p.found(key) {
		p.Found = append(p.Found, key)
		sort.Strings(p.Found)
	}
}

// Context window keys used across serving frameworks. vLLM/SGLang report
// max_model_len, Ollama reports context_length / num_ctx, GGUF-derived
// deployments report n_positions / model_len.
// ctxKeys are the names serving frameworks use for the context window. n_ctx is
// the llama.cpp spelling (published as meta.n_ctx in the model list, which for
// some proxies is the ONLY place the window is published).
var ctxKeys = []string{"max_model_len", "context_length", "context_len", "num_ctx",
	"max_context_length", "n_positions", "model_len", "ctx_len", "n_ctx"}

// maxTokensKeys is only consulted on the per-model endpoints: a server-wide
// info dump also carries token counts that mean something else (batch limits).
var maxTokensKeys = []string{"max_output_tokens", "max_completion_tokens", "max_tokens"}

// samplingKeys are the generation parameters the UI can pre-fill.
var samplingKeys = []string{"temperature", "top_p", "top_k"}

// probeClient is short-lived: the settings dialog waits on it.
var probeClient = &http.Client{Timeout: 12 * time.Second}

// ProbeModelParams asks a serving framework what it knows about one model:
// the context window, the output cap and the default sampling parameters.
// Nothing here is required to exist — every endpoint is tried best-effort and
// whatever the deployment does not report stays zero in the result, which the
// caller turns into "do not send that field".
//
// baseURL follows the same convention as chat/ListModels (the API root, e.g.
// "http://host:8000/v1" or "https://api.deepseek.com"); modelID may be empty,
// in which case the model list is fetched and the first id is probed.
// ollama selects the native /api/show endpoint in addition to the
// OpenAI-compatible ones, since that is where Ollama publishes its defaults.
func ProbeModelParams(baseURL, apiKey, modelID, providerType string) (*ModelParams, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	out := &ModelParams{}

	// 1. Model list: resolves modelID when it was left empty, and is shown
	//    to the user so the picker can offer alternatives. Many llama.cpp /
	//    proxy deployments publish everything they know HERE (meta.n_ctx and
	//    friends) and have no per-model endpoint at all, so the matching list
	//    entry is scanned as a model card too.
	ids, err := ListModels(base, apiKey)
	if err != nil {
		out.Notes = append(out.Notes, "models: "+err.Error())
	} else {
		out.Models = ids
		if modelID == "" && len(ids) > 0 {
			modelID = ids[0]
		}
	}
	out.ModelName = modelID
	if body, err := probeGet(base+"/models", apiKey); err == nil {
		if obj, ok := decodeObject(body); ok {
			if entry := listEntry(obj, modelID); entry != nil {
				applyParams(out, entry, true)
			}
			// The envelope itself may carry the metadata (some proxies put
			// n_ctx / sampling defaults next to "data").
			applyParams(out, obj, true)
		}
	}

	// 2. {base}/models/{id}: OpenAI-compatible model card. vLLM/SGLang put
	//    max_model_len here, and several hosted APIs publish the output cap
	//    and sampling defaults in the same object.
	if modelID != "" {
		if body, err := probeGet(base+"/models/"+url.PathEscape(modelID), apiKey); err != nil {
			out.Notes = append(out.Notes, "models/"+modelID+": "+err.Error())
		} else if obj, ok := decodeObject(body); ok {
			// Per-model endpoint: context, output cap and sampling all count.
			applyParams(out, obj, true)
		} else {
			out.Notes = append(out.Notes, "models/"+modelID+": 无法解析 JSON")
		}
	}

	// 3. Native Ollama: /api/show carries the context length and the
	//    Modelfile/GGUF sampling defaults. Its root has no /v1 suffix.
	if strings.EqualFold(providerType, "ollama") {
		root := strings.TrimSuffix(base, "/v1")
		body, err := probePost(root+"/api/show", apiKey, map[string]string{"model": modelID})
		if err != nil {
			out.Notes = append(out.Notes, "api/show: "+err.Error())
		} else if obj, ok := decodeObject(body); ok {
			applyParams(out, obj, true)
			// model_info is keyed by architecture ("qwen2.context_length").
			for k, v := range flatten(obj) {
				low := strings.ToLower(k)
				if i := strings.LastIndex(low, "."); i >= 0 {
					low = low[i+1:]
				}
				if num, ok := asFloat(v); ok && contains(ctxKeys, low) && num > 0 {
					setContextWindow(out, int(num))
				}
			}
		}
	}

	// 4. vLLM / SGLang server info: the reliable source for max_model_len on
	//    self-hosted deployments. Only the sampling subtree counts there, so
	//    server-wide limits are not mistaken for generation defaults.
	root := strings.TrimSuffix(base, "/v1")
	for _, ep := range []string{"/get_server_info", "/get_model_config"} {
		body, err := probeGet(root+ep, apiKey)
		if err != nil {
			out.Notes = append(out.Notes, strings.TrimPrefix(ep, "/")+": "+err.Error())
			continue
		}
		obj, ok := decodeObject(body)
		if !ok {
			out.Notes = append(out.Notes, strings.TrimPrefix(ep, "/")+": 无法解析 JSON")
			continue
		}
		applySubtree(out, obj, []string{"max_model_len", "context_length", "model_len"})
		for _, name := range []string{"default_sampling_params", "default_sampling_parameters",
			"generation_config", "sampling", "sampling_defaults", "model_config"} {
			if sub, ok := obj[name].(map[string]interface{}); ok {
				applyParams(out, sub, false)
				applySubtree(out, sub, []string{"max_model_len", "context_length"})
			}
		}
	}

	sort.Strings(out.Found)
	return out, nil
}

// applyParams copies recognized keys from a decoded JSON object into out.
// withOutputCap also allows the output-cap keys (per-model endpoints only).
// Values already found are kept: the endpoints are tried from the most
// specific to the most general.
func applyParams(out *ModelParams, obj map[string]interface{}, withOutputCap bool) {
	applySubtree(out, obj, ctxKeys)
	if withOutputCap {
		applySubtree(out, obj, maxTokensKeys)
	}
	for _, k := range samplingKeys {
		for path, v := range flatten(obj) {
			if !strings.EqualFold(lastSegment(path), k) {
				continue
			}
			num, ok := asFloat(v)
			if !ok {
				continue
			}
			switch k {
			case "temperature":
				if !out.found("temperature") && num >= 0 && num <= 2 {
					out.Temperature = num
					out.mark("temperature")
				}
			case "top_p":
				if !out.found("top_p") && num > 0 && num <= 1 {
					out.TopP = num
					out.mark("top_p")
				}
			case "top_k":
				if !out.found("top_k") && num >= 0 && num <= 1000000 {
					out.TopK = int(num)
					out.mark("top_k")
				}
			}
		}
	}
}

// applySubtree copies the first value found under any of the given keys,
// searching the whole nested object.
func applySubtree(out *ModelParams, obj map[string]interface{}, keys []string) {
	flat := flatten(obj)
	for _, want := range keys {
		for path, v := range flat {
			if !strings.EqualFold(lastSegment(path), want) {
				continue
			}
			num, ok := asFloat(v)
			if !ok || num <= 0 {
				continue
			}
			switch {
			case contains(ctxKeys, want):
				setContextWindow(out, int(num))
			case contains(maxTokensKeys, want):
				if !out.found("max_tokens") && int(num) <= 33_554_432 {
					out.MaxTokens = int(num)
					out.mark("max_tokens")
				}
			}
		}
	}
}

func setContextWindow(out *ModelParams, v int) {
	// A served context window is a power-of-two-ish token count; anything
	// wildly out of range came from an unrelated field with a similar name.
	if v <= 0 || v > 1_000_000_000 || out.found("context_window") {
		return
	}
	out.ContextWindow = v
	out.mark("context_window")
}

// flatten walks a decoded JSON object into "a.b.c" → leaf value.
func flatten(obj interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	var walk func(prefix string, v interface{})
	walk = func(prefix string, v interface{}) {
		switch t := v.(type) {
		case map[string]interface{}:
			for k, sub := range t {
				walk(prefix+"."+k, sub)
			}
		default:
			if prefix != "" {
				out[prefix] = v
			}
		}
	}
	walk("", obj)
	return out
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return strings.TrimPrefix(path, ".")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func asFloat(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case string:
		// Ollama puts the Modelfile parameters in a text blob ("temperature 0.8")
		// but some builds expose scalars as strings.
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func decodeObject(body []byte) (map[string]interface{}, bool) {
	var obj map[string]interface{}
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, false
	}
	// OpenAI-compatible endpoints wrap the model card in "data" when the
	// caller asked for the list; unwrap a single-element list so both shapes
	// work with the same key scan.
	if list, ok := obj["data"].([]interface{}); ok && len(list) == 1 {
		if one, ok := list[0].(map[string]interface{}); ok {
			for k, v := range one {
				if _, exists := obj[k]; !exists {
					obj[k] = v
				}
			}
		}
	}
	return obj, true
}

// listEntry picks the entry of an OpenAI-style model list that describes
// modelID — the only entry when exactly one model is served. nil when nothing
// matches, so a stale model name never inherits another model's numbers.
func listEntry(obj map[string]interface{}, modelID string) map[string]interface{} {
	data, ok := obj["data"].([]interface{})
	if !ok || len(data) == 0 {
		return nil
	}
	if len(data) == 1 {
		one, _ := data[0].(map[string]interface{})
		return one
	}
	for _, item := range data {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := m["id"].(string); modelID != "" && id == modelID {
			return m
		}
	}
	return nil
}

func probeGet(url, apiKey string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return probeDo(req)
}

func probePost(url, apiKey string, payload interface{}) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return probeDo(req)
}

func probeDo(req *http.Request) ([]byte, error) {
	resp, err := probeClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		msg := string(body)
		if len(msg) > 160 {
			msg = msg[:160] + "…"
		}
		return nil, fmt.Errorf("%d %s", resp.StatusCode, msg)
	}
	return body, nil
}
