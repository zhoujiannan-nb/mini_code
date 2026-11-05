package provider

import "fmt"

// VLLMProvider is the plain OpenAI-compatible deployment (vLLM itself, and most
// proxies that forward to one).
//
// Thinking depth works here the way it does on SGLang: a Qwen3-style model served
// by vLLM flips thinking through the chat template's enable_thinking variable, and
// the only way to reach that variable per request is chat_template_kwargs. Only the
// kwarg form is sent — a vLLM server has no top-level "thinking" field, and an
// unknown top-level field is a needless 400 risk on a strict server. Templates that
// do not reference enable_thinking ignore the kwarg, so it is safe on any
// deployment; and it is left out entirely unless a switch was asked for, which
// keeps ordinary traffic byte-identical to before.
type VLLMProvider struct {
	BaseURL         string
	APIKey          string
	ModelName       string
	MaxTokens       int
	ContextWindow   int
	Temperature     float64
	TopP            float64
	Thinking        string // "" (template default) | "enabled" | "disabled"
	ReasoningEffort string // "" | "off" | "low" | "medium" | "high"
}

func NewVLLMProvider(baseURL, apiKey, modelName string, maxTokens, contextWindow int, temperature, topP float64, thinking, reasoningEffort string) *VLLMProvider {
	return &VLLMProvider{
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

func (p *VLLMProvider) ProviderName() string { return "vllm" }

func (p *VLLMProvider) BuildURL() string {
	return fmt.Sprintf("%s/chat/completions", p.BaseURL)
}

func (p *VLLMProvider) BuildHeaders() map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + p.APIKey,
		"Content-Type":  "application/json",
	}
}

func (p *VLLMProvider) GetContextWindow() int { return p.ContextWindow }
func (p *VLLMProvider) GetMaxTokens() int     { return p.MaxTokens }
func (p *VLLMProvider) GetMaxInput() int      { return maxInputBudget(p.ContextWindow, p.MaxTokens) }

// ConfigureRequest adds the OpenAI-compatible fields vLLM honors: top_p (when set),
// the thinking switch via chat_template_kwargs (when asked for), reasoning_effort
// (when set), and stream_options.include_usage so the final streaming chunk carries
// the usage block (including prompt_tokens_details.cached_tokens).
func (p *VLLMProvider) ConfigureRequest(req *chatRequest) {
	if p.TopP > 0 {
		req.TopP = p.TopP
	}
	if p.Thinking != "" {
		req.ChatTemplateKwargs = map[string]interface{}{
			"enable_thinking": p.Thinking == "enabled",
		}
	}
	if p.ReasoningEffort != "" {
		req.ReasoningEffort = p.ReasoningEffort
	}
	req.StreamOptions = &streamOptions{IncludeUsage: true}
}
