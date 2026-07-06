package aptutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Update runs `apt-get update` against the given apt.conf (see Write),
// refreshing the container-local package index cache. Shared by every
// checker flavor.
func Update(ctx context.Context, aptConfigPath string) error {
	cmd := exec.CommandContext(ctx, "apt-get", "update", "-q", "-o", "Acquire::Retries=2")
	cmd.Env = Env(aptConfigPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
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
