package provider

import "fmt"

type VLLMProvider struct {
	BaseURL         string
	APIKey          string
	ModelName       string
	MaxTokens       int
	ContextWindow   int
	Temperature     float64
	TopP            float64
	Thinking        string
	ReasoningEffort string
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
