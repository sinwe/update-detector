# update-detector

A small agent that detects (never applies) available OS updates on a host:
package updates, security updates, pending-reboot state, and OS release
upgrades. It exposes the result over HTTP for [Gatus](https://gatus.io) to
poll, and can notify a channel (Telegram today) when something meaningful
changes. Ships as a single Docker image, one container per host.

## Supported platforms

| Platform | Status |
|---|---|
| Ubuntu / Debian-based Linux (bare metal or VM) | ✅ supported now |
| Raspberry Pi 4B (Raspberry Pi OS / Ubuntu Server, arm64) | ✅ supported now — see [Multi-arch builds](#multi-arch-builds) |
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

The container never writes to the host. It bind-mounts the host's apt
sources, dpkg status, `/etc/os-release`, and `/var/run` **read-only**, and
keeps its own apt package-list cache in a container-owned named volume
(`update-detector-state`, see `docker-compose.yml`).

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

### Platform limitations

Third-party repos that use `signed-by` keyrings living only on the host
(custom PPAs, vendor repos) will fail just for those repos unless you also
mount their keyring path (e.g. `/etc/apt/keyrings`, `/etc/apt/trusted.gpg.d`)
read-only into the container. Official Ubuntu repos work out of the box
since `ubuntu-keyring` ships in the base image.

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
  "packages": { "upgradable_total": 5, "upgradable_security": 2, "names": ["curl", "openssl"] },
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

**Known limitations:** the `/admin` page and `/widgets/*` endpoints have no
authentication of their own — same trust model as the rest of this project
(agent `/status`, Gatus polling): keep the aggregator on a private network or
put it behind your own reverse-proxy auth if it's reachable beyond that.
`/widgets/hosts/{hostname}` picks the most-recently-seen approved agent if
two share a hostname — set a unique `HOSTNAME_OVERRIDE` per host to avoid
that ambiguity.

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

`update-aggregator` has its own, much smaller set of env vars:

| Variable | Default | Purpose |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `REGISTRY_FILE` | `/var/lib/update-aggregator/registry.json` | Container-owned, writable — agent records, approval state, last reports |

## Multi-arch builds

Both images run on Ubuntu servers (`amd64`) and Raspberry Pi 4B (`arm64`):

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t update-detector:latest --push .
docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile.aggregator -t update-aggregator:latest --push .
```

## Development

```sh
go test ./...                                          # unit tests (pure parsing/diff/registry logic; no docker/apt needed)
docker build -t update-detector .                        # agent image build
docker build -f Dockerfile.aggregator -t update-aggregator .   # aggregator image build
```

End-to-end verification (host mounts need real apt/dpkg state, so this needs
an actual Ubuntu host): run via `docker compose up -d`, `curl
localhost:8080/status`, point a Gatus endpoint at it, and confirm a Telegram
message on the next state change. For the aggregator: run
`docker compose -f docker-compose.aggregator.yml up -d`, point an agent at it
via `AGGREGATOR_URL`, approve it on `/admin`, and confirm `/widgets/summary`
and `/widgets/hosts/{hostname}` reflect its next report.
