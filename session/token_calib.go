package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/user/mini_code/provider"
)

type tokenCalibrator struct {
	mu      sync.Mutex
	samples []calibSample
	slope   float64
	intc    float64
	fitted  bool
}

type calibSample struct{ x, y float64 }

const calibWindow = 32

func newTokenCalibrator() *tokenCalibrator {
	return &tokenCalibrator{}
}

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

func (c *tokenCalibrator) refit() {
	n := len(c.samples)
	if n == 0 {
		c.fitted = false
		c.slope, c.intc = 0, 0
		return
	}
	if n == 1 {
		s := c.samples[0]
		c.slope, c.intc, c.fitted = s.y/s.x, 0, true
		return
	}
	var sx, sxx, sy, sxy float64
	for _, s := range c.samples {
		sx += s.x
		sxx += s.x * s.x
		sy += s.y
		sxy += s.x * s.y
	}
	det := float64(n)*sxx - sx*sx
	if det < 1e-6*float64(n)*sxx {
		c.slope, c.intc = sy/sx, 0
	} else {
		c.intc = (sxx*sy - sx*sxy) / det
		c.slope = (float64(n)*sxy - sx*sy) / det
	}
	c.fitted = true
}

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
	if max := float64(estimate) * 4; v > max {
		v = max
	}
	return int(v + 0.5)
}

var calibratorRegistry sync.Map

func calibratorFor(model string, defs []provider.ToolSchema) *tokenCalibrator {
	key := model
	if b, err := json.Marshal(defs); err == nil {
		sum := sha256.Sum256(b)
		key += "\x00" + hex.EncodeToString(sum[:8])
	}
	v, _ := calibratorRegistry.LoadOrStore(key, newTokenCalibrator())
	return v.(*tokenCalibrator)
}
