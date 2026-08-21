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

	res := h.Connect("a1", KindCompanion, "")
	if !res.Accepted {
		t.Fatal("expected Connect to be accepted")
	}
	if !h.Connected("a1") {
		t.Fatal("expected a1 connected after Connect")
	}

	action := Action{ID: "act1", Type: ActionUpgrade, CreatedAt: time.Now()}
	if err := h.Push("a1", action); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	select {
	case got := <-res.Ch:
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

	first := h.Connect("a1", KindCompanion, "")
	second := h.Connect("a1", KindCompanion, "") // simulates a reconnect superseding the first

	// A stale Disconnect for the superseded first channel must not tear
	// down the second, current one.
	h.Disconnect("a1", first.Ch)
	if !h.Connected("a1") {
		t.Fatal("expected a1 to remain connected after a stale Disconnect")
	}

	h.Disconnect("a1", second.Ch)
	if h.Connected("a1") {
		t.Fatal("expected a1 disconnected after disconnecting its current channel")
	}
}

func TestCompanionHubPushRejectsWhenActionInFlight(t *testing.T) {
	h := NewCompanionHub()
	h.Connect("a1", KindCompanion, "")

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
	h.Connect("a1", KindCompanion, "")

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
	res := h.Connect("a1", KindCompanion, "")

	if err := h.Push("a1", Action{ID: "act1", Type: ActionUpgrade}); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	h.Disconnect("a1", res.Ch)

	// Reconnecting and pushing again must not be blocked by an action that
	// will now never resolve, since its companion disconnected.
	h.Connect("a1", KindCompanion, "")
	if err := h.Push("a1", Action{ID: "act2", Type: ActionUpgrade}); err != nil {
		t.Fatalf("expected push to succeed after reconnect, got: %v", err)
	}
}

func TestCompanionHubTracksCompanionVersion(t *testing.T) {
	h := NewCompanionHub()

	if got := h.CompanionVersion("a1"); got != "" {
		t.Fatalf("got version %q before any connection, want empty", got)
	}

	res := h.Connect("a1", KindCompanion, "v0.7.0")
	if got := h.CompanionVersion("a1"); got != "v0.7.0" {
		t.Fatalf("got version %q, want v0.7.0", got)
	}

	// Kept even after Disconnect, so the admin page can still show
	// "last seen running vX.Y.Z".
	h.Disconnect("a1", res.Ch)
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
	valid := []ActionType{ActionPackages, ActionUpgrade, ActionFullUpgrade, ActionRecheck, ActionSelfUpdate}
	for _, v := range valid {
		if !v.valid() {
			t.Fatalf("expected %q to be valid", v)
		}
	}
	if ActionType("bogus").valid() {
		t.Fatal("expected an unknown action type to be invalid")
	}
}

func TestActionSelfUpdateRequiresCompanion(t *testing.T) {
	if !ActionSelfUpdate.requiresCompanion() {
		t.Fatal("expected ActionSelfUpdate to require a real companion, like every action except recheck")
	}
}

func TestCompanionHubPushRequiresCompanionForSelfUpdate(t *testing.T) {
	h := NewCompanionHub()
	h.Connect("a1", KindAgent, "")

	action := Action{ID: "act1", Type: ActionSelfUpdate, Component: "agent", TargetVersion: "v0.11.0"}
	if err := h.Push("a1", action); err != ErrCompanionRequired {
		t.Fatalf("got err %v, want ErrCompanionRequired for a self-update action on an agent-only stream", err)
	}
}

