package aggregator

import (
	"testing"
	"time"
)

func TestOutputHubPublishReachesSubscriberForActiveAction(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	ch, cancel := h.Subscribe("a1")
	defer cancel()

	h.Publish("a1", "act1", "hello")

	select {
	case ev := <-ch:
		if ev.Kind != EventLine || ev.Line != "hello" || ev.ActionID != "act1" {
			t.Fatalf("got %#v, want a line event for act1 saying hello", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published line")
	}
}

func TestOutputHubPublishIgnoredForStaleActionID(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	ch, cancel := h.Subscribe("a1")
	defer cancel()

	h.Publish("a1", "act2", "stale") // not the active action for a1

	select {
	case ev := <-ch:
		t.Fatalf("expected nothing, got %#v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestOutputHubEndDoneThenLateDisconnectedIsNoop is the regression test
// for the race this hub exists to resolve: whichever of a normal success
// (done) or the stream's own body ending (disconnected) is reported first
// must win, and the second must never overwrite it -- otherwise almost
// every successful action would show a spurious "disconnected" flash.
func TestOutputHubEndDoneThenLateDisconnectedIsNoop(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	ch, cancel := h.Subscribe("a1")
	defer cancel()

	h.End("a1", "act1", EventDone)
	select {
	case ev := <-ch:
		if ev.Kind != EventDone {
			t.Fatalf("got %#v, want EventDone", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for done event")
	}

	h.End("a1", "act1", EventDisconnected)
	select {
	case ev := <-ch:
		t.Fatalf("expected no further event after done already fired, got %#v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestOutputHubEndDisconnectedForRestartCase(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	ch, cancel := h.Subscribe("a1")
	defer cancel()

	h.End("a1", "act1", EventDisconnected)
	select {
	case ev := <-ch:
		if ev.Kind != EventDisconnected {
			t.Fatalf("got %#v, want EventDisconnected", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestOutputHubMultipleSubscribersAllReceive(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	ch1, cancel1 := h.Subscribe("a1")
	defer cancel1()
	ch2, cancel2 := h.Subscribe("a1")
	defer cancel2()

	h.Publish("a1", "act1", "hi")

	for _, ch := range []<-chan outputEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Line != "hi" {
				t.Fatalf("got %#v", ev)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for a subscriber to receive")
		}
	}
}

func TestOutputHubCancelStopsFurtherDelivery(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	ch, cancel := h.Subscribe("a1")
	cancel()

	// Must not panic or block just because every subscriber already left.
	h.Publish("a1", "act1", "hi")

	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("expected no more events after cancel, got %#v", ev)
		}
	default:
	}
}
