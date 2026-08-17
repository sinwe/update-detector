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
| WSL2 Ubuntu/Debian distro on Windows | ✅ supported now — see [WSL2](docs/wsl2.md) (Docker Desktop's WSL2 integration is usually a CLI shim, not a real engine — `install.sh` offers a native, no-Docker install for this reason) |
| Actual Windows OS (Windows Update, winget) | 🧪 experimental — detection, `install.bat`, and companion apply/self-update all exist, see [Limitations](#platform-limitations); none of it verified against a real Windows host yet |
| Actual macOS host (`softwareupdate`, `brew`) | 🚧 planned — same reason |

## Installation

Follow these in order. Step 1 is required; everything after it is optional
— skip anything you don't need. Every command below is meant to be copied
and run exactly as shown; nothing here assumes you've read any other
section first.

**Prerequisite, on every machine you'll use below:** Docker with the Compose
plugin installed (`docker compose version` should print something, not
"command not found"). That's the only thing you need — no Go toolchain, no
cloning this repo.

> **On WSL2:** skip straight to `install.sh` in
> [Triggering updates](#triggering-updates-companion) instead of Step 1
> below — it detects WSL2 automatically and offers an interactive,
> native (no Docker) install of the agent, the aggregator, the companion,
> or all three, in one run. See [WSL2](docs/wsl2.md) for why WSL2 gets
> different treatment here.

### Step 1 — Deploy the agent (required, do this on every host you want detection on)

```sh
mkdir -p ~/update-detector && cd ~/update-detector
curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/docker-compose.yml -o docker-compose.yml
```

Set the hostname this instance reports as (so multiple hosts don't look
identical), and Telegram credentials if you want alerts — skip either
`TELEGRAM_*` line if you don't have/want that. Put these in a `.env` file
right next to `docker-compose.yml`, not `export` in your shell:

```sh
echo "HOSTNAME_OVERRIDE=$(hostname)" >> .env
echo "TELEGRAM_BOT_TOKEN=your-bot-token-from-BotFather" >> .env
echo "TELEGRAM_CHAT_ID=your-chat-id" >> .env
```

> **Why `.env`, not `export`?** `docker compose` automatically reads a
> `.env` file in the same directory as `docker-compose.yml` and uses it to
> fill in every `${VAR}` in that file — that's the *only* mechanism this
> project relies on, so it's what every command below uses too. `export`
> only lasts for your current terminal session; the moment you open a new
> shell (or this runs unattended, e.g. after a reboot), an `export`-only
> value is gone and everything silently falls back to defaults. `.env` is
> a plain text file, persists on disk, and needs no explaining to
> "future you." (If a variable happens to be set *both* ways, the shell's
> `export` wins over `.env` — one more reason to just pick `.env` and not
> mix the two.)

Pull the image and start it — always `pull` before `up -d`, even on a
host that's run this before: `up -d` alone reuses whatever image is
already cached locally and won't notice a newer one exists, silently
running stale code:

```sh
docker compose pull
docker compose up -d
```

Verify it's actually working:

```sh
curl http://localhost:8080/status | jq
```

You should see JSON like `{"hostname": "...", "packages": {...}, "ok": true, ...}`.
If you get `curl: (7) Failed to connect`, run `docker compose logs` and check
for errors — the most common cause is port 8080 already being used by
something else, fixed by `AGENT_PORT=8081 docker compose up -d` instead.

**That's a complete, working install.** Everything below adds optional
capabilities on top of it.

### Step 2 — Fleet dashboard across multiple hosts (optional)

Skip this entirely if you only have one host, or don't want a combined
view. Do this on one separate machine (not one of your agent hosts) — it
only needs to run once for your whole fleet. **Use a separate directory
from the agent's `docker-compose.yml`** (even if it's the same machine,
e.g. testing both roles on one box) — see the warning below for why:

```sh
mkdir -p ~/update-aggregator && cd ~/update-aggregator
curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/docker-compose.aggregator.yml -o docker-compose.aggregator.yml
docker compose -f docker-compose.aggregator.yml pull
docker compose -f docker-compose.aggregator.yml up -d
```

Verify: `curl http://localhost:9090/admin` should return an HTML page (or
open it in a browser).

> **If you do put both compose files in one directory anyway:** plain
> `docker compose pull`/`up -d` with no `-f` always targets
> `docker-compose.yml` only — it will silently never start or update the
> aggregator, even though the file's sitting right there. Always say
> `-f docker-compose.aggregator.yml` explicitly for every aggregator
> command. You'll also see a `Found orphan containers` warning whenever
> you run one file without the other, since they share a project name by
> directory default — that's expected and harmless, but **never add
> `--remove-orphans`** to "fix" it here, since that flag will delete
> whichever of the two services you *didn't* just target.

Then, back on **each agent host** from Step 1, point it at the aggregator
(in `.env`, same as Step 1) and restart it:

```sh
cd ~/update-detector
echo "AGGREGATOR_URL=http://<aggregator-host-ip-or-name>:9090" >> .env
docker compose pull
docker compose up -d
```

> **Testing both roles on one machine, in one directory?** Since both
> compose files then share a project name (and so the same default Docker
> network), `<aggregator-host-ip-or-name>:9090` won't work here —
> `localhost` inside a container means that container, not the host, and
> `9090` is the *host*-published port, not the one the aggregator
> listens on internally. Use the aggregator's Compose *service* name and
> its internal port instead: `AGGREGATOR_URL=http://update-aggregator:8080`.
> Confirmed working this way; `docker run --rm --network
> <projectname>_default alpine wget -qO- http://update-aggregator:8080/openapi.yaml`
> is a quick way to sanity-check reachability if enrollment still doesn't
> show up on `/admin`.

If the agent was already running, `up -d` here recreates the container to
pick up the `.env` change on its own — no `--force-recreate` needed, and
no separate "stop it first" step. Compose detects that the resolved
config changed and recreates automatically (`docker compose ps` will show
a just-now `Created` time if you want to confirm). If you ever *don't*
see that happen for some reason, `docker compose up -d --force-recreate`
guarantees it.

Open `http://<aggregator-host>:9090/admin` in a browser — you'll see the
new agent listed under "Pending." Click **Approve**. It'll start reporting
within one `CHECK_INTERVAL` (6h by default; restart the agent's container
if you don't want to wait that long for the first report to show up).

Details on setting up Homepage widgets from this: [docs/integrations/homepage.md](docs/integrations/homepage.md).

### Step 3 — Push-button updates (optional, requires Step 2)

Lets you actually trigger package installs from the admin page instead of
just seeing what's pending. Full walkthrough, including the security model
and why it's safe to expose: [Triggering updates](#triggering-updates-companion).

### Step 4 — Gatus / notifications (optional)

- [docs/integrations/gatus.md](docs/integrations/gatus.md) — poll `/status` for uptime-style monitoring.
- [docs/integrations/telegram.md](docs/integrations/telegram.md) — you already set
  `TELEGRAM_BOT_TOKEN`/`TELEGRAM_CHAT_ID` in Step 1 if you want these; there's nothing extra to install.

---

The rest of this README covers each piece above in more depth, plus every
configuration option. If you just want to build from source instead of
using the published image (e.g. to test a change), see
[Development](#development).

## How it works

The container never writes to the host's own files. It bind-mounts the
host's apt sources, dpkg status, `/etc/os-release`, and `/var/run`
**read-only**, and keeps its own apt package-list cache and state under a
container-owned directory bind-mounted at a fixed host path (default
`/var/lib/update-detector`, override with `STATE_DIR`; see
`docker-compose.yml`) rather than a Docker-managed named volume, so a
host-native companion process can share that same directory.

Unlike a named volume, Docker never auto-chowns a bind-mounted host
directory to match the image, so a freshly created `STATE_DIR` (including
one `docker compose` auto-creates because it didn't exist yet) starts out
owned by `root`. The container's entrypoint (`docker-entrypoint.sh`) fixes
this itself on every startup — `chown`s `STATE_DIR` to its own non-root
user before dropping privilege and exec'ing the actual binary — so this is
never something you need to do by hand.

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
agent — the checker is designed as an interface specifically so that's a
new implementation, not a rewrite (see
`docs/plugin-architecture-plan.md`). macOS is still planned; Windows has
an experimental detection-only implementation (`internal/checker/windows`):

- **Windows Update (primary signal)**: queries the Windows Update Agent
  API — the same COM interface (`Microsoft.Update.Session` /
  `CreateUpdateSearcher().Search("IsInstalled=0 and IsHidden=0")`) the
  Settings app's own "Check for updates" ultimately goes through —
  via a `powershell -Command` one-liner emitting JSON, parsed in Go
  (`internal/checker/windows/windowsupdate.go`/`windowsupdate_parse.go`).
  This carries a real **MSRC severity rating** per update (`Critical`/
  `Important`/`Moderate`/`Low`, or empty for a non-security update) —
  counted as security when non-empty, a genuine signal winget has no
  equivalent of at all. Expected (not yet confirmed live) to work under
  `LocalSystem` without the account workaround winget needs below, since
  the Windows Update service is system-level, not tied to a specific
  user's own package registration.
- **Apply, via the companion (⚠️ unverified, actually modifies the
  system)**: `internal/companion/applier_windows.go` extends the same
  Windows Update Agent API to *install* updates, not just detect them —
  a checked "Apply selected" item whose name carries a `(KBnnnnnnn)`
  marker (every Windows Update title does) is downloaded and installed
  via `IUpdateDownloader.Download()`/`IUpdateInstaller.Install()`;
  anything else falls back to winget (`winget upgrade --id <name>`), the
  same optional/supplementary role it already has for detection. "Upgrade
  all"/"Full upgrade all" install every currently pending Windows Update
  (no dist-upgrade/upgrade distinction, same real semantic gap winget's
  own `FullUpgrade` already has). **This is the least-tested code path
  in this entire Windows implementation** — every other piece here is
  read-only detection; this one actually installs updates and can
  require a reboot to take effect. Tested only via fixture tests of the
  KB-vs-winget name-splitting logic, never against a real Windows Update
  install. If you try this, start with a single low-stakes update via
  "Apply selected," not "Upgrade all," and watch the live output pane.
- **Packages via winget (optional, supplementary)**: shells out to
  `winget upgrade`, parsing its table output the same best-effort way the
  Debian checker parses `apt-get -s dist-upgrade`'s text output. Covers
  separately-managed packages winget itself tracks — the same
  relationship apt has to Ubuntu/Debian's own release upgrades — merged
  into the same package list Windows Update above populates. **No
  security/severity signal exists in winget at all** (unlike apt's
  `-security` pocket or Windows Update's own MSRC ratings above) — every
  winget-sourced upgrade reports `security: false`. Winget's own table
  format has changed across App Installer versions, and winget itself
  may be entirely absent on locked-down or Server Windows machines, or
  (see below) simply not runnable from the account the agent/companion
  happens to run as; none of that is treated as an error
  since winget is optional here — Windows Update above is what actually
  matters, and a winget failure only ever means missing out on its
  supplementary package list, not degraded detection overall. Any other
  winget failure (bad output, a real winget error) is still surfaced.
- **Reboot-required**: reads three well-known `HKLM` registry keys —
  no admin privilege or `winget`/other exec needed, the most reliable
  part of this checker. No OS-upgrade detection at all in v1 (same
  posture as Debian) — `current_version`/`current_codename` are
  populated informationally from the registry only.
- **Installer**: `install.bat` (repo root, alongside `install.sh`) installs
  the agent, aggregator, and/or companion as native Windows Services —
  see [Triggering updates](#triggering-updates-companion) below. A `.bat`,
  not a `.ps1`: `cmd.exe` runs it directly with no PowerShell
  ExecutionPolicy to fight, which matters since the companion's own
  self-update feature re-invokes it non-interactively with no operator
  around to grant an exception. Config is stored in each service's own
  registry `Environment` value (`REG_MULTI_SZ`), the native equivalent of
  systemd's `EnvironmentFile=`.
- **winget effectively never runs under `install.bat`, by design**: every
  service `install.bat` creates defaults to running as `LocalSystem`,
  under which `winget` simply doesn't exist — `winget.exe` is an App
  Execution Alias registered per-*user* (it lives under that user's own
  `AppData\Local\Microsoft\WindowsApps`, on *that user's* `PATH` only),
  and `SYSTEM` has no such registration at all, confirmed live as
  `exec.LookPath("winget")` failing outright with "executable file not
  found in %PATH%" even though `winget` works fine interactively.
  `install.bat` used to offer to reconfigure a service's logon account
  to work around this; that prompt/offer has been removed (the code
  behind it is still there, just unreferenced) — since winget is only
  a supplementary signal (Windows Update above is the real one, and
  doesn't have this problem), the account-switching complexity wasn't
  worth it, and the installer no longer prompts for or offers it at
  all. The winget detection code itself still works if you manually
  reconfigure a service to run as a real account
  (`sc config <service> obj= ".\<user>" password= "<password>"`, plus
  granting it "Log on as a service" via `secpol.msc` if needed) — it's
  just not something the installer does for you anymore.
- Also untested against a real Windows machine end-to-end for most of
  this: only fixture-based parsing tests and a hosted CI runner (no
  `winget`/real registry/real Windows Service state to exercise) have
  exercised most of this so far. The Windows Service Control Protocol
  fix and the winget account/PATH issue above were both found and fixed
  from one real, live install — everything else here carries the same
  unverified caveat until it gets the same live exercise.
- **Roadmap, not started**: Windows Update above covers the OS itself,
  but this checker's package-manager story is meant to generalize past
  winget too — Scoop and Chocolatey as alternative/additional Windows
  package sources, a Homebrew-based macOS checker (see the macOS row
  above), and Docker image update detection on Linux (tag/digest drift,
  a different kind of "update" than an OS package manager reports) are
  all intended future checker plugins, none of them started.

On WSL2 specifically, `docker` being on `PATH` doesn't necessarily mean
there's a real engine running inside the distro at all — see
[WSL2](docs/wsl2.md) for why, and for `install.sh`'s native (no-Docker)
install path that avoids the problem entirely.

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
curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/install.sh | sudo sh
```

(On WSL2, this same script detects that and offers a different, native
install instead — see [WSL2](docs/wsl2.md).)

**On actual Windows** (experimental, unverified against a real host — see
[Limitations](#platform-limitations)): there's no Docker path at all, so
`install.bat` always installs natively, and it can't be piped the way
`install.sh` is — download it, then run it, from an elevated
(Administrator) Command Prompt:

```bat
curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/install.bat -o install.bat
install.bat
```

This prompts for which of the aggregator, the agent, and the companion to
install (or set `INSTALL_COMPONENTS=agent,companion` etc. non-interactively),
and installs each as a native Windows Service.

This auto-discovers everything it needs from the running container: its
bind-mounted state dir (for a one-time, in-memory-only token handoff over a
Unix socket — see [How it works](#how-it-works)), its `AGGREGATOR_URL`, and
its published port. It installs `update-detector-companion` as a systemd
service and starts it.

**Uninstall** works the same way, for any native component this script
installed — the companion here, or (on WSL2) the agent/aggregator from
[WSL2](docs/wsl2.md):

```sh
curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/install.sh | sudo sh -s -- --uninstall
```

On Windows, from an elevated Command Prompt, after downloading
`install.bat` as above: `install.bat --uninstall`.

(the `-s --` matters — a bare `sh --uninstall` would treat that as a
script *filename* instead of an argument, and just fail). It prompts for
which to remove, detecting what's actually installed first; for
scripted/non-interactive use, skip the prompt with `UNINSTALL_COMPONENTS`
(comma-separated: `aggregator,agent,companion`, no special `sh`
invocation needed for this one) the same way `INSTALL_COMPONENTS` works
for installing. It fully tears down anything native (unit, binary, env
file, state/data dir, dedicated system user) but only ever warns about a
Docker-based agent/aggregator — this script never created that
deployment, so it has no compose file path or volume names to safely act
on; remove those yourself with `docker compose down`.

**Triggering an action** is `POST /admin/agents/{id}/apply` on the
aggregator, gated by a shared secret (`ADMIN_APPLY_SHARED_SECRET` —
disabled/`501` entirely until set). **No reverse proxy needed** — the
admin page's Apply/Upgrade-all/Full-upgrade-all buttons will `prompt()`
you for this secret the first time you use one, remember it in that
browser's `localStorage`, and send it as `X-Admin-Apply-Secret` on every
apply call from then on (a wrong value gets cleared automatically so you
can retype it). That's the whole setup: set the env var, click a button,
paste the secret once.

**Live output**: once accepted, the admin page opens a live view of that
action's own output right on the triggering row — one line at a time, as
apt-get/install.sh/docker actually produce it, over a per-row `EventSource`
(`GET /admin/agents/{id}/output/stream`). Several hosts updating at once
each get their own independent live view. Reloading the page (or opening
it in another tab) picks up whatever's still in flight automatically. It's
best-effort and shows no history from before the moment it connects — the
action's final (truncated) output is still in the existing "recent
actions" log either way.

You can also trigger it directly, e.g. for scripting:

```sh
curl -X POST http://aggregator-host:9090/admin/agents/<id>/apply \
  -H "X-Admin-Apply-Secret: $ADMIN_APPLY_SHARED_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"type": "packages", "packages": ["curl"]}'
# type is one of: packages (requires "packages"), upgrade, full-upgrade
```

**`upgrade` vs. `full-upgrade` — which one actually clears what's
pending depends on OS flavor**, because of how each one's checker detects
"pending" in the first place:

- **Ubuntu** hosts detect pending packages via `apt-check`/`apt list
  --upgradable` (`internal/checker/ubuntu/packages.go`) — exactly the set
  plain `apt-get upgrade` (`type: upgrade`) can resolve. `upgrade` alone
  should normally clear everything shown as pending.
- **Debian / Raspberry Pi OS** hosts detect pending packages by
  *simulating* `apt-get dist-upgrade` (`internal/checker/debian/packages.go`)
  — a broader set that can include packages plain `upgrade` will hold
  back (skip without erroring) because resolving them needs to install a
  new dependency or remove an obsolete one. Only `full-upgrade` clears
  those.

Rule of thumb regardless of flavor: run `upgrade` first — it's the safer
one, since it never adds or removes packages, only ever a risk-free
version bump. Then check whether anything's still pending (**Force
recheck** gives an immediate answer instead of waiting for the next
`CHECK_INTERVAL`). If so — a real possibility on Debian/Pi given how
detection works there, less likely but not impossible on Ubuntu — that's
the signal to run `full-upgrade` to finish the job.

If you *do* already run a reverse-proxy auth setup (Authentik or
similar), you can point it at the aggregator and have it inject this same
header itself after login instead — the aggregator checks the header
either way, so a compromised proxy or network path alone still isn't
enough on its own to trigger an apply. This is purely optional, not a
requirement.

**Reboots are never automatic**, even if an upgrade sets
`reboot_required` — that stays a manual, human decision.

**The companion always runs `apt-get update` on the host first**, right
before the real install/upgrade/dist-upgrade command. It has to: the
companion acts directly on the host's own apt state, which is separate
from the containerized agent's own package-list cache (see
[How it works](#how-it-works)). Without this, the host's cache can be
stale relative to what the agent detected, and apt-get would silently
no-op ("already the newest version") on a package the admin page still
shows as pending — confirmed live, not just theoretical.

**Only one action at a time per host** — `apply` returns `409` if that
agent already has an unresolved action in flight, rather than queuing up
repeat/overlapping requests. A companion that disconnects mid-action
clears its own in-flight marker on reconnect, so a crashed or restarted
companion never permanently blocks future applies.

**The dashboard updates itself after an apply** — a successful (or even a
failed, since `apt-get` can partially apply before erroring) apply
triggers an out-of-band recheck on the agent (`POST /recheck`), so
`/status`, `/admin`, and the aggregator's next report reflect the change
right away instead of showing an already-applied package as still pending
for up to a full `CHECK_INTERVAL`.

**A "Force recheck" button** is also on the admin page for each connected
host — needs no secret (it can't change anything on the host, only make
it report sooner), so use it any time the numbers look stale and you
don't want to wait for the next `CHECK_INTERVAL`, without needing to
apply a package first just to trigger a refresh. It gets the same live
view as apply/self-update (there's just little to show — a recheck runs
no shell command, only triggers the agent's own detection cycle) and the
same auto-reload once the fresh report actually lands, whether it's
served by a companion or, per the next paragraph, the agent alone.

**Force recheck works even without a companion installed.** The agent
itself can hold the same aggregator connection the companion normally
does — whichever of the two is actually running holds it, and ownership
transfers automatically as they start and stop, so there's never more
than one connection per host. A companion always takes over from the
agent the moment it starts (it needs the connection more: it's the only
one that can actually apply anything), and the agent reclaims it
automatically if the companion later stops or crashes. The admin page
shows which one currently holds it ("connected" vs. "connected (via
agent)"). **Apply still strictly requires a real companion** — an
agent-only connection can only ever carry a recheck, never an apply, no
matter what's requested; the apply buttons and per-package selector
simply don't appear until a companion is actually connected.

**Every version is visible on the admin page** — the aggregator's own
build version is in the page header; each approved host shows its
agent's version under "Last report" and its companion's version next to
"connected," so a fleet with mismatched versions (e.g. some hosts not yet
redeployed after a release) is obvious at a glance rather than needing to
SSH into each host to check.

### Setting up the companion (new installs and upgrades)

Same steps whether this is a brand-new install or an existing deployment
you're adding this feature to — only step 2's volume migration is
upgrade-specific (skip it on a new install; `docker-compose.yml` already
defaults to the bind mount from day one). Do this once, in order —
aggregator first, then each agent host, one at a time.

**1. Aggregator** — wherever `docker-compose.aggregator.yml` actually runs.
Add the new env var to a `.env` file in the same directory as that compose
file (not `export` in your shell — that only lasts for the current session,
and won't be there the next time something runs `docker compose up`):

```sh
cd /path/to/docker-compose.aggregator.yml
echo "ADMIN_APPLY_SHARED_SECRET=$(openssl rand -hex 32)" >> .env
cat .env   # save this value somewhere too — you'll need it for the curl above
docker compose -f docker-compose.aggregator.yml pull
docker compose -f docker-compose.aggregator.yml up -d --force-recreate
docker compose -f docker-compose.aggregator.yml logs --tail 5
```

Confirm the log says `apply endpoint enabled` (not `disabled ... not set`).

**2. Each agent host** — new install: skip straight to the `docker compose
pull && up -d` at the bottom of this block, nothing to migrate. Existing
deployment: migrate `update-detector-state` (the old named volume) to the
new bind mount first, then pull the new image:

```sh
cd /path/to/docker-compose.yml
sudo mkdir -p /var/lib/update-detector
docker run --rm \
  -v update-detector_update-detector-state:/from \
  -v /var/lib/update-detector:/to \
  alpine cp -a /from/. /to/
# (check the exact volume name first with `docker volume ls` if unsure —
# it's prefixed with your compose project name, not always "update-detector")

docker compose pull
docker compose up -d
docker compose logs --tail 20
```

If you'd rather skip the volume copy, that's fine too — the agent just
re-enrolls as a "new" agent and starts state/history fresh (approve it again
on `/admin`); nothing critical is lost either way.

**3. Install the companion on that same host:**

```sh
curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/install.sh | sudo sh
systemctl status update-detector-companion --no-pager
journalctl -u update-detector-companion -n 20 --no-pager
```

Look for `companion: connected to aggregator stream`.

**4. Verify on `/admin`** — that host should now show **connected** under
"Companion," with package checkboxes and the Upgrade-all/Full-upgrade-all
buttons. Try a real apply on one low-risk package before trusting it for
anything bigger, and check the negative case too (request a package that
*isn't* pending — the companion should reject it without running `apt-get`
at all).

Repeat steps 2–4 for the next agent host.

## Self-updating update-detector

The aggregator periodically checks GitHub for a newer update-detector
release (`SELF_UPDATE_CHECK_INTERVAL`, default 24h) and surfaces it on
`/admin`: a fleet-wide banner if the aggregator itself is behind, and
**Update agent**/**Update companion**/**Update aggregator** buttons on
each host row whose companion is connected and reports an older version.
Applying one works exactly like a package apply — pushed to that host's
companion over the same SSE connection, gated by the same
`ADMIN_APPLY_SHARED_SECRET`/`X-Admin-Apply-Secret`. Native components get
a fresh binary swapped in (via `install.sh`) and their systemd unit
restarted. Docker-based ones pull the *exact* requested version's image,
retag it locally onto whatever tag the container's own compose file
already references (usually `:latest`), then `docker compose up -d` for
that service — deliberately not a plain `docker compose pull`, which
would just re-fetch whatever `:latest` currently points to on the
registry rather than the version actually requested. Everything runs
against the exact compose file/working directory Docker Compose itself
recorded when the container was created (so it respects whatever `.env`,
volumes, etc. you're actually running).

**The aggregator is always the dependency root.** Agent/companion can
never be updated past the aggregator's own currently-running version —
enforced server-side (not just hidden in the UI), specifically to avoid
the kind of protocol confusion a newer agent talking to an older
aggregator can cause (confirmed live during development: an aggregator
that predates a given protocol change just mistreats a newer client
instead of rejecting it cleanly). Update the aggregator first if you want
to bring the whole fleet forward.

**Updating the aggregator restarts the process serving this very page.**
The admin page handles this like Jenkins does after a plugin install: a
banner appears and the page polls `GET /healthz` in the background
(fetch failures during the actual restart window are expected and
ignored), reloading automatically only once that endpoint reports a
version that's genuinely newer than what was running before — not just
"got any response," so a brief blip mid-restart can't trigger a
premature reload.

**Updating the aggregator itself needs a companion on *its own* host.**
Every update — including the aggregator's — is executed by a companion,
and a companion's identity is always derived from an agent paired with
it on the same host. A host that runs *only* the aggregator (a real,
explicitly-supported setup — see `docker-compose.aggregator.yml`, "runs
once, wherever you want it... not per-host") has no companion at all, and
so no way to execute this. If you want the aggregator itself to be
self-updatable this way, also run an agent + companion pair on whichever
host actually runs it, and trigger "Update aggregator" from that host's
own row.

**Self-updating the companion itself is the one case where the live
output view can drop mid-update** — that restarts the very process
producing it. It shows as "companion disconnected — waiting for it to
come back" rather than going silent; the action's real outcome still
arrives once the new process comes up and reports back, same as it
always has.

Releases move through four stages, in order: `alpha` < `beta` < `rc` <
`release` (`v0.10.0-alpha1`, `v0.10.0-beta1`, `v0.10.0-rc1`, `v0.10.0`).
Set `SELF_UPDATE_CHANNEL` on the aggregator to the *minimum* stage you
want surfaced as "available" — `release` (the default) only ever offers
a real release; `rc` also offers rc builds (and prefers a newer rc over
an older real release); `beta` also offers beta and rc; `alpha` offers
everything. Most fleets should stay on `release`. The admin page also
has a live channel selector (alpha/beta/rc/release) that takes effect
immediately without a restart — handy for testing a pre-release cut
against a live fleet — but it resets back to whatever `SELF_UPDATE_CHANNEL`
says on every aggregator restart, same as the version cache itself.
(The older `SELF_UPDATE_INCLUDE_PRERELEASE` boolean still works if
`SELF_UPDATE_CHANNEL` is unset — `true` maps to `alpha`, `false` to
`release` — but new setups should use `SELF_UPDATE_CHANNEL` directly.)

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
| `SELF_UPDATE_CHECK_INTERVAL` | `24h` | How often to check GitHub for a newer update-detector release — see [Self-updating update-detector](#self-updating-update-detector) |
| `SELF_UPDATE_CHANNEL` | `release` | Minimum release stage to surface as "available": `alpha`, `beta`, `rc`, or `release` — see [Self-updating update-detector](#self-updating-update-detector) |
| `SELF_UPDATE_INCLUDE_PRERELEASE` | `false` | Deprecated: only consulted when `SELF_UPDATE_CHANNEL` is unset; `true` maps to `alpha`, `false` to `release` |

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
`.github/workflows/release.yml`, which builds both images for
`linux/amd64` and `linux/arm64` (covers Ubuntu servers and Raspberry Pi 4B
from the same tag, via QEMU emulation since GitHub's hosted runners are
amd64-only) and pushes them to GHCR:

```
ghcr.io/sinwe/update-detector:<tag>    and  :latest
ghcr.io/sinwe/update-aggregator:<tag>  and  :latest
```

(`:latest` is skipped for `-alpha`/`-beta`/`-rc` tags — see
[Self-updating update-detector](#self-updating-update-detector) for why.)

The same tag also cross-compiles all three binaries — `update-detector`,
`update-aggregator`, and `update-detector-companion` — for `linux/amd64`,
`linux/arm64`, and `windows/amd64` (no QEMU needed for these — Go
cross-compiles natively) and attaches all nine as plain binary assets on
that tag's GitHub release. `install.sh`/`install.bat` fetch whichever they
need: just the companion on a Docker-based host (see
[Triggering updates](#triggering-updates-companion)), or any/all three for
a native WSL2 or Windows install (see [WSL2](docs/wsl2.md)). The aggregator
also reads this same release list on its own, periodically, to know when a
newer version is available to offer from `/admin` — see
[Self-updating update-detector](#self-updating-update-detector).

No separate secrets needed — the workflow uses the repo's own built-in
`GITHUB_TOKEN` (with `contents: write` and `packages: write` permissions)
for both the GHCR image pushes and creating the release/uploading assets.

Deploying a release is then just `docker compose pull && docker compose up
-d` on each host — no `--build` needed, since `docker-compose.yml` /
`docker-compose.aggregator.yml` both reference the published `image:`
directly (they also keep `build: .` for local dev — see
[Development](#development)).

That also means a deploy host doesn't need this repo cloned at all — just
fetch the one compose file it actually needs with `curl` or `wget`:

On an agent host:

```sh
curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/docker-compose.yml -o docker-compose.yml
# or: wget https://raw.githubusercontent.com/sinwe/update-detector/main/docker-compose.yml

docker compose pull
docker compose up -d
```

On the aggregator host:

```sh
curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/docker-compose.aggregator.yml -o docker-compose.aggregator.yml
# or: wget https://raw.githubusercontent.com/sinwe/update-detector/main/docker-compose.aggregator.yml

docker compose -f docker-compose.aggregator.yml pull
docker compose -f docker-compose.aggregator.yml up -d
```

## Extending

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
