package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/user/mini_code/approval"
	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/session"
)

type fakeBackend struct {
	hub *channels.Hub
	mgr *session.SessionManager
	ap  *approval.Service
	cfg *config.AppConfig
}

func (f *fakeBackend) Hub() *channels.Hub           { return f.hub }
func (f *fakeBackend) Mgr() *session.SessionManager { return f.mgr }
func (f *fakeBackend) Approvals() *approval.Service { return f.ap }
func (f *fakeBackend) Status() StatusInfo           { return StatusInfo{} }
func (f *fakeBackend) GetConfig() *config.AppConfig {
	if f.cfg != nil {
		return f.cfg
	}
	return &config.AppConfig{Model: config.ModelConfig{Provider: "vllm", ModelName: "test"}}
}
func (f *fakeBackend) SaveConfig(cfg *config.AppConfig) (*SaveResult, error) {
	return &SaveResult{}, nil
}

func TestSendMessageWithImagesE2E(t *testing.T) {
	var mu sync.Mutex
	var userContents []interface{}
	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []map[string]interface{} `json:"messages"`
		}
		if err := json.Unmarshal(b, &req); err == nil {
			mu.Lock()
			for _, m := range req.Messages {
				if m["role"] == "user" {
					userContents = append(userContents, m["content"])
				}
			}
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer modelSrv.Close()

	client, err := provider.NewModelClient(config.ModelConfig{
		Provider:      "vllm",
		BaseURL:       modelSrv.URL,
		APIKey:        "test",
		ModelName:     "test-model",
		MaxTokens:     100,
		ContextWindow: 4096,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}

	mgr, err := session.NewSessionManager(client, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	defer mgr.Close()

	hub := channels.NewHub(mgr, "build", t.TempDir(), 5)
	hub.Attach(mgr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	api := httptest.NewServer(NewServer(&fakeBackend{hub: hub, mgr: mgr, ap: approval.NewService(hub, mgr)}))
	defer api.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"content": "look at this",
		"images":  []string{"data:image/png;base64,QUJD"},
	})
	resp, err := http.Post(api.URL+"/api/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/messages: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, b)
	}

	hasMultimodal := func(contents []interface{}) bool {
		for _, content := range contents {
			parts, ok := content.([]interface{})
			if !ok || len(parts) != 2 {
				continue
			}
			p0, _ := parts[0].(map[string]interface{})
			p1, _ := parts[1].(map[string]interface{})
			if p0["type"] == "text" && p0["text"] == "look at this" && p1["type"] == "image_url" {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		contents := append([]interface{}(nil), userContents...)
		mu.Unlock()
		if hasMultimodal(contents) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("model never received the multimodal user message; user contents: %#v", contents)
		}
		time.Sleep(100 * time.Millisecond)
	}

	deadline = time.Now().Add(15 * time.Second)
	for {
		recs, err := mgr.GetStore().ListSessionSummaries(10, 0)
		if err != nil || len(recs) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("session not persisted: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		sess, err := mgr.Get(recs[0].SessionID)
		if err == nil && sess != nil {
			for _, m := range sess.Record().Messages {
				if m.Role != "user" {
					continue
				}
				for _, p := range m.ContentParts {
					if p.Type == "image_url" && p.ImageURL != nil && p.ImageURL.URL == "data:image/png;base64,QUJD" {
						return
					}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("image not persisted in the session record")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestSendMessageRejectsBadImages(t *testing.T) {
	api := httptest.NewServer(NewServer(&fakeBackend{}))
	defer api.Close()

	cases := []struct {
		name string
		body map[string]interface{}
		want int
	}{
		{"empty content and no images", map[string]interface{}{"content": ""}, http.StatusBadRequest},
		{"image without data prefix", map[string]interface{}{"content": "x", "images": []string{"http://evil/img.png"}}, http.StatusBadRequest},
		{"too many images", map[string]interface{}{"content": "x", "images": []string{
			"data:image/png;base64,A", "data:image/png;base64,B",
			"data:image/png;base64,C", "data:image/png;base64,D",
			"data:image/png;base64,E",
		}}, http.StatusBadRequest},
		{"oversized image", map[string]interface{}{"content": "x", "images": []string{
			"data:image/png;base64," + string(make([]byte, maxImageBytes)),
		}}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		b, _ := json.Marshal(tc.body)
		resp, err := http.Post(api.URL+"/api/messages", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: POST failed: %v", tc.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: expected %d, got %d", tc.name, tc.want, resp.StatusCode)
		}
	}
}
