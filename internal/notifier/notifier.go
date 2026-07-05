// Package notifier defines the extension point for alerting channels.
// Telegram is the only implementation today; adding another channel (Slack,
// email, a generic webhook, ...) means implementing the Notifier interface
// and registering it in cmd/update-detector/main.go — nothing else changes.
package notifier

import (
	"context"
	"log"

	"update-detector/internal/checker"
)

// Event describes a meaningful change worth notifying about.
type Event struct {
	Hostname string
	Status   checker.Status
	Previous *checker.Status
	Changes  []string // human-readable, e.g. "3 new package updates available (1 security)"
}

// Notifier delivers an Event to some external channel.
type Notifier interface {
	Name() string
	Send(ctx context.Context, ev Event) error
}

// Manager fans an Event out to every configured Notifier. A failure in one
// channel is logged, not propagated, so it doesn't block the others.
type Manager struct {
	notifiers []Notifier
}

func NewManager(notifiers ...Notifier) *Manager {
	return &Manager{notifiers: notifiers}
}

func (m *Manager) Send(ctx context.Context, ev Event) {
	for _, n := range m.notifiers {
		if err := n.Send(ctx, ev); err != nil {
			log.Printf("notifier %s: send failed: %v", n.Name(), err)
		}
	}
}
