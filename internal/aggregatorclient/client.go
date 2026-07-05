// Package aggregatorclient is the agent-side counterpart to
// internal/aggregator: it manages a persisted per-agent identity and pushes
// enroll/report calls to a central update-aggregator.
package aggregatorclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"update-detector/internal/checker"
)

// Identity is generated once (crypto/rand) on an agent's first run and
// persisted so restarts reuse it instead of re-enrolling as a new agent.
type Identity struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`
}

// LoadOrCreateIdentity returns the agent's persisted identity, creating one
// if path doesn't exist yet.
func LoadOrCreateIdentity(path string) (Identity, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		var id Identity
		if jsonErr := json.Unmarshal(data, &id); jsonErr != nil {
			return Identity{}, fmt.Errorf("aggregatorclient: parsing %s: %w", path, jsonErr)
		}
		return id, nil
	}
	if !os.IsNotExist(err) {
		return Identity{}, fmt.Errorf("aggregatorclient: reading %s: %w", path, err)
	}

	id := Identity{
		AgentID: randomHex(16),
		Token:   randomHex(32),
	}
	if err := saveIdentity(path, id); err != nil {
		return Identity{}, err
	}
	return id, nil
}

func saveIdentity(path string, id Identity) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("aggregatorclient: creating directory for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("aggregatorclient: encoding identity: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("aggregatorclient: writing %s: %w", path, err)
	}
	return nil
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("aggregatorclient: reading random bytes: %v", err))
	}
	return hex.EncodeToString(buf)
}

type Client struct {
	baseURL  string
	identity Identity
	http     *http.Client
}

func New(baseURL string, identity Identity) *Client {
	return &Client{
		baseURL:  baseURL,
		identity: identity,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

type enrollRequest struct {
	AgentID  string `json:"agent_id"`
	Hostname string `json:"hostname"`
	Token    string `json:"token"`
}

// Enroll announces this agent to the aggregator. Safe to call on every
// startup — the aggregator treats a repeat announcement with the same
// agent_id/token as an idempotent no-op, returning the agent's current
// approval status.
func (c *Client) Enroll(ctx context.Context, hostname string) (status string, err error) {
	body, err := json.Marshal(enrollRequest{
		AgentID:  c.identity.AgentID,
		Hostname: hostname,
		Token:    c.identity.Token,
	})
	if err != nil {
		return "", fmt.Errorf("aggregatorclient: encoding enroll request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/enroll", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("aggregatorclient: building enroll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("aggregatorclient: enroll request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("aggregatorclient: enroll: unexpected status %s", resp.Status)
	}

	var out struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("aggregatorclient: decoding enroll response: %w", err)
	}
	return out.Status, nil
}

// Report pushes the current Status to the aggregator. Callers should log
// (not fail on) errors here — local detection, Gatus, and Telegram must
// keep working regardless of the aggregator's reachability.
func (c *Client) Report(ctx context.Context, status checker.Status) error {
	body, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("aggregatorclient: encoding report: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/report", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("aggregatorclient: building report request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", c.identity.AgentID)
	req.Header.Set("Authorization", "Bearer "+c.identity.Token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("aggregatorclient: report request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("aggregatorclient: report: unexpected status %s", resp.Status)
	}
	return nil
}
