# update-detector

A small agent that detects (never applies) available OS updates on a host:
package updates, security updates, pending-reboot state, and OS release
upgrades. It exposes the result over HTTP for [Gatus](https://gatus.io) to
poll, and can notify a channel (Telegram today) when something meaningful
changes. Ships as a single Docker image, one container per host.

## Supported platforms

| Platform | Status |
|---|---|
| Ubuntu (bare metal or VM) | ✅ supported now |
| Plain Debian / Raspberry Pi OS (bare metal or VM) | ✅ supported now — see [OS flavors](#os-flavors) |
| Raspberry Pi 4B (arm64, either flavor above) | ✅ supported now — see [Releases](#releases) |
| WSL2 Ubuntu/Debian distro on Windows | ✅ supported now, for the WSL2 environment itself |
| Actual Windows OS (Windows Update, winget) | 🚧 planned — needs a native (non-container) agent, see [Limitations](#platform-limitations) |
| Actual macOS host (`softwareupdate`, `brew`) | 🚧 planned — same reason |

## Quick start

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t update-detector:latest --load .
# or just: docker build -t update-detector:latest .

export TELEGRAM_BOT_TOKEN=...
export TELEGRAM_CHAT_ID=...
export HOSTNAME_OVERRIDE=$(hostname)
docker compose up -d

curl http://localhost:8080/status | jq
```

## How it works

The container never writes to the host's own files. It bind-mounts the
host's apt sources, dpkg status, `/etc/os-release`, and `/var/run`
**read-only**, and keeps its own apt package-list cache and state under a
container-owned directory bind-mounted at a fixed host path (default
`/var/lib/update-detector`, override with `STATE_DIR`; see
`docker-compose.yml`) rather than a Docker-managed named volume, so a
host-native companion process (planned) can share that same directory.

> **Migrating from an older deployment?** Versions before the
> `STATE_DIR` bind mount stored this state in a named volume called
> `update-detector-state`. To migrate without losing history: stop the
> container, then copy the volume's contents to the new path, e.g.
> `docker run --rm -v update-detector-state:/from -v /var/lib/update-detector:/to alpine cp -a /from/. /to/`,
> then `docker compose up -d` and `docker volume rm update-detector-state`.
> Skipping this is also fine — the agent just re-enrolls with the
> aggregator (if configured) and starts state/history fresh, which is a
> low-cost reset, not data loss of anything critical.

Each detection cycle (default every 6h, `CHECK_INTERVAL`):
1. Runs `apt-get update` against the host's sources, writing indexes only to
   its own cache dir.
2. Runs Ubuntu's own `/usr/lib/update-notifier/apt-check` (from
   `update-notifier-common`) to count upgradable/security packages — the
   same tool the desktop update indicator uses, so counts match what you'd
   see with `apt list --upgradable`.
3. Checks for `/var/run/reboot-required` (host-mounted) to detect a pending
   reboot.
4. Compares the host's `VERSION_ID` against
   `https://changelogs.ubuntu.com/meta-release(-lts)` to see if a newer
   supported Ubuntu release exists (the same file `do-release-upgrade`
   itself reads) — detection only, no upgrade is ever run.

If any individual check fails (e.g. a transient network blip), that field
keeps its last known value instead of resetting to a false "all clear", and
the failure is listed under `errors` in the response.

### OS flavors

The agent runs inside an Ubuntu-based container regardless of which host
it's deployed on, so at startup it reads the *host's* (host-mounted)
`/etc/os-release` `ID` field to decide which checker implementation to run
— logged as `detected host OS flavor: <ubuntu|debian>`. This matters because
`apt-check` (used above) is Ubuntu-only tooling; it doesn't exist on plain
Debian or Raspberry Pi OS.

On a Debian-flavored host (`ID=debian` or `ID=raspbian`), detection instead:
1. Runs `apt-get -s dist-upgrade` (a simulation, changes nothing) and parses
   its `Inst` lines for packages already installed that have a newer
   candidate version. Security updates are identified by `-security`
   appearing in the origin (e.g. Debian's `trixie-security` pocket — the
   same naming convention Ubuntu uses).
2. Checks for `/var/run/reboot-required` same as Ubuntu, but **best-effort
   only** — Debian/Raspberry Pi OS have no update-notifier-equivalent that
   reliably populates it, so `false` here doesn't guarantee no reboot is
   actually needed.
3. Does **not** check for an OS release upgrade — Debian has no
   machine-readable "is there a newer release" feed equivalent to
   `changelogs.ubuntu.com/meta-release`. `os.update_available` always stays
   `false` on this flavor; `os.current_version`/`current_codename` are still
   populated.

### Platform limitations

Third-party repos (Docker, Tailscale, VS Code, etc.) sign with a key
referenced by an **absolute path** baked into `sources.list.d` (either a
`signed-by=/etc/apt/keyrings/...` / `/usr/share/keyrings/...` entry, or the
older `/etc/apt/trusted.gpg.d` mechanism) — apt resolves that path literally,
so it has to exist at the *same* absolute path inside the container, not
under the `/host` prefix used for the other mounts. `docker-compose.yml`
mounts all three read-only by default to cover this; official Ubuntu repos
work regardless since `ubuntu-keyring` ships in the base image. If a repo's
key lives somewhere else entirely, mount that specific path too.

Docker Desktop on macOS/Windows runs containers inside a hidden Linux VM, so
this container has no visibility into the real macOS/Windows host — only
into a WSL2 Ubuntu/Debian distro, if that's what you're running it in.
Monitoring the actual Windows or macOS OS needs a native, non-container
agent (planned) — the checker is designed as an interface so that's a new
implementation, not a rewrite.

## Gatus integration

```yaml
endpoints:
  - name: web01-updates
    url: "http://web01:8080/status"
    interval: 1h
    conditions:
      - "[STATUS] == 200"
      - "[BODY].ok == true"
```

`ok` is a convenience boolean: no security updates, no reboot pending, no OS
upgrade available. For finer-grained alerting, target the individual fields
instead, e.g.:

```yaml
    conditions:
      - "[BODY].packages.upgradable_security == 0"
      - "[BODY].reboot_required == false"
      - "[BODY].os.update_available == false"
```

Example `/status` response:

```json
{
  "hostname": "web01",
  "platform": "ubuntu",
  "checked_at": "2026-07-05T10:00:00Z",
  "reboot_required": false,
  "os": { "current_version": "22.04", "update_available": false },
  "packages": {
    "upgradable_total": 5,
    "upgradable_security": 2,
    "upgrades": [
      { "name": "curl", "current_version": "7.81.0-1ubuntu1.15", "candidate_version": "7.81.0-1ubuntu1.16" },
      { "name": "openssl", "current_version": "3.0.2-0ubuntu1.15", "candidate_version": "3.0.2-0ubuntu1.16" }
    ]
  },
  "ok": false
}
```

`GET /healthz` reports process liveness only (independent of update state),
so it won't get confused with "the host needs patching" — use it for
Docker's own health checking if desired.

## Notifications

Telegram is wired in when both `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID`
are set:
1. Create a bot with [@BotFather](https://t.me/BotFather), grab its token.
2. Add the bot to your chat/channel and find the chat ID (e.g. via
   `https://api.telegram.org/bot<token>/getUpdates` after sending it a
   message).
3. Set the two env vars in `docker-compose.yml` or your environment.

A notification is sent only on a meaningful state transition — new updates
appear, the security count increases, reboot flips from not-required to
required, or an OS upgrade newly becomes available — using the persisted
state file (`STATE_FILE`) as the baseline, so a container restart doesn't
re-send the same alert.

### Adding a new notifier

Implement the `Notifier` interface in `internal/notifier/notifier.go`:

```go
type Notifier interface {
    Name() string
    Send(ctx context.Context, ev Event) error
}
```

Then register an instance in `cmd/update-detector/main.go` next to the
existing Telegram wiring, gated by whatever config it needs. Nothing else
changes — the scheduler, HTTP server, and state diffing are all
notifier-agnostic.

## Fleet dashboard (Homepage) via update-aggregator

For a fleet of hosts, `update-aggregator` (a second, separate binary/image,
`Dockerfile.aggregator` / `docker-compose.aggregator.yml`) is a small central
service that agents push their status to, so [Homepage](https://gethomepage.dev)
can show one summary widget plus one card per host. It runs once, wherever
you like (e.g. next to Homepage) — unlike the agent, it's not per-host.

**Why push, not pull:** the aggregator never needs to reach into any agent's
network — each agent connects *out* to the aggregator, so this works even
across NAT/firewalls the aggregator couldn't otherwise reach through.

**Enrollment & approval:** on first run with `AGGREGATOR_URL` set, an agent
generates a random ID + token (`AGENT_IDENTITY_FILE`, persisted so restarts
don't re-enroll as a new agent) and announces itself with its claimed
hostname. The aggregator holds it as `pending` until you approve or reject
it on its `/admin` page — that's the actual trust decision; the token just
means one agent's credential leaking doesn't expose others. Pushing to the
aggregator is purely additive: local `/status`, Gatus, and Telegram all keep
working even if the aggregator is unreachable or the agent isn't approved
yet.

```sh
docker compose -f docker-compose.aggregator.yml up -d
# on each host's agent:
export AGGREGATOR_URL=http://aggregator-host:9090
docker compose up -d
# then open http://aggregator-host:9090/admin and approve the new host
```

Homepage "Custom API" widgets:

```yaml
- Fleet status:
    - Update Detector:
        widget:
          type: customapi
          url: http://aggregator-host:9090/widgets/summary
          mappings:
            - field: hosts_ok
              label: Hosts OK
            - field: packages_upgradable_security
              label: Security updates pending
- web01:
    - Update Detector:
        widget:
          type: customapi
          url: http://aggregator-host:9090/widgets/hosts/web01
          mappings:
            - field: packages.upgradable_total
              label: Upgradable
            - field: reboot_required
              label: Reboot required
```

The per-host widget URL (`/widgets/hosts/{hostname}`) returns the exact same
JSON shape as an agent's own `/status`, so the mapping is identical whether
Homepage points at the aggregator or straight at an agent.

For "what actually needs updating" across the whole fleet in one call, use
`GET /widgets/packages` — flattens every approved host's pending package
upgrades into one list (`[{hostname, name, current_version,
candidate_version, security}, ...]`); add `?security=true` to only list
security updates. `security` is best-effort per-package (derived from
`-security` appearing in the package's origin/pocket) — the authoritative
count per host is still `packages.upgradable_security`.

**Known limitations:** the `/admin` page and `/widgets/*` endpoints have no
authentication of their own — same trust model as the rest of this project
(agent `/status`, Gatus polling): keep the aggregator on a private network or
put it behind your own reverse-proxy auth if it's reachable beyond that.
`/widgets/hosts/{hostname}` picks the most-recently-seen approved agent if
two share a hostname — set a unique `HOSTNAME_OVERRIDE` per host to avoid
that ambiguity.

## Triggering updates (companion)

By design, the agent itself only ever detects updates — it never writes to
the host, so it can't apply anything even if you wanted it to. Actually
*applying* pending upgrades is a separate, opt-in capability: a small
host-native process called the **companion**, installed alongside the
agent's container (not inside it, since it needs real root to run
`apt-get`).

**Why a separate process, not SSH/Ansible into the fleet:** every
centralized-credential design (a service holding SSH keys or sudo access to
every host) means compromising that one service gives an attacker root
everywhere. The companion inverts this: it connects *out* to the aggregator
(no inbound port, no stored credentials on the aggregator side), and before
running anything, it independently re-checks the requested package(s)
against its own host's last-known pending upgrades (`GET /status`) —
rejecting anything not already, genuinely pending. So even a fully
compromised aggregator can at most force early application of upgrades a
host already considers legitimate, never arbitrary command execution.

**Install** (on a host already running the agent's container with
`AGGREGATOR_URL` set):

```sh
curl -fsSL https://forgejo.winar.to/winarto/update-detector/raw/branch/main/install.sh | sudo sh
```

This auto-discovers everything it needs from the running container: its
bind-mounted state dir (for a one-time, in-memory-only token handoff over a
Unix socket — see [How it works](#how-it-works)), its `AGGREGATOR_URL`, and
its published port. It installs `update-detector-companion` as a systemd
service and starts it.

**Triggering an action** is `POST /admin/agents/{id}/apply` on the
aggregator, gated by a shared secret (`ADMIN_APPLY_SHARED_SECRET` —
disabled/`501` entirely until set):

```sh
curl -X POST http://aggregator-host:9090/admin/agents/<id>/apply \
  -H "X-Admin-Apply-Secret: $ADMIN_APPLY_SHARED_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"type": "packages", "packages": ["curl"]}'
# type is one of: packages (requires "packages"), upgrade, full-upgrade
```

Recommended: put the aggregator behind your own passkey-capable
reverse-proxy auth (e.g. Authentik) and have it inject that header after
successful login — the aggregator checks the header itself regardless, so a
compromised proxy or network path alone still isn't enough on its own.

**Reboots are never automatic**, even if an upgrade sets
`reboot_required` — that stays a manual, human decision.

## API reference

Both services have an OpenAPI 3.0 spec — committed at `openapi/update-detector.yaml`
and `openapi/update-aggregator.yaml`, and also served live at `GET /openapi.yaml`
on the running service. Paste either into
[editor.swagger.io](https://editor.swagger.io) (or point Redoc/any OpenAPI
viewer at the running endpoint) for interactive docs; there's no bundled
Swagger UI in the binaries themselves to keep them dependency-free.

## Configuration

All configuration is via environment variables; defaults match the mounts
in `docker-compose.yml`.

| Variable | Default | Purpose |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `HOSTNAME_OVERRIDE` | OS hostname | Reported in `/status` and notifications |
| `CHECK_INTERVAL` | `6h` | How often to run a detection cycle |
| `APT_SOURCES_LIST` | `/host/etc/apt/sources.list` | Host-mounted |
| `APT_SOURCES_LIST_D` | `/host/etc/apt/sources.list.d` | Host-mounted |
| `DPKG_STATUS_FILE` | `/host/var/lib/dpkg/status` | Host-mounted |
| `APT_LISTS_CACHE_DIR` | `/var/lib/update-detector/apt/lists` | Container-owned, writable |
| `OS_RELEASE_FILE` | `/host/etc/os-release` | Host-mounted |
| `RELEASE_UPGRADES_FILE` | `/host/etc/update-manager/release-upgrades` | Host-mounted, optional |
| `REBOOT_REQUIRED_FILE` | `/host/var/run/reboot-required` | Host-mounted |
| `STATE_FILE` | `/var/lib/update-detector/state.json` | Container-owned, writable |
| `TELEGRAM_BOT_TOKEN` / `TELEGRAM_CHAT_ID` | unset | Enables Telegram notifications when both are set |
| `NOTIFY_ON_STARTUP` | `false` | Also send a notification on the very first check after startup |
| `AGGREGATOR_URL` | unset | Enables pushing to a central `update-aggregator` when set (e.g. `http://aggregator-host:9090`) |
| `AGENT_IDENTITY_FILE` | `/var/lib/update-detector/agent-identity.json` | Container-owned, writable — this agent's self-generated ID + token |
| `COMPANION_SOCKET_PATH` | `/var/lib/update-detector/companion.sock` | Unix socket serving this agent's identity to a host-native companion, see [Triggering updates](#triggering-updates-companion) |

`update-aggregator` has its own, much smaller set of env vars:

| Variable | Default | Purpose |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `REGISTRY_FILE` | `/var/lib/update-aggregator/registry.json` | Container-owned, writable — agent records, approval state, last reports |
| `TELEGRAM_BOT_TOKEN` / `TELEGRAM_CHAT_ID` | unset | Alerts on companion apply results when both are set (independent of each agent's own Telegram config) |
| `ADMIN_APPLY_SHARED_SECRET` | unset | Enables `POST /admin/agents/{id}/apply` when set (disabled/`501` otherwise) — see [Triggering updates](#triggering-updates-companion) |

`update-detector-companion` (host-native, not a container) reads:

| Variable | Default | Purpose |
|---|---|---|
| `COMPANION_SOCKET_PATH` | `/var/lib/update-detector/companion.sock` | Must match the agent's own setting above |
| `AGGREGATOR_URL` | none — required | Same aggregator the agent's container reports to |
| `AGENT_STATUS_URL` | `http://localhost:8080/status` | Used to validate a requested action's packages are actually pending before running anything |

`install.sh` sets all three for you from the running container's own config —
see [Triggering updates](#triggering-updates-companion).

## Releases

Pushing a tag matching `v*` (e.g. `v0.1.0`) triggers
`.forgejo/workflows/release.yml`, which builds both images for
`linux/amd64` and `linux/arm64` (covers Ubuntu servers and Raspberry Pi 4B
from the same tag, via QEMU emulation since the runner itself is amd64-only)
and pushes them to Forgejo's container registry:

```
forgejo.winar.to/winarto/update-detector:<tag>    and  :latest
forgejo.winar.to/winarto/update-aggregator:<tag>  and  :latest
```

The same tag also cross-compiles `update-detector-companion` for
`linux/amd64` and `linux/arm64` (no QEMU needed — Go cross-compiles
natively) and attaches both as plain binary assets on that tag's Forgejo
release, for `install.sh` to fetch (see
[Triggering updates](#triggering-updates-companion)).

Requires a repo secret `REGISTRY_TOKEN` — a Forgejo access token with
`write:package`/`read:package` scope for the image pushes, **and**
read/write repository scope for creating the release and uploading those
binary assets.

Deploying a release is then just `docker compose pull && docker compose up
-d` on each host — no `--build` needed, since `docker-compose.yml` /
`docker-compose.aggregator.yml` both reference the published `image:`
directly (they also keep `build: .` for local dev — see
[Development](#development)).

## Development

```sh
go test ./...                                          # unit tests (pure parsing/diff/registry logic; no docker/apt needed)
docker build -t update-detector .                        # agent image build
docker build -f Dockerfile.aggregator -t update-aggregator .   # aggregator image build
npx @redocly/cli lint openapi/update-detector.yaml openapi/update-aggregator.yaml   # lint the OpenAPI specs
```

End-to-end verification (host mounts need real apt/dpkg state, so this needs
an actual Ubuntu host): run via `docker compose up -d`, `curl
localhost:8080/status`, point a Gatus endpoint at it, and confirm a Telegram
message on the next state change. For the aggregator: run
`docker compose -f docker-compose.aggregator.yml up -d`, point an agent at it
via `AGGREGATOR_URL`, approve it on `/admin`, and confirm `/widgets/summary`
and `/widgets/hosts/{hostname}` reflect its next report.
