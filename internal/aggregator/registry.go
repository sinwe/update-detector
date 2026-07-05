// Package aggregator implements the central service that per-host agents
// push their status to: a trust-on-first-contact enrollment/approval flow,
// and read-only endpoints for Homepage widgets.
package aggregator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"update-detector/internal/checker"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

// AgentRecord is one agent's registry entry. TokenHash is a sha256 digest —
// the plaintext token is never stored here.
type AgentRecord struct {
	ID         string          `json:"id"`
	Hostname   string          `json:"hostname"`
	TokenHash  string          `json:"token_hash"`
	Status     Status          `json:"status"`
	FirstSeen  time.Time       `json:"first_seen"`
	LastSeen   time.Time       `json:"last_seen,omitempty"`
	LastReport *checker.Status `json:"last_report,omitempty"`
}

var ErrNotFound = errors.New("agent not found")

// Registry holds every known agent in memory, guarded by a mutex, and
// rewrites the whole backing file atomically on every mutation. No database
// — appropriate for a fleet of a handful to dozens of hosts.
type Registry struct {
	mu     sync.RWMutex
	path   string
	agents map[string]*AgentRecord
}

func NewRegistry(path string) *Registry {
	return &Registry{path: path, agents: map[string]*AgentRecord{}}
}

// Load reads the backing file if it exists. Safe to call once at startup;
// a missing file just means an empty registry.
func (r *Registry) Load() error {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("registry: reading %s: %w", r.path, err)
	}
	var agents map[string]*AgentRecord
	if err := json.Unmarshal(data, &agents); err != nil {
		return fmt.Errorf("registry: parsing %s: %w", r.path, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = agents
	return nil
}

// saveLocked persists the registry; callers must hold r.mu for writing.
func (r *Registry) saveLocked() error {
	data, err := json.MarshalIndent(r.agents, "", "  ")
	if err != nil {
		return fmt.Errorf("registry: encoding: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("registry: creating directory for %s: %w", r.path, err)
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("registry: writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("registry: renaming %s to %s: %w", tmp, r.path, err)
	}
	return nil
}

// EnrollOutcome tells the HTTP handler which response to send.
type EnrollOutcome int

const (
	EnrollCreatedPending EnrollOutcome = iota
	EnrollAlreadyKnown
	EnrollConflict
)

// Enroll registers a new agent as pending, or — if the id is already
// known — confirms the token matches (idempotent re-announce) or reports a
// conflict if it doesn't (a different token claiming an existing id).
func (r *Registry) Enroll(id, hostname, token string) (EnrollOutcome, Status, error) {
	hash := hashToken(token)

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.agents[id]
	if !ok {
		rec := &AgentRecord{
			ID:        id,
			Hostname:  hostname,
			TokenHash: hash,
			Status:    StatusPending,
			FirstSeen: time.Now(),
		}
		r.agents[id] = rec
		if err := r.saveLocked(); err != nil {
			return EnrollCreatedPending, StatusPending, err
		}
		return EnrollCreatedPending, StatusPending, nil
	}

	if !tokensMatch(hash, existing.TokenHash) {
		return EnrollConflict, "", nil
	}

	existing.Hostname = hostname
	if err := r.saveLocked(); err != nil {
		return EnrollAlreadyKnown, existing.Status, err
	}
	return EnrollAlreadyKnown, existing.Status, nil
}

// ReportOutcome tells the HTTP handler which response to send.
type ReportOutcome int

const (
	ReportUnknownAgent ReportOutcome = iota
	ReportUnauthorized
	ReportNotApproved
	ReportAccepted
)

// Report records status as id's latest report, if id is a known, approved
// agent presenting the correct token.
func (r *Registry) Report(id, token string, status checker.Status) (ReportOutcome, error) {
	hash := hashToken(token)

	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.agents[id]
	if !ok {
		return ReportUnknownAgent, nil
	}
	if !tokensMatch(hash, rec.TokenHash) {
		return ReportUnauthorized, nil
	}
	if rec.Status != StatusApproved {
		return ReportNotApproved, nil
	}

	rec.LastSeen = time.Now()
	statusCopy := status
	rec.LastReport = &statusCopy
	if err := r.saveLocked(); err != nil {
		return ReportAccepted, err
	}
	return ReportAccepted, nil
}

// SetStatus transitions an existing agent's approval status (approve,
// reject, or revoke-by-rejecting an already-approved agent).
func (r *Registry) SetStatus(id string, status Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.agents[id]
	if !ok {
		return ErrNotFound
	}
	rec.Status = status
	return r.saveLocked()
}

// Get returns a snapshot of one agent by id.
func (r *Registry) Get(id string) (AgentRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.agents[id]
	if !ok {
		return AgentRecord{}, false
	}
	return *rec, true
}

// List returns a snapshot of every agent, sorted by hostname.
func (r *Registry) List() []AgentRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]AgentRecord, 0, len(r.agents))
	for _, rec := range r.agents {
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	return out
}

// FindApprovedByHostname returns the most-recently-seen approved agent
// claiming hostname, if any.
func (r *Registry) FindApprovedByHostname(hostname string) (AgentRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var best *AgentRecord
	for _, rec := range r.agents {
		if rec.Status != StatusApproved || rec.Hostname != hostname {
			continue
		}
		if best == nil || rec.LastSeen.After(best.LastSeen) {
			best = rec
		}
	}
	if best == nil {
		return AgentRecord{}, false
	}
	return *best, true
}
