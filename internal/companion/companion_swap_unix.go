//go:build !windows

package companion

import (
	"context"
	"time"

	"update-detector/internal/aggregator"
)

// CompleteCompanionSwap is a no-op on Linux. On Linux, the companion
// self-updates via install.sh (which can stop/restart the companion
// without killing itself, thanks to inode semantics). The agent runs
// inside Docker and cannot access the host filesystem or manage host
// services, so it cannot perform the swap. ActionCompleteCompanionSwap
// should never be pushed to a Linux agent.
func CompleteCompanionSwap(_ context.Context, action aggregator.Action) aggregator.ActionResult {
	return aggregator.ActionResult{
		ActionID:    action.ID,
		Success:     false,
		Message:     "companion swap not supported on this platform",
		CompletedAt: time.Now(),
	}
}
