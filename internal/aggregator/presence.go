package aggregator

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/notifier"
)

// presenceCheckInterval is how often the PresenceWatcher re-evaluates
// every approved host's connectivity. One minute keeps the worst-case
// detection lag at ~1m past OFFLINE_ALERT_AFTER without waking up often
// enough to matter for a fleet of dozens of hosts.
const presenceCheckInterval = time.Minute

// presenceState is one agent's debounced connectivity as the watcher
// last left it.
type presenceState struct {
	// online is whether the agent had any live stream (agent or
	// companion -- same definition as the admin page's own Host offline
	// badge) at the last check.
	online bool
	// offlineSince is when the current uninterrupted disconnected
	// stretch started. Zero while online.
	offlineSince time.Time
	// alerted is whether an offline alert was already sent for the
	// current stretch -- guards both repeat alerts and a recovery
	// message for a flap that never alerted in the first place.
	alerted bool
}

// PresenceWatcher notifies (Telegram today, via notifyMgr) when an
// approved host's agent *and* companion are both disconnected -- the
// same condition the admin page renders as Host offline -- and again
// when it comes back. A host must stay continuously disconnected for
// offlineAfter before the first alert fires, so a companion restart,
// an aggregator restart (which empties the in-memory hub for the whole
// fleet at once), or any other brief flap stays silent.
//
// Deliberately polling, not event-driven off CompanionHub.Connect /
// Disconnect: those are per-connection hot paths, and debouncing needs
// a timer per host either way. Polling hub.Connected keeps this
// entirely out of the stream lifecycle.
//
// Each host can additionally opt out via its own NotifyDown registry
// flag (the /admin toggle), or snooze until MutedUntil (the "mute for"
// control) — either only suppresses this watcher's offline/recovery
// messages, never apply-result notifications.
//
// All state is in-memory only (same trade-off CompanionHub and
// OutputHub already accept): an aggregator restart resets every host
// to "assumed online," so a host that was already down before the
// restart gets a fresh offlineAfter grace period rather than an
// immediate alert.
type PresenceWatcher struct {
	registry     *Registry
	hub          *CompanionHub
	notifyMgr    *notifier.Manager
	offlineAfter time.Duration

	mu     sync.Mutex
	states map[string]*presenceState

	// now is time.Now by default, swappable in tests to advance the
	// clock without sleeping through the debounce.
	now func() time.Time
}

func NewPresenceWatcher(registry *Registry, hub *CompanionHub, notifyMgr *notifier.Manager, offlineAfter time.Duration) *PresenceWatcher {
	return &PresenceWatcher{
		registry:     registry,
		hub:          hub,
		notifyMgr:    notifyMgr,
		offlineAfter: offlineAfter,
		states:       map[string]*presenceState{},
		now:          time.Now,
	}
}

// Run evaluates every approved host every presenceCheckInterval until
// ctx is done. Callers must only start it when offlineAfter > 0 (see
// OFFLINE_ALERT_AFTER); checkOnce double-guards that anyway.
func (w *PresenceWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(presenceCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkOnce(ctx)
		}
	}
}

// checkOnce reconciles one poll round: exactly one alert per offline
// stretch (after offlineAfter of continuous disconnection), exactly
// one recovery per alerted stretch, silence otherwise.
func (w *PresenceWatcher) checkOnce(ctx context.Context) {
	if w.offlineAfter <= 0 {
		return
	}
	now := w.now()

	w.mu.Lock()
	defer w.mu.Unlock()

	seen := map[string]struct{}{}
	for _, rec := range w.registry.List() {
		if rec.Status != StatusApproved {
			continue
		}
		// Never reported (no LastSeen): a freshly approved host that
		// hasn't checked in yet, not a host that went down. Its
		// first report starts the clock like any other transition
		// to online.
		if rec.LastSeen.IsZero() {
			continue
		}
		seen[rec.ID] = struct{}{}

		st, ok := w.states[rec.ID]
		if !ok {
			st = &presenceState{online: true}
			w.states[rec.ID] = st
		}

		if w.hub.Connected(rec.ID) {
			if st.alerted && rec.NotifyDownEffective(now) {
				w.send(ctx, rec, "is back online",
					fmt.Sprintf("reachable again (was unreachable since %s)", st.offlineSince.Format(time.RFC3339)))
			}
			st.online = true
			st.offlineSince = time.Time{}
			st.alerted = false
			continue
		}

		if st.online {
			st.online = false
			st.offlineSince = now
		}
		// A muted host still advances offlineSince above (so unmuting a
		// still-down host alerts on the next round instead of restarting
		// the debounce), but neither fires while muted nor marks the
		// stretch alerted. MutedUntil is evaluated against this round's
		// clock, so a temporary mute lifts itself the moment it expires.
		if !st.alerted && rec.NotifyDownEffective(now) && !st.offlineSince.IsZero() && now.Sub(st.offlineSince) >= w.offlineAfter {
			w.send(ctx, rec, "went offline",
				fmt.Sprintf("no agent or companion connected (last seen %s) — powered off?", rec.LastSeen.Format(time.RFC3339)))
			st.alerted = true
		}
	}

	// Drop state for agents that are gone (forgotten) or no longer
	// approved, so a re-enrolled host with a recycled... (ids are
	// never recycled, but a rejected-then-approved host shouldn't
	// inherit a stale offlineSince either -- rejection already
	// removed it here).
	for id := range w.states {
		if _, ok := seen[id]; !ok {
			delete(w.states, id)
		}
	}
}

// send fans one presence event out. The last known report (if any)
// rides along as Status so future notifiers can include it; Brief
// keeps the update-specific trailer out of what's purely a
// connectivity message.
func (w *PresenceWatcher) send(ctx context.Context, rec AgentRecord, title, change string) {
	if w.notifyMgr == nil {
		return
	}
	var status checker.Status
	if rec.LastReport != nil {
		status = *rec.LastReport
	}
	log.Printf("presence: %s %s", rec.Hostname, title)
	w.notifyMgr.Send(ctx, notifier.Event{
		Hostname: rec.Hostname,
		Title:    title,
		Status:   status,
		Changes:  []string{change},
		Brief:    true,
	})
}
