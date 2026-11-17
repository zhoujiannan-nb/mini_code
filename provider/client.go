package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/user/mini_code/config"
)

type ModelClient struct {
	provider     Provider
	providerType string
	httpClient   *http.Client
	topK int
}

type chatRequest struct {
	Model       string       `json:"model"`
	Messages    []Message    `json:"messages"`
	Temperature     *float64       `json:"temperature,omitempty"`
	TopP            float64        `json:"top_p,omitempty"`
	TopK            *int           `json:"top_k,omitempty"`
	MaxTokens       *int           `json:"max_tokens,omitempty"`
	Stream          bool           `json:"stream"`
	StreamOptions   *streamOptions `json:"stream_options,omitempty"`
	Tools           []ToolSchema   `json:"tools,omitempty"`
	ToolChoice      string         `json:"tool_choice,omitempty"`
	Thinking        *thinkingConfig `json:"thinking,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	ChatTemplateKwargs map[string]interface{} `json:"chat_template_kwargs,omitempty"`
	PresencePenalty    *float64               `json:"presence_penalty,omitempty"`

	temperatureExplicit bool
}

type chatCompletion struct {
	Choices []struct {
		Message struct {
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

var providerRegistry = map[string]func(cfg config.ModelConfig) Provider{
	"vllm": func(cfg config.ModelConfig) Provider {
		return NewVLLMProvider(cfg.BaseURL, cfg.APIKey, cfg.ModelName, cfg.MaxTokens, cfg.ContextWindow, cfg.Temperature, cfg.TopP, cfg.Thinking, cfg.ReasoningEffort)
	},
	"sglang": func(cfg config.ModelConfig) Provider {
		return NewSGLangProvider(cfg.BaseURL, cfg.APIKey, cfg.ModelName, cfg.MaxTokens, cfg.ContextWindow, cfg.Temperature, cfg.TopP, cfg.Thinking, cfg.ReasoningEffort)
	},
	"deepseek": func(cfg config.ModelConfig) Provider {
		return NewDeepSeekProvider(cfg.BaseURL, cfg.APIKey, cfg.ModelName, cfg.MaxTokens, cfg.ContextWindow, cfg.Temperature, cfg.TopP, cfg.Thinking, cfg.ReasoningEffort)
	},
	"ollama": func(cfg config.ModelConfig) Provider {
		return NewOllamaProvider(cfg.BaseURL, cfg.APIKey, cfg.ModelName, cfg.MaxTokens, cfg.ContextWindow, cfg.Temperature, cfg.TopP)
	},
}

func NewModelClient(cfg config.ModelConfig) (*ModelClient, error) {
	factory, ok := providerRegistry[strings.ToLower(cfg.Provider)]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", cfg.Provider)
	}
	return &ModelClient{
		provider:     factory(cfg),
		providerType: cfg.Provider,
		httpClient:   &http.Client{Timeout: 3600 * time.Second},
		topK:         cfg.TopK,
	}, nil
}

func temperaturePtr(t float64) *float64 {
	if t <= 0 {
		return nil
	}
	return &t
}

func maxTokensPtr(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

func (mc *ModelClient) applySampling(req *chatRequest) {
	if mc.topK > 0 && req.TopK == nil {
		k := mc.topK
		req.TopK = &k
	}
}

func dropStreamOnlyFields(req *chatRequest) {
	if !req.Stream {
		req.StreamOptions = nil
	}
}

func (mc *ModelClient) WithReasoning(thinking, effort string) *ModelClient {
	if mc == nil || (thinking == "" && effort == "") {
		return mc
	}
	switch p := mc.provider.(type) {
	case *VLLMProvider:
		cp := *p
		cp.Thinking = thinking
		cp.ReasoningEffort = effort
		return &ModelClient{provider: &cp, providerType: mc.providerType, httpClient: mc.httpClient, topK: mc.topK}
	case *SGLangProvider:
		cp := *p
		cp.Thinking = thinking
		cp.ReasoningEffort = effort
		return &ModelClient{provider: &cp, providerType: mc.providerType, httpClient: mc.httpClient, topK: mc.topK}
	case *DeepSeekProvider:
		cp := *p
		cp.Thinking = thinking
		cp.ReasoningEffort = effort
		return &ModelClient{provider: &cp, providerType: mc.providerType, httpClient: mc.httpClient, topK: mc.topK}
	}
	return mc
}

func ListModels(baseURL, apiKey string) ([]string, error) {
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer " + apiKey)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := string(body)
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, fmt.Errorf("models endpoint returned %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse models response: %w", err)
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}

func (mc *ModelClient) Chat(ctx context.Context, messages []Message, tools []ToolSchema, opts ...ChatOption) (*ChatResponse, error) {
	o := chatOptions{
		MaxTokens: 0,
	}
	if p, ok := mc.provider.(*VLLMProvider); ok {
		o.Temperature = p.Temperature
		o.MaxTokens = p.MaxTokens
	}
	if p, ok := mc.provider.(*OllamaProvider); ok {
		o.Temperature = p.Temperature
		o.MaxTokens = p.MaxTokens
	}
	if p, ok := mc.provider.(*SGLangProvider); ok && p.MaxTokens > 0 {
		o.MaxTokens = p.MaxTokens
	}
	if p, ok := mc.provider.(*DeepSeekProvider); ok && p.MaxTokens > 0 {
		o.MaxTokens = p.MaxTokens
	}
	for _, opt := range opts {
		opt(&o)
	}

	req := chatRequest{
		Model:               mc.modelName(),
		Messages:            messages,
		Temperature:         temperaturePtr(o.Temperature),
		MaxTokens:           maxTokensPtr(o.MaxTokens),
		Stream:              false,
		temperatureExplicit: o.TemperatureExplicit,
	}
	if len(tools) > 0 {
		req.Tools = tools
		req.ToolChoice = "auto"
	}
	mc.provider.ConfigureRequest(&req)
	mc.applySampling(&req)
	dropStreamOnlyFields(&req)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	maxRetries := 3
	for retry := 0; retry <= maxRetries; retry++ {
		resp, err := mc.doRequest(ctx, body)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if mc.isTokenError(err.Error()) {
				return nil, err
			}
			if retry < maxRetries {
				time.Sleep(time.Duration(1+rand.Intn(3)) * time.Second)
				continue
			}
			return nil, err
		}

		if resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			errText := string(respBody)
			if mc.isTokenError(errText) {
				return &ChatResponse{Error: fmt.Sprintf("API error: %d - %s", resp.StatusCode, errText)}, nil
			}
			if isNonRetryableStatus(resp.StatusCode) {
				return &ChatResponse{Error: fmt.Sprintf("API error: %d - %s", resp.StatusCode, errText)}, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if retry < maxRetries {
				time.Sleep(time.Duration(1+rand.Intn(3)) * time.Second)
				continue
			}
			return &ChatResponse{Error: fmt.Sprintf("API error: %d - %s", resp.StatusCode, errText)}, nil
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		var completion chatCompletion
		if err := json.Unmarshal(respBody, &completion); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}

		if len(completion.Choices) == 0 {
			return &ChatResponse{Error: "no choices in response"}, nil
		}

		ch := completion.Choices[0]
		return &ChatResponse{
			Content:          ch.Message.Content,
			ReasoningContent: ch.Message.ReasoningContent,
			ToolCalls:        ch.Message.ToolCalls,
			FinishReason:     ch.FinishReason,
			Usage:            completion.Usage,
		}, nil
	}
	return &ChatResponse{Error: "max retries exceeded"}, nil
}

func (mc *ModelClient) doRequest(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", mc.provider.BuildURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range mc.provider.BuildHeaders() {
		req.Header.Set(k, v)
	}
	return mc.httpClient.Do(req)
}

func (mc *ModelClient) modelName() string {
	switch p := mc.provider.(type) {
	case *VLLMProvider:
		return p.ModelName
	case *SGLangProvider:
		return p.ModelName
	case *DeepSeekProvider:
		return p.ModelName
	case *OllamaProvider:
		return p.ModelName
	}
	return ""
}

func (mc *ModelClient) ModelName() string { return mc.modelName() }

func isNonRetryableStatus(code int) bool {
	switch code {
	case 401, 402, 422:
		return true
	}
	return false
}

func (mc *ModelClient) isTokenError(text string) bool {
	lower := strings.ToLower(text)
	keywords := []string{
		"context length",
		"context window",
		"max context",
		"token limit",
		"max tokens",
		"max_tokens",
		"input tokens",
		"output tokens",
		"too long",
	}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func (mc *ModelClient) Close() error {
	return nil
}

func (mc *ModelClient) GetContextWindow() int { return mc.provider.GetContextWindow() }
func (mc *ModelClient) GetMaxTokens() int     { return mc.provider.GetMaxTokens() }
func (mc *ModelClient) GetMaxInput() int      { return mc.provider.GetMaxInput() }
