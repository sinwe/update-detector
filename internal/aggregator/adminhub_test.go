package aggregator

import (
	"net/http/httptest"
	"testing"
	"time"
)

// TestAdminHubNotifyActionPayload checks the payload actually names the
// host and action -- an open admin page keys its console-opening logic on
// this (openLiveOutput in templates.go), so an empty or misnamed payload
// would silently break the live console without any compile-time signal.
func TestAdminHubNotifyActionPayload(t *testing.T) {
	h := NewAdminHub()
	ch, cancel := h.Subscribe()
	defer cancel()

	h.NotifyAction("a1", "action-1")

	select {
	case ev := <-ch:
		if ev.Kind != adminEventAction || ev.AgentID != "a1" || ev.ActionID != "action-1" {
			t.Fatalf("got %+v, want the action event for a1/action-1", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the action event")
	}

	h.Notify()

	select {
	case ev := <-ch:
		if ev.Kind != adminEventRegistry {
			t.Fatalf("got %q, want a registry event", ev.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the registry event")
	}
}

// TestAdminHubDropOnFull pins the non-blocking broadcast contract: a
// subscriber whose single-slot buffer is full gets the event dropped, not
// back-pressured -- a frozen tab must never stall an action push (the
// same tolerance the output stream's SSE accepts for a slow consumer).
func TestAdminHubDropOnFull(t *testing.T) {
	h := NewAdminHub()
	ch, cancel := h.Subscribe()
	defer cancel()

	h.Notify()
	h.NotifyAction("a1", "action-1") // buffer still holds the registry event; dropped

	select {
	case ev := <-ch:
		if ev.Kind != adminEventRegistry {
			t.Fatalf("got %q, want the earlier registry event", ev.Kind)
		}
	default:
		t.Fatal("expected the buffered registry event")
	}
	select {
	case ev := <-ch:
		t.Fatalf("got %+v, want no second event -- the action event should have been dropped", ev)
	default:
	}
}

func TestAdminHubUnsubscribe(t *testing.T) {
	h := NewAdminHub()
	ch, cancel := h.Subscribe()
	cancel()
	h.Notify()

	select {
	case ev := <-ch:
		t.Fatalf("got %+v after cancel, want silence", ev)
	default:
	}
}

// TestWriteAdminEventFrames pins the exact wire format an admin page's
// EventSource listeners switch on: a wrong event name would make the page
// ignore pushes (or reload on everything) without any test on either side
// noticing.
func TestWriteAdminEventFrames(t *testing.T) {
	t.Run("registry", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := writeAdminEvent(rec, adminEvent{Kind: adminEventRegistry}); err != nil {
			t.Fatal(err)
		}
		if got, want := rec.Body.String(), "event: registry\ndata: {}\n\n"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("action", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := writeAdminEvent(rec, adminEvent{Kind: adminEventAction, AgentID: "a1", ActionID: "b2"}); err != nil {
			t.Fatal(err)
		}
		want := "event: action\ndata: {\"agent_id\":\"a1\",\"action_id\":\"b2\"}\n\n"
		if got := rec.Body.String(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}
