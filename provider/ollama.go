package provider

import "fmt"

type OllamaProvider struct {
	BaseURL       string
	APIKey        string
	ModelName     string
	MaxTokens     int
	ContextWindow int
	Temperature   float64
	TopP          float64
}

func NewOllamaProvider(baseURL, apiKey, modelName string, maxTokens, contextWindow int, temperature, topP float64) *OllamaProvider {
	return &OllamaProvider{
		BaseURL:       baseURL,
		APIKey:        apiKey,
		ModelName:     modelName,
		MaxTokens:     maxTokens,
		ContextWindow: contextWindow,
		Temperature:   temperature,
		TopP:          topP,
	}
}

func (p *OllamaProvider) ProviderName() string { return "ollama" }

func (p *OllamaProvider) BuildURL() string {
	return fmt.Sprintf("%s/chat/completions", p.BaseURL)
}

func (p *OllamaProvider) BuildHeaders() map[string]string {
	h := map[string]string{"Content-Type": "application/json"}
	if p.APIKey != "" && p.APIKey != "not-needed" {
		h["Authorization"] = "Bearer " + p.APIKey
	}
	return h
}

func (p *OllamaProvider) GetContextWindow() int { return p.ContextWindow }
func (p *OllamaProvider) GetMaxTokens() int     { return p.MaxTokens }
func (p *OllamaProvider) GetMaxInput() int      { return p.ContextWindow - p.MaxTokens - 2048 }
