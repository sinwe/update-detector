# Running on WSL2

**TL;DR:** on WSL2, install natively via `install.sh` (no Docker) unless
you've specifically confirmed you have a real Linux Docker engine running
*inside* your distro — not just Docker Desktop's Windows-side CLI.

## The risk this avoids

WSL2 distros very commonly have `docker` on `PATH` without having any real
Docker engine inside them at all — if you have Docker Desktop for Windows
installed, its Windows-side CLI binary gets exposed into WSL2 via the
`/mnt/c/...` filesystem passthrough. Confirmed on a real Windows machine:

```
$ which docker
/mnt/c/Program Files/Docker/Docker/resources/bin/docker
```

That binary talks to Docker Desktop's own hidden Linux VM, not your WSL2
distro's own kernel or filesystem. If `update-detector` runs as a container
against that shim, its host bind mounts (`/etc/apt/sources.list`,
`/var/lib/dpkg`, `/var/run/reboot-required`, etc.) resolve against Docker
Desktop's VM instead of your actual WSL2 filesystem — **the agent would
faithfully detect updates for the wrong system, with no visible error.**
Not a crash, not a warning — just silently wrong data.

## Check which one you actually have

```sh
readlink -f "$(command -v docker)"
```

- Resolves under `/mnt/c/...` → that's the Windows shim. No real engine in
  this distro. Use the native install below.
- Resolves to a normal path (e.g. `/usr/bin/docker`) → you likely have a
  genuine engine (e.g. installed via `apt install docker-ce` directly
  inside a systemd-enabled distro, a real and supported setup). A second
  confirming check: `pgrep -f dockerd` should find a real process in this
  case — it never will for the Windows-shim case, since that daemon isn't
  running inside this distro at all.

