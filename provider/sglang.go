package provider

import "fmt"

type SGLangProvider struct {
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
func (p *SGLangProvider) GetMaxInput() int      { return maxInputBudget(p.ContextWindow, p.MaxTokens) }

func (p *SGLangProvider) ConfigureRequest(req *chatRequest) {
	req.StreamOptions = &streamOptions{IncludeUsage: true}
	if p.Thinking != "" {
		req.Thinking = &thinkingConfig{Type: p.Thinking}
		req.ChatTemplateKwargs = map[string]interface{}{
			"enable_thinking": p.Thinking == "enabled",
		}
	}
	if p.ReasoningEffort != "" {
		req.ReasoningEffort = p.ReasoningEffort
	}

	fallbackTemp, fallbackTopP := 1.0, 0.95
	if p.Thinking == "disabled" {
		fallbackTemp, fallbackTopP = 0.7, 0.80
		presencePenalty := 1.5
		req.PresencePenalty = &presencePenalty
	}
	if !req.temperatureExplicit {
		t := fallbackTemp
		if p.Temperature > 0 {
			t = p.Temperature
		}
		req.Temperature = &t
	}
	req.TopP = fallbackTopP
	if p.TopP > 0 {
		req.TopP = p.TopP
	}
}
