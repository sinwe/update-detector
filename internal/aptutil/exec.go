package aptutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"update-detector/internal/checker"
	"update-detector/internal/linetee"
)

// Update runs `apt-get update` against the given apt.conf (see Write),
// refreshing the container-local package index cache. Shared by every
// checker flavor. If ctx carries a line sink (see checker.WithLineSink),
// this command's real stdout/stderr are also tapped live, one line at a
// time -- purely additive, e.g. for a UI-triggered verbose recheck; the
// normal periodic detection cycle never attaches one, so this behaves
// exactly as before in that case.
func Update(ctx context.Context, aptConfigPath string) error {
	cmd := exec.CommandContext(ctx, "apt-get", "update", "-q", "-o", "Acquire::Retries=2")
	cmd.Env = Env(aptConfigPath)
	var stderr bytes.Buffer
	var out, errOut io.Writer = io.Discard, &stderr
	if sink := checker.LineSinkFromContext(ctx); sink != nil {
		// Both point at the same *linetee.Writer value (not two separate
		// tees, one per stream) -- os/exec only guarantees single-
		// goroutine-at-a-time writes when Stdout and Stderr are the same
		// comparable writer, and sink itself relies on that guarantee
		// (see OutputSink.Push). Side effect only while verbose streaming
		// is active: stdout's routine progress output also lands in the
		// stderr buffer below, so a failure's error message may include
		// some of it too -- an acceptable trade-off for a live/best-effort
		// view, not the normal (no sink) path this function's callers see.
		tee := linetee.New(&stderr, sink)
		defer tee.Flush()
		out, errOut = tee, tee
	}
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("apt-get update: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Env returns the environment for any apt-get/apt-check/dpkg invocation
// using the given apt.conf.
func Env(aptConfigPath string) []string {
	return append(os.Environ(),
		"APT_CONFIG="+aptConfigPath,
		"DEBIAN_FRONTEND=noninteractive",
	)
}