If you have a genuine engine, the normal
[Installation](../README.md#installation) instructions (Docker Compose)
work exactly as documented — nothing WSL2-specific needed. The rest of
this page is for the (much more common) shim case.

## Native install, step by step

Same `install.sh` used everywhere else in this project — it detects WSL2
automatically and offers a different choice than it does elsewhere. This
walkthrough covers all three components in one run (the most common case
if you're setting up a fresh host); skip whichever numbered step doesn't
apply if you're only installing a subset.

### 1. (Optional) Set configuration before running install.sh

Native config isn't `.env`-based — see
[Configuration](#configuration--theres-no-env-here) below for the full
explanation. If you need anything other than defaults (an aggregator
elsewhere, Telegram credentials, an apply secret), export it now, in the
same shell that's about to run `install.sh`:

```sh
export AGGREGATOR_URL=http://localhost:9090   # only needed on an agent/companion-only host pointing elsewhere
export ADMIN_APPLY_SHARED_SECRET=$(openssl rand -hex 32)   # only meaningful if installing the aggregator
```

If you skip this step, every value just takes its documented default —
nothing below breaks, you just get default behavior (e.g. no apply secret
set, so `/admin` apply buttons stay disabled until you set one later).

### 2. Run install.sh and pick components

```sh
curl -fsSL https://forgejo.winar.to/winarto/update-detector/raw/branch/main/install.sh | sudo -E sh
```

(`-E` carries through anything you exported in step 1 — plain `sudo`
silently drops it instead, see below.)

It detects WSL2 on its own and prompts:

```
  1) aggregator only
  2) detector (agent) only
  3) companion only
  4) all three
```

For scripted/non-interactive use (e.g. provisioning automation), skip the
prompt entirely with `INSTALL_COMPONENTS` (comma-separated):

```sh
INSTALL_COMPONENTS=aggregator,agent,companion curl -fsSL https://forgejo.winar.to/winarto/update-detector/raw/branch/main/install.sh | sudo -E sh
```

### 3. Check that everything you picked actually started

Each component becomes a normal systemd unit (`update-detector.service`,
`update-aggregator.service`, `update-detector-companion.service`) — check
status the same way you would for any of them, for each one you installed:

```sh
systemctl status update-detector update-aggregator update-detector-companion
journalctl -u update-detector -f
```

All should show `active (running)`. If the agent's log shows `report
failed: ... unexpected status 403 Forbidden` right now, that's expected —
continue to step 4, it resolves itself there.

### 4. If you installed the agent: approve it

A newly-installed agent enrolls with the aggregator as `pending` on its
very first contact — it gets rejected with that `403` above until a human
approves it, by design (trust-on-first-contact). Open
`http://<aggregator-host>:9090/admin`, find the new host under "Pending,"
and click **Approve**. It starts reporting successfully within one
`CHECK_INTERVAL` (6h by default) on its own, or immediately if you
`systemctl restart update-detector` right after approving.

### 5. If you installed the companion: confirm it's connected

Refresh `/admin` — the approved host's row should say **connected**
(not "not connected"), and a **Force recheck** button should be present.
If you installed the agent *without* a companion, you'll still see
"connected (via agent)" here — that's the agent holding the same
connection a companion normally would, enough for Force-recheck but not
for applying anything; see [Companion discovery, either
way](#companion-discovery-either-way) below for how the two coordinate.

### 6. Verify the data is actually correct

Since the whole point of this page is avoiding a *silent* wrong-data bug
(see [The risk this avoids](#the-risk-this-avoids) above), it's worth a
one-time sanity check: compare the agent's own count against a direct
`apt` check in the same distro.

```sh
curl -s http://localhost:8080/status | python3 -c "import json,sys; print(json.load(sys.stdin)['packages']['upgradable_total'])"
apt list --upgradable 2>/dev/null | tail -n +2 | wc -l
```

These two numbers should match exactly. If they don't, something's
resolving against the wrong filesystem — recheck the Docker-shim question
at the top of this page.

## Configuration — there's no `.env` here

The `.env` file the main [Configuration](../README.md#configuration)
instructions mention is a **`docker compose`-specific mechanism** — it
doesn't exist for the native install at all. Don't create one next to
`install.sh` expecting it to be picked up; it won't be. Native config
works two ways instead, both using the same variable names as the
Configuration table:

**Set before installing** — export in the shell that runs `install.sh`
itself, so its own values get baked into the systemd env file below.
Note the `sudo -E`, not plain `sudo` — without `-E`, `sudo` resets the
environment and your exported vars never reach the script at all,
silently falling back to defaults:

```sh
export AGGREGATOR_URL=http://localhost:9090
export TELEGRAM_BOT_TOKEN=your-bot-token-from-BotFather
export TELEGRAM_CHAT_ID=your-chat-id
curl -fsSL https://forgejo.winar.to/winarto/update-detector/raw/branch/main/install.sh | sudo -E sh
```

**Change config after installing (or at any time)** — edit the systemd
`EnvironmentFile` directly, then restart the unit to apply it:

```sh
$EDITOR /etc/default/update-detector      # or /etc/default/update-aggregator
systemctl restart update-detector          # match the unit to the file you edited
```

This is the native equivalent of editing `.env` and re-running
`docker compose up -d` — a plain `systemctl restart` always re-reads the
file (unlike `enable --now`, whose implicit `start` is a no-op if the
service is already running, silently keeping the old values). Aggregator
vars use a separate `AGGREGATOR_`-prefixed set of names at install time
(`AGGREGATOR_TELEGRAM_BOT_TOKEN`/`_CHAT_ID`, `ADMIN_APPLY_SHARED_SECRET`)
specifically so installing both the agent and the aggregator in one run
can't accidentally point one component's secret at the other's file —
but `/etc/default/update-aggregator` itself gets the real, unprefixed
name (`TELEGRAM_BOT_TOKEN`, etc.), matching what `update-aggregator`'s own
code actually reads.

## Companion discovery, either way

If you installed the agent natively (above), the companion — installed in
that same `install.sh` run, or separately later — finds it automatically
via `/etc/default/update-detector` and `systemctl is-active
update-detector`; no Docker inspection involved. If you instead have a
genuine in-distro Docker engine and ran the containerized agent, the
companion falls back to the same Docker-based discovery this project uses
everywhere else. Running both a native agent and a containerized one on
the same host at once is a real ambiguous state (double detection cycles,
double aggregator enrollment) — `install.sh` warns loudly if it finds both,
though it doesn't stop you; pick one and remove the other.

## One WSL2-specific gotcha to avoid

If you override `STATE_DIR` (agent) or `COMPANION_SOCKET_PATH`, keep it on
the distro's own native filesystem — the default `/var/lib/update-detector`
is fine. **Never point it at a `/mnt/c/...` Windows-drive path.** Unix
domain sockets (used for the companion's one-time token handoff) and POSIX
permission bits don't work correctly on Windows drvfs/9p mounts, and the
handoff would silently fail.

Back to [README](../README.md).
