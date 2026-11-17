package provider

import "testing"

func TestModelClientWithReasoning(t *testing.T) {
	mc := &ModelClient{provider: NewSGLangProvider("http://x", "k", "m", 100, 1000, 0.6, 0.9, "", "")}

	derived := mc.WithReasoning("enabled", "low")
	if derived == mc {
		t.Fatal("expected a derived client for a non-empty override")
	}
	if p := mc.provider.(*SGLangProvider); p.Thinking != "" || p.ReasoningEffort != "" {
		t.Fatalf("base provider was mutated: %+v", p)
	}
	req := &chatRequest{}
	derived.provider.ConfigureRequest(req)
	if req.Thinking == nil || req.Thinking.Type != "enabled" {
		t.Fatalf("derived thinking = %+v, want enabled", req.Thinking)
	}
	if req.ReasoningEffort != "low" {
		t.Fatalf("derived reasoning_effort = %q, want low", req.ReasoningEffort)
	}

	if mc.WithReasoning("", "") != mc {
		t.Fatal("empty override should return the receiver")
	}
}

func TestUsageCacheReported(t *testing.T) {
	if (&Usage{}).CacheReported() {
		t.Fatal("empty usage should not report cache")
	}
	if !(&Usage{PromptTokensDetails: &PromptTokensDetails{}}).CacheReported() {
		t.Fatal("prompt_tokens_details present => reported (even at 0)")
	}
	if !(&Usage{PromptCacheHitTokens: 5}).CacheReported() {
		t.Fatal("deepseek hit tokens > 0 => reported")
	}
	if (&Usage{PromptCacheHitTokens: 0}).CacheReported() {
		t.Fatal("deepseek 0 without details => not reported")
	}
}
