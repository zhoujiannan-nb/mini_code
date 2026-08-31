package provider

import (
	"sort"
	"sync"
)

// ModelRegistry maps model names (config keys) to model clients, with a
// default fallback. Sessions reference a model by name; an empty or unknown
// name resolves to the default model's client. The whole registry can be
// hot-replaced when the config is saved (ModelClient is stateless, so old
// clients kept by in-flight tasks remain usable).
type ModelRegistry struct {
	mu      sync.RWMutex
	clients map[string]*ModelClient
	defName string
}

func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{clients: make(map[string]*ModelClient)}
}

// Set replaces the entire registry (startup / config save). If defName is
// not among the clients, the first available name becomes the default.
func (r *ModelRegistry) Set(clients map[string]*ModelClient, defName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients = clients
	if len(clients) == 0 {
		r.defName = ""
		return
	}
	if _, ok := clients[defName]; !ok {
		defName = ""
		for n := range clients {
			defName = n
			break
		}
	}
	r.defName = defName
}

// Client returns the client for the given model name. An empty or unknown
// name falls back to the default model's client (nil when the registry is
// empty, e.g. in tests that inject prompt functions).
func (r *ModelRegistry) Client(name string) *ModelClient {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name != "" {
		if c, ok := r.clients[name]; ok {
			return c
		}
	}
	return r.clients[r.defName]
}

// Has reports whether the registry contains a model with the given name.
func (r *ModelRegistry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.clients[name]
	return ok
}

// DefaultName returns the default model's name ("" when empty).
func (r *ModelRegistry) DefaultName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defName
}

// Names returns the configured model names, sorted.
func (r *ModelRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.clients))
	for n := range r.clients {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ContextWindow returns the context window of the given model (default
// model for empty/unknown names); 0 when no client resolves.
func (r *ModelRegistry) ContextWindow(name string) int {
	c := r.Client(name)
	if c == nil {
		return 0
	}
	return c.GetContextWindow()
}
