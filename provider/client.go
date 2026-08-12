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
}

type chatRequest struct {
	Model       string       `json:"model"`
	Messages    []Message    `json:"messages"`
	Temperature float64      `json:"temperature"`
	MaxTokens   int          `json:"max_tokens"`
	Stream      bool         `json:"stream"`
	Tools       []ToolSchema `json:"tools,omitempty"`
	ToolChoice  string       `json:"tool_choice,omitempty"`
}

type chatCompletion struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

var providerRegistry = map[string]func(cfg config.ModelConfig) Provider{
	"vllm": func(cfg config.ModelConfig) Provider {
		return NewVLLMProvider(cfg.BaseURL, cfg.APIKey, cfg.ModelName, cfg.MaxTokens, cfg.ContextWindow, cfg.Temperature, cfg.TopP)
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
		httpClient:   &http.Client{Timeout: 180 * time.Second},
	}, nil
}

func (mc *ModelClient) Chat(ctx context.Context, messages []Message, tools []ToolSchema, opts ...ChatOption) (*ChatResponse, error) {
	o := chatOptions{
		Temperature: 0.7,
		MaxTokens:   8192,
	}
	if p, ok := mc.provider.(*VLLMProvider); ok {
		o.Temperature = p.Temperature
		o.MaxTokens = p.MaxTokens
	}
	if p, ok := mc.provider.(*OllamaProvider); ok {
		o.Temperature = p.Temperature
		o.MaxTokens = p.MaxTokens
	}
	for _, opt := range opts {
		opt(&o)
	}

	req := chatRequest{
		Model:       mc.modelName(),
		Messages:    messages,
		Temperature: o.Temperature,
		MaxTokens:   o.MaxTokens,
		Stream:      false,
	}
	if len(tools) > 0 {
		req.Tools = tools
		req.ToolChoice = "auto"
	}

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
			Content:      ch.Message.Content,
			ToolCalls:    ch.Message.ToolCalls,
			FinishReason: ch.FinishReason,
			Usage:        completion.Usage,
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
	case *OllamaProvider:
		return p.ModelName
	}
	return ""
}

func (mc *ModelClient) isTokenError(text string) bool {
	lower := strings.ToLower(text)
	keywords := []string{"context length", "token limit", "max_tokens", "input tokens", "output tokens", "exceeds", "too long"}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func (mc *ModelClient) TestConnection() bool {
	resp, err := mc.Chat(context.Background(), []Message{{Role: "user", Content: "Hi"}}, nil)
	return err == nil && resp != nil && resp.Error == ""
}

func (mc *ModelClient) Close() error {
	return nil
}

func (mc *ModelClient) GetContextWindow() int { return mc.provider.GetContextWindow() }
func (mc *ModelClient) GetMaxTokens() int     { return mc.provider.GetMaxTokens() }
func (mc *ModelClient) GetMaxInput() int      { return mc.provider.GetMaxInput() }
