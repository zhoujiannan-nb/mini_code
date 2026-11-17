package util

import (
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strings"

	_ "golang.org/x/image/webp"
)


const (
	imagePatchFactor = 28
	imageMinPixels   = 3136
	imageMaxPixels   = 12845056

	fallbackImageTokens = 16384

	maxImageHeaderBytes = 1 << 20
)

func imageTokensFromDataURL(dataURL string) int {
	payload, ok := base64PayloadOf(dataURL)
	if !ok {
		return fallbackImageTokens
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload))
	header := io.LimitReader(decoder, maxImageHeaderBytes)
	cfg, _, err := image.DecodeConfig(header)
	if err != nil {
		return fallbackImageTokens
	}
	return qwenVLImageTokens(cfg.Width, cfg.Height)
}

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
