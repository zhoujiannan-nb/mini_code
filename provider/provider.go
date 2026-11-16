package provider

import (
	"encoding/json"
	"fmt"
)

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

func NewTextPart(text string) ContentPart {
	return ContentPart{Type: "text", Text: text}
}

func NewImagePart(dataURL string) ContentPart {
	return ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: dataURL}}
}

type Message struct {
	Role             string        `json:"role"`
	Content          string        `json:"-"`
	ContentParts     []ContentPart `json:"-"`
	ReasoningContent string        `json:"-"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	Name             string        `json:"name,omitempty"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	type Msg struct {
		Role             string      `json:"role"`
		Content          interface{} `json:"content"`
		ReasoningContent string      `json:"reasoning_content,omitempty"`
		ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
		ToolCallID       string      `json:"tool_call_id,omitempty"`
		Name             string      `json:"name,omitempty"`
	}
	var content interface{}
	if len(m.ContentParts) > 0 {
		content = m.ContentParts
	} else {
		content = m.Content
	}
	return json.Marshal(Msg{
		Role:             m.Role,
		Content:          content,
		ReasoningContent: m.ReasoningContent,
		ToolCalls:        m.ToolCalls,
		ToolCallID:       m.ToolCallID,
		Name:             m.Name,
	})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type Msg struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ReasoningContent string          `json:"reasoning_content"`
		ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID       string          `json:"tool_call_id,omitempty"`
		Name             string          `json:"name,omitempty"`
	}
	var raw Msg
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.ReasoningContent = raw.ReasoningContent
	m.ToolCalls = raw.ToolCalls
	m.ToolCallID = raw.ToolCallID
	m.Name = raw.Name
	if len(raw.Content) == 0 || string(raw.Content) == "null" {
		return nil
	}
	if raw.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(raw.Content, &s); err != nil {
			return err
		}
		m.Content = s
	} else if raw.Content[0] == '[' {
		var parts []ContentPart
		if err := json.Unmarshal(raw.Content, &parts); err != nil {
			return fmt.Errorf("unmarshal content parts: %w", err)
		}
		m.ContentParts = parts
		for _, p := range parts {
			if p.Type == "text" {
				m.Content += p.Text
			}
		}
	}
	return nil
}

func (m *Message) GetText() string {
	if m.Content != "" {
		return m.Content
	}
	var text string
	for _, p := range m.ContentParts {
		if p.Type == "text" {
			text += p.Text
		}
	}
	return text
}

// SetTextContent sets text-only content (clears ContentParts)
func (m *Message) SetTextContent(text string) {
	m.Content = text
	m.ContentParts = nil
}

// SetMultimodalContent sets content with parts (clears Content string)
func (m *Message) SetMultimodalContent(parts []ContentPart) {
	m.ContentParts = parts
	m.Content = ""
}

type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function FuncCall `json:"function"`
}

type FuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatResponse struct {
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	FinishReason     string     `json:"finish_reason"`
	Usage            *Usage     `json:"usage,omitempty"`
	Error            string     `json:"-"`
}

// StreamChunk is delivered to the onChunk callback during streaming.
// Content and ReasoningContent are accumulated values (never deltas),
// so the consumer can render them directly.
type StreamChunk struct {
	Content          string
	ReasoningContent string
	// ToolCalls lists the tool calls being generated so far, in stream order,
	// with the arguments accumulated up to this point. It exists so a caller can
	// show what the model is about to call WHILE it is still typing the
	// arguments (a 20 KB write_file payload takes minutes to generate, and until
	// now the UI had nothing to show for it). Same accumulate-not-delta
	// convention as Content, so a dropped snapshot costs nothing.
	ToolCalls []StreamToolCallProgress
}

// StreamToolCallProgress is one in-flight tool call mid-generation.
type StreamToolCallProgress struct {
	Index int    // stream index; parallel calls arrive as 0, 1, ...
	ID    string // may be empty until the provider sends it
	Name  string // arrives with the first delta of this call
	Args  string // arguments accumulated so far (raw JSON, possibly incomplete)
}

