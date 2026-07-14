package aggregator

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ActionType identifies which apt operation a companion should run.
type ActionType string

const (
	ActionPackages    ActionType = "packages"
	ActionUpgrade     ActionType = "upgrade"
	ActionFullUpgrade ActionType = "full-upgrade"
	// ActionRecheck asks the companion to trigger the local agent's
	// out-of-band POST /recheck instead of running apt-get -- doesn't
	// change anything on the host, so unlike the other three it needs no
	// shared secret (see handleAdminRecheck).
	ActionRecheck ActionType = "recheck"
	// ActionSelfUpdate asks the companion to update update-detector's own
	// Component ("agent"|"aggregator"|"companion") to TargetVersion,
	// detecting native vs. Docker on that host itself (see
	// internal/companion/deploykind.go) -- one type, not three, since the
	// three components differ only in which detect+execute branch runs
	// and which field selects it, not in the outer shape the way
	// packages/upgrade/full-upgrade genuinely run different apt-get
	// subcommands.
	ActionSelfUpdate ActionType = "self-update"
)

func (t ActionType) valid() bool {
	switch t {
	case ActionPackages, ActionUpgrade, ActionFullUpgrade, ActionRecheck, ActionSelfUpdate:
		return true
	default:
		return false
	}
}

// requiresCompanion is true for every action type except ActionRecheck --
// only the companion has root and an apt-get code path at all, so anything
// else pushed at an agent-kind stream (see ClientKind) can never be
// carried out and must be rejected before it's even sent.
func (t ActionType) requiresCompanion() bool {
	return t != ActionRecheck
}

// ClientKind distinguishes which local process holds the stream for a
// given agent ID: the host-native companion (can run apt-get, needs
// root), or the agent itself (detection only, no apply capability at
// all). Exactly one of the two ever holds the stream at a time -- see
// CompanionHub.Connect.
type ClientKind string

const (
	KindAgent     ClientKind = "agent"
	KindCompanion ClientKind = "companion"
)

// ParseClientKind maps the X-Client-Kind header to a ClientKind, defaulting
// to KindCompanion for an empty/unrecognized value -- backward compat with
// existing companion binaries that predate this header entirely.
func ParseClientKind(s string) ClientKind {
	if ClientKind(s) == KindAgent {
		return KindAgent
	}
	return KindCompanion
}

// Action is a single trigger pushed down to a connected companion over SSE.
// The companion re-validates Packages against its own host's last-known
// pending upgrades before acting -- this struct alone is not a trust
// boundary, that local check is.
type Action struct {
	ID        string     `json:"id"`
	Type      ActionType `json:"type"`
	Packages  []string   `json:"packages,omitempty"`
	CreatedAt time.Time  `json:"created_at"`

	// Component and TargetVersion are only set for ActionSelfUpdate --
	// which of update-detector's own three binaries to update, and which
	// release tag to update it to.
	Component     string `json:"component,omitempty"`
	TargetVersion string `json:"target_version,omitempty"`
}

// ActionResult is what a companion reports back after attempting an Action.
type ActionResult struct {
	ActionID    string    `json:"action_id"`
	Success     bool      `json:"success"`
	Message     string    `json:"message,omitempty"`
	CompletedAt time.Time `json:"completed_at"`
}

const actionLogLimit = 20

var (
	ErrCompanionNotConnected = errors.New("no companion connected for this agent")
	ErrCompanionRequired     = errors.New("connected via agent only -- install the companion to enable this action")
	ErrActionInFlight        = errors.New("agent already has an action in flight")
)

// streamEntry is one connected stream (either the agent or the companion,
// never both -- see CompanionHub.Connect) for a given agent ID.
type streamEntry struct {
	ch         chan Action
	kind       ClientKind
	superseded chan struct{} // closed exactly once, when a higher-priority Connect preempts this entry
}

// CompanionHub tracks which agents currently have a live stream (from
// either the host-native companion or the agent itself -- exactly one at
// a time), which agent (if any) has an action currently in flight, and a
// capped in-memory log of recent action results per agent. Nothing here
// is persisted to disk (unlike Registry) -- a client just reconnects and
// its "connected" state rebuilds itself, and losing the result log across
// a restart is an acceptable trade-off for not needing a database.
type CompanionHub struct {
	mu       sync.Mutex
	streams  map[string]streamEntry
	versions map[string]string // agentID -> last-reported companion version
	pending  map[string]string // agentID -> in-flight action ID
	results  map[string][]ActionResult
}

func NewCompanionHub() *CompanionHub {
	return &CompanionHub{
		streams:  map[string]streamEntry{},
		versions: map[string]string{},
		pending:  map[string]string{},
		results:  map[string][]ActionResult{},
	}
}

