package provider

import "fmt"

type VLLMProvider struct {
	BaseURL       string
	APIKey        string
	ModelName     string
	MaxTokens     int
	ContextWindow int
	Temperature   float64
	TopP          float64
}

func NewVLLMProvider(baseURL, apiKey, modelName string, maxTokens, contextWindow int, temperature, topP float64) *VLLMProvider {
	return &VLLMProvider{
		BaseURL:       baseURL,
		APIKey:        apiKey,
		ModelName:     modelName,
		MaxTokens:     maxTokens,
		ContextWindow: contextWindow,
		Temperature:   temperature,
		TopP:          topP,
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

// ConfigureRequest adds the OpenAI-compatible fields vLLM honors: top_p (when
// set) and stream_options.include_usage so the final streaming chunk carries
// the usage block (including prompt_tokens_details.cached_tokens).
func (p *VLLMProvider) ConfigureRequest(req *chatRequest) {
	if p.TopP > 0 {
		req.TopP = p.TopP
	}
	req.StreamOptions = &streamOptions{IncludeUsage: true}
}
