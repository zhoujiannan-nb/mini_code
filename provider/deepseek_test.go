package provider

import "testing"

// TestDeepSeekConfigureRequest pins the thinking-mode request construction:
// the resolved thinking switch, reasoning_effort passthrough, temperature
// omission while thinking is on, and the top_p 0.95 floor.
func TestDeepSeekConfigureRequest(t *testing.T) {
	base := func(thinking, effort string) *DeepSeekProvider {
		return NewDeepSeekProvider("https://api.deepseek.com", "k", "deepseek-v4-pro", 8192, 1000000, 0.7, 0.9, thinking, effort)
	}
	newReq := func() *chatRequest {
		temp := 0.7
		return &chatRequest{Model: "m", Temperature: &temp, Stream: true}
	}

	cases := []struct {
		name          string
		thinking      string
		effort        string
		wantThinking  string // "" = field omitted
		wantEffort    string
		wantTempNil   bool
		wantTopP      float64
	}{
		{"default -> thinking on, no effort, temp omitted, top_p floored", "", "", "enabled", "", true, 0.95},
		{"explicit enabled", "enabled", "", "enabled", "", true, 0.95},
		{"explicit disabled -> temp kept", "disabled", "", "disabled", "", false, 0.9},
		{"effort off -> disabled, temp kept", "", "off", "disabled", "", false, 0.9},
		{"effort low -> enabled + effort", "", "low", "enabled", "low", true, 0.95},
		{"effort max -> enabled + effort", "", "max", "enabled", "max", true, 0.95},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := newReq()
			base(c.thinking, c.effort).ConfigureRequest(req)

			gotThinking := ""
			if req.Thinking != nil {
				gotThinking = req.Thinking.Type
			}
			if gotThinking != c.wantThinking {
				t.Errorf("thinking = %q, want %q", gotThinking, c.wantThinking)
			}
			if req.ReasoningEffort != c.wantEffort {
				t.Errorf("reasoning_effort = %q, want %q", req.ReasoningEffort, c.wantEffort)
			}
			if (req.Temperature == nil) != c.wantTempNil {
				t.Errorf("temperature nil = %v, want %v", req.Temperature == nil, c.wantTempNil)
			}
			if req.TopP != c.wantTopP {
				t.Errorf("top_p = %v, want %v", req.TopP, c.wantTopP)
			}
			if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
				t.Errorf("stream_options.include_usage not set")
			}
		})
	}
}

// TestUsageCachedTokens pins the cache-hit field resolution: the OpenAI/SGLang
// prompt_tokens_details.cached_tokens wins, falling back to DeepSeek's
// prompt_cache_hit_tokens, and nil/absent yields 0.
func TestUsageCachedTokens(t *testing.T) {
	if (&Usage{}).CachedTokens() != 0 {
		t.Errorf("empty usage should report 0 cached")
	}
	var nilU *Usage
	if nilU.CachedTokens() != 0 {
		t.Errorf("nil usage should report 0 cached")
	}
	u := &Usage{PromptTokensDetails: &PromptTokensDetails{CachedTokens: 100}, PromptCacheHitTokens: 50}
	if got := u.CachedTokens(); got != 100 {
		t.Errorf("CachedTokens = %d, want 100 (prompt_tokens_details wins)", got)
	}
	u2 := &Usage{PromptCacheHitTokens: 50}
	if got := u2.CachedTokens(); got != 50 {
		t.Errorf("CachedTokens = %d, want 50 (deepseek fallback)", got)
	}
}
