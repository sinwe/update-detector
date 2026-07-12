package aggregator

import (
	"testing"
	"time"
)

func TestCompanionHubConnectAndPush(t *testing.T) {
	h := NewCompanionHub()

	if h.Connected("a1") {
		t.Fatal("expected a1 not connected before Connect")
	}

	ch := h.Connect("a1")
	if !h.Connected("a1") {
		t.Fatal("expected a1 connected after Connect")
	}

	action := Action{ID: "act1", Type: ActionUpgrade, CreatedAt: time.Now()}
	if err := h.Push("a1", action); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	select {
	case got := <-ch:
		if got.ID != "act1" {
			t.Fatalf("got action %#v, want id act1", got)
		}
	default:
		t.Fatal("expected action to be waiting on the channel")
	}
}

func TestCompanionHubPushWithoutConnection(t *testing.T) {
	h := NewCompanionHub()
	if err := h.Push("unknown", Action{ID: "act1"}); err != ErrCompanionNotConnected {
		t.Fatalf("got err %v, want ErrCompanionNotConnected", err)
	}
}

func TestCompanionHubDisconnectGuardsAgainstStaleChannel(t *testing.T) {
	h := NewCompanionHub()

	first := h.Connect("a1")
	second := h.Connect("a1") // simulates a reconnect superseding the first

	// A stale Disconnect for the superseded first channel must not tear
	// down the second, current one.
	h.Disconnect("a1", first)
	if !h.Connected("a1") {
		t.Fatal("expected a1 to remain connected after a stale Disconnect")
	}

	h.Disconnect("a1", second)
	if h.Connected("a1") {
		t.Fatal("expected a1 disconnected after disconnecting its current channel")
	}
}

func TestCompanionHubResultsCapped(t *testing.T) {
	h := NewCompanionHub()
	for i := 0; i < actionLogLimit+5; i++ {
		h.RecordResult("a1", ActionResult{ActionID: string(rune('a' + i%26)), CompletedAt: time.Now()})
	}
	results := h.Results("a1")
	if len(results) != actionLogLimit {
		t.Fatalf("got %d results, want capped at %d", len(results), actionLogLimit)
	}
}

func TestActionTypeValid(t *testing.T) {
	valid := []ActionType{ActionPackages, ActionUpgrade, ActionFullUpgrade}
	for _, v := range valid {
		if !v.valid() {
			t.Fatalf("expected %q to be valid", v)
		}
	}
	if ActionType("bogus").valid() {
		t.Fatal("expected an unknown action type to be invalid")
	}
}
