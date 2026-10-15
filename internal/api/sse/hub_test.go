package sse

import (
	"testing"
	"time"
)

func drain(t *testing.T, s *Subscription, want int) []Event {
	t.Helper()
	out := make([]Event, 0, want)
	deadline := time.After(2 * time.Second)
	for len(out) < want {
		select {
		case ev, ok := <-s.C():
			if !ok {
				t.Fatalf("subscription closed early after %d events", len(out))
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("timeout waiting for %d events, got %d", want, len(out))
		}
	}
	return out
}

func TestPublishAssignsMonotonicIDs(t *testing.T) {
	h := NewHub(16)
	a := h.Publish(Event{Type: "a", TenantID: "t1"})
	b := h.Publish(Event{Type: "b", TenantID: "t1"})
	if a.ID != "1" || b.ID != "2" {
		t.Fatalf("ids not monotonic: %q %q", a.ID, b.ID)
	}
}

func TestSubscribeReplaysAfterLastEventID(t *testing.T) {
	h := NewHub(16)
	h.Publish(Event{Type: "e1", TenantID: "t1"})
	h.Publish(Event{Type: "e2", TenantID: "t1"})
	h.Publish(Event{Type: "e3", TenantID: "t1"})

	s, err := h.Subscribe("t1", "1")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer h.Close(s)

	got := drain(t, s, 2)
	if got[0].Type != "e2" || got[1].Type != "e3" {
		t.Fatalf("replay mismatch: %+v", got)
	}
}

func TestSubscribeRejectsInvalidLastEventID(t *testing.T) {
	h := NewHub(4)
	if _, err := h.Subscribe("t1", "not-a-number"); err == nil {
		t.Fatalf("expected error for invalid Last-Event-ID")
	}
}

func TestTenantFiltering(t *testing.T) {
	h := NewHub(16)
	s, err := h.Subscribe("t1", "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer h.Close(s)

	h.Publish(Event{Type: "other", TenantID: "t2"})
	h.Publish(Event{Type: "mine", TenantID: "t1"})
	got := drain(t, s, 1)
	if got[0].Type != "mine" {
		t.Fatalf("tenant filter failed: %+v", got)
	}
}

func TestBackpressureDropsNormalKeepsCritical(t *testing.T) {
	h := NewHub(1024)
	s, err := h.Subscribe("t1", "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer h.Close(s)

	// 不消费：先灌满订阅者队列（defaultBuffer 条 NORMAL）。
	for i := 0; i < defaultBuffer+8; i++ {
		h.Publish(Event{Type: "progress", TenantID: "t1", Criticality: Normal})
	}
	if s.Dropped() == 0 {
		t.Fatalf("expected NORMAL events to be dropped under backpressure")
	}
	if h.DroppedTotal() == 0 {
		t.Fatalf("hub dropped counter not updated")
	}
	// 清空队列，再投递 CRITICAL：应能被送达。
	for len(s.C()) > 0 {
		<-s.C()
	}
	h.Publish(Event{Type: "approval", TenantID: "t1", Criticality: Critical})
	got := drain(t, s, 1)
	if got[0].Type != "approval" {
		t.Fatalf("critical event lost: %+v", got)
	}
}

func TestBufferIsBounded(t *testing.T) {
	h := NewHub(4)
	for i := 0; i < 10; i++ {
		h.Publish(Event{Type: "e", TenantID: "t1"})
	}
	if h.Buffered() != 4 {
		t.Fatalf("buffer=%d want 4", h.Buffered())
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	h := NewHub(4)
	s, _ := h.Subscribe("t1", "")
	h.Close(s)
	h.Close(s)
	if h.Subscribers() != 0 {
		t.Fatalf("subscribers=%d want 0", h.Subscribers())
	}
}
