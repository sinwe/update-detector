//go:build !windows

package companion

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// installShRawBase is where a per-release install.sh is fetched from --
// a var, not a const, so tests can point it at a local server.
var installShRawBase = "https://raw.githubusercontent.com/sinwe/update-detector"

// refreshInstallShFunc is refreshInstallSh, a package var so the test
// binary never touches the network.
var refreshInstallShFunc = refreshInstallSh

// refreshInstallSh replaces the cached install.sh with the copy from
// targetVersion's own tag, right before that version's install runs.
//
// install.sh already re-caches itself, but only once it is new enough to
// do so before its own restart step (see its cache_install_sh_for_companion
// call sites) -- and a host whose cache predates that ordering can never
// heal, because the stale script it runs has the refresh in the wrong
// place, or no refresh at all. Confirmed live: a NUC and a Raspberry Pi
// were still running install.sh from months earlier, so no later fix
// could ever reach them through self-update, and a Mac mini kept
// rewriting old launchd plists for the same reason.
//
// Best-effort: the caller logs and falls back to the cached copy, since a
// host with no egress should still be able to self-update from what it
// already has.
func refreshInstallSh(ctx context.Context, targetVersion string) error {
	if targetVersion == "" {
		return fmt.Errorf("no target version to fetch install.sh for")
	}
	url := installShRawBase + "/" + targetVersion + "/install.sh"
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// install.sh sets this too; some corporate proxies filter on it.
	req.Header.Set("User-Agent", "update-detector-install.sh")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	// A captive portal or proxy can answer 200 with an HTML page; refuse
	// to overwrite a working script with something that isn't one.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(data, []byte("#!")) {
		return fmt.Errorf("%s: response is not a shell script", url)
	}
	tmp := installShPath + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, installShPath)
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
	// Refresh the script from the release being installed before running
	// it: the cached copy may predate a fix this very self-update needs,
	// and a stale cache cannot heal itself (see refreshInstallSh).
	// Best-effort -- an offline host still updates from its cached copy.
	if err := refreshInstallShFunc(ctx, targetVersion); err != nil {
		log.Printf("selfupdate: could not refresh %s for %s, using the cached copy: %v",
			installShPath, targetVersion, err)
	}
	cmd := exec.CommandContext(ctx, "sh", installShPath)
	cmd.Env = append(append(os.Environ(), existingConfigEnv(component)...),
		"INSTALL_COMPONENTS="+component,
		"INSTALL_VERSION="+targetVersion,
		// SELF_UPDATE_INVOCATION tells install.sh this run was
		// triggered by the companion updating itself (rather than a
		// human reinstalling): on macOS the companion install path
		// uses it to pick the suicide-proof restart (see install.sh's
		// own launchd_restart), since a bootout-first cycle would kill
		// this very process before it ever re-bootstraps.
		"SELF_UPDATE_INVOCATION=1",
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
		values := readEnvFile(agentEnvFilePath())
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

// agentEnvFilePath locates the agent's env file for config
// pass-through. On Linux that's /etc/default/update-detector; on macOS
// it's the sidecar agent.env inside the agent's state dir (written by
// install.sh's own install_agent_launchd). The state dir is STATE_DIR
// when set, else ~/.update-detector of the user this process runs as --
// the companion's own LaunchDaemon runs as the Homebrew owner by
// design, which is exactly whose home the agent lives under, so the
// default resolves correctly with no extra configuration. Empty when
// unresolvable: readEnvFile treats that as "no prior config", same as
// a missing file.
func agentEnvFilePath() string {
	if runtime.GOOS == "darwin" {
		dir := os.Getenv("STATE_DIR")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return ""
			}
			dir = filepath.Join(home, ".update-detector")
		}
		return filepath.Join(dir, "agent.env")
	}
	return filepath.Join(envFileDir, "update-detector")
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
