package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/user/mini_code/provider"
	gdraw "golang.org/x/image/draw"
)

var imgSemaphore = make(chan struct{}, 10)

const (
	maxImageDimension = 2048
	targetMaxWidth    = 1920
	targetMaxHeight   = 1080
)

type ReadImgTool struct {
	workspace string
}

func NewReadImgTool(workspace string) *ReadImgTool {
	return &ReadImgTool{workspace: workspace}
}

func (t *ReadImgTool) Name() string { return "readimg" }
func (t *ReadImgTool) Description() string {
	return "Read a local image file and return its base64-encoded content for multimodal model input. Supported formats: JPEG, PNG, WebP, GIF."
}
func (t *ReadImgTool) IsHidden() bool { return false }
func (t *ReadImgTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute local file path of the image",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadImgTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return NewTextResult("Error: missing image path"), nil
	}

	fp := path
	if !filepath.IsAbs(fp) && t.workspace != "" {
		fp = filepath.Join(t.workspace, fp)
	}
	fp = filepath.Clean(fp)

	info, err := os.Stat(fp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: image not found: %s", path)), nil
	}
	if info.IsDir() {
		return NewTextResult(fmt.Sprintf("Error: path is a directory: %s", path)), nil
	}

	imgSemaphore <- struct{}{}
	defer func() { <-imgSemaphore }()

	raw, err := os.ReadFile(fp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error reading image: %s", err)), nil
	}

	mime := detectImgMIME(raw)
	if mime == "" {
		return NewTextResult(fmt.Sprintf("Error: unsupported or unrecognizable image format: %s", path)), nil
	}

	// Resize large images to reduce token consumption
	raw, mime = resizeImageIfNeeded(raw, mime)

	encoded := base64.StdEncoding.EncodeToString(raw)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mime, encoded)

	parts := []provider.ContentPart{
		provider.NewTextPart(fmt.Sprintf("Image loaded from: %s (%dx%d, %s)", path, 0, 0, mime)),
		provider.NewImagePart(dataURL),
	}
	return NewImageResult(parts), nil
}

func resizeImageIfNeeded(raw []byte, mime string) ([]byte, string) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw, mime
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if w <= maxImageDimension && h <= maxImageDimension {
		return raw, mime
	}

	var newW, newH int
	if w >= h {
		newW = targetMaxWidth
		newH = int(float64(h) * float64(targetMaxWidth) / float64(w))
	} else {
		newH = targetMaxHeight
		newW = int(float64(w) * float64(targetMaxHeight) / float64(h))
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	gdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), gdraw.Over, nil)

	var buf bytes.Buffer
	outMime := mime
	switch mime {
	case "image/jpeg":
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85})
	case "image/png":
		err = png.Encode(&buf, dst)
	case "image/gif":
		err = gif.Encode(&buf, dst, nil)
	default:
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85})
		outMime = "image/jpeg"
	}
	if err != nil {
		return raw, mime
	}

	return buf.Bytes(), outMime
}

func detectImgMIME(data []byte) string {
	if len(data) < 4 {
		return ""
	}
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if data[0] == 0x89 && string(data[1:4]) == "PNG" {
		return "image/png"
	}
	if string(data[:4]) == "RIFF" && len(data) > 12 && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return "image/gif"
	}
	fallback := http.DetectContentType(data)
	if strings.HasPrefix(fallback, "image/") {
		return fallback
	}
	return ""
}
