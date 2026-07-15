package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"update-detector/internal/aggregatorclient"
)

// outputFrame is one line of an action's live output, newline-delimited
// JSON in the request body StreamOutput POSTs.
type outputFrame struct {
	ActionID string `json:"action_id"`
	Line     string `json:"line"`
}

// StreamOutput posts sink's lines to the aggregator as they arrive, for
// the lifetime of actionID's action -- a live, best-effort companion to
// the existing one-shot ReportResult. Any error returned is for logging
// only: it must never be treated as the action itself having failed, and
// it must never block the command producing sink's lines (see OutputSink's
// own non-blocking push, which guarantees that upstream of this function).
//
// Deliberately no client.Timeout, and no retry/reconnect if the POST
// fails or drops mid-action -- this can legitimately run as long as a
// dist-upgrade does (same reasoning as agentstream.Run's own long-lived
// SSE client), and losing the live view for one action is an accepted
// trade-off for not duplicating that client's reconnect logic here.
func StreamOutput(ctx context.Context, aggregatorURL string, identity aggregatorclient.Identity, actionID string, sink *OutputSink) error {
	reqURL, err := url.Parse(aggregatorURL + "/companion/output")
	if err != nil {
		return fmt.Errorf("companion: parsing aggregator URL: %w", err)
	}
	q := reqURL.Query()
	q.Set("action_id", actionID)
	reqURL.RawQuery = q.Encode()

	pr, pw := io.Pipe()

	// io.Pipe has no internal buffering -- a Write blocks until a Read
	// consumes it. Closing either end immediately fails any pending (or
	// future) op on the *other* end, which is what actually unblocks a
	// stuck pw.Write below if ctx is canceled while nobody's reading (e.g.
	// the aggregator is unreachable and the dial never even completes, so
	// nothing ever calls pr.Read). Context cancellation does not do this
	// on its own -- net/http's Transport tearing down the socket has no
	// effect on a pipe that isn't the socket.
	go func() {
		<-ctx.Done()
		pw.CloseWithError(ctx.Err())
	}()

	go func() {
		defer pw.Close()
		enc := json.NewEncoder(pw)
		for line := range sink.Lines() {
			if err := enc.Encode(outputFrame{ActionID: actionID, Line: line}); err != nil {
				return
			}
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), pr)
	if err != nil {
		pr.Close()
		return fmt.Errorf("companion: building output-stream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("X-Agent-ID", identity.AgentID)
	req.Header.Set("Authorization", "Bearer "+identity.Token)

	resp, err := http.DefaultClient.Do(req)
	// pr must be closed either way: if Do failed before ever reading (e.g.
	// the dial never completed), the pump goroutine above could otherwise
	// block forever writing to a pipe nobody will ever read from again.
	pr.Close()
	if err != nil {
		return fmt.Errorf("companion: output-stream request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("companion: output-stream: unexpected status %s", resp.Status)
	}
	return nil
}
