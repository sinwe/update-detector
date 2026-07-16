//go:build windows

package companion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// installBatPath is where a cached copy of install.bat lives on Windows,
// refreshed whenever the companion itself updates. Shelling out to it
// reuses its already battle-tested download/service-recreate logic
// instead of duplicating any of it here. A var, not a const, so tests
// can point it at a fake script.
//
// A .bat, not a .ps1: cmd.exe runs a .bat directly, while a .ps1 invoked
// via `powershell -File` is blocked outright on any host still at
// PowerShell's historical default ExecutionPolicy (Restricted) unless
// every single invocation also passes -ExecutionPolicy Bypass -- and
// this gets re-invoked non-interactively by a Windows Service with no
// operator present to grant an exception if that bites.
var installBatPath = `C:\Program Files\update-detector\install.bat`

// installNative re-invokes install.bat non-interactively to update
// component to targetVersion. The Windows Service restart for each
// component is bundled into install.bat's own install step -- the
// companion's own restart happens inside the script, so code after this
// call may never run for Component == "companion".
//
// The exec.Cmd below is deliberately built from context.Background(),
// never ctx: for a self-update of "companion" specifically, install.bat's
// own `sc stop update-detector-companion` call (see its stop_if_running)
// stops *this very process*, which is exactly what cancels ctx as part
// of this service's own graceful-shutdown handling (see internal/winsvc
// -- SCM's stop control triggers cancel() there). If install.bat were
// still governed by that same ctx, exec.CommandContext would respond to
// that cancellation by killing install.bat right then -- immediately
// after it stopped the service and before it ever gets to replace the
// binary or start it again, leaving the service stopped for good.
// Confirmed live: this is exactly what "the companion never reconnects
// after a self-update" turned out to be. Once a self-update has been
// decided on, it must run to completion independent of whatever happens
// to the process that launched it. ctx is still passed to runCapped
// below for output-sink tapping (a plain context.Value lookup, entirely
// unrelated to cancellation) -- only the exec.Cmd's own kill-on-cancel
// behavior needs decoupling.
func installNative(ctx context.Context, component, targetVersion string) error {
	cmd := exec.CommandContext(context.Background(), "cmd", "/c", installBatPath)
	cmd.Env = append(append(os.Environ(), existingConfigEnv(component)...),
		"INSTALL_COMPONENTS="+component,
		"INSTALL_VERSION="+targetVersion,
	)
	out, err := runCapped(ctx, cmd)
	if err != nil {
		return fmt.Errorf("selfupdate: install.bat failed: %w\n%s", err, out)
	}
	return nil
}

// existingConfigEnv reads component's currently-installed Windows
// Service's own registry Environment value (the native equivalent of
// systemd's EnvironmentFile=, written by install.bat's own
// set_service_env) and translates it back into the *input* variable
// names install.bat's own install_agent_native/install_aggregator_native
// actually read -- not always the same names, e.g. the aggregator's own
// install.bat input is AGGREGATOR_LISTEN_ADDR, but the registry value it
// writes says LISTEN_ADDR (the name update-aggregator's own config.Load
// actually reads at runtime) -- same reasoning, and the same translation
// table, as selfupdate_unix.go's own existingConfigEnv. Companion has no
// preserved config at all: its service Environment is rebuilt fresh from
// freshly rediscovered agent info on every install.bat run, same as on
// Linux. A service that doesn't exist yet (a fresh install, not a
// self-update) or an unreadable registry value -> that key just falls
// back to install.bat's own hardcoded default, same as a genuinely fresh
// install.
func existingConfigEnv(component string) []string {
	switch component {
	case "agent":
		values := readServiceEnv("update-detector")
		return passThrough(values, "LISTEN_ADDR", "HOSTNAME_OVERRIDE", "CHECK_INTERVAL",
			"TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "AGGREGATOR_URL")
	case "aggregator":
		values := readServiceEnv("update-aggregator")
		env := translate(values, map[string]string{
			"LISTEN_ADDR":        "AGGREGATOR_LISTEN_ADDR",
			"TELEGRAM_BOT_TOKEN": "AGGREGATOR_TELEGRAM_BOT_TOKEN",
			"TELEGRAM_CHAT_ID":   "AGGREGATOR_TELEGRAM_CHAT_ID",
		})
		return append(env, passThrough(values, "ADMIN_APPLY_SHARED_SECRET")...)
	default:
		return nil
	}
}

// readServiceEnv reads serviceName's own registry Environment value
// (REG_MULTI_SZ, HKLM\SYSTEM\CurrentControlSet\Services\<name>,
// value name "Environment" -- not a subkey of that name) into a map,
// splitting each "KEY=value" entry on its first "=". Returns an empty
// map, not an error, if the service or its Environment value doesn't
// exist yet -- a self-update of a service install.bat hasn't configured
// before just falls back to defaults entirely, same as readEnvFile's
// behavior on Linux.
func readServiceEnv(serviceName string) map[string]string {
	values := map[string]string{}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\`+serviceName, registry.QUERY_VALUE)
	if err != nil {
		return values
	}
	defer key.Close()
	entries, _, err := key.GetStringsValue("Environment")
	if err != nil {
		return values
	}
	for _, entry := range entries {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		values[k] = v
	}
	return values
}
