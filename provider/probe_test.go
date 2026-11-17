package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeModelParamsVLLMStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Qwen3.8-Flash","object":"model"}]}`))
		case "/v1/models/Qwen3.8-Flash":
			_, _ = w.Write([]byte(`{"id":"Qwen3.8-Flash","object":"model","max_model_len":1048576}`))
		case "/get_server_info":
			_, _ = w.Write([]byte(`{"max_num_batched_tokens":8192,"temperature":0.1,
			  "model_config":{"max_model_len":1048576},
			  "default_sampling_params":{"temperature":0.6,"top_p":0.95,"top_k":20}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p, err := ProbeModelParams(srv.URL+"/v1", "k", "", "vllm")
	if err != nil {
		t.Fatalf("ProbeModelParams: %v", err)
	}
	if p.ModelName != "Qwen3.8-Flash" {
		t.Errorf("model name not resolved from the list: %q", p.ModelName)
	}
	if p.ContextWindow != 1048576 {
		t.Errorf("context window = %d, want 1048576", p.ContextWindow)
	}
	if p.Temperature != 0.6 || p.TopP != 0.95 || p.TopK != 20 {
		t.Errorf("sampling = %v/%v/%v, want 0.6/0.95/20", p.Temperature, p.TopP, p.TopK)
	}
	for _, k := range []string{"context_window", "temperature", "top_p", "top_k"} {
		if !p.found(k) {
			t.Errorf("found missing %q (got %v)", k, p.Found)
		}
	}
	if p.MaxTokens != 0 {
		t.Errorf("max_tokens = %d, want 0 (nothing reported an output cap)", p.MaxTokens)
	}
}

func TestProbeModelParamsNothingAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-x"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p, err := ProbeModelParams(srv.URL, "", "gpt-x", "vllm")
	if err != nil {
		t.Fatalf("ProbeModelParams: %v", err)
	}
	if p.ContextWindow != 0 || p.MaxTokens != 0 || p.Temperature != 0 || p.TopP != 0 || p.TopK != 0 {
		t.Errorf("nothing should be reported, got %+v", p)
	}
	if len(p.Notes) == 0 {
		t.Error("expected notes explaining the missing endpoints")
	}
	if len(p.Models) != 1 || p.Models[0] != "gpt-x" {
		t.Errorf("models = %v, want [gpt-x]", p.Models)
	}
}

func TestProbeModelParamsOllama(t *testing.T) {
	var showReqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3:8b"}]}`))
		case "/api/show":
			showReqs++
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "qwen3:8b" {
				t.Errorf("api/show model = %q", body["model"])
			}
			_, _ = w.Write([]byte(`{"model_info":{"qwen2.context_length":32768},
			  "parameters":{"temperature":0.8,"top_p":0.9,"top_k":40}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p, err := ProbeModelParams(srv.URL+"/v1", "", "qwen3:8b", "ollama")
	if err != nil {
		t.Fatalf("ProbeModelParams: %v", err)
	}
	if showReqs == 0 {
		t.Error("ollama probe never called /api/show")
	}
	if p.ContextWindow != 32768 {
		t.Errorf("context window = %d, want 32768", p.ContextWindow)
	}
	if p.Temperature != 0.8 || p.TopP != 0.9 || p.TopK != 40 {
		t.Errorf("sampling = %v/%v/%v, want 0.8/0.9/40", p.Temperature, p.TopP, p.TopK)
	}
}

func TestProbeModelParamsListOnlyDeployment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"object":"list","data":[` +
				`{"id":"other-model","object":"model.object","status":{"value":"stopped","n_ctx":4096}},` +
				`{"id":"qwen3.8-flash","object":"model.object","status":{"value":"loaded","n_ctx":262144},"meta":{"n_ctx":262144,"vocab_size":151936}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error": {"message": "not found"}}`))
		}
	}))
	defer srv.Close()

	got, err := ProbeModelParams(srv.URL, "k", "qwen3.8-flash", "vllm")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if got.ContextWindow != 262_144 {
		t.Errorf("context window = %d, want 262144 (meta.n_ctx of the matching list entry)", got.ContextWindow)
	}
	if !got.found("context_window") {
		t.Error("context_window must be marked as found")
	}
	if got.ModelName != "qwen3.8-flash" {
		t.Errorf("model name = %q", got.ModelName)
	}
	for _, k := range []string{"max_tokens", "temperature", "top_p", "top_k"} {
		if got.found(k) {
			t.Errorf("%s was reported as found although the endpoint knows nothing about it", k)
		}
	}
	if len(got.Models) != 2 {
		t.Errorf("model list = %v, want both ids", got.Models)
	}
}
