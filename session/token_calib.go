package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/user/mini_code/provider"
)

// tokenCalibrator corrects the local token estimate (util.CountMessagesTokens,
// cl100k-based) onto the provider's own token scale using the provider-reported
// prompt_tokens of past requests.
//
// The local estimate is a systematic proxy, not the truth: cl100k is a GPT
// tokenizer, the model under test (Qwen, DeepSeek, ...) uses its own, and the
// rendered request also carries chat-template and tool-schema overhead the
// estimate does not count. Both effects are (nearly) linear in the estimate,
// so the calibrator fits a two-parameter model
//
//	corrected = slope * estimate + intercept
//
// to the recent (estimate, reported prompt_tokens) observations with closed
// form least squares (normal equations over the last calibWindow samples,
// refit on every new observation). The slope captures the tokenizer ratio;
// the intercept captures the fixed template/schema overhead.
//
// Closed form over a bounded window is chosen over recursive updates
// (RLS): no covariance state that can degenerate numerically, old samples
// slide out on their own, and the fit always reflects the context range the
// session actually moves through. When the window carries no leverage in the
// estimate (all samples at (nearly) the same size) the fit falls back to a
// pure ratio (intercept 0), which is the best the data can say.
type tokenCalibrator struct {
	mu      sync.Mutex
	samples []calibSample
	slope   float64 // cached fit: corrected = slope*x + intercept
	intc    float64
	fitted  bool
}

type calibSample struct{ x, y float64 }

// calibWindow bounds how many (estimate, reported) observations feed the fit.
const calibWindow = 32

func newTokenCalibrator() *tokenCalibrator {
	return &tokenCalibrator{}
}

// update folds one (estimate, reported prompt_tokens) observation into the
// window and refits. Zero/negative values (provider did not report usage)
// are ignored.
func (c *tokenCalibrator) update(estimate, reported int) {
	if c == nil || estimate <= 0 || reported <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, calibSample{x: float64(estimate), y: float64(reported)})
	if len(c.samples) > calibWindow {
		c.samples = c.samples[len(c.samples)-calibWindow:]
	}
	c.refit()
}

// refit re-solves the two-parameter fit from the current window. The caller
// holds c.mu.
func (c *tokenCalibrator) refit() {
	n := len(c.samples)
	if n == 0 {
		c.fitted = false
		c.slope, c.intc = 0, 0
		return
	}
	if n == 1 {
		s := c.samples[0]
		// One sample fixes one degree of freedom: anchor a pure ratio there.
		c.slope, c.intc, c.fitted = s.y/s.x, 0, true
		return
	}
	// Normal equations for y = a + b*x:
	//   n*a + sx*b  = sy
	//   sx*a + sxx*b = sxy
	// solved by Cramer's rule.
	var sx, sxx, sy, sxy float64
	for _, s := range c.samples {
		sx += s.x
		sxx += s.x * s.x
		sy += s.y
		sxy += s.x * s.y
	}
	det := float64(n)*sxx - sx*sx // = n * Σ(x - x̄)² ≥ 0
	if det < 1e-6*float64(n)*sxx {
		// No leverage in the estimate: all samples sit at (nearly) the same
		// context size, so scale and offset are indistinguishable. Use the
		// mean ratio with zero offset.
		c.slope, c.intc = sy/sx, 0
	} else {
		c.intc = (sxx*sy - sx*sxy) / det
		c.slope = (float64(n)*sxy - sx*sy) / det
	}
	c.fitted = true
}

// apply returns the provider-scale token count for a local estimate. With no
// observations yet it returns the raw estimate unchanged.
func (c *tokenCalibrator) apply(estimate int) int {
	if c == nil || estimate <= 0 {
		return estimate
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.fitted {
		return estimate
	}
	v := c.slope*float64(estimate) + c.intc
	if v < 0 {
		v = 0
	}
	// Safety valve: never trust a divergent fit more than 4x the raw estimate.
	if max := float64(estimate) * 4; v > max {
		v = max
	}
	return int(v + 0.5)
}

// calibratorRegistry shares one calibrator per (model, toolset) across tasks
// and sessions, so a new task immediately benefits from what earlier tasks
// observed from the same model. The toolset is part of the key because the
// tool-schema overhead (the intercept) differs between toolsets.
var calibratorRegistry sync.Map // key string -> *tokenCalibrator

func calibratorFor(model string, defs []provider.ToolSchema) *tokenCalibrator {
	key := model
	if b, err := json.Marshal(defs); err == nil {
		// encoding/json sorts map keys, so the serialization is deterministic.
		sum := sha256.Sum256(b)
		key += "\x00" + hex.EncodeToString(sum[:8])
	}
	v, _ := calibratorRegistry.LoadOrStore(key, newTokenCalibrator())
	return v.(*tokenCalibrator)
}
