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

	ch := h.Connect("a1", "")
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

	first := h.Connect("a1", "")
	second := h.Connect("a1", "") // simulates a reconnect superseding the first

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

func TestCompanionHubPushRejectsWhenActionInFlight(t *testing.T) {
	h := NewCompanionHub()
	h.Connect("a1", "")

	if err := h.Push("a1", Action{ID: "act1", Type: ActionUpgrade}); err != nil {
		t.Fatalf("first push failed: %v", err)
	}

	err := h.Push("a1", Action{ID: "act2", Type: ActionUpgrade})
	if err != ErrActionInFlight {
		t.Fatalf("got err %v, want ErrActionInFlight for a second push before the first resolves", err)
	}
}

func TestCompanionHubRecordResultClearsInFlight(t *testing.T) {
	h := NewCompanionHub()
	h.Connect("a1", "")

	if err := h.Push("a1", Action{ID: "act1", Type: ActionUpgrade}); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	h.RecordResult("a1", ActionResult{ActionID: "act1", Success: true, CompletedAt: time.Now()})

	if err := h.Push("a1", Action{ID: "act2", Type: ActionUpgrade}); err != nil {
		t.Fatalf("expected push to succeed once the in-flight action resolved, got: %v", err)
	}
}

func TestCompanionHubDisconnectClearsInFlight(t *testing.T) {
	h := NewCompanionHub()
	ch := h.Connect("a1", "")

	if err := h.Push("a1", Action{ID: "act1", Type: ActionUpgrade}); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	h.Disconnect("a1", ch)

	// Reconnecting and pushing again must not be blocked by an action that
	// will now never resolve, since its companion disconnected.
	h.Connect("a1", "")
	if err := h.Push("a1", Action{ID: "act2", Type: ActionUpgrade}); err != nil {
		t.Fatalf("expected push to succeed after reconnect, got: %v", err)
	}
}

func TestCompanionHubTracksCompanionVersion(t *testing.T) {
	h := NewCompanionHub()

	if got := h.CompanionVersion("a1"); got != "" {
		t.Fatalf("got version %q before any connection, want empty", got)
	}

	ch := h.Connect("a1", "v0.7.0")
	if got := h.CompanionVersion("a1"); got != "v0.7.0" {
		t.Fatalf("got version %q, want v0.7.0", got)
	}

	// Kept even after Disconnect, so the admin page can still show
	// "last seen running vX.Y.Z".
	h.Disconnect("a1", ch)
	if got := h.CompanionVersion("a1"); got != "v0.7.0" {
		t.Fatalf("got version %q after disconnect, want it retained as v0.7.0", got)
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
	valid := []ActionType{ActionPackages, ActionUpgrade, ActionFullUpgrade, ActionRecheck}
	for _, v := range valid {
		if !v.valid() {
			t.Fatalf("expected %q to be valid", v)
		}
	}
	if ActionType("bogus").valid() {
		t.Fatal("expected an unknown action type to be invalid")
	}
}
