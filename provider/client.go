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
	// topK is the configured top_k (0 = never sent). It lives on the client
	// rather than on each provider because every OpenAI-compatible server takes
	// the same field, so the four providers need no copy of it.
	topK int
}

type chatRequest struct {
	Model       string       `json:"model"`
	Messages    []Message    `json:"messages"`
	// Temperature is a pointer so a provider can omit it entirely: DeepSeek
	// rejects temperature (and the other sampling penalties) when thinking mode
	// is enabled, so the DeepSeek provider nils it in that case.
	Temperature     *float64       `json:"temperature,omitempty"`
	TopP            float64        `json:"top_p,omitempty"`
	// TopK is a pointer so it can be omitted: OpenAI-compatible servers take
	// top_k = 0 as "sample from everything", so only an explicitly configured
	// value is ever sent.
	TopK            *int           `json:"top_k,omitempty"`
	MaxTokens       *int           `json:"max_tokens,omitempty"`
	Stream          bool           `json:"stream"`
	StreamOptions   *streamOptions `json:"stream_options,omitempty"`
	Tools           []ToolSchema   `json:"tools,omitempty"`
	ToolChoice      string         `json:"tool_choice,omitempty"`
	Thinking        *thinkingConfig `json:"thinking,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	// ChatTemplateKwargs is passed verbatim to the model's Jinja chat
	// template (SGLang). Qwen3-style templates use it for the thinking
	// switch (enable_thinking); templates that do not reference a kwarg
	// simply ignore it, so it is safe to send on any deployment.
	ChatTemplateKwargs map[string]interface{} `json:"chat_template_kwargs,omitempty"`
	PresencePenalty    *float64               `json:"presence_penalty,omitempty"`

	// temperatureExplicit marks a per-call temperature override (set from
	// chatOptions.TemperatureExplicit, e.g. the 0.3 compaction summary).
	// Unexported: never serialized.
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
	// SGLang is OpenAI-compatible like vLLM. The endpoint is {base}/chat/completions;
	// a direct SGLang deployment puts /v1 in base_url, while a reverse proxy that
	// rewrites the path (e.g. /proxy/model/x -> /v1) keeps base_url bare.
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
		httpClient:   &http.Client{Timeout: 3600 * time.Second}, // 1 hour timeout for long agent tasks
		topK:         cfg.TopK,
	}, nil
}

// temperaturePtr turns a config/call-site temperature into the request field:
// 0 means "not set", and the field is then left out so the server's own default
// (usually the model's recommended value) is used. WithTemperature(0.3) still
// produces a non-zero value, so an explicit override is always sent.
func temperaturePtr(t float64) *float64 {
	if t <= 0 {
		return nil
	}
	return &t
}

// maxTokensPtr turns a configured/call-site output budget into the request field:
// 0 means "not set" and the field is then left out, so the server caps the output
// with its own default instead of a number this program invented.
func maxTokensPtr(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

// applySampling adds the config-level sampling fields that every
// OpenAI-compatible server spells the same way. It runs after the provider's
// ConfigureRequest, so a provider that chose a value of its own keeps it.
func (mc *ModelClient) applySampling(req *chatRequest) {
	if mc.topK > 0 && req.TopK == nil {
		k := mc.topK
		req.TopK = &k
	}
}

// dropStreamOnlyFields strips the fields that are only legal on a streaming
// call. Providers add stream_options.include_usage unconditionally in
// ConfigureRequest because their main traffic streams, but a non-streaming
// request that carries it is a hard 400 on vLLM/SGLang ("Stream options can
// only be defined when `stream=True`") — which silently killed every
// non-streaming call, the continue guard's judge included. The strip runs
// after ConfigureRequest so the providers keep their tests and their default.
func dropStreamOnlyFields(req *chatRequest) {
	if !req.Stream {
		req.StreamOptions = nil
	}
}

// WithReasoning returns a client whose provider carries an explicit thinking
// depth (thinking switch + reasoning_effort), leaving the shared registry client
// untouched. It backs two things: the per-session thinking override set from the
// composer, and the session-title call (which always asks for "disabled"). vLLM,
// SGLang and DeepSeek honor these fields; any other provider (or an empty
// override) returns the receiver unchanged. The HTTP client is shared, so the
// derived client adds no connections.
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

// ListModels calls the OpenAI-compatible {baseURL}/models endpoint and returns
// the model IDs. baseURL should be the API root the same way it is for chat
// (e.g. "https://api.deepseek.com" or "http://proxy/proxy/model/x"); the client
// only appends /models, so a reverse proxy that rewrites /v1 works unchanged.
// Used by the web UI's "fetch models" button to populate the model picker.
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
	// Temperature is left unset (0) by default: the request then omits it and
	// the serving framework applies the model's own default. A configured value
	// (the vllm/ollama branches below) or an explicit WithTemperature still wins.
	o := chatOptions{
		MaxTokens: 0, // unset: omitted from the request unless configured/passed
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
		// Respect the configured output budget; sampling defaults are
		// applied by SGLangProvider.ConfigureRequest.
		o.MaxTokens = p.MaxTokens
	}
	if p, ok := mc.provider.(*DeepSeekProvider); ok && p.MaxTokens > 0 {
		// Respect the configured output budget (previously the hardcoded
		// 8192 default was sent regardless of config).
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
			// Auth / balance / parameter errors will not heal on retry: fail fast
			// instead of burning 3 attempts (and the user's quota) on a 401/402/422.
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

// ModelName returns the provider's model identifier. The token scale
// (tokenizer + template/schema overhead) is model-specific, so the token
// calibrator keys on it.
func (mc *ModelClient) ModelName() string { return mc.modelName() }

// isNonRetryableStatus reports whether an HTTP status is a deterministic
// failure that retrying cannot fix: bad credentials (401), insufficient
// balance (402), or a malformed request (422). These fail fast so we do not
// burn the retry budget (and the user's quota) re-sending a request the
// provider will reject identically every time.
func isNonRetryableStatus(code int) bool {
	switch code {
	case 401, 402, 422:
		return true
	}
	return false
}

// isTokenError reports whether an API error message indicates the request
// is too large for the model's context window (so the run should fail fast
// instead of retrying the same oversized request).
//
// Keywords are deliberately specific. A broad match such as "exceeds" would
// misclassify unrelated, retryable failures — e.g. "payload exceeds the
// maximum size", "max concurrent requests exceeded" — and suppress retries.
func (mc *ModelClient) isTokenError(text string) bool {
	lower := strings.ToLower(text)
	keywords := []string{
		"context length", // vLLM/OpenAI: "This model's maximum context length is ..."
		"context window", // Ollama-style context errors
		"max context",    // "max context size/tokens ..."
		"token limit",
		"max tokens",
		"max_tokens",
		"input tokens",
		"output tokens",
		"too long", // "prompt is too long: N tokens > M maximum"
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
