package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"update-detector/internal/aggregator"
	"update-detector/internal/checker"
)

const outputTruncateLimit = 4000

// Apply independently re-validates action against agentStatusURL's current
// pending upgrades before running anything. This local check -- not the
// SSE connection's direction -- is the real safety property: even a fully
// compromised aggregator can at most force early application of upgrades
// this host already, independently, considers pending, never arbitrary
// command execution. Never reboots, even if the upgrade sets
// reboot_required.
func Apply(ctx context.Context, agentStatusURL string, action aggregator.Action) aggregator.ActionResult {
	// Recheck never touches the package manager or the pending-packages
	// list at all -- it just asks the agent to refresh itself sooner, so
	// it skips straight to that rather than going through local validation.
	if action.Type == aggregator.ActionRecheck {
		triggerRecheck(ctx, agentStatusURL)
		return aggregator.ActionResult{
			ActionID:    action.ID,
			Success:     true,
			Message:     "recheck triggered",
			CompletedAt: time.Now(),
		}
	}

	// Self-update replaces a binary and restarts a service -- none of
	// that involves the package manager, and none of the pending-packages
	// validation below applies to it either.
	if action.Type == aggregator.ActionSelfUpdate {
		return SelfUpdate(ctx, action)
	}

	applier, err := applierFor()
	if err != nil {
		return aggregator.ActionResult{
			ActionID:    action.ID,
			Message:     err.Error(),
			CompletedAt: time.Now(),
		}
	}

	status, err := fetchLocalStatus(ctx, agentStatusURL)
	if err != nil {
		return aggregator.ActionResult{
			ActionID:    action.ID,
			Message:     fmt.Sprintf("could not verify local status: %v", err),
			CompletedAt: time.Now(),
		}
	}

	if action.Type == aggregator.ActionPackages {
		if missing := missingFromPending(action.Packages, status); len(missing) > 0 {
			return aggregator.ActionResult{
				ActionID:    action.ID,
				Message:     fmt.Sprintf("rejected: %v not currently pending on this host", missing),
				CompletedAt: time.Now(),
			}
		}
	}

	var output string
	switch action.Type {
	case aggregator.ActionPackages:
		output, err = applier.Packages(ctx, action.Packages)
	case aggregator.ActionUpgrade:
		output, err = applier.Upgrade(ctx)
	case aggregator.ActionFullUpgrade:
		output, err = applier.FullUpgrade(ctx)
	default:
		return aggregator.ActionResult{
			ActionID:    action.ID,
			Message:     fmt.Sprintf("unknown action type %q", action.Type),
			CompletedAt: time.Now(),
		}
	}

	if err != nil {
		// Still worth rechecking even on failure -- the package manager
		// can partially apply changes before hitting an error. Except
		// when the failure is the Applier's own metadata-refresh
		// prologue (e.g. apt-get update): nothing on the host changed in
		// that case, so a recheck would be pointless.
		if !errors.Is(err, ErrUpdateFailed) {
			triggerRecheck(ctx, agentStatusURL)
		}
		return aggregator.ActionResult{
			ActionID:    action.ID,
			Message:     fmt.Sprintf("%s failed: %v\n%s", action.Type, err, output),
			CompletedAt: time.Now(),
		}
	}

	// Best-effort, non-fatal: makes the agent's own /status (and its next
	// report to the aggregator) reflect this apply immediately, instead of
	// showing the just-applied package as still pending for up to a full
	// CHECK_INTERVAL.
	triggerRecheck(ctx, agentStatusURL)

	return aggregator.ActionResult{
		ActionID:    action.ID,
		Success:     true,
		Message:     fmt.Sprintf("%s succeeded\n%s", action.Type, output),
		CompletedAt: time.Now(),
	}
}

// triggerRecheck asks the local agent to run an out-of-band detection
// cycle via POST .../recheck (derived from agentStatusURL, which points at
// .../status). Best-effort -- errors are deliberately ignored, since the
// agent will still re-check on its own regular schedule regardless.
func triggerRecheck(ctx context.Context, agentStatusURL string) {
	url := strings.TrimSuffix(agentStatusURL, "/status") + "/recheck"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// runCapped runs cmd, capturing combined stdout+stderr into the returned
// string (truncated to the last outputTruncateLimit bytes). If ctx carries
// an *OutputSink (see WithOutputSink), each complete line is also tapped
// live as it's written -- purely additive, the returned string is
// identical either way. cmd.Stdout and cmd.Stderr are always set to the
// exact same writer value (whichever one that is), preserving os/exec's
// single-writer-at-a-time guarantee that lineTee itself relies on.
// runCappedImpl is the real implementation used by default. Tests may
// replace the package-level runCapped variable with a mock to capture and
// validate executed commands without actually running them.
func runCappedImpl(ctx context.Context, cmd *exec.Cmd) (string, error) {
	var buf bytes.Buffer
	var w io.Writer = &buf
	var tee *lineTee
	if sink := sinkFromContext(ctx); sink != nil {
		tee = newLineTee(&buf, sink.push)
		w = tee
	}
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	if tee != nil {
		tee.flush()
	}
	out := buf.String()
	if len(out) > outputTruncateLimit {
		out = "...(truncated)...\n" + out[len(out)-outputTruncateLimit:]
	}
	return out, err
}

// runCapped is a package-level variable referencing the implementation used to
// execute commands. Tests may override this variable to intercept command
// execution for verification.
var runCapped = runCappedImpl

func missingFromPending(requested []string, status checker.Status) []string {
	pending := map[string]bool{}
	for _, u := range status.Packages.Upgrades {
		pending[u.Name] = true
	}
	var missing []string
	for _, name := range requested {
		if !pending[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

func fetchLocalStatus(ctx context.Context, agentStatusURL string) (checker.Status, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, agentStatusURL, nil)
	if err != nil {
		return checker.Status{}, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return checker.Status{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return checker.Status{}, fmt.Errorf("unexpected status %s", resp.Status)
	}
	var status checker.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return checker.Status{}, err
	}
	return status, nil
}
