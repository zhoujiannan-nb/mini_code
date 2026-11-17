package provider

import "testing"

func TestSGLangConfigureRequest(t *testing.T) {
	base := func(thinking, effort string) *SGLangProvider {
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
		wantThinking string
		wantEffort   string
		wantEnableThinking string
		wantTemp           float64
		wantTopP           float64
		wantPresence       *float64
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
		if req.PresencePenalty == nil || *req.PresencePenalty != 1.5 {
			t.Errorf("presence_penalty = %v, want 1.5", req.PresencePenalty)
		}
		if req.TopK != nil {
			t.Errorf("top_k = %d, want omitted", *req.TopK)
		}
	})
}

func ptr(f float64) *float64 { return &f }
