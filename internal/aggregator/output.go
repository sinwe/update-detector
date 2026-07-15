package aggregator

import "sync"

// outputEventKind discriminates the three SSE event types
// handleAdminOutputStream forwards to a browser subscriber.
type outputEventKind string

const (
	// EventLine is one line of live output.
	EventLine outputEventKind = "line"
	// EventDone means the action's final ActionResult was recorded
	// (handleCompanionResult) -- the normal, successful end of a stream.
	EventDone outputEventKind = "done"
	// EventDisconnected means the companion's output-stream POST body
	// ended without a "done" having arrived first -- notably, the
	// companion-self-update-restart case: the process producing the
	// stream was killed mid-action. The action's real outcome, if any,
	// still arrives separately via the existing ActionResult reporting
	// path; this only ends the *live view*.
	EventDisconnected outputEventKind = "disconnected"
)

// outputEvent is one fanned-out event for a single agent's live output.
type outputEvent struct {
	Kind     outputEventKind
	ActionID string
	Line     string // only set when Kind == EventLine
}

// OutputHub fans a companion's live action output out to however many
// browsers are watching that agent's admin-page row, for however long its
// action is in flight. Deliberately parallel to CompanionHub: in-memory
// only, nothing persisted -- a subscriber just reconnects and picks up
// whatever's live from that point on (see the "no history replay" scoping
// decision), and losing everything on an aggregator restart is an
// acceptable trade-off for the same reason CompanionHub already accepts
// it for its own result log.
type OutputHub struct {
	mu     sync.Mutex
	active map[string]string // agentID -> the action ID currently streaming
	subs   map[string]map[chan outputEvent]struct{}
}

func NewOutputHub() *OutputHub {
	return &OutputHub{
		active: map[string]string{},
		subs:   map[string]map[chan outputEvent]struct{}{},
	}
}

// Begin marks actionID as agentID's currently-streaming action.
// Unconditional: CompanionHub's own in-flight guard (Push's
// ErrActionInFlight) already ensures at most one action is ever in flight
// per agent, so there's never a second concurrent Begin to race against.
func (h *OutputHub) Begin(agentID, actionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active[agentID] = actionID
}

// Publish fans a line out to every current subscriber for agentID,
// non-blocking per subscriber -- the same drop-on-full tolerance the
// existing heartbeat-based SSE precedent already accepts for a slow
// consumer. A no-op if actionID isn't (or is no longer) the active one for
// agentID, e.g. a late line from a superseded/ended stream.
func (h *OutputHub) Publish(agentID, actionID, line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active[agentID] != actionID {
		return
	}
	h.broadcast(agentID, outputEvent{Kind: EventLine, ActionID: actionID, Line: line})
}

// End reports actionID's stream as finished, either because the final
// ActionResult was recorded (kind=EventDone) or because the companion's
// output-stream POST body ended without one having arrived yet
// (kind=EventDisconnected). A no-op if actionID is no longer the active
// one for agentID -- whichever of the two natural end-of-stream signals
// fires first wins, and the second must not overwrite it (a normal
// success's clean stream-close must never show as "disconnected" after
// handleCompanionResult already recorded "done", regardless of which of
// the two HTTP requests happens to finish first).
func (h *OutputHub) End(agentID, actionID string, kind outputEventKind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active[agentID] != actionID {
		return
	}
	delete(h.active, agentID)
	h.broadcast(agentID, outputEvent{Kind: kind, ActionID: actionID})
}

// broadcast must be called with h.mu held.
func (h *OutputHub) broadcast(agentID string, event outputEvent) {
	for ch := range h.subs[agentID] {
		select {
		case ch <- event:
		default:
		}
	}
}

// Subscribe registers a new subscriber for agentID's live output. The
// returned cancel func must be called exactly once when the caller
// (typically an SSE handler, on the request context ending) is done, to
// unregister the channel and avoid leaking it.
func (h *OutputHub) Subscribe(agentID string) (<-chan outputEvent, func()) {
	ch := make(chan outputEvent, 32)

	h.mu.Lock()
	if h.subs[agentID] == nil {
		h.subs[agentID] = map[chan outputEvent]struct{}{}
	}
	h.subs[agentID][ch] = struct{}{}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		delete(h.subs[agentID], ch)
		if len(h.subs[agentID]) == 0 {
			delete(h.subs, agentID)
		}
		h.mu.Unlock()
	}
	return ch, cancel
}
