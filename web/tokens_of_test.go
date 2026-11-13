package web

import "testing"

// tokensOf sums metadata.usage_total across the JSON round-trip (float64)
// and tolerates sessions without a counter.
func TestTokensOf(t *testing.T) {
	m := map[string]interface{}{
		"usage_total": map[string]interface{}{"prompt": float64(1200000), "completion": float64(34000)},
	}
	if got := tokensOf(m); got != 1234000 {
		t.Errorf("tokensOf = %d, want 1234000", got)
	}
	if got := tokensOf(map[string]interface{}{}); got != 0 {
		t.Errorf("tokensOf(empty) = %d, want 0", got)
	}
	if got := tokensOf(map[string]interface{}{"usage_total": "garbage"}); got != 0 {
		t.Errorf("tokensOf(garbage) = %d, want 0", got)
	}
}
