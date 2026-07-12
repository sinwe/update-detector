package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorclient"
)

// ReportResult posts the outcome of a previously received Action back to
// the aggregator.
func ReportResult(ctx context.Context, aggregatorURL string, identity aggregatorclient.Identity, result aggregator.ActionResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("companion: encoding result: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, aggregatorURL+"/companion/result", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("companion: building result request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", identity.AgentID)
	req.Header.Set("Authorization", "Bearer "+identity.Token)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("companion: result request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("companion: result: unexpected status %s", resp.Status)
	}
	return nil
}
