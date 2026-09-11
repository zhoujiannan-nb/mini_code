package provider

import "testing"

// TestModelClientWithReasoning pins the per-session thinking override: the
// derived client carries the new thinking switch + effort, the shared base
// client is left untouched, and an empty override returns the receiver.
func TestModelClientWithReasoning(t *testing.T) {
	mc := &ModelClient{provider: NewSGLangProvider("http://x", "k", "m", 100, 1000, 0.6, 0.9, "", "")}

	derived := mc.WithReasoning("enabled", "low")
	if derived == mc {
		t.Fatal("expected a derived client for a non-empty override")
	}
	// The base provider must not be mutated (the registry client is shared).
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

	// An empty override is a no-op (returns the receiver).
	if mc.WithReasoning("", "") != mc {
		t.Fatal("empty override should return the receiver")
	}
}

// TestUsageCacheReported pins the cache-report detection used to tell a real
// 0% hit apart from a deployment that does not report cache hits at all.
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
