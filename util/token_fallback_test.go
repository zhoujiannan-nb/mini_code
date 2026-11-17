package util

import (
	"strings"
	"testing"
)

func TestEstimateTextFallbackIsCJKAware(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		minMult float64
		maxMult float64
	}{
		{"ascii", strings.Repeat("a", 400), 0.20, 0.26},
		{"chinese", strings.Repeat("中", 400), 1.4, 1.6},
		{"mixed", strings.Repeat("a中", 200), 0.7, 1.0},
		{"code", "func main() {\n\tfmt.Println(\"hi\")\n}\n", 0.20, 0.60},
	}
	for _, c := range cases {
		runes := len([]rune(c.text))
		got := EstimateTextFallback(c.text)
		perRune := float64(got) / float64(runes)
		if perRune < c.minMult || perRune > c.maxMult {
			t.Fatalf("%s: %d tokens for %d runes = %.2f/char, want between %.2f and %.2f",
				c.name, got, runes, perRune, c.minMult, c.maxMult)
		}
	}
}

func TestEstimateTextFallbackNeverUnderCountsChinese(t *testing.T) {
	text := strings.Repeat("上下文压缩会消耗很多 token。", 50)
	naiveBytes := len(text) / 4
	if got := EstimateTextFallback(text); got <= naiveBytes {
		t.Fatalf("fallback %d <= old byte estimate %d; the CJK fix is not in effect", got, naiveBytes)
	}
}

func TestEstimateTextFallbackEmpty(t *testing.T) {
	if got := EstimateTextFallback(""); got != 0 {
		t.Fatalf("empty string = %d tokens, want 0", got)
	}
}
