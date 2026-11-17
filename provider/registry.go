package provider

import (
	"sync"
)

type ModelRegistry struct {
	mu      sync.RWMutex
	clients map[string]*ModelClient
	defName string
}

func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{clients: make(map[string]*ModelClient)}
}

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

func (r *ModelRegistry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.clients[name]
	return ok
}

func (r *ModelRegistry) ContextWindow(name string) int {
	c := r.Client(name)
	if c == nil {
		return 0
	}
	return c.GetContextWindow()
}
