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

// installShPath is where a cached copy of install.sh lives, refreshed
// whenever the companion itself updates (see cmd/update-detector-companion's
// own self-update handling) -- shelling out to it reuses its already
// battle-tested download/atomic-swap/systemctl-restart logic instead of
// duplicating any of it here. A var, not a const, so tests can point it
// at a fake script.
var installShPath = "/usr/local/lib/update-detector/install.sh"

// componentUnitName maps a self-update Action's Component to the
// systemd unit / Docker image name that component actually runs under
// (install.sh's own INSTALL_COMPONENTS values -- "agent", "aggregator",
// "companion" -- are not the same strings as the unit/image names).
func componentUnitName(component string) (string, error) {
	switch component {
	case "agent":
		return "update-detector", nil
	case "aggregator":
		return "update-aggregator", nil
	case "companion":
		return "update-detector-companion", nil
	default:
		return "", fmt.Errorf("selfupdate: unknown component %q", component)
	}
}

// SelfUpdate carries out action (Type == ActionSelfUpdate): detects
// whether action.Component is running natively or as a Docker container
// on this host, and updates it to action.TargetVersion accordingly.
//
// Never restarts *this* process as part of this call, even when
// Component == "companion" -- the systemctl restart for that case is
// bundled into install.sh's own install_unit (already true for the
// other two components too, which is fine there since restarting them
// doesn't kill the process running this code). The caller
// (cmd/update-detector-companion/main.go) must report this action's
// result *before* calling SelfUpdate for Component == "companion"
// specifically, since code after install.sh restarts this very process
// may never run to report it.
func SelfUpdate(ctx context.Context, action aggregator.Action) aggregator.ActionResult {
	fail := func(format string, args ...any) aggregator.ActionResult {
		return aggregator.ActionResult{ActionID: action.ID, Message: fmt.Sprintf(format, args...), CompletedAt: time.Now()}
	}
	succeed := func(msg string) aggregator.ActionResult {
		return aggregator.ActionResult{ActionID: action.ID, Success: true, Message: msg, CompletedAt: time.Now()}
	}

	// Companion is always native, never containerized (it needs real
	// root to run apt-get) -- install_companion's own uninstall code
	// already documents this same fact, so there's no Docker case to
	// even check here.
	if action.Component == "companion" {
		if err := installNative(ctx, "companion", action.TargetVersion); err != nil {
			return fail("%v", err)
		}
		return succeed("update installed, restarting")
	}

	unitName, err := componentUnitName(action.Component)
	if err != nil {
		return fail("%v", err)
	}

	detection, err := Detect(ctx, unitName)
	if err != nil {
		return fail("detecting how %s is deployed: %v", action.Component, err)
	}

	switch detection.Kind() {
	case DeployNative:
		if err := installNative(ctx, action.Component, action.TargetVersion); err != nil {
			return fail("%v", err)
		}
	case DeployDocker:
		if err := updateDockerCompose(ctx, detection.DockerContainerID); err != nil {
			return fail("%v", err)
		}
	default:
		return fail("%s is not running natively or as a Docker container on this host", action.Component)
	}

	msg := fmt.Sprintf("%s updated to %s", action.Component, action.TargetVersion)
	if detection.Ambiguous() {
		msg += " (both a native install and a Docker container were found -- updated the native one; remove the Docker one to avoid confusion)"
	}
	return succeed(msg)
}

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
	out, err := runCapped(cmd)
	if err != nil {
		return fmt.Errorf("selfupdate: install.sh failed: %w\n%s", err, out)
	}
	return nil
}

// envFileDir is where install.sh writes agent/aggregator env files -- a
// var, not a const, purely so tests can point it at a temp dir.
var envFileDir = "/etc/default"

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
		values := readEnvFile(envFileDir + "/update-detector")
		env := passThrough(values, "LISTEN_ADDR", "HOSTNAME_OVERRIDE", "CHECK_INTERVAL",
			"TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "AGGREGATOR_URL")
		if dir := filepath.Dir(values["AGENT_IDENTITY_FILE"]); dir != "" && dir != "." && dir != "/" {
			env = append(env, "STATE_DIR="+dir)
		}
		return env
	case "aggregator":
		values := readEnvFile(envFileDir + "/update-aggregator")
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

// updateDockerCompose updates a Docker-based component via
// `docker compose pull && up -d` for the specific service, using the
// exact compose file(s)/working dir/service name Docker Compose itself
// recorded as labels on containerID when it created it -- so this
// respects the user's own compose file, env, and volumes exactly,
// without install.sh or the companion needing to track or duplicate
// that path anywhere.
func updateDockerCompose(ctx context.Context, containerID string) error {
	configFiles, err := dockerInspectLabel(ctx, containerID, "com.docker.compose.project.config_files")
	if err != nil {
		return err
	}
	workingDir, err := dockerInspectLabel(ctx, containerID, "com.docker.compose.working_dir")
	if err != nil {
		return err
	}
	service, err := dockerInspectLabel(ctx, containerID, "com.docker.compose.service")
	if err != nil {
		return err
	}
	if configFiles == "" || workingDir == "" || service == "" {
		return fmt.Errorf("selfupdate: container %s is missing expected Docker Compose labels -- was it started with `docker compose up`?", containerID)
	}

	// config_files can be a comma-separated list if the original compose
	// invocation used more than one -f flag -- pass each as its own -f,
	// not a single comma-joined value (docker compose's -f takes one
	// path per flag, repeatable).
	var fileArgs []string
	for _, f := range strings.Split(configFiles, ",") {
		fileArgs = append(fileArgs, "-f", f)
	}

	for _, extra := range [][]string{{"pull", service}, {"up", "-d", service}} {
		args := append(append([]string{"compose"}, fileArgs...), extra...)
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Dir = workingDir
		out, err := runCapped(cmd)
		if err != nil {
			return fmt.Errorf("selfupdate: docker %s: %w\n%s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

func dockerInspectLabel(ctx context.Context, containerID, label string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format",
		fmt.Sprintf(`{{index .Config.Labels "%s"}}`, label), containerID)
	out, err := runCapped(cmd)
	if err != nil {
		return "", fmt.Errorf("selfupdate: docker inspect %s: %w\n%s", containerID, err, out)
	}
	return strings.TrimSpace(out), nil
}
