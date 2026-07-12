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
)

func (t ActionType) valid() bool {
	switch t {
	case ActionPackages, ActionUpgrade, ActionFullUpgrade:
		return true
	default:
		return false
	}
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
}

// ActionResult is what a companion reports back after attempting an Action.
type ActionResult struct {
	ActionID    string    `json:"action_id"`
	Success     bool      `json:"success"`
	Message     string    `json:"message,omitempty"`
	CompletedAt time.Time `json:"completed_at"`
}

const actionLogLimit = 20

var ErrCompanionNotConnected = errors.New("no companion connected for this agent")

// CompanionHub tracks which agents currently have a companion connected via
// SSE, and a capped in-memory log of recent action results per agent.
// Nothing here is persisted to disk (unlike Registry) -- a companion just
// reconnects and its "connected" state rebuilds itself, and losing the
// result log across a restart is an acceptable trade-off for not needing a
// database.
type CompanionHub struct {
	mu      sync.Mutex
	streams map[string]chan Action
	results map[string][]ActionResult
}

func NewCompanionHub() *CompanionHub {
	return &CompanionHub{
		streams: map[string]chan Action{},
		results: map[string][]ActionResult{},
	}
}

// Connect registers agentID as having a live companion stream, replacing
// any previous one for the same agent (a reconnect supersedes its
// predecessor). Callers must call Disconnect with the returned channel when
// the stream ends.
func (h *CompanionHub) Connect(agentID string) chan Action {
	ch := make(chan Action, 4)
	h.mu.Lock()
	h.streams[agentID] = ch
	h.mu.Unlock()
	return ch
}

// Disconnect removes agentID's stream, but only if ch is still the current
// one -- guards against a stale connection's teardown clobbering a newer
// one that already reconnected in the meantime.
func (h *CompanionHub) Disconnect(agentID string, ch chan Action) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.streams[agentID]; ok && cur == ch {
		delete(h.streams, agentID)
	}
}

func (h *CompanionHub) Connected(agentID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.streams[agentID]
	return ok
}

// Push sends action down agentID's stream. Returns ErrCompanionNotConnected
// if no companion is currently connected for that agent.
func (h *CompanionHub) Push(agentID string, action Action) error {
	h.mu.Lock()
	ch, ok := h.streams[agentID]
	h.mu.Unlock()
	if !ok {
		return ErrCompanionNotConnected
	}
	select {
	case ch <- action:
		return nil
	default:
		return errors.New("companion stream busy, action dropped")
	}
}

// RecordResult appends result to agentID's capped action log.
func (h *CompanionHub) RecordResult(agentID string, result ActionResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
