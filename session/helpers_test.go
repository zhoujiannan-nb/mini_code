package session

import (
	"strings"
	"sync"
	"testing"
)

type deliveryRecorder struct {
	mu sync.Mutex
	dr []string
}

func (r *deliveryRecorder) record(id, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dr = append(r.dr, id+"\t"+text)
}

func (r *deliveryRecorder) forSession(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.dr {
		if strings.SplitN(e, "\t", 2)[0] == id {
			out = append(out, strings.SplitN(e, "\t", 2)[1])
		}
	}
	return out
}

func newDeliveryEnv(t *testing.T) (*SessionManager, *deliveryRecorder) {
	t.Helper()
	dbPath := t.TempDir() + "/agent.db"
	mgr, err := NewSessionManager(nil, dbPath)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	rec := &deliveryRecorder{}
	mgr.SetDeliverHook(rec.record)
	return mgr, rec
}

func mustSession(t *testing.T, mgr *SessionManager, title string) *Session {
	t.Helper()
	s, err := mgr.CreateSessionRecord(title, "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	return s
}
