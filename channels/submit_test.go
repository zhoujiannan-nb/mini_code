package channels

import (
	"strings"
	"testing"
	"time"
)

func TestSubmitRejectsInsteadOfDropping(t *testing.T) {
	h := NewHub(nil, "", "", 0)

	filled := 0
	for filled < cap(h.in) {
		if err := h.Submit(NewUserMessage("web", "fill")); err != nil {
			t.Fatalf("submit %d of a non-full queue failed: %v", filled, err)
		}
		filled++
	}
	if filled != cap(h.in) {
		t.Fatalf("filled %d of %d slots", filled, cap(h.in))
	}

	start := time.Now()
	err := h.Submit(NewUserMessage("web", "the message that would have been lost"))
	waited := time.Since(start)
	if err == nil {
		t.Fatal("a full queue accepted the message: it would have been dropped silently")
	}
	if waited < hubSubmitWait {
		t.Fatalf("rejected after %s, want it to wait for a slot for %s first", waited, hubSubmitWait)
	}
	if !strings.Contains(err.Error(), "retry") {
		t.Fatalf("error is not actionable: %q", err.Error())
	}
}

func TestSubmitAcceptsWhenDrained(t *testing.T) {
	h := NewHub(nil, "", "", 0)
	for i := 0; i < cap(h.in); i++ {
		if err := h.Submit(NewUserMessage("web", "fill")); err != nil {
			t.Fatalf("fill: %v", err)
		}
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		<-h.in
	}()
	if err := h.Submit(NewUserMessage("web", "accepted")); err != nil {
		t.Fatalf("a slot freed within the wait window was not used: %v", err)
	}
}
