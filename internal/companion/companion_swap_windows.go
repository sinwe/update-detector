//go:build windows

package companion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"update-detector/internal/aggregator"
)

// CompleteCompanionSwap performs the agent-side half of a companion
// self-update on Windows: stops the companion service, swaps the
// .exe.new file over the running binary, and restarts the companion.
// Called by the agent when it receives ActionCompleteCompanionSwap.
func CompleteCompanionSwap(ctx context.Context, action aggregator.Action) aggregator.ActionResult {
	fail := func(format string, args ...any) aggregator.ActionResult {
		return aggregator.ActionResult{ActionID: action.ID, Message: fmt.Sprintf(format, args...), CompletedAt: time.Now()}
	}
	succeed := func(msg string) aggregator.ActionResult {
		return aggregator.ActionResult{ActionID: action.ID, Success: true, Message: msg, CompletedAt: time.Now()}
	}

	newPath := companionExePath + ".new"

	// Verify the .new file exists and looks valid.
	info, err := os.Stat(newPath)
	if err != nil {
		return fail("companion update not found at %s: %v", newPath, err)
	}
	if info.Size() == 0 {
		os.Remove(newPath)
		return fail("companion update file is empty, removing")
	}

	emit := emitFromContext(ctx)

	// Stop the companion service.
	emit("Stopping update-detector-companion service...")
	if out, err := runCapped(ctx, exec.CommandContext(ctx, "sc", "stop", "update-detector-companion")); err != nil {
		return fail("stopping companion service: %v\n%s", err, out)
	}

	// Wait for the service to actually stop (up to 30s).
	emit("Waiting for companion service to stop...")
	for i := 0; i < 30; i++ {
		out, _ := runCapped(ctx, exec.CommandContext(ctx, "sc", "query", "update-detector-companion"))
		if strings.Contains(out, "STOPPED") {
			break
		}
		time.Sleep(time.Second)
	}

	// Swap the binary.
	emit("Swapping companion binary...")
	if err := os.Rename(newPath, companionExePath); err != nil {
		// Try to restart the companion even if the swap failed --
		// the old binary is still there.
		exec.CommandContext(ctx, "sc", "start", "update-detector-companion").Run()
		return fail("swapping companion binary: %v", err)
	}

	// Restart the companion service.
	emit("Starting update-detector-companion service...")
	if out, err := runCapped(ctx, exec.CommandContext(ctx, "sc", "start", "update-detector-companion")); err != nil {
		return fail("starting companion service: %v\n%s", err, out)
	}

	return succeed("companion updated and restarted")
}
