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

// OutputHub fans a companion's (or agent's) live action output out to
// however many browsers are watching that agent's admin-page row, for
// however long its action is in flight. Buffers the in-flight action's
// lines in memory (backlog) so a reconnecting subscriber -- e.g. a page
// refresh mid-action -- can replay everything published so far before
// continuing live; see Subscribe. Deliberately scoped to in-flight
// replay only: End clears the backlog the moment an action ends, so
// nothing is retained once it's over, and an aggregator restart still
// loses everything, same trade-off CompanionHub's own result log already
// accepts for the same reason. A completed action's output is not
// retrievable after the fact -- only its final summary lives on in the
// existing recent-actions log.
type OutputHub struct {
	mu      sync.Mutex
	active  map[string]string   // agentID -> the action ID currently streaming
	backlog map[string][]string // agentID -> lines published so far for the active action
	subs    map[string]map[chan outputEvent]struct{}
}

func NewOutputHub() *OutputHub {
	return &OutputHub{
		active:  map[string]string{},
		backlog: map[string][]string{},
		subs:    map[string]map[chan outputEvent]struct{}{},
	}
}

// Begin marks actionID as agentID's currently-streaming action, and
// resets its backlog. Called once an action is successfully pushed
// (handleAdminApply and friends), not when a companion's output stream
// actually shows up -- some actions never have one at all (a bare
// recheck served by an agent-only connection never opens POST
// /companion/output; an old companion that predates output streaming
// entirely never will either), and those must still resolve to a correct
// "done" via End once their real result arrives, rather than a live pane
// that waits forever for an End call that never comes. Unconditional:
// CompanionHub's own in-flight guard (Push's ErrActionInFlight) already
// ensures at most one action is ever in flight per agent, so there's
// never a second concurrent Begin to race against.
func (h *OutputHub) Begin(agentID, actionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active[agentID] = actionID
	h.backlog[agentID] = nil
}

// Publish fans a line out to every current subscriber for agentID,
// non-blocking per subscriber -- the same drop-on-full tolerance the
// existing heartbeat-based SSE precedent already accepts for a slow
// consumer -- and appends it to agentID's backlog so a subscriber that
// (re)connects later still sees it (see Subscribe). A no-op if actionID
// isn't (or is no longer) the active one for agentID, e.g. a late line
// from a superseded/ended stream.
func (h *OutputHub) Publish(agentID, actionID, line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active[agentID] != actionID {
		return
	}
	h.backlog[agentID] = append(h.backlog[agentID], line)
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
// the two HTTP requests happens to finish first). Clears the backlog too
// -- once an action is over there's nothing left to replay for it.
func (h *OutputHub) End(agentID, actionID string, kind outputEventKind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active[agentID] != actionID {
		return
	}
	delete(h.active, agentID)
	delete(h.backlog, agentID)
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

// Subscribe registers a new subscriber for agentID's live output and
// atomically returns a snapshot of whatever's already been published for
// the currently active action (nil if none, or if there's no action in
// flight at all) -- the caller should replay this backlog before
// forwarding the live channel, so a browser reconnecting mid-action (e.g.
// a page refresh) resumes from where it left off instead of missing
// everything published before it (re)connected. Snapshotting the backlog
// and registering the channel happen under one lock acquisition
// specifically so no line published in between could be lost (missed by
// the snapshot, then never broadcast because the channel wasn't
// registered yet) or double-delivered (in the snapshot and then broadcast
// again after registration).
//
// The returned cancel func must be called exactly once when the caller
// (typically an SSE handler, on the request context ending) is done, to
// unregister the channel and avoid leaking it.
func (h *OutputHub) Subscribe(agentID string) (backlog []string, ch <-chan outputEvent, cancel func()) {
	c := make(chan outputEvent, 32)

	h.mu.Lock()
	backlog = append([]string(nil), h.backlog[agentID]...)
	if h.subs[agentID] == nil {
		h.subs[agentID] = map[chan outputEvent]struct{}{}
	}
	h.subs[agentID][c] = struct{}{}
	h.mu.Unlock()

	cancel = func() {
		h.mu.Lock()
		delete(h.subs[agentID], c)
		if len(h.subs[agentID]) == 0 {
			delete(h.subs, agentID)
		}
		h.mu.Unlock()
	}
	return backlog, c, cancel
}