// ConnectResult is the outcome of a Connect call.
type ConnectResult struct {
	// Accepted is false when a companion already holds the slot and kind
	// is KindAgent -- companion always outranks agent, since only it can
	// ever carry out an apply-type action. The caller must reject the
	// connection (e.g. HTTP 409) without registering anything.
	Accepted bool
	// Ch is where pushed Actions arrive. Only valid when Accepted.
	Ch chan Action
	// Superseded is closed exactly once if a higher-priority Connect
	// later preempts this entry (kind=agent, then a companion connects).
	// The holder must treat this the same as its own context being
	// done: stop, emit a distinguishing signal if it can (e.g. one final
	// SSE event), and return. Only valid when Accepted.
	Superseded <-chan struct{}
}

// Connect registers agentID as having a live stream, with the following
// arbitration (companion always outranks agent -- see ActionType.requiresCompanion):
//   - no existing entry: accept unconditionally, either kind.
//   - existing kind == new kind (a reconnect, e.g. after a restart):
//     replace as before, no signal fired -- this is not a priority change.
//   - existing=agent, new=companion: companion preempts. The old entry's
//     superseded channel is closed so its holder notices and tears down.
//   - existing=companion, new=agent: rejected outright; the existing
//     companion stream is left untouched.
//
// companionVersion is only recorded when kind is KindCompanion -- it has
// no meaning for an agent-only connection, and must not clobber the last
// real companion version the admin page shows.
func (h *CompanionHub) Connect(agentID string, kind ClientKind, companionVersion string) ConnectResult {
	h.mu.Lock()
	defer h.mu.Unlock()

	existing, hasExisting := h.streams[agentID]
	if hasExisting && existing.kind == KindCompanion && kind == KindAgent {
		return ConnectResult{Accepted: false}
	}

	entry := streamEntry{
		ch:         make(chan Action, 4),
		kind:       kind,
		superseded: make(chan struct{}),
	}
	if hasExisting && existing.kind != kind {
		close(existing.superseded)
	}
	h.streams[agentID] = entry
	if kind == KindCompanion {
		h.versions[agentID] = companionVersion
	}
	return ConnectResult{Accepted: true, Ch: entry.ch, Superseded: entry.superseded}
}

// CompanionVersion returns the last-reported companion version for
// agentID, or "" if none has ever connected.
func (h *CompanionHub) CompanionVersion(agentID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.versions[agentID]
}

// Kind returns the kind of the currently connected stream for agentID, if
// any.
func (h *CompanionHub) Kind(agentID string) (ClientKind, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.streams[agentID]
	return entry.kind, ok
}

// Disconnect removes agentID's stream, but only if ch is still the current
// one -- guards against a stale connection's teardown clobbering a newer
// one that already reconnected (or preempted it) in the meantime. Also
// clears any in-flight marker for this agent: once disconnected, that
// stream isn't going to report a result for it either way, and
// permanently blocking future actions because of a run that will never
// resolve would be worse than occasionally letting a stale in-flight
// action get superseded.
func (h *CompanionHub) Disconnect(agentID string, ch chan Action) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.streams[agentID]; ok && cur.ch == ch {
		delete(h.streams, agentID)
		delete(h.pending, agentID)
	}
}

func (h *CompanionHub) Connected(agentID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.streams[agentID]
	return ok
}

// Push sends action down agentID's stream. Returns ErrCompanionNotConnected
// if nothing is connected for that agent, ErrCompanionRequired if the only
// connection is agent-kind and action needs a real companion,
// or ErrActionInFlight if that agent already has an unresolved action --
// actions are processed one at a time per agent anyway, so a second apply
// before the first resolves would just queue up redundant work (and,
// since apt-get itself is idempotent, mostly just duplicate "update
// applied" notifications once it finally runs).
func (h *CompanionHub) Push(agentID string, action Action) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	entry, ok := h.streams[agentID]
	if !ok {
		return ErrCompanionNotConnected
	}
	if action.Type.requiresCompanion() && entry.kind != KindCompanion {
		return ErrCompanionRequired
	}
	if _, inFlight := h.pending[agentID]; inFlight {
		return ErrActionInFlight
	}

	select {
	case entry.ch <- action:
		h.pending[agentID] = action.ID
		return nil
	default:
		return errors.New("companion stream busy, action dropped")
	}
}

// RecordResult appends result to agentID's capped action log, and clears
// the in-flight marker Push set for this action -- but only if it's still
// the current one, so a stale/duplicate result for an already-superseded
// action can't clobber a newer in-flight marker.
func (h *CompanionHub) RecordResult(agentID string, result ActionResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending[agentID] == result.ActionID {
		delete(h.pending, agentID)
	}
	log := append(h.results[agentID], result)
	if len(log) > actionLogLimit {
		log = log[len(log)-actionLogLimit:]
	}
	h.results[agentID] = log
}

// Results returns a snapshot of agentID's recent action results, oldest
// first.
func (h *CompanionHub) Results(agentID string) []ActionResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ActionResult(nil), h.results[agentID]...)
}

func newActionID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic("aggregator: reading random bytes: " + err.Error())
	}
	return hex.EncodeToString(buf)
}
