package companion

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorclient"
	"update-detector/internal/version"
)

const maxStreamBackoff = 60 * time.Second

// StreamRun connects to the aggregator's companion SSE stream and invokes
// onAction for each Action received, reconnecting with exponential backoff
// (capped at maxStreamBackoff) whenever the connection drops. Blocks until
// ctx is done.
func StreamRun(ctx context.Context, aggregatorURL string, identity aggregatorclient.Identity, onAction func(aggregator.Action)) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := streamOnce(ctx, aggregatorURL, identity, onAction)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("companion: stream disconnected (%v), reconnecting in %s", err, backoff)
		} else {
			backoff = time.Second // reset after a clean run
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if err != nil {
			backoff *= 2
			if backoff > maxStreamBackoff {
				backoff = maxStreamBackoff
			}
		}
	}
}

func streamOnce(ctx context.Context, aggregatorURL string, identity aggregatorclient.Identity, onAction func(aggregator.Action)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, aggregatorURL+"/companion/stream", nil)
	if err != nil {
		return fmt.Errorf("building stream request: %w", err)
	}
	req.Header.Set("X-Agent-ID", identity.AgentID)
	req.Header.Set("Authorization", "Bearer "+identity.Token)
	req.Header.Set("X-Companion-Version", version.Version)

	// Deliberately no client Timeout: this response is a long-lived SSE
	// stream by design. ctx governs cancellation instead.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	log.Println("companion: connected to aggregator stream")

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue // heartbeat comments (": ...") and blank frame separators
		}
		var action aggregator.Action
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &action); err != nil {
			log.Printf("companion: ignoring malformed action: %v", err)
			continue
		}
		onAction(action)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stream: %w", err)
	}
	return nil // server closed the stream cleanly (e.g. restart)
}
