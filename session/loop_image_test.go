package session

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/user/mini_code/provider"
)

// TestBuildUserMessagePlain verifies that without images the initial user
// message stays a plain text message (legacy behavior, e.g. DingTalk).
func TestBuildUserMessagePlain(t *testing.T) {
	l := &AgentLoop{}
	m := l.buildUserMessage("hello")
	if m.Role != "user" || m.Content != "hello" || len(m.ContentParts) != 0 {
		t.Fatalf("expected plain text message, got %+v", m)
	}
}

// TestBuildUserMessageWithImages verifies text + image parts in order.
func TestBuildUserMessageWithImages(t *testing.T) {
	l := &AgentLoop{}
	l.SetUserImages([]string{"data:image/png;base64,AAA", "data:image/jpeg;base64,BBB"})
	m := l.buildUserMessage("look")
	if len(m.ContentParts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(m.ContentParts))
	}
	if m.ContentParts[0].Type != "text" || m.ContentParts[0].Text != "look" {
		t.Fatalf("first part should be the text, got %+v", m.ContentParts[0])
	}
	if m.ContentParts[1].Type != "image_url" || m.ContentParts[1].ImageURL.URL != "data:image/png;base64,AAA" {
		t.Fatalf("second part should be the first image, got %+v", m.ContentParts[1])
	}
	if m.ContentParts[2].Type != "image_url" || m.ContentParts[2].ImageURL.URL != "data:image/jpeg;base64,BBB" {
		t.Fatalf("third part should be the second image, got %+v", m.ContentParts[2])
	}
	if m.GetText() != "look" {
		t.Fatalf("GetText should return the text part, got %q", m.GetText())
	}
}

// TestBuildUserMessageImagesOnly verifies an image-only message (no text).
func TestBuildUserMessageImagesOnly(t *testing.T) {
	l := &AgentLoop{}
	l.SetUserImages([]string{"data:image/png;base64,AAA"})
	m := l.buildUserMessage("")
	if len(m.ContentParts) != 1 || m.ContentParts[0].Type != "image_url" {
		t.Fatalf("expected a single image part, got %+v", m.ContentParts)
	}
}

func multimodalUser(t *testing.T, text, img string) provider.Message {
	t.Helper()
	m := provider.Message{Role: "user"}
	m.SetMultimodalContent([]provider.ContentPart{
		provider.NewTextPart(text),
		provider.NewImagePart(img),
	})
	return m
}

// TestUserMessageMatches covers the dedup predicate: identical messages
// match; same text with/without (or with different) images must not.
func TestUserMessageMatches(t *testing.T) {
	plain := provider.Message{Role: "user", Content: "hi"}
	plain2 := provider.Message{Role: "user", Content: "hi"}
	imgA := multimodalUser(t, "hi", "data:image/png;base64,AAA")
	imgB := multimodalUser(t, "hi", "data:image/png;base64,BBB")
	imgACopy := multimodalUser(t, "hi", "data:image/png;base64,AAA")

	if !userMessageMatches(plain, plain2) {
		t.Fatal("identical plain messages should match")
	}
	if userMessageMatches(plain, imgA) {
		t.Fatal("plain and image messages with the same text must not match")
	}
	if userMessageMatches(imgA, imgB) {
		t.Fatal("messages with different images must not match")
	}
	if !userMessageMatches(imgA, imgACopy) {
		t.Fatal("identical multimodal messages should match")
	}
	assistant := provider.Message{Role: "assistant", Content: "hi"}
	if userMessageMatches(plain, assistant) {
		t.Fatal("non-user roles must not match")
	}
}

// TestRunAttachesImagesToUserMessage verifies the request actually sent to
// the model carries the initial user message as multimodal parts.
func TestRunAttachesImagesToUserMessage(t *testing.T) {
	var captured struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEChunk(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.SetUserImages([]string{"data:image/png;base64,AAA"})

	res, err := loop.Run(context.Background(), "system", "look at this", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}

	// system + multimodal user.
	if len(captured.Messages) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(captured.Messages))
	}
	parts, ok := captured.Messages[1]["content"].([]interface{})
	if !ok {
		t.Fatalf("user content should be an array of parts, got %T", captured.Messages[1]["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts (text + image), got %d", len(parts))
	}
	p0, _ := parts[0].(map[string]interface{})
	if p0["type"] != "text" || p0["text"] != "look at this" {
		t.Fatalf("first part should be the text, got %v", p0)
	}
	p1, _ := parts[1].(map[string]interface{})
	if p1["type"] != "image_url" {
		t.Fatalf("second part should be image_url, got %v", p1)
	}
}

// TestRunDoesNotReAppendSameMultimodalUserMessage verifies interrupt-recovery
// dedup: when the history already ends with the identical multimodal user
// message, it is not appended twice.
func TestRunDoesNotReAppendSameMultimodalUserMessage(t *testing.T) {
	var userCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured struct {
			Messages []map[string]interface{} `json:"messages"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		for _, m := range captured.Messages {
			if m["role"] == "user" {
				userCount++
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEChunk(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.SetUserImages([]string{"data:image/png;base64,AAA"})

	history := []provider.Message{
		{Role: "system", Content: "system"},
		multimodalUser(t, "look at this", "data:image/png;base64,AAA"),
	}

	if _, err := loop.Run(context.Background(), "system", "look at this", history); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("expected the user message to appear once (dedup), got %d", userCount)
	}
}

// TestRunReAppendsWhenImagesDiffer verifies the inverse: history ends with
// the same text as a plain message, but this run carries images — the
// multimodal message must still be appended (images would be lost otherwise).
func TestRunReAppendsWhenImagesDiffer(t *testing.T) {
	var userCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured struct {
			Messages []map[string]interface{} `json:"messages"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		for _, m := range captured.Messages {
			if m["role"] == "user" {
				userCount++
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEChunk(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.SetUserImages([]string{"data:image/png;base64,AAA"})

	history := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "look at this"}, // same text, no images
	}

	if _, err := loop.Run(context.Background(), "system", "look at this", history); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if userCount != 2 {
		t.Fatalf("expected the multimodal user message to be appended (2 user messages), got %d", userCount)
	}
}
