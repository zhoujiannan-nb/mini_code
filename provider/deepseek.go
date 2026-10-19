package provider

import "fmt"

// DeepSeekProvider targets the DeepSeek official API (OpenAI-compatible).
//
// Endpoint convention: the request URL is {BaseURL}/chat/completions, where
// base_url is "https://api.deepseek.com" (NO /v1 — DeepSeek's root already
// serves /chat/completions directly).
//
// Cache hits: DeepSeek reports usage.prompt_cache_hit_tokens (a custom
// top-level field, not prompt_tokens_details). Note DeepSeek's prompt_tokens
// already INCLUDES the cached portion, so the uncached input is
// prompt_tokens - prompt_cache_hit_tokens.
//
// Thinking mode: DeepSeek defaults to thinking ON (effort high). While thinking
// is enabled the API rejects temperature / presence_penalty / frequency_penalty
// and requires top_p >= 0.95, so ConfigureRequest omits temperature and floors
// top_p in that case. reasoning_content must be passed back on assistant turns
// (the Message serialization already carries it).
type DeepSeekProvider struct {
	BaseURL         string
	APIKey          string
	ModelName       string
	MaxTokens       int
	ContextWindow   int
	Temperature     float64
	TopP            float64
	Thinking        string // "" (provider default) | "enabled" | "disabled"
	ReasoningEffort string // "" | "off" | "low" | "high" | "max"
}

func NewDeepSeekProvider(baseURL, apiKey, modelName string, maxTokens, contextWindow int, temperature, topP float64, thinking, reasoningEffort string) *DeepSeekProvider {
	return &DeepSeekProvider{
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

func (p *DeepSeekProvider) ProviderName() string { return "deepseek" }

func (p *DeepSeekProvider) BuildURL() string {
	return fmt.Sprintf("%s/chat/completions", p.BaseURL)
}

func (p *DeepSeekProvider) BuildHeaders() map[string]string {
	h := map[string]string{"Content-Type": "application/json"}
	if p.APIKey != "" {
		h["Authorization"] = "Bearer " + p.APIKey
	}
	return h
}

func (p *DeepSeekProvider) GetContextWindow() int { return p.ContextWindow }
func (p *DeepSeekProvider) GetMaxTokens() int     { return p.MaxTokens }
func (p *DeepSeekProvider) GetMaxInput() int      { return maxInputBudget(p.ContextWindow, p.MaxTokens) }

// ConfigureRequest sets DeepSeek's request fields: top_p, stream_options, and
// the thinking mode. DeepSeek defaults to thinking ON; the resolved mode is
// always sent explicitly so behavior is deterministic.
func (p *DeepSeekProvider) ConfigureRequest(req *chatRequest) {
	if p.TopP > 0 {
		req.TopP = p.TopP
	}

	// Resolve the effective thinking mode. DeepSeek's default is thinking ON
	// (effort high), so thinkingOn starts true and is only turned off by an
	// explicit "off" effort or a "disabled" thinking switch.
	thinkingOn := true
	var effort string
	switch p.ReasoningEffort {
	case "off":
		thinkingOn = false
	case "low", "high", "max":
		thinkingOn = true
		effort = p.ReasoningEffort
	case "":
		if p.Thinking == "disabled" {
			thinkingOn = false
		}
		// "enabled" or unset -> thinking on (DeepSeek default).
	}

	if thinkingOn {
		req.Thinking = &thinkingConfig{Type: "enabled"}
		if effort != "" {
			req.ReasoningEffort = effort
		}
		// DeepSeek rejects sampling penalties while thinking is enabled.
		req.Temperature = nil
		// DeepSeek requires top_p >= 0.95 in thinking mode.
		if req.TopP > 0 && req.TopP < 0.95 {
			req.TopP = 0.95
		}
	} else {
		req.Thinking = &thinkingConfig{Type: "disabled"}
	}

	req.StreamOptions = &streamOptions{IncludeUsage: true}
}
