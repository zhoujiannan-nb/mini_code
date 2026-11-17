package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCatalogueDeduplicatesAliasBuckets(t *testing.T) {
	pluginHome(t)

	one := `{"id":"mc-test","name":"测试动画","version":"1.0.0","path":"boot-anim/mc-test",
	         "category":"appearance",
	         "files":[{"path":"boot-anim/mc-test/plugin.json","size":2,"sha256":"00"}]}`
	idx := `{"schema":1,"repo":"x/y","ref":"main","kinds":{
	          "appearance": [` + one + `],
	          "boot-anim":  [` + strings.Replace(one, `,"category":"appearance"`, "", 1) + `]
	        }}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(idx))
	}))
	defer srv.Close()

	old := bootAnimGitHubBase
	bootAnimGitHubBase = srv.URL
	defer func() { bootAnimGitHubBase = old }()
	pluginCatalogueCache.mu.Lock()
	pluginCatalogueCache.key, pluginCatalogueCache.idx = "", nil
	pluginCatalogueCache.at, pluginCatalogueCache.err = time.Time{}, nil
	pluginCatalogueCache.mu.Unlock()

	s := httptest.NewServer(NewServer(newTestBackend(t)))
	defer s.Close()

	code, body := doJSON(t, "GET", s.URL+"/api/plugins/catalog", "")
	if code != 200 {
		t.Fatalf("catalog status = %d\n%s", code, body)
	}
	var payload struct {
		Entries []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Category string `json:"category"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("payload: %v\n%s", err, body)
	}
	var ids []string
	for _, e := range payload.Entries {
		ids = append(ids, e.ID)
	}
	if len(ids) != 1 {
		t.Fatalf("the same plugin in two buckets became %d cards: %v\n%s",
			len(ids), ids, body)
	}
	if got := payload.Entries[0]; got.ID != "mc-test" || got.Category != "appearance" {
		t.Errorf("kept the wrong entry: %+v", got)
	}
	if kind := payload.Entries[0].Kind; kind != "appearance" {
		t.Errorf("kept the alias bucket %q, want appearance (buckets are sorted, "+
			"so appearance wins)", kind)
	}
}

func TestCatalogueKeepsDistinctPluginsAcrossBuckets(t *testing.T) {
	pluginHome(t)

	idx := `{"schema":1,"repo":"x/y","ref":"main","kinds":{
	          "appearance":[{"id":"skin-a","name":"A","version":"1.0.0","path":"skin/skin-a",
	                         "files":[{"path":"skin/skin-a/plugin.json","size":2,"sha256":"00"}]}],
	          "tool":[{"id":"web-b","name":"B","version":"1.0.0","path":"tool/web-b",
	                   "files":[{"path":"tool/web-b/plugin.json","size":2,"sha256":"00"}]}]
	        }}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(idx))
	}))
	defer srv.Close()

	old := bootAnimGitHubBase
	bootAnimGitHubBase = srv.URL
	defer func() { bootAnimGitHubBase = old }()
	pluginCatalogueCache.mu.Lock()
	pluginCatalogueCache.key, pluginCatalogueCache.idx = "", nil
	pluginCatalogueCache.at, pluginCatalogueCache.err = time.Time{}, nil
	pluginCatalogueCache.mu.Unlock()

	s := httptest.NewServer(NewServer(newTestBackend(t)))
	defer s.Close()

	_, body := doJSON(t, "GET", s.URL+"/api/plugins/catalog", "")
	for _, want := range []string{"skin-a", "web-b"} {
		if !strings.Contains(body, want) {
			t.Errorf("catalogue lost %q: %s", want, body)
		}
	}
}
