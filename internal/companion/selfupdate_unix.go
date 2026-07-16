//go:build !windows

package companion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
		// names 1:1 (no prefix) -- only STATE_DIR needs deriving from
		// the env file's STATE_FILE (which is STATE_DIR/<hostname>.json).
		values := readEnvFile(filepath.Join(envFileDir, "update-detector"))
		env := passThrough(values, "AGGREGATOR_URL", "CHECK_INTERVAL",
			"TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID")
		if sf := values["STATE_FILE"]; sf != "" {
			env = append(env, "STATE_DIR="+filepath.Dir(sf))
		}
		return env
	case "aggregator":
		values := readEnvFile(filepath.Join(envFileDir, "update-aggregator"))
		env := passThrough(values, "ADMIN_APPLY_SHARED_SECRET",
			"TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID")
		env = append(env, translate(values, map[string]string{
			"LISTEN_ADDR":   "AGGREGATOR_LISTEN_ADDR",
			"REGISTRY_FILE": "AGGREGATOR_REGISTRY_FILE",
		})...)
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

// passThrough returns "KEY=value" env entries for each of keys present
// (and non-empty) in values, unchanged.
func passThrough(values map[string]string, keys ...string) []string {
	var env []string
	for _, k := range keys {
		if v := values[k]; v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// translate returns "newName=value" env entries, renaming each key in
// rename (oldName -> newName) that's present and non-empty in values.
func translate(values map[string]string, rename map[string]string) []string {
	var env []string
	for oldName, newName := range rename {
		if v := values[oldName]; v != "" {
			env = append(env, newName+"="+v)
		}
	}
	return env
}
