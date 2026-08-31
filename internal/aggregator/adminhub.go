package aggregator

import "sync"

// AdminHub fans registry-change notifications to admin page browsers.
// It is a minimal broadcast hub - one event means "something changed, reload".
// Keeping payload minimal avoids leaking registry details and keeps the
// SSE simple (clients just reload on any event).
type AdminHub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func NewAdminHub() *AdminHub {
	return &AdminHub{subs: map[chan struct{}]struct{}{}}
}

// Subscribe registers a new browser SSE subscriber.
// Caller must call cancel exactly once.
func (h *AdminHub) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
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

// Notify broadcasts to all subscribers, non-blocking per subscriber.
func (h *AdminHub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
