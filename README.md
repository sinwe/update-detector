# update-detector

A small agent that detects available OS updates on a host: package
updates, security updates, pending-reboot state, and OS release upgrades.
It exposes the result over HTTP for [Gatus](https://gatus.io) to poll, and
can notify a channel (Telegram today) when something meaningful changes.
Ships as its own Docker image (`update-detector`), one container per host.
The agent itself never writes to the host — an optional aggregator (a
separate Docker image, one instance for your whole fleet) and companion
(always native, never containerized) add a central dashboard and
push-button apply on top — see
[Fleet dashboard and push-button updates](#fleet-dashboard-and-push-button-updates)
below.

## Supported platforms

| Platform | Status |
|---|---|
| Ubuntu (bare metal or VM) | ✅ supported now |
| Plain Debian / Raspberry Pi OS (bare metal or VM) | ✅ supported now — see [OS flavors](docs/reference.md#os-flavors) |
| Raspberry Pi 4B (arm64, either flavor above) | ✅ supported now — see [Releases](docs/reference.md#releases) |
| WSL2 Ubuntu/Debian distro on Windows | ✅ supported now — see [WSL2](docs/wsl2.md) (Docker Desktop's WSL2 integration is usually a CLI shim, not a real engine — `install.sh` offers a native, no-Docker install for this reason) |
| Actual Windows OS (Windows Update) | 🧪 experimental — detection, `install.bat`, and companion apply/self-update confirmed against a real Windows host, see [Limitations](docs/reference.md#platform-limitations); **winget is not supported** |
| Actual macOS host (`softwareupdate`, `brew`) | 🚧 planned — same reason |

## Installation

On any Linux host (bare metal, VM, or a genuine-engine WSL2 distro — see
[WSL2](docs/wsl2.md) if `docker` there turns out to be Docker Desktop's
Windows-side shim instead), as root:

```sh
curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/install.sh | sudo sh
```

It figures out what this host needs on its own:

- **Nothing installed here yet?** Prompts for which of the aggregator,
  the agent, and the companion to set up (or set
  `INSTALL_COMPONENTS=agent,companion` etc. non-interactively). For the
  aggregator and the agent, it also checks whether Docker is actually
  usable on this host and, if so, offers a Docker Compose deployment —
  defaulting to `~/docker-updater` for both (their compose files have
  different names, so they coexist there fine; set `USE_DOCKER=0` to skip
  straight to native, or `DOCKER_DIR` / `AGGREGATOR_DOCKER_DIR` to put
  either somewhere else non-interactively) — falling back to a native
  systemd service where Docker isn't usable (most WSL2 distros; see
  [WSL2](docs/wsl2.md)). The companion is always native, since it needs
  real root to run `apt-get`.
- **Already have an agent running here** (Docker or native)? Installs
  just the companion, auto-discovering everything it needs from that
  agent.
- **Re-run later against an existing Docker deployment?** Finds it
  automatically and updates it in place (`docker compose pull && up -d`)
  instead of re-prompting or creating a duplicate.

Verify: `curl http://localhost:8080/status | jq` should return JSON
including `"ok": true`.

**On Windows** (experimental, no Docker path at all — see
[Platform limitations](docs/reference.md#platform-limitations)), from an
elevated Command Prompt:

```bat
curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/install.bat -o install.bat
install.bat
```

Re-running this later detects a previous install and stops its service
before replacing it, so it's also how you upgrade.

**To uninstall**, run `install.sh` with `--uninstall` or `install.bat
--uninstall` — both detect what's actually installed and prompt for what
to remove (or set `UNINSTALL_COMPONENTS` / `INSTALL_COMPONENTS` for
scripted use). Neither ever tears down a Docker deployment it didn't
create — it prints where to `docker compose down` it yourself instead.

### Fleet dashboard and push-button updates

Installing the aggregator (above) on one central host gives every agent
that points `AGGREGATOR_URL` at it a combined dashboard — open
`http://<aggregator-host>:9090/admin` and approve each new host under
"Pending." Installing the companion (also above, on each agent host) adds
push-button apply and self-update from that same page. Full walkthrough
— security model, apply mechanics, upgrade-vs-full-upgrade, self-update —
in [Technical reference](docs/reference.md#triggering-updates-companion).

## See also

- [docs/reference.md](docs/reference.md) — how detection works per OS,
  the companion/apply/self-update mechanics, full configuration and API
  reference, and how releases are built.
- [docs/wsl2.md](docs/wsl2.md) — WSL2-specific detail: the Docker Desktop
  shim gotcha, and native config without `.env`.
- [docs/integrations/gatus.md](docs/integrations/gatus.md) — poll `/status` for uptime-style monitoring.
- [docs/integrations/homepage.md](docs/integrations/homepage.md) — fleet widgets on a Homepage dashboard.
- [docs/integrations/telegram.md](docs/integrations/telegram.md) — notification setup.
