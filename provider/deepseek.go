package provider

import "fmt"

type DeepSeekProvider struct {
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

func (p *DeepSeekProvider) ConfigureRequest(req *chatRequest) {
	if p.TopP > 0 {
		req.TopP = p.TopP
	}

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
	}

	if thinkingOn {
		req.Thinking = &thinkingConfig{Type: "enabled"}
		if effort != "" {
			req.ReasoningEffort = effort
		}
		req.Temperature = nil
		if req.TopP > 0 && req.TopP < 0.95 {
			req.TopP = 0.95
		}
	} else {
		req.Thinking = &thinkingConfig{Type: "disabled"}
	}

	req.StreamOptions = &streamOptions{IncludeUsage: true}
}
