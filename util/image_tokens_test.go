package util

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"testing"

	"github.com/user/mini_code/provider"
)

func TestQwenVLImageTokens(t *testing.T) {
	cases := []struct {
		name     string
		width    int
		height   int
		expected int
	}{
		{"tiny 1x1 scaled to min", 1, 1, 4},
		{"small 100x100", 100, 100, 9},
		{"square 1024", 1024, 1024, 1296},
		{"hd 1920x1080", 1920, 1080, 2584},
		{"readimg cap 2048", 2048, 2048, 5329},
		{"large 4000x3000", 4000, 3000, 15194},
		{"huge 6000x4000 capped", 6000, 4000, 16224},
		{"zero width", 0, 100, fallbackImageTokens},
		{"negative height", 100, -5, fallbackImageTokens},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := qwenVLImageTokens(c.width, c.height); got != c.expected {
				t.Errorf("qwenVLImageTokens(%d,%d) = %d, want %d", c.width, c.height, got, c.expected)
			}
		})
	}
}

func makePNGDataURL(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestImageTokensFromDataURLRealPNG(t *testing.T) {
	for _, dim := range []struct{ w, h int }{{100, 100}, {1024, 1024}, {1920, 1080}} {
		url := makePNGDataURL(t, dim.w, dim.h)
		got := imageTokensFromDataURL(url)
		want := qwenVLImageTokens(dim.w, dim.h)
		if got != want {
			t.Errorf("imageTokensFromDataURL(%dx%d png) = %d, want %d", dim.w, dim.h, got, want)
		}
	}
}

func TestImageTokensFromDataURLIdempotent(t *testing.T) {
	url := makePNGDataURL(t, 640, 480)
	first := imageTokensFromDataURL(url)
	for i := 0; i < 5; i++ {
		if got := imageTokensFromDataURL(url); got != first {
			t.Fatalf("call %d = %d, want stable %d", i+1, got, first)
		}
	}
}

func TestImageTokensFromDataURLFallbacks(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"not a data url": "http://example.com/img.png",
		"no base64":     "data:image/png,notbase64",
		"empty payload": "data:image/png;base64,",
		"bad base64":    "data:image/png;base64,@@@!!!",
		"not an image": "data:image/png;base64," +
			base64.StdEncoding.EncodeToString([]byte("definitely not an image")),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got := imageTokensFromDataURL(in); got != fallbackImageTokens {
				t.Errorf("imageTokensFromDataURL(%q) = %d, want fallback %d", in, got, fallbackImageTokens)
			}
		})
	}
}

func TestCountMessagesTokensDoesNotMutate(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user"},
	}
	msgs[1].SetMultimodalContent([]provider.ContentPart{
		provider.NewTextPart("look at this"),
		provider.NewImagePart(makePNGDataURL(t, 800, 600)),
	})

	before, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}
	for i := 0; i < 3; i++ {
		_ = CountMessagesTokens(msgs)
	}
	after, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("CountMessagesTokens mutated its input (context corruption!):\nbefore=%s\nafter=%s", before, after)
	}
}

func TestBase64PayloadOf(t *testing.T) {
	if p, ok := base64PayloadOf("data:image/png;base64,AAAA"); !ok || p != "AAAA" {
		t.Errorf("valid data url: got (%q,%v), want (AAAA,true)", p, ok)
	}
	if _, ok := base64PayloadOf("http://x/y.png"); ok {
		t.Error("plain url should not parse as data url")
	}
	if _, ok := base64PayloadOf("data:image/png;base64,"); ok {
		t.Error("empty payload should not parse")
	}
	if _, ok := base64PayloadOf("data:image/png,xxxx"); ok {
		t.Error("non-base64 data url should not parse")
	}
}
