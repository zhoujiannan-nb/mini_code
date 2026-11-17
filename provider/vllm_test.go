package provider

import (
	"testing"

	"github.com/user/mini_code/config"
)

func TestVLLMConfigureRequestThinking(t *testing.T) {
	base := func(thinking, effort string) *VLLMProvider {
		return NewVLLMProvider("http://h/v1", "k", "qwen3.8", 8192, 131072, 0, 0, thinking, effort)
	}

	t.Run("unset sends nothing extra", func(t *testing.T) {
		req := &chatRequest{Model: "m", Stream: true}
		base("", "").ConfigureRequest(req)
		if len(req.ChatTemplateKwargs) != 0 {
			t.Errorf("chat_template_kwargs = %v, want none for an unset switch", req.ChatTemplateKwargs)
		}
		if req.ReasoningEffort != "" {
			t.Errorf("reasoning_effort = %q, want empty", req.ReasoningEffort)
		}
		if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
			t.Error("stream_options.include_usage not set")
		}
	})

	t.Run("disabled turns the template off", func(t *testing.T) {
		req := &chatRequest{Model: "m", Stream: true}
		base("disabled", "").ConfigureRequest(req)
		if got, ok := req.ChatTemplateKwargs["enable_thinking"]; !ok || got != false {
			t.Errorf("chat_template_kwargs.enable_thinking = %v (present=%v), want false", got, ok)
		}
		if req.Thinking != nil {
			t.Errorf("thinking = %+v, want the request to carry no top-level thinking field", req.Thinking)
		}
	})

	t.Run("enabled and effort", func(t *testing.T) {
		req := &chatRequest{Model: "m", Stream: true}
		base("enabled", "low").ConfigureRequest(req)
		if got := req.ChatTemplateKwargs["enable_thinking"]; got != true {
			t.Errorf("chat_template_kwargs.enable_thinking = %v, want true", got)
		}
		if req.ReasoningEffort != "low" {
			t.Errorf("reasoning_effort = %q, want low", req.ReasoningEffort)
		}
	})

	t.Run("configured top_p still applies", func(t *testing.T) {
		p := NewVLLMProvider("http://h/v1", "k", "m", 1024, 8192, 0, 0.9, "disabled", "")
		req := &chatRequest{Model: "m", Stream: true}
		p.ConfigureRequest(req)
		if req.TopP != 0.9 {
			t.Errorf("top_p = %v, want 0.9", req.TopP)
		}
	})
}

func TestWithReasoningCoversVLLM(t *testing.T) {
	mc, err := NewModelClient(config.ModelConfig{
		Provider: "vllm", BaseURL: "http://h/v1", APIKey: "k",
		ModelName: "m", MaxTokens: 1024, ContextWindow: 8192,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	defer mc.Close()

	derived := mc.WithReasoning("disabled", "")
	if derived == mc {
		t.Fatal("WithReasoning returned the receiver unchanged for vLLM — a thinking-off call would silently keep thinking")
	}
	p, ok := derived.provider.(*VLLMProvider)
	if !ok {
		t.Fatalf("derived provider is %T, want *VLLMProvider", derived.provider)
	}
	if p.Thinking != "disabled" {
		t.Errorf("derived Thinking = %q, want disabled", p.Thinking)
	}
	if orig, ok := mc.provider.(*VLLMProvider); ok && orig.Thinking != "" {
		t.Errorf("the shared client was mutated: Thinking = %q", orig.Thinking)
	}
	if got := mc.WithReasoning("", ""); got != mc {
		t.Error("an empty override should return the receiver unchanged")
	}
}
