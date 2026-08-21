package aggregator

import (
	"testing"
	"time"
)

func TestOutputHubPublishReachesSubscriberForActiveAction(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	_, ch, cancel := h.Subscribe("a1")
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
	_, ch, cancel := h.Subscribe("a1")
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
	_, ch, cancel := h.Subscribe("a1")
	defer cancel()

	h.End("a1", "act1", EventDone, false)
	select {
	case ev := <-ch:
		if ev.Kind != EventDone {
			t.Fatalf("got %#v, want EventDone", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for done event")
	}

	h.End("a1", "act1", EventDisconnected, false)
	select {
	case ev := <-ch:
		t.Fatalf("expected no further event after done already fired, got %#v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestOutputHubEndPropagatesStaged is the regression test for a Windows
// companion self-update never showing its second phase (stop/swap/start
// the service) live: the browser's "done" handler needs to know this
// first "done" was a staged intermediate result, not the real end, so it
// can keep watching instead of switching to version-polling.
func TestOutputHubEndPropagatesStaged(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	_, ch, cancel := h.Subscribe("a1")
	defer cancel()

	h.End("a1", "act1", EventDone, true)
	select {
	case ev := <-ch:
		if ev.Kind != EventDone || !ev.Staged {
			t.Fatalf("got %#v, want a Staged EventDone", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for done event")
	}
}

func TestOutputHubEndDisconnectedForRestartCase(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	_, ch, cancel := h.Subscribe("a1")
	defer cancel()

	h.End("a1", "act1", EventDisconnected, false)
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
	_, ch1, cancel1 := h.Subscribe("a1")
	defer cancel1()
	_, ch2, cancel2 := h.Subscribe("a1")
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

// TestOutputHubSubscribeReplaysBacklogForActiveAction is the regression
// test for browser-refresh resume: a subscriber that connects *after*
// some lines were already published must still see them, then keep
// receiving new ones live.
func TestOutputHubSubscribeReplaysBacklogForActiveAction(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	h.Publish("a1", "act1", "one")
	h.Publish("a1", "act1", "two")

	backlog, ch, cancel := h.Subscribe("a1")
	defer cancel()

	if len(backlog) != 2 || backlog[0] != "one" || backlog[1] != "two" {
		t.Fatalf("got backlog %#v, want [one two]", backlog)
	}

	h.Publish("a1", "act1", "three")
	select {
	case ev := <-ch:
		if ev.Kind != EventLine || ev.Line != "three" {
			t.Fatalf("got %#v, want a live line event saying three", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the live line published after Subscribe")
	}
}

// TestOutputHubEndClearsBacklog: once an action ends, there's nothing
// left to replay for it -- only in-flight output is meant to survive a
// reconnect, not a completed action's history.
func TestOutputHubEndClearsBacklog(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	h.Publish("a1", "act1", "one")
	h.End("a1", "act1", EventDone, false)

	backlog, _, cancel := h.Subscribe("a1")
	defer cancel()
	if len(backlog) != 0 {
		t.Fatalf("got backlog %#v after End, want empty", backlog)
	}
}

// TestOutputHubBeginResetsBacklogForNewAction guards against a new
// action's backlog leaking lines left over from a prior one on the same
// agent.
func TestOutputHubBeginResetsBacklogForNewAction(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	h.Publish("a1", "act1", "from act1")
	h.End("a1", "act1", EventDone, false)

	h.Begin("a1", "act2")
	backlog, _, cancel := h.Subscribe("a1")
	defer cancel()
	if len(backlog) != 0 {
		t.Fatalf("got backlog %#v for a freshly begun action, want empty", backlog)
	}
}

func TestOutputHubCancelStopsFurtherDelivery(t *testing.T) {
	h := NewOutputHub()
	h.Begin("a1", "act1")
	_, ch, cancel := h.Subscribe("a1")
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
