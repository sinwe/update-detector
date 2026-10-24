package aggregator

import "sync"

// adminEventKind discriminates the two event shapes handleAdminEvents
// forwards to admin page browsers.
type adminEventKind string

const (
	// adminEventRegistry means "fleet state changed -- reload". Emitted by
	// enroll/report/approve and friend/foe presence changes: the payload is
	// deliberately empty because the fresh data comes from the reload
	// itself, not from the event.
	adminEventRegistry adminEventKind = "registry"
	// adminEventAction means an action is now in flight for a host, pushed
	// from any source -- the Telegram apply-all button, the secret-gated
	// apply endpoint, a self-update, another admin tab. The payload names
	// the host and action so an open page can open that host's live output
	// console immediately (openLiveOutput in templates.go) instead of
	// waiting for its next reload to re-derive the pane state.
	adminEventAction adminEventKind = "action"
)

// adminEvent is one fanned-out event on the admin events SSE stream.
type adminEvent struct {
	Kind     adminEventKind
	AgentID  string // only set for adminEventAction
	ActionID string // only set for adminEventAction
}

// AdminHub fans push events to admin page browsers. It is a minimal
// broadcast hub: registry events tell every subscriber "reload", action
// events tell it exactly which host just got an action pushed. Keeping
// payloads minimal avoids leaking registry details and keeps the SSE
// simple (registry events just reload; action events open a console).
type AdminHub struct {
	mu   sync.Mutex
	subs map[chan adminEvent]struct{}
}

func NewAdminHub() *AdminHub {
	return &AdminHub{subs: map[chan adminEvent]struct{}{}}
}

// Subscribe registers a new browser SSE subscriber.
// Caller must call cancel exactly once.
func (h *AdminHub) Subscribe() (<-chan adminEvent, func()) {
	ch := make(chan adminEvent, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
	return ch, cancel
}

// Notify broadcasts a "reload" registry event to all subscribers.
func (h *AdminHub) Notify() {
	h.broadcast(adminEvent{Kind: adminEventRegistry})
}

// NotifyAction broadcasts "an action is now in flight for agentID" to all
// subscribers. Called with PushApply's and friends' outputHub.Begin so an
// open admin page reacts to actions it didn't initiate: Telegram
// apply-all button presses, apply-API calls, self-updates, actions from
// another tab.
func (h *AdminHub) NotifyAction(agentID, actionID string) {
	h.broadcast(adminEvent{Kind: adminEventAction, AgentID: agentID, ActionID: actionID})
}

// broadcast must be called with h.mu held. Drops the event for a
// subscriber whose single-slot buffer is still full -- the same
// drop-on-full tolerance the output stream accepts for a slow consumer;
// a registry event's data comes from the reload, so losing a stale one
// while the tab is frozen loses nothing.
func (h *AdminHub) broadcast(ev adminEvent) {
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