func TestParseClientKind(t *testing.T) {
	cases := map[string]ClientKind{
		"agent":     KindAgent,
		"companion": KindCompanion,
		"":          KindCompanion,
		"bogus":     KindCompanion,
	}
	for in, want := range cases {
		if got := ParseClientKind(in); got != want {
			t.Fatalf("ParseClientKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompanionHubAgentConnectsFirstThenCompanionPreempts(t *testing.T) {
	h := NewCompanionHub()

	agentRes := h.Connect("a1", KindAgent, "")
	if !agentRes.Accepted {
		t.Fatal("expected agent Connect to be accepted when nothing else is connected")
	}
	if kind, ok := h.Kind("a1"); !ok || kind != KindAgent {
		t.Fatalf("got kind=%q ok=%v, want KindAgent", kind, ok)
	}

	select {
	case <-agentRes.Superseded:
		t.Fatal("agent stream superseded before any companion connected")
	default:
	}

	companionRes := h.Connect("a1", KindCompanion, "v1.0.0")
	if !companionRes.Accepted {
		t.Fatal("expected companion Connect to be accepted, preempting the agent")
	}

	select {
	case <-agentRes.Superseded:
	default:
		t.Fatal("expected agent's Superseded channel to be closed once companion connected")
	}

	if kind, ok := h.Kind("a1"); !ok || kind != KindCompanion {
		t.Fatalf("got kind=%q ok=%v, want KindCompanion after preemption", kind, ok)
	}
}

func TestCompanionHubCompanionConnectedAcceptsAgentInAgentStreams(t *testing.T) {
	h := NewCompanionHub()

	companionRes := h.Connect("a1", KindCompanion, "v1.0.0")
	if !companionRes.Accepted {
		t.Fatal("expected companion Connect to be accepted")
	}

	agentRes := h.Connect("a1", KindAgent, "")
	if !agentRes.Accepted {
		t.Fatal("expected agent Connect to be accepted alongside a companion (routed to agentStreams)")
	}

	// The existing companion stream must be completely untouched.
	select {
	case <-companionRes.Superseded:
		t.Fatal("companion's stream must not be superseded by an agent connect")
	default:
	}
	if kind, ok := h.Kind("a1"); !ok || kind != KindCompanion {
		t.Fatalf("got kind=%q ok=%v, want KindCompanion unchanged", kind, ok)
	}

	// The agent stream should be in agentStreams.
	h.mu.Lock()
	_, ok := h.agentStreams["a1"]
	h.mu.Unlock()
	if !ok {
		t.Fatal("expected agent stream to be in agentStreams")
	}
}

func TestCompanionHubSameKindReconnectDoesNotSupersede(t *testing.T) {
	h := NewCompanionHub()

	first := h.Connect("a1", KindCompanion, "v1.0.0")
	second := h.Connect("a1", KindCompanion, "v1.0.1") // simulates companion restarting

	select {
	case <-first.Superseded:
		t.Fatal("a same-kind reconnect must not fire Superseded -- it's not a priority change")
	default:
	}
	if !second.Accepted {
		t.Fatal("expected the reconnect to be accepted")
	}
}

func TestCompanionHubPushRequiresCompanionForApplyActions(t *testing.T) {
	h := NewCompanionHub()
	h.Connect("a1", KindAgent, "")

	if err := h.Push("a1", Action{ID: "act1", Type: ActionUpgrade}); err != ErrCompanionRequired {
		t.Fatalf("got err %v, want ErrCompanionRequired for an apply-type action on an agent-only stream", err)
	}

	// Recheck has no such requirement -- either kind can carry it out.
	if err := h.Push("a1", Action{ID: "act2", Type: ActionRecheck}); err != nil {
		t.Fatalf("expected recheck to succeed on an agent-only stream, got: %v", err)
	}
}

// TestCompanionHubPushSetsAgentPendingForNonCompanionActions is the
// regression test for a real bug: Push's agentStreams branch pushed the
// action but never recorded it as in-flight anywhere, so
// handleCompanionOutput's authorization check (IsPending) always
// rejected a recheck's own output-stream POST with 409 -- "Force
// recheck" silently never streamed anything, on every platform,
// regardless of the verbose flag.
func TestCompanionHubPushSetsAgentPendingForNonCompanionActions(t *testing.T) {
	h := NewCompanionHub()
	h.Connect("a1", KindAgent, "")

	if err := h.Push("a1", Action{ID: "act1", Type: ActionRecheck}); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	if !h.IsPending("a1", "act1") {
		t.Fatal("expected IsPending to report the recheck's action ID as in flight")
	}
	if id, ok := h.Pending("a1"); !ok || id != "act1" {
		t.Fatalf("got Pending()=%q,%v, want act1,true", id, ok)
	}
}

// TestCompanionHubRecheckDoesNotClobberInFlightCompanionAction covers the
// other half of the same fix: a recheck routed via agentStreams must
// track its own in-flight marker independently of an already in-flight
// companion action for the same agent, so IsPending can authorize output
// streaming for *both* without either clobbering the other's tracking
// (and so RecordResult for either one clears only its own marker).
func TestCompanionHubRecheckDoesNotClobberInFlightCompanionAction(t *testing.T) {
	h := NewCompanionHub()
	h.Connect("a1", KindCompanion, "")
	// Matches real topology: the agent always opens its own KindAgent
	// stream too, landing in agentStreams alongside the companion's main
	// slot (see Connect) -- without this, Push's agentStreams branch has
	// nothing to route the recheck to and it falls through to share the
	// companion's own pending slot instead, which is Push's documented,
	// legitimate fallback for a host with no separate agent connection at
	// all (e.g. an old agent binary), not what this test means to cover.
	h.Connect("a1", KindAgent, "")

	if err := h.Push("a1", Action{ID: "upgrade1", Type: ActionUpgrade}); err != nil {
		t.Fatalf("push upgrade failed: %v", err)
	}
	if err := h.Push("a1", Action{ID: "recheck1", Type: ActionRecheck}); err != nil {
		t.Fatalf("push recheck failed: %v", err)
	}

	if !h.IsPending("a1", "upgrade1") {
		t.Fatal("expected the in-flight upgrade to still be authorized")
	}
	if !h.IsPending("a1", "recheck1") {
		t.Fatal("expected the in-flight recheck to also be authorized")
	}

	h.RecordResult("a1", ActionResult{ActionID: "recheck1", Success: true, CompletedAt: time.Now()})
	if h.IsPending("a1", "recheck1") {
		t.Fatal("expected the recheck's marker to be cleared after its result")
	}
	if !h.IsPending("a1", "upgrade1") {
		t.Fatal("expected the still-in-flight upgrade to be unaffected by the recheck's own result")
	}
}

func TestCompanionHubAgentConnectDoesNotTouchCompanionVersion(t *testing.T) {
	h := NewCompanionHub()
	res := h.Connect("a1", KindCompanion, "v1.0.0")
	h.Disconnect("a1", res.Ch)

	// Agent connects fresh (companion→agent is never a preemption, only a
	// rejection while companion still holds the slot -- but once
	// disconnected, the slot is free and agent can take it, same as
	// "no existing entry" at all).
	agentRes := h.Connect("a1", KindAgent, "")
	if !agentRes.Accepted {
		t.Fatal("expected agent Connect to be accepted once the companion has disconnected")
	}

	if got := h.CompanionVersion("a1"); got != "v1.0.0" {
		t.Fatalf("got version %q, want the last real companion version v1.0.0 retained", got)
	}
}
