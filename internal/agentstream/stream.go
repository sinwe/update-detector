// Package agentstream is the shared SSE client for the aggregator's
// /companion/stream endpoint, used by both the agent (cmd/update-detector)
// and the companion (cmd/update-detector-companion) -- whichever of the
// two is running holds this connection; see internal/aggregator's
// CompanionHub for the server-side arbitration that enforces exactly one
// at a time.
package agentstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorclient"
	"update-detector/internal/version"
)

const (
	maxStreamBackoff = 60 * time.Second
	// supersededRetryInterval is the fixed cadence used while a
	// higher-priority connection (companion, if this is the agent) holds
	// the slot -- deliberately not part of the exponential backoff, since
	// being superseded isn't a worsening transient failure, it's a
	// steady state that should be checked periodically, at roughly the
	// same cadence as the aggregator's own heartbeat so reclaiming the
	// slot after the companion dies stays fast.
	supersededRetryInterval = 30 * time.Second
)

// errSuperseded means a higher-priority connection already holds (or just
// took over) this agent ID's stream slot -- signaled either by an
// immediate 409 on connect, or by an "event: superseded" frame on an
// already-open stream that just got preempted.
var errSuperseded = errors.New("superseded by a higher-priority connection")

// Run connects to the aggregator's stream and invokes onAction for each
// Action received, reconnecting for as long as ctx is not done. Ordinary
// transient errors use exponential backoff (capped at maxStreamBackoff,
// reset on a clean run); being superseded uses a separate, non-escalating
// cadence (see supersededRetryInterval) -- see nextRetryDelay. Blocks
// until ctx is done.
func Run(ctx context.Context, aggregatorURL string, identity aggregatorclient.Identity, kind aggregator.ClientKind, onAction func(aggregator.Action)) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := runOnce(ctx, aggregatorURL, identity, kind, onAction)
		if err != nil && ctx.Err() != nil {
			return
		}

		wait, next := nextRetryDelay(err, backoff)
		backoff = next
		if err != nil {
			if errors.Is(err, errSuperseded) {
				log.Printf("%s: superseded by a higher-priority connection, retrying in %s", kind, wait)
			} else {
				log.Printf("%s: stream disconnected (%v), reconnecting in %s", kind, err, wait)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// nextRetryDelay picks how long to wait before the next connection
// attempt, and the backoff value to carry into the round after that. A
// clean run (err == nil) resets to a fast 1s retry. A superseded run
// holds steady at supersededRetryInterval without escalating -- it's not
// a failure that's getting worse, just "something else still holds this."
// Anything else grows exponentially, capped at maxStreamBackoff, exactly
// as before this package existed.
func nextRetryDelay(err error, backoff time.Duration) (wait, nextBackoff time.Duration) {
	switch {
	case err == nil:
		return time.Second, time.Second
	case errors.Is(err, errSuperseded):
		return supersededRetryInterval, backoff
	default:
		next := backoff * 2
		if next > maxStreamBackoff {
			next = maxStreamBackoff
		}
		return backoff, next
	}
}

func runOnce(ctx context.Context, aggregatorURL string, identity aggregatorclient.Identity, kind aggregator.ClientKind, onAction func(aggregator.Action)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, aggregatorURL+"/companion/stream", nil)
	if err != nil {
		return fmt.Errorf("building stream request: %w", err)
	}
	req.Header.Set("X-Agent-ID", identity.AgentID)
	req.Header.Set("Authorization", "Bearer "+identity.Token)
	req.Header.Set("X-Client-Kind", string(kind))
	req.Header.Set("X-Companion-Version", version.Version)

	// Deliberately no client Timeout: this response is a long-lived SSE
	// stream by design. ctx governs cancellation instead.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return errSuperseded
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	log.Printf("%s: connected to aggregator stream", kind)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	currentEvent := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			currentEvent = "" // blank line ends an SSE frame
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if currentEvent == "superseded" {
				return errSuperseded
			}
			var action aggregator.Action
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &action); err != nil {
				log.Printf("%s: ignoring malformed action: %v", kind, err)
				continue
			}
			onAction(action)
		}
		// anything else (e.g. ": heartbeat" comments) is ignored.
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stream: %w", err)
	}
	return nil // server closed the stream cleanly (e.g. restart)
}
