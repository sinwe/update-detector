//go:build !windows

package companion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"update-detector/internal/aggregator"
)

// companionSelfUpdate on Linux runs install.sh directly -- the companion
// process survives the rename (inode semantics), so install.sh can stop
// the companion, replace the binary, and restart it without killing
// install.sh itself.
func companionSelfUpdate(ctx context.Context, action aggregator.Action) aggregator.ActionResult {
	fail := func(format string, args ...any) aggregator.ActionResult {
		return aggregator.ActionResult{ActionID: action.ID, Message: fmt.Sprintf(format, args...), CompletedAt: time.Now()}
	}
	succeed := func(msg string) aggregator.ActionResult {
		return aggregator.ActionResult{ActionID: action.ID, Success: true, Message: msg, CompletedAt: time.Now()}
	}
	if err := installNative(ctx, "companion", action.TargetVersion); err != nil {
		return fail("%v", err)
	}
	return succeed("update installed, restarting")
}

// installShPath is where a cached copy of install.sh lives, refreshed
// whenever the companion itself updates (see cmd/update-detector-companion's
// own self-update handling) -- shelling out to it reuses its already
// battle-tested download/atomic-swap/systemctl-restart logic instead of
// duplicating any of it here. A var, not a const, so tests can point it
// at a fake script.
var installShPath = "/usr/local/lib/update-detector/install.sh"

// envFileDir is where install.sh writes agent/aggregator env files -- a
// var, not a const, purely so tests can point it at a temp dir.
var envFileDir = "/etc/default"

// installNative re-invokes install.sh non-interactively to update
// component to targetVersion. Passes the component's *existing*
// configuration through as env vars first -- install_agent_native and
// install_aggregator_native always regenerate their env file from
// scratch from whatever's in the invoking environment (by design, for a
// fresh install), so without this, a self-update would silently reset
// AGGREGATOR_URL, ADMIN_APPLY_SHARED_SECRET, Telegram tokens, etc. back
// to their defaults on every use -- confirmed live: a first pass at this
// wiped a configured ADMIN_APPLY_SHARED_SECRET clean off an aggregator
// it had just updated.
func installNative(ctx context.Context, component, targetVersion string) error {
	cmd := exec.CommandContext(ctx, "sh", installShPath)
	cmd.Env = append(append(os.Environ(), existingConfigEnv(component)...),
		"INSTALL_COMPONENTS="+component,
		"INSTALL_VERSION="+targetVersion,
	)
	out, err := runCapped(ctx, cmd)
	if err != nil {
		return fmt.Errorf("selfupdate: install.sh failed: %w\n%s", err, out)
	}
	return nil
}

// existingConfigEnv reads component's current env file (if any) and
// translates its values back into the *input* variable names
// install_agent_native/install_aggregator_native actually read (not
// always the same names -- e.g. the aggregator's own install.sh input is
// AGGREGATOR_LISTEN_ADDR, but the env file it writes says LISTEN_ADDR;
// see install.sh's own env-file heredocs for the authoritative mapping).
// Companion has no env file at all (its unit sets Environment= directly)
// so this is a no-op for that component. Missing file or unreadable
// value -> that key just falls back to install.sh's own hardcoded
// default, same as a genuinely fresh install.
func existingConfigEnv(component string) []string {
	switch component {
	case "agent":
		// Agent's own input names already match its env file's output
		// names 1:1 (no prefix) -- only STATE_DIR needs deriving back
		// from a file path, the same way install.sh's own
		// uninstall_agent already does.
		values := readEnvFile(filepath.Join(envFileDir, "update-detector"))
		env := passThrough(values, "LISTEN_ADDR", "HOSTNAME_OVERRIDE", "CHECK_INTERVAL",
			"TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "AGGREGATOR_URL")
		if dir := filepath.Dir(values["AGENT_IDENTITY_FILE"]); dir != "" && dir != "." && dir != "/" {
			env = append(env, "STATE_DIR="+dir)
		}
		return env
	case "aggregator":
		values := readEnvFile(filepath.Join(envFileDir, "update-aggregator"))
		env := translate(values, map[string]string{
			"LISTEN_ADDR":        "AGGREGATOR_LISTEN_ADDR",
			"TELEGRAM_BOT_TOKEN": "AGGREGATOR_TELEGRAM_BOT_TOKEN",
			"TELEGRAM_CHAT_ID":   "AGGREGATOR_TELEGRAM_CHAT_ID",
		})
		env = append(env, passThrough(values, "ADMIN_APPLY_SHARED_SECRET")...)
		if dir := filepath.Dir(values["REGISTRY_FILE"]); dir != "" && dir != "." && dir != "/" {
			env = append(env, "AGGREGATOR_DATA_DIR="+dir)
		}
		return env
	default:
		return nil
	}
}

// readEnvFile parses a simple KEY=value-per-line file (exactly what
// install.sh's own heredocs write -- no quoting, no multi-line values)
// into a map. Returns an empty map, not an error, if the file doesn't
// exist or can't be read -- a self-update on a host with no prior config
// file just falls back to install.sh's own defaults entirely.
func readEnvFile(path string) map[string]string {
	values := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[key] = val
	}
	return values
}

