package web

import (
	"context"
	"testing"

	"github.com/user/mini_code/channels"
)

// childrenRunning must read "running" for a session that itself has no live
// task but has one in (transitively) any of its sub-agent sessions.
func TestChildrenRunning(t *testing.T) {
	hub := channels.NewHub(nil, "", "", 0)
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub.RegisterExternalTask("grandchild", cancel)

	children := map[string][]string{
		"root":  {"child"},
		"child": {"grandchild"},
	}
	if !childrenRunning(hub, "root", children) {
		t.Fatal("root should read as running: descendant grandchild has a live task")
	}
	if childrenRunning(hub, "idle-root", children) {
		t.Fatal("root without live descendants should not read as running")
	}

	// A task removed from the registry (background job finished) must not count.
	_, cancel2 := context.WithCancel(context.Background())
	hub.RegisterExternalTask("done-child", cancel2)
	if !hub.ActiveTask("done-child") {
		t.Fatal("setup: expected done-child to be active before unregistering")
	}
	hub.UnregisterExternalTask("done-child")
	if childrenRunning(hub, "done-root", map[string][]string{"done-root": {"done-child"}}) {
		t.Fatal("deregistered task should not count as running")
	}
}
