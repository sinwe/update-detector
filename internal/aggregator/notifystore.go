package aggregator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HostNotifyConfig is one host's own Telegram delivery target: the bot
// it sends through and the chat that receives its messages. This exists
// so hosts owned by different people can each notify their own chat,
// without every agent having to carry TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID
// in its own environment (which is exactly how the Pi went silent — see
// docs/reference.md's per-host notification section).
//
// Keyed by agent ID in the store, never by hostname: hostnames are
// operator-chosen and can change, the agent ID is stable for the life of
// the enrollment.
type HostNotifyConfig struct {
	// BotToken is a Telegram bot token from @BotFather. Stored as-is
	// (it must be usable to send), masked in the UI, and written to a
	// 0600 file.
	BotToken string `json:"bot_token"`
	// ChatID is the destination chat: a positive user id for a DM, or
	// a negative id (-100...) for a group/supergroup/channel.
	ChatID string `json:"chat_id"`
	// Enabled turns this host's own delivery on without forgetting the
	// token/chat id (so it can be paused and resumed).
	Enabled bool `json:"enabled"`
	// UpdatedAt is when an operator last saved this entry, for the
	// "configured ..." line on the page.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Configured reports whether both halves of a usable target are present,
// regardless of Enabled.
func (c HostNotifyConfig) Configured() bool {
	return c.BotToken != "" && c.ChatID != ""
}

// Active reports whether this host should actually be delivered to:
// configured and switched on.
func (c HostNotifyConfig) Active() bool {
	return c.Enabled && c.Configured()
}

// MaskedToken renders the bot token for display: the bot's own numeric id
// (the part before the colon, which is not secret) plus the last four
// characters, so an operator can tell two tokens apart without the page
// ever echoing a usable credential back.
func (c HostNotifyConfig) MaskedToken() string {
	return maskBotToken(c.BotToken)
}

func maskBotToken(token string) string {
	if token == "" {
		return ""
	}
	id, secret, ok := strings.Cut(token, ":")
	if !ok {
		if len(token) <= 4 {
			return "…"
		}
		return "…" + token[len(token)-4:]
	}
	if len(secret) <= 4 {
		return id + ":…"
	}
	return id + ":…" + secret[len(secret)-4:]
}

// NotifyStore persists per-host Telegram targets to a single JSON file
// next to the registry (same atomic temp-file + rename discipline as
// Registry/AlertStore). Unlike the agent tokens in the registry — which
// are only ever stored hashed — a bot token has to stay readable to be
// usable for sending, so the file is written 0600 and the UI never shows
// it back in full.
type NotifyStore struct {
	mu    sync.RWMutex
	path  string
	hosts map[string]HostNotifyConfig
}

func NewNotifyStore(path string) *NotifyStore {
	return &NotifyStore{path: path, hosts: map[string]HostNotifyConfig{}}
}

// Load reads the backing file. A missing file is not an error (no host
// has per-host notifications yet); a corrupt one is, same as Registry —
// silently discarding delivery targets would be worse than refusing to
// start.
func (s *NotifyStore) Load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("notify: reading %s: %w", s.path, err)
	}
	hosts := map[string]HostNotifyConfig{}
	if err := json.Unmarshal(data, &hosts); err != nil {
		return fmt.Errorf("notify: parsing %s: %w", s.path, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts = hosts
	return nil
}

// Get returns one host's config, if any.
func (s *NotifyStore) Get(id string) (HostNotifyConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.hosts[id]
	return cfg, ok
}

// Set stores (or replaces) one host's config and persists the file. The
// caller is responsible for validating the token/chat id shapes — the
// store only refuses an entry that has no target at all.
func (s *NotifyStore) Set(id string, cfg HostNotifyConfig) error {
	if !cfg.Configured() {
		return fmt.Errorf("notify: both bot token and chat id are required")
	}
	cfg.UpdatedAt = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts[id] = cfg
	return s.saveLocked()
}

// Delete forgets one host's config (the "Clear" button) and persists.
// Deleting an unknown id is a no-op, not an error.
func (s *NotifyStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[id]; !ok {
		return nil
	}
	delete(s.hosts, id)
	return s.saveLocked()
}

// Snapshot returns a copy of every entry, for the admin page.
func (s *NotifyStore) Snapshot() map[string]HostNotifyConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]HostNotifyConfig, len(s.hosts))
	for id, cfg := range s.hosts {
		out[id] = cfg
	}
	return out
}

// saveLocked persists the map; callers must hold s.mu for writing.
func (s *NotifyStore) saveLocked() error {
	data, err := json.MarshalIndent(s.hosts, "", "  ")
	if err != nil {
		return fmt.Errorf("notify: encoding: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("notify: creating directory for %s: %w", s.path, err)
	}
	tmp := s.path + ".tmp"
	// 0600: this file holds live bot tokens.
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("notify: writing %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("notify: chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("notify: renaming %s to %s: %w", tmp, s.path, err)
	}
	return nil
}