// PromptTokensDetails is the OpenAI/SGLang/vLLM cache-hit shape:
// usage.prompt_tokens_details.cached_tokens.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	// Cache-hit reporting. Providers use different fields: SGLang/vLLM (and
	// OpenAI) populate PromptTokensDetails.CachedTokens, while DeepSeek uses the
	// custom top-level PromptCacheHitTokens. Both are parsed; CachedTokens()
	// reads whichever is present.
	PromptTokensDetails  *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	PromptCacheHitTokens int                  `json:"prompt_cache_hit_tokens,omitempty"`
}

// CachedTokens returns the number of prompt tokens served from the provider's
// KV prefix cache, reading whichever field the provider populated. It returns
// 0 when the provider does not report cache hits (e.g. an SGLang server started
// without --enable-cache-report), so callers can treat 0 as "unknown/none"
// rather than a real miss.
func (u *Usage) CachedTokens() int {
	if u == nil {
		return 0
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		return u.PromptTokensDetails.CachedTokens
	}
	return u.PromptCacheHitTokens
}

// CacheReported reports whether the provider actually included cache-hit
// information, even when the hit count is a genuine 0. OpenAI/SGLang/vLLM
// include the prompt_tokens_details object (so its presence means "reported"),
// while DeepSeek only sends prompt_cache_hit_tokens when reporting is on. This
// lets the UI tell "0% cache hits" apart from "this deployment does not report
// cache hits at all".
func (u *Usage) CacheReported() bool {
	if u == nil {
		return false
	}
	return u.PromptTokensDetails != nil || u.PromptCacheHitTokens > 0
}

// streamOptions requests the provider to include a usage block in the final
// streaming chunk. Without it, OpenAI-compatible servers (vLLM/SGLang) omit
// usage from streams, so cache-hit accounting would be invisible.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// thinkingConfig is DeepSeek's thinking-mode switch: {"type":"enabled"|"disabled"}.
type thinkingConfig struct {
	Type string `json:"type"`
}

type ToolSchema struct {
	Type     string         `json:"type"`
	Function ToolSchemaFunc `json:"function"`
}

type ToolSchemaFunc struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type Provider interface {
	// ProviderName() was dropped: no caller ever asked a provider its own
	// name — the registry keys everything by model name instead.
	BuildURL() string
	BuildHeaders() map[string]string
	GetContextWindow() int
	GetMaxTokens() int
	GetMaxInput() int
	// ConfigureRequest sets provider-specific request-body fields (top_p,
	// stream_options, thinking mode, ...) on a request whose model/messages/
	// temperature/max_tokens/stream are already set. Providers that add nothing
	// leave the request unchanged.
	ConfigureRequest(req *chatRequest)
}

// maxInputBudget is the input space a request may use: the context window minus the
// output reserve minus a small safety margin for template/chat-format overhead.
// An unknown window (0) means the budget is unknown too, so this returns 0 rather
// than inventing one — callers then skip window-based thresholds entirely.
func maxInputBudget(contextWindow, maxTokens int) int {
	if contextWindow <= 0 {
		return 0
	}
	if n := contextWindow - maxTokens - 2048; n > 0 {
		return n
	}
	return 0
}

type ChatOption func(*chatOptions)

type chatOptions struct {
	Temperature float64
	MaxTokens   int
	// TemperatureExplicit marks a per-call temperature override (e.g. the
	// 0.3 compaction summary). Providers may apply their own sampling
	// defaults unless this is set.
	TemperatureExplicit bool
}

// (There used to be a WithTemperature option: no caller ever set it, so the
// providers' own sampling defaults always applied. ChatOption itself stays —
// WithMaxTokens below is the live one.)

func WithMaxTokens(n int) ChatOption {
	return func(o *chatOptions) { o.MaxTokens = n }
}
