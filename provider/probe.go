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

var ctxKeys = []string{"max_model_len", "context_length", "context_len", "num_ctx",
	"max_context_length", "n_positions", "model_len", "ctx_len", "n_ctx"}

var maxTokensKeys = []string{"max_output_tokens", "max_completion_tokens", "max_tokens"}

var samplingKeys = []string{"temperature", "top_p", "top_k"}

var probeClient = &http.Client{Timeout: 12 * time.Second}

func ProbeModelParams(baseURL, apiKey, modelID, providerType string) (*ModelParams, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	out := &ModelParams{}

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
			applyParams(out, obj, true)
		}
	}

	if modelID != "" {
		if body, err := probeGet(base+"/models/"+url.PathEscape(modelID), apiKey); err != nil {
			out.Notes = append(out.Notes, "models/"+modelID+": "+err.Error())
		} else if obj, ok := decodeObject(body); ok {
			applyParams(out, obj, true)
		} else {
			out.Notes = append(out.Notes, "models/"+modelID+": 无法解析 JSON")
		}
	}

	if strings.EqualFold(providerType, "ollama") {
		root := strings.TrimSuffix(base, "/v1")
		body, err := probePost(root+"/api/show", apiKey, map[string]string{"model": modelID})
		if err != nil {
			out.Notes = append(out.Notes, "api/show: "+err.Error())
		} else if obj, ok := decodeObject(body); ok {
			applyParams(out, obj, true)
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
	if v <= 0 || v > 1_000_000_000 || out.found("context_window") {
		return
	}
	out.ContextWindow = v
	out.mark("context_window")
}

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
