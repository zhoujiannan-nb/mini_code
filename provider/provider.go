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
	Role         string        `json:"role"`
	Content      string        `json:"-"`
	ContentParts []ContentPart `json:"-"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty"`
	Name         string        `json:"name,omitempty"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	type Msg struct {
		Role       string      `json:"role"`
		Content    interface{} `json:"content"`
		ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
		ToolCallID string      `json:"tool_call_id,omitempty"`
		Name       string      `json:"name,omitempty"`
	}
	var content interface{}
	if len(m.ContentParts) > 0 {
		content = m.ContentParts
	} else {
		content = m.Content
	}
	return json.Marshal(Msg{
		Role:       m.Role,
		Content:    content,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
	})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type Msg struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
		Name       string          `json:"name,omitempty"`
	}
	var raw Msg
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
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

func (m *Message) HasImages() bool {
	for _, p := range m.ContentParts {
		if p.Type == "image_url" && p.ImageURL != nil {
			return true
		}
	}
	return false
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
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	FinishReason string     `json:"finish_reason"`
	Usage        *Usage     `json:"usage,omitempty"`
	Error        string     `json:"-"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
	ProviderName() string
	BuildURL() string
	BuildHeaders() map[string]string
	GetContextWindow() int
	GetMaxTokens() int
	GetMaxInput() int
}

type ChatOption func(*chatOptions)

type chatOptions struct {
	Temperature float64
	MaxTokens   int
}

func WithTemperature(t float64) ChatOption {
	return func(o *chatOptions) { o.Temperature = t }
}

func WithMaxTokens(n int) ChatOption {
	return func(o *chatOptions) { o.MaxTokens = n }
}
