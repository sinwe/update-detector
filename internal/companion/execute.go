package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
	// Recheck never touches apt-get or the pending-packages list at all --
	// it just asks the agent to refresh itself sooner, so it skips
	// straight to that rather than going through local validation.
	if action.Type == aggregator.ActionRecheck {
		triggerRecheck(ctx, agentStatusURL)
		return aggregator.ActionResult{
			ActionID:    action.ID,
			Success:     true,
			Message:     "recheck triggered",
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

	// The companion runs directly against the host's own apt state, which
	// is separate from the containerized agent's own package-list cache
	// (see README "How it works" -- the agent never writes to the host, so
	// it maintains its own cache instead). Without refreshing the host's
	// lists here first, apt-get can see an older candidate than what the
	// agent detected and silently no-op ("already the newest version")
	// even though a real upgrade is pending -- confirmed live: base-files
	// showed 13.8+deb13u5 -> 13.8+deb13u6 pending, but the host's stale
	// apt cache still thought 13.8+deb13u5 was current.
	if updateOut, updateErr := runCapped(aptCommand(ctx, "update")); updateErr != nil {
		return aggregator.ActionResult{
			ActionID:    action.ID,
			Message:     fmt.Sprintf("apt-get update failed: %v\n%s", updateErr, updateOut),
			CompletedAt: time.Now(),
		}
	}

	var cmd *exec.Cmd
	switch action.Type {
	case aggregator.ActionPackages:
		args := append([]string{"install", "-y", "--only-upgrade"}, action.Packages...)
		cmd = aptCommand(ctx, args...)
	case aggregator.ActionUpgrade:
		cmd = aptCommand(ctx, "upgrade", "-y")
	case aggregator.ActionFullUpgrade:
		cmd = aptCommand(ctx, "dist-upgrade", "-y")
	default:
		return aggregator.ActionResult{
			ActionID:    action.ID,
			Message:     fmt.Sprintf("unknown action type %q", action.Type),
			CompletedAt: time.Now(),
		}
	}

	output, err := runCapped(cmd)
	if err != nil {
		// Still worth rechecking even on failure -- apt-get can partially
		// apply changes before hitting an error on a later package.
		triggerRecheck(ctx, agentStatusURL)
		return aggregator.ActionResult{
			ActionID:    action.ID,
			Message:     fmt.Sprintf("%s failed: %v\n%s", action.Type, err, output),
			CompletedAt: time.Now(),
		}
	}

	// Best-effort cleanup that never gates the primary result -- an
	// upgrade/dist-upgrade can leave packages autoremove would clear.
	msg := fmt.Sprintf("%s succeeded\n%s", action.Type, output)
	if autoOut, autoErr := runCapped(aptCommand(ctx, "autoremove", "-y")); autoErr != nil {
		msg += fmt.Sprintf("\napt-get autoremove failed: %v\n%s", autoErr, autoOut)
	}

	// Best-effort, non-fatal: makes the agent's own /status (and its next
	// report to the aggregator) reflect this apply immediately, instead of
	// showing the just-applied package as still pending for up to a full
	// CHECK_INTERVAL.
	triggerRecheck(ctx, agentStatusURL)

	return aggregator.ActionResult{
		ActionID:    action.ID,
		Success:     true,
		Message:     msg,
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

func aptCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "apt-get", args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	return cmd
}

func runCapped(cmd *exec.Cmd) (string, error) {
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if len(out) > outputTruncateLimit {
		out = "...(truncated)...\n" + out[len(out)-outputTruncateLimit:]
	}
	return out, err
}

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
