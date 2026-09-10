package util

import (
	"encoding/base64"
	"image"
	_ "image/gif" // register GIF decoder for image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strings"

	_ "golang.org/x/image/webp" // register WebP decoder
)

// Qwen2-VL family image-token estimation.
//
// These VLMs turn an image into visual tokens on a dynamic-resolution grid:
// the image is resized so both dimensions are multiples of imagePatchFactor
// (patch_size 14 * merge_size 2 = 28) and the total pixel count stays within
// [imageMinPixels, imageMaxPixels]; the resulting token count is
//
//	tokens = (H'/28) * (W'/28)  ==  resizedPixels / 784
//
// IMPORTANT (context safety): everything in this file is a PURE, read-only
// estimate used solely for context-window budgeting (the compaction decision
// in session/compaction.go). It never mutates any provider.Message, so it
// cannot alter or corrupt the context sequence that is actually sent to the
// model. The only observable effect of a more accurate (higher) count is that
// compaction may trigger a little earlier, which is the intended, safe
// behavior.

const (
	imagePatchFactor = 28       // patch_size(14) * merge_size(2)
	imageMinPixels   = 3136     // 56*56     -> 4 tokens     (Qwen2-VL default)
	imageMaxPixels   = 12845056 // 16384*784 -> 16384 tokens (Qwen2-VL default)

	// fallbackImageTokens is returned when the image dimensions cannot be
	// determined (not a data URL, malformed base64, unsupported/undecodable
	// format, ...). It is set to the maximum per-image token count so the
	// estimate never under-counts: under-counting is the dangerous case,
	// because it can let a request exceed the model's context window. The
	// cost of over-counting (small or undecodable images) is only that
	// compaction may trigger a little earlier, which is safe.
	fallbackImageTokens = 16384

	// maxImageHeaderBytes bounds how much of the base64-decoded image we read
	// to learn its dimensions. image.DecodeConfig only needs the header: a
	// few bytes for PNG/GIF/WebP, up to ~100KB for a JPEG carrying a large
	// EXIF block. 1MB is a safe ceiling that avoids ever materializing the
	// full (potentially multi-MB) image in memory.
	maxImageHeaderBytes = 1 << 20
)

// imageTokensFromDataURL estimates the visual-token count the Qwen2-VL image
// processor will produce for the image carried by a data URL
// (data:image/...;base64,...). It is a pure function: it only reads the
// string and returns an int, mutating nothing. On any problem it returns
// fallbackImageTokens (a safe over-estimate) rather than panicking.
func imageTokensFromDataURL(dataURL string) int {
	payload, ok := base64PayloadOf(dataURL)
	if !ok {
		return fallbackImageTokens
	}
	// Decode base64 lazily and cap the read at the header size, so we never
	// allocate the full image — only up to maxImageHeaderBytes of it.
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload))
	header := io.LimitReader(decoder, maxImageHeaderBytes)
	cfg, _, err := image.DecodeConfig(header)
	if err != nil {
		return fallbackImageTokens
	}
	return qwenVLImageTokens(cfg.Width, cfg.Height)
}

// base64PayloadOf returns the base64 payload of a well-formed base64 data URL
// and true; otherwise "" and false. It only inspects the string (the returned
// payload is a subslice that shares the input's backing array — no copy).
func base64PayloadOf(dataURL string) (string, bool) {
	const prefix = "data:"
	if !strings.HasPrefix(dataURL, prefix) {
		return "", false
	}
	const delim = ";base64,"
	idx := strings.Index(dataURL, delim)
	if idx < 0 {
		return "", false
	}
	payload := dataURL[idx+len(delim):]
	if payload == "" {
		return "", false
	}
	return payload, true
}

// qwenVLImageTokens computes the visual-token count for an image of the given
// pixel dimensions, mirroring the Qwen2-VL smart_resize + patch/merge math.
func qwenVLImageTokens(width, height int) int {
	if width <= 0 || height <= 0 {
		return fallbackImageTokens
	}
	pw, ph := float64(width), float64(height)
	pixels := pw * ph
	switch {
	case pixels > imageMaxPixels:
		scale := math.Sqrt(float64(imageMaxPixels) / pixels)
		pw = math.Floor(pw*scale/imagePatchFactor) * imagePatchFactor
		ph = math.Floor(ph*scale/imagePatchFactor) * imagePatchFactor
	case pixels < imageMinPixels:
		scale := math.Sqrt(float64(imageMinPixels) / pixels)
		pw = math.Ceil(pw*scale/imagePatchFactor) * imagePatchFactor
		ph = math.Ceil(ph*scale/imagePatchFactor) * imagePatchFactor
	default:
		pw = math.Floor(pw/imagePatchFactor) * imagePatchFactor
		ph = math.Floor(ph/imagePatchFactor) * imagePatchFactor
	}
	tw := int(pw / imagePatchFactor)
	th := int(ph / imagePatchFactor)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	return tw * th
}
