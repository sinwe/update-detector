// Package companion implements the host-native process that receives
// upgrade-trigger Actions from the aggregator over SSE and applies them via
// apt-get, after independently validating each one against this host's own
// last-known pending upgrades. It never runs inside Docker -- unlike
// update-detector and update-aggregator, it needs real root on the host to
// install packages.
package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"update-detector/internal/aggregatorclient"
)

// FetchIdentity retrieves the local agent's identity over its Unix-socket
// token endpoint (see internal/companiontoken). The companion never
// persists this itself -- callers should hold it only in memory and
// re-fetch on every restart.
func FetchIdentity(ctx context.Context, socketPath string) (aggregatorclient.Identity, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/companion/token", nil)
	if err != nil {
		return aggregatorclient.Identity{}, fmt.Errorf("companion: building token request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return aggregatorclient.Identity{}, fmt.Errorf("companion: fetching token from %s: %w", socketPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return aggregatorclient.Identity{}, fmt.Errorf("companion: token endpoint returned %s", resp.Status)
	}

	var identity aggregatorclient.Identity
	if err := json.NewDecoder(resp.Body).Decode(&identity); err != nil {
		return aggregatorclient.Identity{}, fmt.Errorf("companion: decoding token response: %w", err)
	}
	return identity, nil
}

// FetchIdentityWithRetry retries FetchIdentity with exponential backoff
// (capped at maxBackoff) until it succeeds or ctx is done -- the agent's
// container may not be up yet when the companion's systemd unit starts.
func FetchIdentityWithRetry(ctx context.Context, socketPath string, maxBackoff time.Duration) (aggregatorclient.Identity, error) {
	backoff := time.Second
	for {
		identity, err := FetchIdentity(ctx, socketPath)
		if err == nil {
			return identity, nil
		}
		log.Printf("companion: waiting for agent token (%v), retrying in %s", err, backoff)

		select {
		case <-ctx.Done():
			return aggregatorclient.Identity{}, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
