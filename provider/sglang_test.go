package provider

import "testing"

// TestSGLangConfigureRequest pins SGLang's request construction:
// thinking / reasoning_effort passthrough (only when set), the
// chat_template_kwargs.enable_thinking switch (what actually flips thinking
// off on Qwen3-style deployments), the official Qwen3.8 sampling parameters
// per thinking state, and stream_options.include_usage.
//
// Sampling (official Qwen3.8 recommendation; Qwen3-style models think by
// default, so an unset switch means thinking ON). Precedence: explicit per-call
// temperature > configured temperature / top_p (0 = not set) > these fallbacks:
//
//	thinking ON:  temperature=1.0, top_p=0.95
//	thinking OFF: temperature=0.7, top_p=0.80, presence_penalty=1.5
//
// An explicit per-call temperature (temperatureExplicit, e.g. the 0.3
// compaction summary) wins over the default.
func TestSGLangConfigureRequest(t *testing.T) {
	base := func(thinking, effort string) *SGLangProvider {
		// temperature / top_p left unset (0): this exercises the per-thinking-state
		// fallbacks. A configured value is covered by its own subtest below.
		return NewSGLangProvider("http://proxy/proxy/model/x", "k", "Qwen3.8-27B-FP8", 31072, 200000, 0, 0, thinking, effort)
	}
	newReq := func() *chatRequest {
		temp := 0.6
		return &chatRequest{Model: "m", Temperature: &temp, Stream: true}
	}

	cases := []struct {
		name         string
		thinking     string
		effort       string
		wantThinking string // "" = field omitted
		wantEffort   string
		// wantEnableThinking: "" = no chat_template_kwargs at all;
		// "true"/"false" = the enable_thinking kwarg value.
		wantEnableThinking string
		wantTemp           float64
		wantTopP           float64
		wantPresence       *float64 // nil = field omitted
	}{
		{"unset -> thinking ON defaults", "", "", "", "", "", 1.0, 0.95, nil},
		{"thinking enabled only", "enabled", "", "enabled", "", "true", 1.0, 0.95, nil},
		{"thinking disabled only", "disabled", "", "disabled", "", "false", 0.7, 0.80, ptr(1.5)},
		{"effort xhigh only", "", "xhigh", "", "xhigh", "", 1.0, 0.95, nil},
		{"effort medium only", "", "medium", "", "medium", "", 1.0, 0.95, nil},
		{"enabled + xhigh", "enabled", "xhigh", "enabled", "xhigh", "true", 1.0, 0.95, nil},
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
			// enable_thinking kwarg: present exactly when the thinking
			// switch is set, with the mapped boolean value.
			gotKwarg, hasKwarg := req.ChatTemplateKwargs["enable_thinking"]
			if c.wantEnableThinking == "" {
				if hasKwarg {
					t.Errorf("chat_template_kwargs.enable_thinking = %v, want absent", gotKwarg)
				}
			} else {
				want := c.wantEnableThinking == "true"
				if !hasKwarg || gotKwarg != want {
					t.Errorf("chat_template_kwargs.enable_thinking = %v (present=%v), want %v", gotKwarg, hasKwarg, want)
				}
			}
			// Official sampling parameters per thinking state.
			if req.Temperature == nil || *req.Temperature != c.wantTemp {
				t.Errorf("temperature = %v, want %v", req.Temperature, c.wantTemp)
			}
			if req.TopP != c.wantTopP {
				t.Errorf("top_p = %v, want %v", req.TopP, c.wantTopP)
			}
			if c.wantPresence == nil {
				if req.PresencePenalty != nil {
					t.Errorf("presence_penalty = %v, want omitted", *req.PresencePenalty)
				}
			} else if req.PresencePenalty == nil || *req.PresencePenalty != *c.wantPresence {
				t.Errorf("presence_penalty = %v, want %v", req.PresencePenalty, *c.wantPresence)
			}
			if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
				t.Errorf("stream_options.include_usage not set")
			}
		})
	}

	t.Run("explicit per-call temperature wins", func(t *testing.T) {
		temp := 0.3
		req := &chatRequest{Model: "m", Temperature: &temp, Stream: true, temperatureExplicit: true}
		base("disabled", "").ConfigureRequest(req)
		if req.Temperature == nil || *req.Temperature != 0.3 {
			t.Errorf("explicit temperature = %v, want 0.3", req.Temperature)
		}
		// top_p / presence_penalty still follow the thinking state.
		if req.TopP != 0.80 {
			t.Errorf("top_p = %v, want 0.80", req.TopP)
		}
		if req.PresencePenalty == nil || *req.PresencePenalty != 1.5 {
			t.Errorf("presence_penalty = %v, want 1.5", req.PresencePenalty)
		}
	})

	t.Run("configured sampling overrides the per-state defaults", func(t *testing.T) {
		p := NewSGLangProvider("http://h", "k", "m", 1024, 8192, 0.5, 0.7, "disabled", "")
		req := &chatRequest{Model: "m", Stream: true}
		p.ConfigureRequest(req)
		if req.Temperature == nil || *req.Temperature != 0.5 {
			t.Errorf("configured temperature ignored: %v", req.Temperature)
		}
		if req.TopP != 0.7 {
			t.Errorf("configured top_p ignored: %v", req.TopP)
		}
		// The thinking-state presence penalty still applies.
		if req.PresencePenalty == nil || *req.PresencePenalty != 1.5 {
			t.Errorf("presence_penalty = %v, want 1.5", req.PresencePenalty)
		}
		// An unset top_k (0 in the config) is never sent.
		if req.TopK != nil {
			t.Errorf("top_k = %d, want omitted", *req.TopK)
		}
	})
}

func ptr(f float64) *float64 { return &f }
