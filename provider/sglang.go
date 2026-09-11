package provider

import "fmt"

// SGLangProvider targets an SGLang server's OpenAI-compatible endpoint.
//
// Endpoint convention: the request URL is {BaseURL}/chat/completions. A direct
// SGLang deployment serves the OpenAI API under /v1, so base_url should be
// "http://host:port/v1". A reverse proxy that rewrites the path (e.g.
// /proxy/model/x -> the backend's /v1) keeps base_url bare, e.g.
// "http://proxy/proxy/model/x" — the client must NOT add /v1 in that case, or
// the proxy produces /v1/v1 and returns 404. Either way the client only ever
// appends /chat/completions; the /v1 placement is the deployment's concern.
//
// Cache hits: SGLang reports usage.prompt_tokens_details.cached_tokens, but
// only when the server is started with --enable-cache-report. Without that
// flag the field is null, so CachedTokens() returns 0 (treated as unknown).
//
// Thinking depth: reasoning_effort is passed through to the model's chat
// template (Qwen3-style deployments accept {xhigh, medium, low} — note: NOT
// "high"). The thinking switch is sent two ways: as a top-level thinking
// object AND as chat_template_kwargs.enable_thinking. SGLang ignores the
// top-level object, while Qwen3-style templates (e.g. Qwen3.8) only honor
// the template kwarg — so the kwargs form is what actually flips thinking
// off. Templates that do not use enable_thinking ignore the extra kwarg.
// Unlike DeepSeek, SGLang accepts temperature alongside thinking, so no
// sampling-penalty adjustment is done.
type SGLangProvider struct {
	BaseURL         string
	APIKey          string
	ModelName       string
	MaxTokens       int
	ContextWindow   int
	Temperature     float64
	TopP            float64
	Thinking        string // "" (provider default) | "enabled" | "disabled"
	ReasoningEffort string // "" | provider-specific (e.g. Qwen3: xhigh/medium/low)
}

func NewSGLangProvider(baseURL, apiKey, modelName string, maxTokens, contextWindow int, temperature, topP float64, thinking, reasoningEffort string) *SGLangProvider {
	return &SGLangProvider{
		BaseURL:         baseURL,
		APIKey:          apiKey,
		ModelName:       modelName,
		MaxTokens:       maxTokens,
		ContextWindow:   contextWindow,
		Temperature:     temperature,
		TopP:            topP,
		Thinking:        thinking,
		ReasoningEffort: reasoningEffort,
	}
}

func (p *SGLangProvider) ProviderName() string { return "sglang" }

func (p *SGLangProvider) BuildURL() string {
	return fmt.Sprintf("%s/chat/completions", p.BaseURL)
}

func (p *SGLangProvider) BuildHeaders() map[string]string {
	h := map[string]string{"Content-Type": "application/json"}
	if p.APIKey != "" {
		h["Authorization"] = "Bearer " + p.APIKey
	}
	return h
}

func (p *SGLangProvider) GetContextWindow() int { return p.ContextWindow }
func (p *SGLangProvider) GetMaxTokens() int     { return p.MaxTokens }
func (p *SGLangProvider) GetMaxInput() int      { return p.ContextWindow - p.MaxTokens - 2048 }

// ConfigureRequest sets SGLang's request fields: top_p (when set),
// stream_options.include_usage for cache-hit accounting, and the thinking
// depth (thinking switch + reasoning_effort) when the user configured it.
// SGLang accepts temperature alongside thinking, so no penalty adjustment.
func (p *SGLangProvider) ConfigureRequest(req *chatRequest) {
	req.StreamOptions = &streamOptions{IncludeUsage: true}
	if p.Thinking != "" {
		req.Thinking = &thinkingConfig{Type: p.Thinking}
		// Qwen3-style chat templates (e.g. Qwen3.8) control the thinking
		// switch through the template's enable_thinking variable, which
		// SGLang only reaches via chat_template_kwargs — the top-level
		// thinking object above is ignored by SGLang. Verified live:
		// enable_thinking=false drops reasoning to 0 tokens, while the
		// top-level object alone leaves thinking on.
		req.ChatTemplateKwargs = map[string]interface{}{
			"enable_thinking": p.Thinking == "enabled",
		}
	}
	if p.ReasoningEffort != "" {
		req.ReasoningEffort = p.ReasoningEffort
	}

	// Sampling parameters: the official Qwen3.8 recommendation per
	// thinking state. Qwen3-style models think by default, so an unset
	// switch (Thinking == "") means thinking ON. SGLang is used for Qwen
	// here, so the config temperature/top_p are deliberately not applied:
	// the official values are the tuned defaults and they differ between
	// the two thinking states (a static config value would be wrong for
	// one of them). An explicit per-call temperature (temperatureExplicit,
	// e.g. the 0.3 compaction summary) wins over the default.
	//
	//	thinking ON:  temperature=1.0, top_p=0.95
	//	thinking OFF: temperature=0.7, top_p=0.80, presence_penalty=1.5
	if p.Thinking == "disabled" {
		if !req.temperatureExplicit {
			temp := 0.7
			req.Temperature = &temp
		}
		req.TopP = 0.80
		presencePenalty := 1.5
		req.PresencePenalty = &presencePenalty
	} else {
		if !req.temperatureExplicit {
			temp := 1.0
			req.Temperature = &temp
		}
		req.TopP = 0.95
	}
}
