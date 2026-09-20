#!/bin/sh
# install.sh installs update-detector's pieces -- as Docker containers
# where that's actually usable, native systemd services otherwise. Run as
# root:
#
#   curl -fsSL https://raw.githubusercontent.com/sinwe/update-detector/main/install.sh | sudo sh
#
# On a host that already has an agent (Docker or native), this installs
# just the companion, auto-discovering everything it needs from the
# existing one: its state dir/bind mount (for the Unix-socket token
# handoff), its AGGREGATOR_URL, and its published port. This is the same
# behavior install.sh has always had.
#
# On a host with nothing installed yet, this prompts (interactively, or
# via INSTALL_COMPONENTS below) for which of the aggregator, the agent, and
# the companion to set up. For the aggregator and the agent specifically
# (the companion is always native -- it needs real root to run apt-get),
# it then checks whether Docker is actually usable here (see
# docker_available below) and defaults to a Docker Compose deployment when
# it is -- prompting for a directory (default ~/docker-updater; the agent
# and aggregator share one by default -- see default_docker_dir -- since
# their compose files have different names and the aggregator always runs
# under its own fixed Compose project name, they don't collide there) --
# or a native systemd service otherwise. Re-running this later against an
# existing Docker deployment finds it automatically (via the same
# Compose-label introspection the companion's own self-update already
# relies on) and updates it in place, rather than re-prompting or creating
# a duplicate. Set USE_DOCKER=1/0 to skip that prompt non-interactively;
# DOCKER_DIR/AGGREGATOR_DOCKER_DIR override the default directory the same
# way (set both to genuinely separate the two, if you'd rather not share).
#
# "Actually usable" excludes the common WSL2 case where `docker` is only
# Docker Desktop's Windows-side CLI shim (confirmed live: resolves to
# /mnt/c/Program Files/Docker/...), talking to Docker Desktop's own hidden
# VM, not the WSL2 distro's own filesystem -- a containerized agent there
# would silently detect updates for the wrong system, with no visible
# error. See docs/wsl2.md for the full explanation. A WSL2 distro with a
# genuine in-distro Docker engine is treated as a normal Docker host.
#
# On macOS this installs the agent and companion as native LaunchDaemons
# (no Docker path -- a container has no visibility into the host's
# Homebrew cellar, so it could never detect anything there -- and no
# aggregator port). The agent runs as the Homebrew owner, the companion
# as root (like Linux -- so self-update can re-invoke this script; brew
# itself still runs as the owner via sudo -u), both at boot with no
# login required, keeping everything under the owner's
# ~/.update-detector. Homebrew itself must already be installed.
# The companion pairs through the agent, so install the agent first.
#
# Set INSTALL_VERSION to pin a release instead of "latest". Set
# INSTALL_COMPONENTS (comma-separated: aggregator,agent,companion) for a
# scripted/non-interactive install of any of those three on any host --
# this is also how the companion's own self-update feature re-invokes this
# exact script to update a specific *native* component wherever it's
# actually running (Docker-based components self-update through a
# different path -- see internal/companion/selfupdate.go -- so this reuse
# only ever targets native installs).
#
# Set SELF_UPDATE_CHANNEL (one of alpha/beta/rc/release, default release)
# to track a pre-release channel instead: Docker installs pin the compose
# image to the matching :latest-<channel> tag (and the aggregator records
# it for its own update checks), so a later `docker compose pull` follows
# that same channel instead of silently downgrading to whatever older tag
# the file happened to pin before. AGGREGATOR_SELF_UPDATE_CHANNEL
# overrides it for the aggregator alone.
#
# To remove a native install instead, either pipe with an explicit
# argument --
#
#   curl -fsSL .../install.sh | sudo sh -s -- --uninstall
#
# -- (the `-s --` is required: plain `sh --uninstall` treats that as a
# script *pathname*, not stdin+args, and will not work) or, for
# scripted/non-interactive use, set UNINSTALL_COMPONENTS the same way as
# INSTALL_COMPONENTS above -- no special invocation needed for that one.
# Either way, this only ever touches a *native* install it can find unit
# files for; a Docker-based agent/aggregator is left alone with a warning
# (naming the directory it found, if any) -- uninstall never runs `docker
# compose down` itself, since it can't safely tell "install.sh set this up"
# apart from "you did, by hand," and a wrong guess there means deleting a
# volume.

set -eu

GITHUB_API="https://api.github.com/repos/sinwe/update-detector"
INSTALL_SH_RAW_URL="https://raw.githubusercontent.com/sinwe/update-detector/main/install.sh"
INSTALL_VERSION="${INSTALL_VERSION:-latest}"
# Where the companion caches its own copy of this script for self-update
# use (see internal/companion/selfupdate.go) -- it re-invokes this file
# non-interactively rather than duplicating the download/atomic-swap/
# systemctl-restart logic already tested and shipped here.
CACHED_INSTALL_SH="/usr/local/lib/update-detector/install.sh"

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh: must be run as root" >&2
  exit 1
fi

# Neither docker nor systemctl is required unconditionally here: docker
# is frequently present but not a working engine (WSL2's CLI shim, see
# header), and a Docker-only install (agent/aggregator via Compose, no
# companion) never needs systemctl at all. Each code path below checks
# for what it actually needs instead -- systemctl inside install_unit,
# used only by the native install functions and the always-native
# companion.
if ! command -v curl >/dev/null 2>&1; then
  echo "install.sh: curl is required but not found on PATH" >&2
  exit 1
fi

# is_macos -> true on macOS (Darwin), where there is no systemd, no
# /proc, and no Docker path worth offering (see header).
is_macos() {
  [ "$(uname -s)" = "Darwin" ]
}

# goos_name -> GOOS infix for release asset names (update-detector
# supports linux/darwin agents; the companion/aggregator stay linux-only
# for now, so this only ever matters for the agent download below).
goos_name() {
  if is_macos; then echo darwin; else echo linux; fi
}

# Resolved lazily, on first actual native binary download -- a pure
# Docker install (no native component involved at all) never needs this,
# and shouldn't fail on an architecture this script has no native build
# for but Docker/the published images support fine.
goarch=""
resolve_goarch() {
  [ -n "$goarch" ] && return 0
  arch="$(uname -m)"
  case "$arch" in
    x86_64) goarch=amd64 ;;
    aarch64|arm64) goarch=arm64 ;;
    *) echo "install.sh: unsupported architecture $arch for a native install" >&2; exit 1 ;;
  esac
}

# Any one of these being true is sufficient; checking all three is just
# defense against any single one being absent on some WSL2 build.
is_wsl2() {
  [ -n "${WSL_DISTRO_NAME:-}" ] && return 0
  grep -qi microsoft /proc/version 2>/dev/null && return 0
  [ -e /proc/sys/fs/binfmt_misc/WSLInterop ] && return 0
  return 1
}

# docker_available -> true if docker is actually usable for a real
# deployment here: present, a live engine actually answers to it, and
# (on WSL2 specifically) it isn't just Docker Desktop's Windows-side CLI
# shim -- confirmed live, that shim resolves under /mnt/c/... and answers
# `docker info` just fine, but talks to Docker Desktop's own hidden VM,
# not this distro's filesystem, so a container using it would silently
# detect updates for the wrong system (see docs/wsl2.md, "Check which one
# you actually have" -- the same check reused here).
docker_available() {
  # Never on macOS, even with Docker Desktop installed: a container there
  # sees the Linux VM, never the Mac host's Homebrew, so a containerized
  # agent would silently detect updates for the wrong system -- same class
  # of mistake as the WSL2 shim below.
  is_macos && return 1
  command -v docker >/dev/null 2>&1 || return 1
  docker info >/dev/null 2>&1 || return 1
  if is_wsl2; then
    case "$(readlink -f "$(command -v docker)" 2>/dev/null)" in
      /mnt/*) return 1 ;;
    esac
  fi
  return 0
}

# resolve_asset_url ASSET_NAME -> prints that asset's browser_download_url
# for $INSTALL_VERSION, or nothing if not found in that release.
resolve_asset_url() {
  asset_name="$1"
  if [ "$INSTALL_VERSION" = "latest" ]; then
    release_url="$GITHUB_API/releases/latest"
  else
    release_url="$GITHUB_API/releases/tags/$INSTALL_VERSION"
  fi
  # Deliberately not `curl -f ... | jq ...`: same reasoning as
  # release.yml's own publish step -- a failed request here would silently
  # produce an empty result via a suppressed body, rather than a clear
  # error. grep+sed avoids needing jq on the *installing* host too, which
  # (unlike the release workflow's own runner) we don't control.
  # GitHub's API rejects requests with no User-Agent at all (403).
  curl -fsSL -H "User-Agent: update-detector-install.sh" "$release_url" \
    | grep -o "\"browser_download_url\":[^,]*$asset_name\"" \
    | head -1 \
    | sed -E 's/.*"(https[^"]+)"$/\1/'
}

# download_binary NAME DEST -> downloads NAME-$goos-$goarch to DEST,
# atomically (via a .new + mv, so a partial download never replaces a
# working binary) and executable.
download_binary() {
  name="$1" dest="$2"
  resolve_goarch
  asset_name="$name-$(goos_name)-$goarch"
  echo "install.sh: resolving $asset_name from release $INSTALL_VERSION..."
  download_url=$(resolve_asset_url "$asset_name")
  if [ -z "$download_url" ]; then
    echo "install.sh: could not find asset $asset_name in release $INSTALL_VERSION" >&2
    exit 1
  fi
  echo "install.sh: downloading $download_url"
  curl -fsSL "$download_url" -o "$dest.new"
  chmod 0755 "$dest.new"
  mv "$dest.new" "$dest"
}

# cache_install_sh_for_companion -> saves a fresh copy of this exact
# script to CACHED_INSTALL_SH, for the companion's own self-update
# feature to re-invoke later (this script is normally run via
# `curl | sh`, piped straight from stdin with no file of its own on
# disk to copy -- so this re-downloads it by URL instead). Only called
# from install_companion, since only a host with a companion has any
# use for this at all. Best-effort: a failure here is a warning, not
# fatal -- the companion still works for apply/recheck either way, and
# every future companion install/self-update naturally retries this.
cache_install_sh_for_companion() {
  echo "install.sh: caching a copy of install.sh for the companion's own self-update use..."
  mkdir -p "$(dirname "$CACHED_INSTALL_SH")"
  # Pinned to the exact tag just installed/self-updated to, not main's
  # raw content -- fetching main here would cache whatever install.sh
  # happens to be on main *right now*, which can be arbitrarily far
  # behind (or, on a release branch not yet merged, entirely missing
  # fixes) the version actually running. This keeps the cached script
  # self-consistent with the release it was cached alongside.
  if [ "$INSTALL_VERSION" = "latest" ]; then
    raw_url="$INSTALL_SH_RAW_URL"
  else
    raw_url="https://raw.githubusercontent.com/sinwe/update-detector/$INSTALL_VERSION/install.sh"
  fi
  if curl -fsSL "$raw_url" -o "$CACHED_INSTALL_SH.new"; then
    chmod 0755 "$CACHED_INSTALL_SH.new"
    mv "$CACHED_INSTALL_SH.new" "$CACHED_INSTALL_SH"
  else
    echo "install.sh: warning: could not cache a copy of install.sh -- self-update via the companion won't work until this succeeds" >&2
  fi
}

# launchd_reload LABEL PLIST -> bootout (if loaded), wait for the unload
# to actually complete, then bootstrap. bootout is asynchronous: it
# returns before the job has finished unloading, and an immediate
# bootstrap then fails (confirmed live as "Bootstrap failed: 5:
# Input/output error", leaving the old daemon dead with nothing
# replacing it).
launchd_reload() {
  label="$1" plist="$2"
  launchctl bootout "system/$label" 2>/dev/null || true
  i=0
  while launchctl print "system/$label" >/dev/null 2>&1; do
    i=$((i + 1))
    if [ "$i" -ge 30 ]; then
      echo "install.sh: timed out waiting for $label to unload" >&2
      exit 1
    fi
    sleep 1
  done
  launchctl bootstrap system "$plist"
}

# install_unit NAME -> daemon-reload + enable + restart. Not `enable --now`
# -- its implicit `start` is a no-op if the service is already running
# (e.g. re-running this script to pick up a config change), silently
# leaving the old process running with its stale environment. `restart`
# unconditionally stops-then-starts regardless of current state.
install_unit() {
  if ! command -v systemctl >/dev/null 2>&1; then
    echo "install.sh: systemctl is required for a native install but not found on PATH" >&2
    exit 1
  fi
  systemctl daemon-reload
  systemctl enable "$1"
  systemctl restart "$1"
}

# ensure_system_user NAME -> creates an unprivileged system user if it
# doesn't already exist yet. Neither the agent nor the aggregator ever
# needs root: both only read world-readable host files (or nothing host-
# related at all, for the aggregator) and write to their own state dir --
# same posture as the container images' own non-root USER directive.
ensure_system_user() {
  if ! id "$1" >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin "$1"
  fi
}

# remove_system_user NAME -> symmetric counterpart to ensure_system_user,
# for uninstall. Guarded the same way (id check first) so removing an
# already-clean or never-created user is a safe no-op under set -eu,
# rather than aborting the rest of an uninstall.
remove_system_user() {
  if id "$1" >/dev/null 2>&1; then
    userdel "$1"
  fi
}

# native_unit_present NAME -> true if a native install of NAME exists on
# this host, regardless of running state. systemd unit file on Linux, a
# LaunchDaemon plist on macOS (label com.sinwe.NAME, see
# install_agent_launchd -- only the agent exists on macOS so far).
native_unit_present() {
  if is_macos; then
    [ -f "/Library/LaunchDaemons/com.sinwe.$1.plist" ]
  else
    [ -f "/etc/systemd/system/$1.service" ]
  fi
}

# env_value FILE KEY -> prints whatever follows the first "=" on KEY's
# line in FILE, verbatim, or nothing if absent. Deliberately not `. FILE`
# (shell-sourcing) -- these files are written for systemd's own
# EnvironmentFile=, which treats everything after "=" as the literal
# value with no shell parsing at all, so a value containing a space (e.g.
# a HOSTNAME_OVERRIDE like "Pegasus WSL2") is completely valid there.
# Sourcing the same file as a shell script instead tries to *execute*
# that line as a command with an env var set, and fails outright the
# moment any value contains whitespace or another shell metacharacter --
# confirmed live, this is exactly what broke install_companion's own
# native-agent discovery on a real WSL2 host.
env_value() {
  sed -n "s/^$2=//p" "$1" 2>/dev/null | head -1
}

# docker_container_for PATTERN -> prints the first container ID (running
# or stopped -- unlike install_companion's own discovery below, which
# deliberately only looks at running containers since it needs a *live*
# agent to wire a new companion against; this is for uninstall's "is
# anything here at all" question instead, a different purpose, so it's
# a separate helper rather than reusing that code) whose image matches
# PATTERN, or nothing. Callers must pass an anchored pattern, e.g.
# "(^|/)update-detector(:|$)" -- see install_companion's own discovery
# awk below for why the anchoring matters (so "update-detector" can't
# accidentally match an "update-detector-companion" image).
#
# Deliberately not "docker ps --format {{.Image}}": that field silently
# falls back to printing a bare image ID once a container's original tag
# has been reassigned to a different image (e.g. a later `docker pull`
# of the same :latest this repo's own compose files pin, moved every
# time release.yml pushes a new tag) -- confirmed live against a real
# long-running container whose `docker ps` showed a raw hex ID while
# `docker inspect .Config.Image` still correctly reported its real tag.
# Config.Image is the reference the container was actually created with
# and never silently changes, so each candidate is inspected instead.
docker_container_for() {
  command -v docker >/dev/null 2>&1 || return 0
  {
    for id in $(docker ps -a --format '{{.ID}}' 2>/dev/null); do
      echo "$id $(docker inspect --format '{{.Config.Image}}' "$id" 2>/dev/null)"
    done
  } | awk -v pat="$1" '$2 ~ pat {print $1; exit}'
}

# docker_compose_dir_for PATTERN -> prints the working directory of a
# running container's Compose project (matching PATTERN, same anchoring
# rules as docker_container_for above), or nothing if none matches. Same
# Compose-label introspection internal/companion/selfupdate.go's own
# updateDockerCompose already relies on for Go-side self-update -- Docker's
# own container metadata is the source of truth for "where did this get
# set up," not a separate state file this script would have to keep in
# sync. Used by install_agent_docker/install_aggregator_docker to update
# an existing deployment in place on re-run, instead of re-prompting for a
# directory or creating a duplicate.
docker_compose_dir_for() {
  container_id=$(docker_container_for "$1")
  [ -z "$container_id" ] && return 0
  docker inspect --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$container_id" 2>/dev/null
}

# raw_url_for PATH -> raw.githubusercontent.com URL for PATH at
# $INSTALL_VERSION (main, for "latest") -- same pinning reasoning as
# cache_install_sh_for_companion below: a docker-compose.yml downloaded
# for a pinned release should match the images that release actually
# expects, not whatever's on main right now.
raw_url_for() {
  if [ "$INSTALL_VERSION" = "latest" ]; then
    printf 'https://raw.githubusercontent.com/sinwe/update-detector/main/%s' "$1"
  else
    printf 'https://raw.githubusercontent.com/sinwe/update-detector/%s/%s' "$INSTALL_VERSION" "$1"
  fi
}

# prompt_use_docker LABEL -> prints "1" or "0". Only called after
# docker_available has already confirmed Docker is actually usable here,
# so there's a real choice to make. USE_DOCKER overrides non-
# interactively (any value other than "0" counts as yes). With a
# terminal, prompts [Y/n] defaulting to yes -- Docker is the documented
# default when it's available (see header) -- and with no terminal,
# defaults to yes silently for the same reason.
prompt_use_docker() {
  if [ -n "${USE_DOCKER:-}" ]; then
    case "$USE_DOCKER" in
      0) echo 0 ;;
      *) echo 1 ;;
    esac
    return
  fi
  if [ ! -r /dev/tty ]; then
    echo 1
    return
  fi
  printf "Docker is available -- use it for the %s (recommended)? [Y/n]: " "$1" >&2
  read -r reply < /dev/tty
  case "$reply" in
    [nN]*) echo 0 ;;
    *) echo 1 ;;
  esac
}

# default_docker_dir -> ~/docker-updater, unless a Docker deployment of
# the *other* component (agent or aggregator) already exists somewhere --
# in which case that directory becomes the suggested default too. The
# agent and aggregator share one directory by default (their Compose
# files have different names -- docker-compose.yml vs
# docker-compose.aggregator.yml -- so they don't collide there), so
# installing both normally lands in the same place without the operator
# having to type the same path twice.
default_docker_dir() {
  dir=$(docker_compose_dir_for '(^|/)update-detector(:|$)') || dir=""
  if [ -z "$dir" ]; then
    dir=$(docker_compose_dir_for '(^|/)update-aggregator(:|$)') || dir=""
  fi
  printf '%s' "${dir:-$HOME/docker-updater}"
}

# set_env_var FILE KEY VALUE -> creates/updates KEY=VALUE in FILE,
# preserving every other line untouched, and always leaves FILE mode
# 0600. Needed (rather than a truncating heredoc) because the agent and
# aggregator now default to sharing one directory, and so one .env --
# `docker compose` reads exactly one per directory -- overwriting the
# whole file on each install would erase whatever the other component
# already wrote there. Unconditionally 0600 rather than only when the
# aggregator itself happens to write to it: the shared file can carry
# ADMIN_APPLY_SHARED_SECRET regardless of which component's install last
# touched it.
set_env_var() {
  file="$1" key="$2" value="$3"
  touch "$file"
  grep -v "^$key=" "$file" > "$file.new" 2>/dev/null || true
  echo "$key=$value" >> "$file.new"
  mv "$file.new" "$file"
  chmod 0600 "$file"
}

# set_env_var_if_set FILE KEY VALUE -> like set_env_var, but a no-op when
# VALUE is empty. Used for the handful of variables the agent's and the
# aggregator's Compose files both read under the very same unprefixed
# name (TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID) now that they default to
# sharing one .env -- an unset input on one component's install must
# never silently blank out a value the other component already
# configured there.
set_env_var_if_set() {
  [ -n "$3" ] && set_env_var "$1" "$2" "$3"
  return 0
}

# channel_image_tag CHANNEL -> the ghcr.io channel tag tracking that
# release channel: release -> latest, anything else -> latest-<channel>.
# Fails fast on anything outside version.Channels' own set
# (alpha/beta/rc/release) so a typo errors here instead of pulling a
# nonexistent tag three minutes later.
channel_image_tag() {
  case "${1:-release}" in
    release) echo "latest" ;;
    alpha|beta|rc) echo "latest-$1" ;;
    *)
      echo "install.sh: invalid channel '$1' (want one of: alpha, beta, rc, release)" >&2
      return 1
      ;;
  esac
}

# ensure_compose_label FILE SERVICE LABEL -> appends a `labels:` block
# with LABEL under SERVICE when the file has no wud.watch entry yet, so
# pre-existing deployments (whose compose files predate the opt-out
# labels this repo now ships) get the same protection on the next
# install.sh run. No-op when any wud.watch entry already exists, and
# when the service isn't in the file at all. awk (not sed) for identical
# behavior on GNU and BSD.
ensure_compose_label() {
  file="$1" service="$2" label="$3"
  [ -f "$file" ] || return 0
  if grep -q "wud\\.watch" "$file"; then
    return 0
  fi
  awk -v svc="  $service:" -v lbl="$label" '
    $0 == svc { print; print "    labels:"; print "      - \"" lbl "\""; next }
    { print }
  ' "$file" > "$file.new" && mv "$file.new" "$file"
  echo "install.sh: added $label opt-out to $service in $file"
}

# pin_compose_image FILE REPO TAG -> rewrites FILE's `image: REPO:<tag>`
# line to TAG, leaving the file untouched when no such line exists (a
# custom registry mirror is never "fixed" into ghcr.io). Called on both
# the fresh and the update paths, immediately before pull, so the tag
# can never drift from the channel the deployment actually follows: a
# later `docker compose pull` then fetches the channel head, never a
# stale tag left over from an earlier choice -- which is exactly how a
# deployment tracking alphas via self-update used to get silently
# downgraded back to the newest beta on the next manual pull.
pin_compose_image() {
  file="$1" repo="$2" tag="$3"
  [ -f "$file" ] || return 0
  if grep -q "^[[:space:]]*image: $repo:" "$file"; then
    # -i.bak (removed right after) rather than bare -i: the latter is
    # GNU-only, and this script is also piped into sh on macOS where
    # sed demands a backup suffix.
    sed -i.bak "s#^\([[:space:]]*image: $repo:\)[^[:space:]]*#\1$tag#" "$file"
    rm -f "$file.bak"
    echo "install.sh: pinned $file image to $repo:$tag"
  fi
}

install_agent_native() {
  if is_macos; then
    install_agent_launchd
    return
  fi
  echo "install.sh: installing update-detector (agent) natively..."
  bin_path="/usr/local/bin/update-detector"
  download_binary update-detector "$bin_path"

  ensure_system_user update-detector
  state_dir="${STATE_DIR:-/var/lib/update-detector}"
  mkdir -p "$state_dir"
  chown update-detector:update-detector "$state_dir"

  # Read back whatever's already configured (a re-install/upgrade of an
  # existing native agent) so re-running this doesn't reset every value to
  # blank just because the shell re-running it doesn't happen to have
  # these exported again -- confirmed live, this is exactly what made a
  # WSL2 host with an already-connected agent look like it had "forgotten"
  # its own AGGREGATOR_URL on a later agent-only re-run. Empty strings
  # (fresh install, nothing to read yet) are harmless as fallbacks below.
  env_file="/etc/default/update-detector"
  existing_hostname_override="" existing_bot_token="" existing_chat_id="" existing_agg_url=""
  if [ -f "$env_file" ]; then
    existing_hostname_override="$(env_value "$env_file" HOSTNAME_OVERRIDE)"
    existing_bot_token="$(env_value "$env_file" TELEGRAM_BOT_TOKEN)"
    existing_chat_id="$(env_value "$env_file" TELEGRAM_CHAT_ID)"
    existing_agg_url="$(env_value "$env_file" AGGREGATOR_URL)"
  fi

  # Every file-path var is set to its real absolute value here, never the
  # config package's Docker-oriented /host/... defaults -- there's no
  # container boundary to cross on a native install, so /host/etc/... etc.
  # would just be wrong. RELEASE_UPGRADES_FILE and REBOOT_REQUIRED_FILE
  # point at paths WSL2 may never populate, which internal/checker/reboot
  # and the release-upgrades reader already handle gracefully (treated as
  # false/empty, not an error).
  # Mandatory: without one, this agent can never enroll with (or be
  # applied through) an aggregator at all, and a companion could never
  # pair with it either -- there's no meaningful standalone mode for a
  # native install the way there arguably is for a bare Docker Compose
  # deployment. Prompts only when interactive and nothing's already known
  # -- neither a fresh value from the environment nor one already
  # configured on this host from before (see existing_agg_url above) --
  # so a re-run never hangs a scripted/non-interactive install waiting for
  # input that will never come; that case is fatal instead, same as a
  # human leaving the prompt blank on a genuinely first-time install.
  resolved_aggregator_url="$(prompt_aggregator_url "${AGGREGATOR_URL:-$existing_agg_url}")"
  if [ -z "$resolved_aggregator_url" ]; then
    echo "install.sh: AGGREGATOR_URL is required -- set it and re-run." >&2
    exit 1
  fi

  cat > "$env_file" <<EOF
LISTEN_ADDR=${LISTEN_ADDR:-:8080}
HOSTNAME_OVERRIDE=${HOSTNAME_OVERRIDE:-$existing_hostname_override}
CHECK_INTERVAL=${CHECK_INTERVAL:-6h}
APT_SOURCES_LIST=/etc/apt/sources.list
APT_SOURCES_LIST_D=/etc/apt/sources.list.d
DPKG_STATUS_FILE=/var/lib/dpkg/status
APT_LISTS_CACHE_DIR=$state_dir/apt/lists
OS_RELEASE_FILE=/etc/os-release
RELEASE_UPGRADES_FILE=/etc/update-manager/release-upgrades
REBOOT_REQUIRED_FILE=/var/run/reboot-required
STATE_FILE=$state_dir/state.json
TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN:-$existing_bot_token}
TELEGRAM_CHAT_ID=${TELEGRAM_CHAT_ID:-$existing_chat_id}
NOTIFY_ON_STARTUP=false
AGGREGATOR_URL=$resolved_aggregator_url
AGENT_IDENTITY_FILE=$state_dir/agent-identity.json
COMPANION_SOCKET_PATH=$state_dir/companion.sock
EOF
  chmod 0644 "$env_file"

  cat > /etc/systemd/system/update-detector.service <<EOF
[Unit]
Description=update-detector agent (detects, never applies, OS/package updates)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$bin_path
EnvironmentFile=$env_file
User=update-detector
Group=update-detector
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

  install_unit update-detector
  echo "install.sh: update-detector installed and started. Check: systemctl status update-detector"
}

# install_agent_launchd -> macOS equivalent of install_agent_native
# above: a LaunchDaemon (runs at boot with no login required, unlike a
# per-user LaunchAgent) under the Homebrew owner's account -- brew
# refuses to run as root, and the whole point of this agent is reading
# that user's Homebrew. Everything lives under that user's
# ~/.update-detector (binary, state, sidecar env file); only the plist
# itself lives in /Library/LaunchDaemons, which is why this still needs
# root despite running the agent unprivileged.
install_agent_launchd() {
  echo "install.sh: installing update-detector (agent) as a macOS LaunchDaemon..."
  brew_bin=""
  for candidate in /opt/homebrew/bin/brew /usr/local/bin/brew; do
    if [ -x "$candidate" ]; then brew_bin="$candidate"; break; fi
  done
  if [ -z "$brew_bin" ] && command -v brew >/dev/null 2>&1; then
    brew_bin="$(command -v brew)"
  fi
  if [ -z "$brew_bin" ]; then
    echo "install.sh: Homebrew is required for the macOS agent but was not found -- install it from https://brew.sh first." >&2
    exit 1
  fi
  brew_owner="$(stat -f '%Su' "$brew_bin")"
  owner_home="$(eval echo "~$brew_owner")"
  if [ -z "$owner_home" ] || [ ! -d "$owner_home" ]; then
    echo "install.sh: could not resolve a home directory for Homebrew owner $brew_owner" >&2
    exit 1
  fi

  state_dir="${STATE_DIR:-$owner_home/.update-detector}"
  mkdir -p "$state_dir"
  chown "$brew_owner" "$state_dir"

  bin_path="$state_dir/update-detector"
  download_binary update-detector "$bin_path"
  chown "$brew_owner" "$bin_path"

  # Sidecar env file: the single source of truth for the plist's own
  # EnvironmentVariables below, so a re-run can read back what's already
  # configured (same reason install_agent_native reads back
  # /etc/default/update-detector) instead of resetting everything.
  env_file="$state_dir/agent.env"
  existing_hostname_override="" existing_bot_token="" existing_chat_id="" existing_agg_url=""
  if [ -f "$env_file" ]; then
    existing_hostname_override="$(env_value "$env_file" HOSTNAME_OVERRIDE)"
    existing_bot_token="$(env_value "$env_file" TELEGRAM_BOT_TOKEN)"
    existing_chat_id="$(env_value "$env_file" TELEGRAM_CHAT_ID)"
    existing_agg_url="$(env_value "$env_file" AGGREGATOR_URL)"
  fi
  resolved_aggregator_url="$(prompt_aggregator_url "${AGGREGATOR_URL:-$existing_agg_url}")"
  if [ -z "$resolved_aggregator_url" ]; then
    echo "install.sh: AGGREGATOR_URL is required -- set it and re-run." >&2
    exit 1
  fi

  cat > "$env_file" <<EOF
LISTEN_ADDR=${LISTEN_ADDR:-:8080}
HOSTNAME_OVERRIDE=${HOSTNAME_OVERRIDE:-$existing_hostname_override}
CHECK_INTERVAL=${CHECK_INTERVAL:-6h}
STATE_FILE=$state_dir/state.json
TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN:-$existing_bot_token}
TELEGRAM_CHAT_ID=${TELEGRAM_CHAT_ID:-$existing_chat_id}
NOTIFY_ON_STARTUP=false
AGGREGATOR_URL=$resolved_aggregator_url
AGENT_IDENTITY_FILE=$state_dir/agent-identity.json
COMPANION_SOCKET_PATH=$state_dir/companion.sock
EOF
  chmod 0644 "$env_file"
  chown "$brew_owner" "$env_file"

  brew_dir="$(dirname "$brew_bin")"
  plist_label="com.sinwe.update-detector"
  plist_path="/Library/LaunchDaemons/$plist_label.plist"
  cat > "$plist_path" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>$plist_label</string>
    <key>ProgramArguments</key>
    <array>
        <string>$bin_path</string>
    </array>
    <key>UserName</key>
    <string>$brew_owner</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ThrottleInterval</key>
    <integer>30</integer>
    <key>WorkingDirectory</key>
    <string>$state_dir</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>$brew_dir:/usr/bin:/bin:/usr/sbin:/sbin</string>
        <key>LISTEN_ADDR</key>
        <string>${LISTEN_ADDR:-:8080}</string>
        <key>HOSTNAME_OVERRIDE</key>
        <string>${HOSTNAME_OVERRIDE:-$existing_hostname_override}</string>
        <key>CHECK_INTERVAL</key>
        <string>${CHECK_INTERVAL:-6h}</string>
        <key>STATE_FILE</key>
        <string>$state_dir/state.json</string>
        <key>TELEGRAM_BOT_TOKEN</key>
        <string>${TELEGRAM_BOT_TOKEN:-$existing_bot_token}</string>
        <key>TELEGRAM_CHAT_ID</key>
        <string>${TELEGRAM_CHAT_ID:-$existing_chat_id}</string>
        <key>NOTIFY_ON_STARTUP</key>
        <string>false</string>
        <key>AGGREGATOR_URL</key>
        <string>$resolved_aggregator_url</string>
        <key>AGENT_IDENTITY_FILE</key>
        <string>$state_dir/agent-identity.json</string>
        <key>COMPANION_SOCKET_PATH</key>
        <string>$state_dir/companion.sock</string>
    </dict>
    <key>StandardOutPath</key>
    <string>$state_dir/launchd.out.log</string>
    <key>StandardErrorPath</key>
    <string>$state_dir/launchd.err.log</string>
</dict>
</plist>
EOF
  chmod 0644 "$plist_path"

  # launchd PATH note: its default PATH lacks Homebrew entirely, hence
  # the explicit PATH above -- without it exec.LookPath("brew") fails
  # and every check errors.
  # bootout is asynchronous -- see launchd_reload above for why this
  # waits instead of bootstrapping immediately.
  launchd_reload "$plist_label" "$plist_path"
  check_port="${LISTEN_ADDR:-:8080}"
  check_port="${check_port##*:}"
  echo "install.sh: update-detector installed and started. Check: curl http://localhost:$check_port/status"
}

# install_agent_docker -> Docker Compose equivalent of install_agent_native
# above. If docker_compose_dir_for finds an existing deployment, updates
# it in place (pull + up -d) rather than re-prompting for a directory or
# creating a duplicate one -- this is the "detect this for future update
# as well" behavior. Otherwise resolves a directory (DOCKER_DIR, prompted,
# defaulting to wherever the aggregator's own Compose deployment already
# lives if there is one, else ~/docker-updater -- see default_docker_dir),
# downloads docker-compose.yml into it, and writes/updates a .env there
# with the same variables install_agent_native accepts as env vars --
# Compose reads .env from the project directory automatically, no
# --env-file flag needed, same as a human following the README by hand.
# No -f/-p flags needed for any of this: docker-compose.yml is Compose's
# own default filename, and its project name defaults to the directory's
# own basename -- both exactly as if this were the only compose file in
# that directory, even when the aggregator's docker-compose.aggregator.yml
# also lives there (see install_aggregator_docker for how that one stays
# out of the way). AGGREGATOR_URL is mandatory here for the same reason
# it's mandatory in install_agent_native: without one this agent can never
# enroll with (or be applied through) an aggregator, and a companion could
# never pair with it either.
install_agent_docker() {
  dir=$(docker_compose_dir_for '(^|/)update-detector(:|$)') || dir=""
  if [ -n "$dir" ]; then
    echo "install.sh: found an existing update-detector Compose deployment at $dir -- updating it in place"
    # Never stored for the agent (it reads no channel itself): prefer the
    # invocation's choice, else the shared .env's (written by an
    # aggregator install in this same directory), else the default --
    # same precedence the aggregator's own path below uses.
    agent_channel="${SELF_UPDATE_CHANNEL:-$(env_value "$dir/.env" SELF_UPDATE_CHANNEL)}"
    agent_channel="${agent_channel:-release}"
  else
    dir="${DOCKER_DIR:-}"
    if [ -z "$dir" ]; then
      suggested_dir="$(default_docker_dir)"
      if [ -r /dev/tty ]; then
        printf "Directory for the update-detector Compose deployment [%s]: " "$suggested_dir" >&2
        read -r dir < /dev/tty
      fi
      dir="${dir:-$suggested_dir}"
    fi
    echo "install.sh: setting up update-detector (agent) via Docker Compose in $dir..."

    resolved_aggregator_url="$(prompt_aggregator_url "${AGGREGATOR_URL:-}")"
    if [ -z "$resolved_aggregator_url" ]; then
      echo "install.sh: AGGREGATOR_URL is required -- set it and re-run." >&2
      exit 1
    fi

    mkdir -p "$dir"
    curl -fsSL "$(raw_url_for docker-compose.yml)" -o "$dir/docker-compose.yml"
    set_env_var "$dir/.env" HOSTNAME_OVERRIDE "${HOSTNAME_OVERRIDE:-}"
    # TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID: see set_env_var_if_set's own
    # comment -- this directory may already have an aggregator-configured
    # value in there (they share one .env), so an unset input here must
    # leave it alone rather than blank it out.
    set_env_var_if_set "$dir/.env" TELEGRAM_BOT_TOKEN "${TELEGRAM_BOT_TOKEN:-}"
    set_env_var_if_set "$dir/.env" TELEGRAM_CHAT_ID "${TELEGRAM_CHAT_ID:-}"
    set_env_var "$dir/.env" AGGREGATOR_URL "$resolved_aggregator_url"
    agent_channel="${SELF_UPDATE_CHANNEL:-release}"
  fi

  agent_tag="$(channel_image_tag "$agent_channel")" # exits on invalid channel
  pin_compose_image "$dir/docker-compose.yml" "ghcr.io/sinwe/update-detector" "$agent_tag"
  ensure_compose_label "$dir/docker-compose.yml" "update-detector" "wud.watch=false"

  ( cd "$dir" && docker compose pull && docker compose up -d )
  echo "install.sh: update-detector running via Docker Compose in $dir. Check: cd $dir && docker compose logs -f"
}

install_aggregator_native() {
  echo "install.sh: installing update-aggregator natively..."
  bin_path="/usr/local/bin/update-aggregator"
  download_binary update-aggregator "$bin_path"

  ensure_system_user update-aggregator
  data_dir="${AGGREGATOR_DATA_DIR:-/var/lib/update-aggregator}"
  mkdir -p "$data_dir"
  chown update-aggregator:update-aggregator "$data_dir"

  # Prefixed AGGREGATOR_* input names (distinct from the agent's own
  # TELEGRAM_BOT_TOKEN etc. above), specifically so installing both agent
  # and aggregator in the same run can't accidentally share one secret/
  # token meant for only one of them. Written into the env file under the
  # actual names update-aggregator's own config package expects.
  #
  # Read back whatever's already configured first, same reasoning as
  # install_agent_native's own existing_* reads above -- a re-install
  # without re-exporting these must not silently blank out
  # ADMIN_APPLY_SHARED_SECRET (disabling apply/self-update fleet-wide
  # until someone notices and regenerates it) or reset the Telegram
  # config, just because this particular run didn't happen to set them.
  env_file="/etc/default/update-aggregator"
  existing_agg_bot_token="" existing_agg_chat_id="" existing_admin_secret=""
  if [ -f "$env_file" ]; then
    existing_agg_bot_token="$(env_value "$env_file" TELEGRAM_BOT_TOKEN)"
    existing_agg_chat_id="$(env_value "$env_file" TELEGRAM_CHAT_ID)"
    existing_admin_secret="$(env_value "$env_file" ADMIN_APPLY_SHARED_SECRET)"
  fi
  resolved_admin_secret="${ADMIN_APPLY_SHARED_SECRET:-$existing_admin_secret}"

  cat > "$env_file" <<EOF
LISTEN_ADDR=${AGGREGATOR_LISTEN_ADDR:-:9090}
REGISTRY_FILE=$data_dir/registry.json
TELEGRAM_BOT_TOKEN=${AGGREGATOR_TELEGRAM_BOT_TOKEN:-$existing_agg_bot_token}
TELEGRAM_CHAT_ID=${AGGREGATOR_TELEGRAM_CHAT_ID:-$existing_agg_chat_id}
ADMIN_APPLY_SHARED_SECRET=$resolved_admin_secret
EOF
  chmod 0600 "$env_file" # contains a secret, unlike the agent's own env file

  cat > /etc/systemd/system/update-aggregator.service <<EOF
[Unit]
Description=update-aggregator (central fleet status + apply-trigger service)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$bin_path
EnvironmentFile=$env_file
User=update-aggregator
Group=update-aggregator
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

  install_unit update-aggregator
  echo "install.sh: update-aggregator installed and started. Check: systemctl status update-aggregator"
  if [ -n "$resolved_admin_secret" ]; then
    echo "install.sh: ADMIN_APPLY_SHARED_SECRET=$resolved_admin_secret" >&2
    echo "  Keep this somewhere safe (e.g. a password manager) -- it's the only" >&2
    echo "  credential gating apply/self-update actions, it's stored 0600 in" >&2
    echo "  $env_file and never printed again after this, and every browser or" >&2
    echo "  script that triggers an apply needs this exact value." >&2
  else
    echo "install.sh: no ADMIN_APPLY_SHARED_SECRET set -- apply/self-update stay" >&2
    echo "  disabled (501) until you set one and restart update-aggregator." >&2
  fi
}

# update-aggregator's own project name is fixed, deliberately not derived
# from the directory (Compose's own default) -- since it defaults to
# sharing a directory with the agent's docker-compose.yml (see
# default_docker_dir), an implicit directory-basename project name would
# put both compose files in the very same Compose project, which is
# exactly the "Found orphan containers" confusion a fixed, distinct name
# avoids entirely: every command below is explicit about both which
# compose file and which project it means, so it can never be confused
# for (or accidentally torn down by) a plain `docker compose ...` run
# against the agent's own docker-compose.yml sitting right next to it.
AGGREGATOR_COMPOSE_PROJECT="update-aggregator"

# install_aggregator_docker -> Docker Compose equivalent of
# install_aggregator_native above, same structure as install_agent_docker:
# update an existing deployment in place if docker_compose_dir_for finds
# one, otherwise resolve a directory (AGGREGATOR_DOCKER_DIR, prompted,
# defaulting to wherever the agent's own Compose deployment already lives
# if there is one, else ~/docker-updater -- see default_docker_dir; the
# agent and aggregator share one directory by default, distinguished by
# compose filename and the fixed project name above, not by directory) and
# set up fresh. Unlike the agent, nothing here is mandatory --
# ADMIN_APPLY_SHARED_SECRET unset just leaves apply/self-update disabled,
# same as install_aggregator_native.
install_aggregator_docker() {
  dir=$(docker_compose_dir_for '(^|/)update-aggregator(:|$)') || dir=""
  if [ -n "$dir" ]; then
    echo "install.sh: found an existing update-aggregator Compose deployment at $dir -- updating it in place"
    # Same precedence as a fresh install below, except a re-run without
    # re-exporting the channel falls back to the shared .env's stored
    # value (like the native path's existing_* reads) instead of the
    # default -- otherwise re-running plain install.sh would yank an
    # alpha-tracking deployment back to :latest.
    aggregator_channel="${AGGREGATOR_SELF_UPDATE_CHANNEL:-${SELF_UPDATE_CHANNEL:-$(env_value "$dir/.env" SELF_UPDATE_CHANNEL)}}"
    aggregator_channel="${aggregator_channel:-release}"
  else
    dir="${AGGREGATOR_DOCKER_DIR:-}"
    if [ -z "$dir" ]; then
      suggested_dir="$(default_docker_dir)"
      if [ -r /dev/tty ]; then
        printf "Directory for the update-aggregator Compose deployment [%s]: " "$suggested_dir" >&2
        read -r dir < /dev/tty
      fi
      dir="${dir:-$suggested_dir}"
    fi
    echo "install.sh: setting up update-aggregator via Docker Compose in $dir..."
    mkdir -p "$dir"
    curl -fsSL "$(raw_url_for docker-compose.aggregator.yml)" -o "$dir/docker-compose.aggregator.yml"
    # Prefixed AGGREGATOR_* input names, same reasoning as
    # install_aggregator_native's own env file -- keeps a same-run agent +
    # aggregator install from accidentally sharing one secret/token meant
    # for only one of them. Written into the shared .env under the real,
    # unprefixed names update-aggregator's own compose file actually
    # reads, same as install_aggregator_native's own env file does for its
    # systemd equivalent.
    set_env_var_if_set "$dir/.env" TELEGRAM_BOT_TOKEN "${AGGREGATOR_TELEGRAM_BOT_TOKEN:-}"
    set_env_var_if_set "$dir/.env" TELEGRAM_CHAT_ID "${AGGREGATOR_TELEGRAM_CHAT_ID:-}"
    set_env_var "$dir/.env" ADMIN_APPLY_SHARED_SECRET "${ADMIN_APPLY_SHARED_SECRET:-}"
    # The channel the aggregator's own update checks follow (see
    # SELF_UPDATE_CHANNEL in docs/reference.md): persisted only when
    # explicitly provided, so a re-run never blanks a hand-tuned value.
    # The image tag below always follows this same channel, so `docker
    # compose pull` fetches the channel head, never a stale tag.
    aggregator_channel="${AGGREGATOR_SELF_UPDATE_CHANNEL:-${SELF_UPDATE_CHANNEL:-release}}"
    set_env_var_if_set "$dir/.env" SELF_UPDATE_CHANNEL "${AGGREGATOR_SELF_UPDATE_CHANNEL:-${SELF_UPDATE_CHANNEL:-}}"
  fi

  aggregator_tag="$(channel_image_tag "$aggregator_channel")" # exits on invalid channel
  pin_compose_image "$dir/docker-compose.aggregator.yml" "ghcr.io/sinwe/update-aggregator" "$aggregator_tag"
  ensure_compose_label "$dir/docker-compose.aggregator.yml" "update-aggregator" "wud.watch=false"

  ( cd "$dir" && docker compose -f docker-compose.aggregator.yml -p "$AGGREGATOR_COMPOSE_PROJECT" pull \
      && docker compose -f docker-compose.aggregator.yml -p "$AGGREGATOR_COMPOSE_PROJECT" up -d )
  echo "install.sh: update-aggregator running via Docker Compose in $dir. Check: cd $dir && docker compose -f docker-compose.aggregator.yml -p $AGGREGATOR_COMPOSE_PROJECT logs -f"
  if [ -n "${ADMIN_APPLY_SHARED_SECRET:-}" ]; then
    echo "install.sh: ADMIN_APPLY_SHARED_SECRET=$ADMIN_APPLY_SHARED_SECRET" >&2
    echo "  Keep this somewhere safe (e.g. a password manager) -- it's the only" >&2
    echo "  credential gating apply/self-update actions, it's stored 0600 in" >&2
    echo "  $dir/.env and never printed again after this, and every browser or" >&2
    echo "  script that triggers an apply needs this exact value." >&2
  else
    echo "install.sh: no ADMIN_APPLY_SHARED_SECRET set -- apply/self-update stay" >&2
    echo "  disabled (501) until you set one and restart the aggregator." >&2
  fi
}

install_companion() {
  if is_macos; then
    install_companion_launchd
    return
  fi
  echo "install.sh: installing update-detector-companion..."
  bin_path="/usr/local/bin/update-detector-companion"
  download_binary update-detector-companion "$bin_path"

  # Discovery: try a native agent first (no Docker involved at all), then
  # fall back to a containerized one. Checked in that order deliberately
  # -- a host running both is genuinely ambiguous (see the warning below),
  # but a native agent is the one this script itself might have *just*
  # installed in this same run, so it takes precedence.
  socket_path="" agg_url="" agent_status_url=""
  native_found=0 docker_found=0

  agent_env_file="/etc/default/update-detector"
  if [ -f "$agent_env_file" ] && systemctl is-active --quiet update-detector 2>/dev/null; then
    native_found=1
    echo "install.sh: found a native update-detector.service on this host"
    state_dir="/var/lib/update-detector"
    socket_path="$(env_value "$agent_env_file" COMPANION_SOCKET_PATH)"
    socket_path="${socket_path:-$state_dir/companion.sock}"
    agg_url="$(env_value "$agent_env_file" AGGREGATOR_URL)"
    agent_listen_addr="$(env_value "$agent_env_file" LISTEN_ADDR)"
    agent_status_url="http://localhost:${agent_listen_addr#*:}/status"
  fi

  # docker is checked, but never required -- a broken/absent docker here
  # must not abort the script when native discovery already succeeded (or
  # even when it didn't: the "neither found" case below gives a clear
  # error either way).
  if command -v docker >/dev/null 2>&1; then
    # Not "docker ps --format {{.Image}}" -- see docker_container_for's
    # own comment above for why: that field goes stale (falls back to a
    # bare image ID) once this container's tag has since been reassigned
    # to a different image, which is exactly what happens to a
    # long-running agent pinned to :latest across later release pushes.
    container_id=$(
      for cid in $(docker ps --format '{{.ID}}' 2>/dev/null); do
        echo "$cid $(docker inspect --format '{{.Config.Image}}' "$cid" 2>/dev/null)"
      done | awk '$2 ~ /(^|\/)update-detector(:|$)/ {print $1; exit}'
    ) || container_id=""
    if [ -n "$container_id" ]; then
      docker_found=1
      if [ "$native_found" = "1" ]; then
        echo "install.sh: warning: both a native update-detector.service and a" >&2
        echo "  containerized one ($container_id) are running on this host --" >&2
        echo "  using the native one. Having both means duplicate detection" >&2
        echo "  cycles and duplicate aggregator enrollment; remove one." >&2
      else
        state_dir=$(docker inspect --format \
          '{{range .Mounts}}{{if eq .Destination "/var/lib/update-detector"}}{{.Source}}{{end}}{{end}}' \
          "$container_id")
        if [ -z "$state_dir" ]; then
          echo "install.sh: could not find container $container_id's /var/lib/update-detector bind mount" >&2
          exit 1
        fi
        socket_path="$state_dir/companion.sock"

        agg_url=$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$container_id" \
          | sed -n 's/^AGGREGATOR_URL=//p')
        # No hard failure here if empty -- the unified prompt fallback
        # below (after both native and Docker discovery) gets a chance to
        # ask for one interactively before this whole install gives up.

        # AGGREGATOR_URL is only guaranteed correct from *inside* the agent
        # container's own Docker network -- e.g. http://update-aggregator:8080,
        # a Compose service name + the aggregator's internal port, valid
        # when both services share a network. This companion runs
        # natively, with no access to that network namespace or Docker's
        # internal DNS, so that address needs translating to something
        # reachable from here. If the hostname matches a container
        # actually running on this host (by Compose service name), rewrite
        # it to that container's host-published port instead; otherwise
        # leave it alone -- it's presumably already a real, externally-
        # reachable address for a genuinely separate aggregator host.
        if [ -n "$agg_url" ]; then
          agg_hostport=${agg_url#*://}
          agg_hostport=${agg_hostport%%/*}
          agg_host=${agg_hostport%%:*}
          agg_port=${agg_hostport#*:}
          agg_container_id=$(docker ps --filter "label=com.docker.compose.service=$agg_host" --format '{{.ID}}' 2>/dev/null | head -1) || agg_container_id=""
          if [ -n "$agg_container_id" ]; then
            agg_host_port=$(docker inspect --format \
              "{{with index .NetworkSettings.Ports \"$agg_port/tcp\"}}{{(index . 0).HostPort}}{{end}}" \
              "$agg_container_id" 2>/dev/null) || agg_host_port=""
            if [ -n "$agg_host_port" ]; then
              echo "install.sh: $agg_host is a local container published at localhost:$agg_host_port -- using that instead of the Docker-internal address"
              agg_url="http://localhost:$agg_host_port"
            else
              # No published-port mapping found -- likely --network host,
              # where the container's own LISTEN_ADDR *is* the host's own
              # port. Read that directly from the aggregator container's
              # own env, rather than assuming it still matches whatever
              # port happens to be in the agent's AGGREGATOR_URL string --
              # confirmed live, those two can go stale independently of
              # each other (e.g. the aggregator's LISTEN_ADDR changed after
              # switching to host networking, while the agent's own
              # AGGREGATOR_URL, baked in at the agent's own install time,
              # still says the old internal port).
              agg_own_listen_addr=$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$agg_container_id" \
                | sed -n 's/^LISTEN_ADDR=//p')
              if [ -n "$agg_own_listen_addr" ]; then
                agg_port="${agg_own_listen_addr#*:}"
              fi
              echo "install.sh: $agg_host is a local container with no published-port mapping (likely --network host) -- using localhost:$agg_port"
              agg_url="http://localhost:$agg_port"
            fi
          fi
        fi

        # Empty for --network host containers (no published-port mapping
        # to read), which is exactly when localhost:8080 is already
        # correct anyway.
        host_port=$(docker inspect --format \
          '{{with index .NetworkSettings.Ports "8080/tcp"}}{{(index . 0).HostPort}}{{end}}' \
          "$container_id" 2>/dev/null) || host_port=""
        agent_status_url="http://localhost:${host_port:-8080}/status"
      fi
    fi
  fi

  if [ "$native_found" = "0" ] && [ "$docker_found" = "0" ]; then
    echo "install.sh: no active update-detector.service and no running update-detector" >&2
    echo "  container found on this host -- install one first (see README)." >&2
    exit 1
  fi

  # Neither discovery path above found one -- last chance, ask
  # interactively before giving up. Unlike the agent's own native install,
  # the companion has no meaningful way to run without one at all, so an
  # empty result here (no terminal to prompt on, or left blank) is fatal.
  if [ -z "$agg_url" ]; then
    agg_url="$(prompt_aggregator_url "")"
  fi
  if [ -z "$agg_url" ]; then
    echo "install.sh: no AGGREGATOR_URL available -- the companion has no purpose without one." >&2
    echo "  Set AGGREGATOR_URL and re-run, or configure it on the agent this host" >&2
    echo "  runs first (native: /etc/default/update-detector; Docker: the" >&2
    echo "  container's own env) and re-run." >&2
    exit 1
  fi

  echo "install.sh: socket=$socket_path aggregator=$agg_url agent_status=$agent_status_url"

  # Best-effort only -- doesn't block install, since the aggregator being
  # briefly unreachable right now isn't fatal (the companion reconnects
  # with backoff on its own). Just a heads-up if something's still off
  # despite the discovery above.
  if [ -n "$agg_url" ] && ! curl -fsS -o /dev/null --max-time 5 "$agg_url/openapi.yaml"; then
    echo "install.sh: warning: $agg_url doesn't look reachable from this host right now -- continuing anyway, but check AGGREGATOR_URL in /etc/systemd/system/update-detector-companion.service if the companion never connects" >&2
  fi

  cat > /etc/systemd/system/update-detector-companion.service <<EOF
[Unit]
Description=update-detector companion (applies pending package upgrades on trigger)
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=$bin_path
Environment=COMPANION_SOCKET_PATH=$socket_path
Environment=AGGREGATOR_URL=$agg_url
Environment=AGENT_STATUS_URL=$agent_status_url
Restart=on-failure
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
EOF

  install_unit update-detector-companion
  cache_install_sh_for_companion
  echo "install.sh: done. Check status with: systemctl status update-detector-companion"
}

# install_companion_launchd -> macOS equivalent of install_companion
# above: a LaunchDaemon running as root (like Linux -- it must, so
# self-update can re-invoke this root-requiring script; brew itself
# still runs as the Homebrew owner via sudo -u, since brew refuses root
# outright). Discovery mirrors the Linux path but reads the agent's sidecar env file ($state_dir/
# agent.env, written by install_agent_launchd) instead of
# /etc/default/update-detector -- Docker discovery doesn't apply, since
# a containerized agent could never see this host's Homebrew anyway.
install_companion_launchd() {
  echo "install.sh: installing update-detector-companion as a macOS LaunchDaemon..."
  brew_bin=""
  for candidate in /opt/homebrew/bin/brew /usr/local/bin/brew; do
    if [ -x "$candidate" ]; then brew_bin="$candidate"; break; fi
  done
  if [ -z "$brew_bin" ] && command -v brew >/dev/null 2>&1; then
    brew_bin="$(command -v brew)"
  fi
  if [ -z "$brew_bin" ]; then
    echo "install.sh: Homebrew is required for the macOS companion but was not found -- install it from https://brew.sh first." >&2
    exit 1
  fi
  brew_owner="$(stat -f '%Su' "$brew_bin")"
  owner_home="$(eval echo "~$brew_owner")"
  if [ -z "$owner_home" ] || [ ! -d "$owner_home" ]; then
    echo "install.sh: could not resolve a home directory for Homebrew owner $brew_owner" >&2
    exit 1
  fi

  state_dir="${STATE_DIR:-$owner_home/.update-detector}"
  agent_env_file="$state_dir/agent.env"
  if [ ! -f "$agent_env_file" ]; then
    echo "install.sh: no agent install found at $agent_env_file -- install the agent first," >&2
    echo "  then re-run for the companion (it pairs through the agent)." >&2
    exit 1
  fi
  if ! launchctl print system/com.sinwe.update-detector >/dev/null 2>&1; then
    echo "install.sh: warning: the agent daemon doesn't look loaded right now -- continuing anyway," >&2
    echo "  but the companion can't pair until the agent is actually running." >&2
  fi

  socket_path="$(env_value "$agent_env_file" COMPANION_SOCKET_PATH)"
  socket_path="${socket_path:-$state_dir/companion.sock}"
  agg_url="$(env_value "$agent_env_file" AGGREGATOR_URL)"
  agent_listen_addr="$(env_value "$agent_env_file" LISTEN_ADDR)"
  agent_status_url="http://localhost:${agent_listen_addr#*:}/status"

  if [ -z "$agg_url" ]; then
    agg_url="$(prompt_aggregator_url "${AGGREGATOR_URL:-}")"
  fi
  if [ -z "$agg_url" ]; then
    echo "install.sh: no AGGREGATOR_URL available -- the companion has no purpose without one." >&2
    echo "  Set AGGREGATOR_URL and re-run, or configure it on the agent first and re-run." >&2
    exit 1
  fi

  echo "install.sh: socket=$socket_path aggregator=$agg_url agent_status=$agent_status_url"

  if [ -n "$agg_url" ] && ! curl -fsS -o /dev/null --max-time 5 "$agg_url/openapi.yaml"; then
    echo "install.sh: warning: $agg_url doesn't look reachable from this host right now -- continuing anyway." >&2
  fi

  bin_path="$state_dir/update-detector-companion"
  download_binary update-detector-companion "$bin_path"
  mkdir -p "$state_dir"
  chown "$brew_owner" "$state_dir"

  brew_dir="$(dirname "$brew_bin")"
  plist_label="com.sinwe.update-detector-companion"
  plist_path="/Library/LaunchDaemons/$plist_label.plist"
  cat > "$plist_path" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>$plist_label</string>
    <key>ProgramArguments</key>
    <array>
        <string>$bin_path</string>
    </array>
    <!-- No UserName: the companion runs as root on macOS, same as on
         Linux -- it must, so self-update can re-invoke this
         root-requiring script. brew itself still runs as $brew_owner
         (the companion prefixes sudo -u; root-to-user never prompts),
         since brew refuses root outright. -->
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ThrottleInterval</key>
    <integer>30</integer>
    <key>WorkingDirectory</key>
    <string>$state_dir</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>$brew_dir:/usr/bin:/bin:/usr/sbin:/sbin</string>
        <key>BREW_OWNER</key>
        <string>$brew_owner</string>
        <!-- STATE_DIR doubles as the self-update discovery hook: the
             companion runs as root (so ~/.update-detector can't mean
             the agent owner's home here), and its own config has no
             state-dir field -- without this, a later agent self-update
             can't find the agent's sidecar env and silently resets
             LISTEN_ADDR etc. to defaults (confirmed live: a :8081
             agent came back on :8080 after an update). -->
        <key>STATE_DIR</key>
        <string>$state_dir</string>
        <key>COMPANION_SOCKET_PATH</key>
        <string>$socket_path</string>
        <key>AGGREGATOR_URL</key>
        <string>$agg_url</string>
        <key>AGENT_STATUS_URL</key>
        <string>$agent_status_url</string>
    </dict>
    <key>StandardOutPath</key>
    <string>$state_dir/companion.out.log</string>
    <key>StandardErrorPath</key>
    <string>$state_dir/companion.err.log</string>
</dict>
</plist>
EOF
  chmod 0644 "$plist_path"

  launchd_reload "$plist_label" "$plist_path"
  cache_install_sh_for_companion
  echo "install.sh: done. The companion pairs through the agent -- it should show as connected on /admin shortly."
}

# warn_docker_not_managed NAME PATTERN [COMPOSE_CMD] -> if a Docker
# container matching PATTERN exists (running or stopped), print a warning
# that install.sh won't touch it -- it never created that deployment, so
# it has no compose file path or volume names to safely act on, unlike a
# native systemd unit it fully owns end to end. Covers both the
# Docker-only case and the ambiguous both-native-and-Docker case (called
# unconditionally after any native teardown below). COMPOSE_CMD defaults
# to plain "docker compose" (correct for the agent's docker-compose.yml,
# Compose's own default file+project); the aggregator's own caller passes
# its actual -f/-p invocation instead, since a bare "docker compose down"
# in a shared directory (see default_docker_dir) would silently target
# the agent's project, not the aggregator's.
warn_docker_not_managed() {
  container_id=$(docker_container_for "$2")
  if [ -n "$container_id" ]; then
    dir=$(docker_compose_dir_for "$2") || dir=""
    compose_cmd="${3:-docker compose}"
    echo "install.sh: found a Docker container for $1 (id=$container_id) --" >&2
    echo "  install.sh doesn't manage Docker deployments it didn't create." >&2
    if [ -n "$dir" ]; then
      echo "  Remove it yourself: cd $dir && $compose_cmd down" >&2
    else
      echo "  Remove it yourself, e.g. \`$compose_cmd down\` from wherever" >&2
      echo "  that service's compose file lives." >&2
    fi
  fi
}

uninstall_agent() {
  native=0
  native_unit_present update-detector && native=1

  if [ "$native" = "0" ]; then
    echo "install.sh: no native update-detector (agent) install found"
  elif is_macos; then
    echo "install.sh: removing update-detector (agent) LaunchDaemon..."
    launchctl bootout system/com.sinwe.update-detector 2>/dev/null || true
    rm -f /Library/LaunchDaemons/com.sinwe.update-detector.plist
    # Binary and sidecar env live in the state dir on macOS (see
    # install_agent_launchd), so removing the dir covers both -- same
    # guards as the Linux path below against a degenerate rm -rf.
    # NOTE: $HOME is /var/root under sudo, so resolve the state dir from
    # the Homebrew owner (the account the agent runs as), not $HOME.
    agent_state_dir="${STATE_DIR:-}"
    if [ -z "$agent_state_dir" ]; then
      brew_owner=""
      for candidate in /opt/homebrew/bin/brew /usr/local/bin/brew; do
        if [ -x "$candidate" ]; then brew_owner="$(stat -f '%Su' "$candidate")"; break; fi
      done
      if [ -n "$brew_owner" ]; then
        agent_state_dir="$(eval echo "~$brew_owner")/.update-detector"
      fi
    fi
    if [ -n "$agent_state_dir" ] && [ "$agent_state_dir" != "." ] && [ "$agent_state_dir" != "/" ]; then
      echo "install.sh: removing $agent_state_dir (includes this agent's aggregator identity)"
      rm -rf "$agent_state_dir"
    fi
  else
    echo "install.sh: removing update-detector (agent)..."
    agent_state_dir=$(dirname "$(env_value /etc/default/update-detector AGENT_IDENTITY_FILE)")

    systemctl disable --now update-detector 2>/dev/null || true
    rm -f /etc/systemd/system/update-detector.service
    systemctl daemon-reload
    rm -f /usr/local/bin/update-detector
    rm -f /etc/default/update-detector
    # dirname on an empty/malformed path returns "." or "/" -- guard
    # against both, or a degenerate case turns into `rm -rf .` as root.
    if [ -n "$agent_state_dir" ] && [ "$agent_state_dir" != "." ] && [ "$agent_state_dir" != "/" ]; then
      echo "install.sh: removing $agent_state_dir (includes this agent's aggregator identity)"
      rm -rf "$agent_state_dir"
    fi
    remove_system_user update-detector
  fi

  warn_docker_not_managed update-detector '(^|/)update-detector(:|$)'

  if native_unit_present update-detector-companion; then
    echo "install.sh: note: update-detector-companion is still installed on this" >&2
    echo "  host and depends on the agent -- consider uninstalling it too." >&2
  fi
}

uninstall_aggregator() {
  native=0
  native_unit_present update-aggregator && native=1

  if [ "$native" = "0" ]; then
    echo "install.sh: no native update-aggregator install found"
  else
    echo "install.sh: removing update-aggregator..."
    agg_data_dir=$(dirname "$(env_value /etc/default/update-aggregator REGISTRY_FILE)")

    systemctl disable --now update-aggregator 2>/dev/null || true
    rm -f /etc/systemd/system/update-aggregator.service
    systemctl daemon-reload
    rm -f /usr/local/bin/update-aggregator
    rm -f /etc/default/update-aggregator
    if [ -n "$agg_data_dir" ] && [ "$agg_data_dir" != "." ] && [ "$agg_data_dir" != "/" ]; then
      echo "install.sh: removing $agg_data_dir (includes the fleet registry -- all enrolled/approved hosts)"
      rm -rf "$agg_data_dir"
    fi
    remove_system_user update-aggregator
  fi

  warn_docker_not_managed update-aggregator '(^|/)update-aggregator(:|$)' \
    "docker compose -f docker-compose.aggregator.yml -p $AGGREGATOR_COMPOSE_PROJECT"
}

uninstall_companion() {
  if is_macos; then
    if [ ! -f /Library/LaunchDaemons/com.sinwe.update-detector-companion.plist ]; then
      echo "install.sh: no update-detector-companion install found"
      return
    fi
    echo "install.sh: removing update-detector-companion LaunchDaemon..."
    launchctl bootout system/com.sinwe.update-detector-companion 2>/dev/null || true
    rm -f /Library/LaunchDaemons/com.sinwe.update-detector-companion.plist
    # Binary lives in the agent's state dir on macOS (see
    # install_companion_launchd) -- the dir itself stays: it belongs to
    # the agent, which may still be installed.
    brew_bin=""
    for candidate in /opt/homebrew/bin/brew /usr/local/bin/brew; do
      if [ -x "$candidate" ]; then brew_bin="$candidate"; break; fi
    done
    if [ -n "$brew_bin" ]; then
      brew_owner="$(stat -f '%Su' "$brew_bin")"
      owner_home="$(eval echo "~$brew_owner")"
      rm -f "$owner_home/.update-detector/update-detector-companion"
    fi
    rm -f "$CACHED_INSTALL_SH"
    return
  fi
  if ! native_unit_present update-detector-companion; then
    echo "install.sh: no update-detector-companion install found"
    return
  fi

  echo "install.sh: removing update-detector-companion..."
  # Companion is always native, never containerized (needs real root to
  # run apt-get) -- no Docker case to check here. It also has no
  # separate env file, state dir, or dedicated system user: its unit
  # sets Environment= directly and it runs as root (see
  # install_companion above).
  systemctl disable --now update-detector-companion 2>/dev/null || true
  rm -f /etc/systemd/system/update-detector-companion.service
  systemctl daemon-reload
  rm -f /usr/local/bin/update-detector-companion
  rm -f "$CACHED_INSTALL_SH"
}

# prompt_components -> prints a comma-separated list of components to
# install (aggregator,agent,companion), read from INSTALL_COMPONENTS if
# set (for scripted/non-interactive use), otherwise prompted interactively.
#
# Reading from /dev/tty rather than plain stdin is deliberate: this script
# is normally invoked as `curl ... | sh`, which means stdin is the pipe
# itself, not the user's keyboard -- a plain `read` here would silently
# consume pipe data instead of prompting. /dev/tty bypasses that, reading
# directly from the controlling terminal, and works fine even when piped
# this way, as long as there's a real terminal attached (true for an
# interactive SSH session).
prompt_components() {
  if [ -n "${INSTALL_COMPONENTS:-}" ]; then
    echo "$INSTALL_COMPONENTS"
    return
  fi
  if is_macos; then
    # No Docker choice to offer and no aggregator port on macOS -- just
    # agent, companion, or both, all as LaunchDaemons.
    if [ ! -r /dev/tty ]; then
      echo "install.sh: no terminal to prompt on and INSTALL_COMPONENTS not set -- defaulting to agent only" >&2
      echo "agent"
      return
    fi
    echo "Which of update-detector's pieces would you like to set up on this host? (agent and" >&2
    echo "companion both run as LaunchDaemons; there is no aggregator or Docker path on macOS)" >&2
    echo >&2
    echo "  1) detector (agent) only" >&2
    echo "  2) companion only (pairs through an already-installed agent)" >&2
    echo "  3) both" >&2
    printf "Choose [1-3]: " >&2
    read -r choice < /dev/tty
    case "$choice" in
      1) echo "agent" ;;
      2) echo "companion" ;;
      3) echo "agent,companion" ;;
      *) echo "install.sh: invalid choice: $choice" >&2; exit 1 ;;
    esac
    return
  fi
  if [ ! -r /dev/tty ]; then
    echo "install.sh: no terminal to prompt on and INSTALL_COMPONENTS not set -- defaulting to companion only" >&2
    echo "companion"
    return
  fi
  echo "Which of update-detector's pieces would you like to set up on this host?" >&2
  echo "(the aggregator and the agent can each go into Docker or run as a" >&2
  echo "native systemd service -- you'll be asked which, if Docker is usable" >&2
  echo "here; the companion is always native, since it needs real root to run" >&2
  echo "apt-get)" >&2
  echo >&2
  echo "  1) aggregator only" >&2
  echo "  2) detector (agent) only" >&2
  echo "  3) companion only" >&2
  echo "  4) all three" >&2
  printf "Choose [1-4]: " >&2
  read -r choice < /dev/tty
  case "$choice" in
    1) echo "aggregator" ;;
    2) echo "agent" ;;
    3) echo "companion" ;;
    4) echo "aggregator,agent,companion" ;;
    *) echo "install.sh: invalid choice: $choice" >&2; exit 1 ;;
  esac
}

# prompt_aggregator_url CURRENT -> prints CURRENT unchanged if it's
# already non-empty. Otherwise, if a terminal is available, prompts for
# one interactively (same /dev/tty rationale as prompt_components
# above -- this script is normally invoked via `curl | sh`, so stdin
# can't be used for a plain read); otherwise prints nothing. AGGREGATOR_URL
# is mandatory for both the agent and the companion -- without one,
# neither has any purpose (the agent can never enroll with or be applied
# through an aggregator; a companion can't even pair with an agent that
# has none) -- so both callers treat an empty result (no terminal to
# prompt on, or a human leaving it blank) as fatal.
prompt_aggregator_url() {
  if [ -n "${1:-}" ]; then
    printf '%s' "$1"
    return
  fi
  if [ ! -r /dev/tty ]; then
    return
  fi
  echo "install.sh: no AGGREGATOR_URL could be found automatically." >&2
  echo "  You only need one aggregator, reachable from this host -- it doesn't" >&2
  echo "  have to be on the same network as this host; anywhere reachable over" >&2
  echo "  the internet works fine too, as long as this installer can reach it." >&2
  printf "Enter the aggregator's URL (e.g. http://aggregator-host:9090): " >&2
  read -r url < /dev/tty
  printf '%s' "$url"
}

# prompt_uninstall_components -> like prompt_components, but for
# uninstall: prints what was actually detected (native or Docker) before
# offering the menu, and prints nothing (not exit -- this runs inside a
# command substitution, where exit would only terminate that subshell,
# not the script) if nothing is found anywhere.
prompt_uninstall_components() {
  if [ -n "${UNINSTALL_COMPONENTS:-}" ]; then
    echo "$UNINSTALL_COMPONENTS"
    return
  fi

  found=""
  agg_docker_id=$(docker_container_for '(^|/)update-aggregator(:|$)') || agg_docker_id=""
  if native_unit_present update-aggregator || [ -n "$agg_docker_id" ]; then
    found="$found aggregator"
  fi
  agent_docker_id=$(docker_container_for '(^|/)update-detector(:|$)') || agent_docker_id=""
  if native_unit_present update-detector || [ -n "$agent_docker_id" ]; then
    found="$found agent"
  fi
  if native_unit_present update-detector-companion; then
    found="$found companion"
  fi

  if [ -z "$found" ]; then
    echo "install.sh: --uninstall requested, but no update-detector components" >&2
    echo "  (native or Docker) were found on this host." >&2
    return
  fi

  if is_macos; then
    # Agent and/or companion at most (no Docker path, no aggregator port
    # on macOS) -- confirm once for whatever was actually found, same
    # /dev/tty rationale as above.
    echo "Found installed:$found" >&2
    if [ ! -r /dev/tty ]; then
      echo "install.sh: no terminal to prompt on and UNINSTALL_COMPONENTS not set --" >&2
      echo "  found:$found -- set UNINSTALL_COMPONENTS explicitly (e.g. agent,companion) to proceed non-interactively." >&2
      return
    fi
    printf "Uninstall:%s? [y/N]: " "$found" >&2
    read -r choice < /dev/tty
    case "$choice" in
      y|Y|yes|YES) echo "$found" | sed 's/^ *//' ;;
      *) echo "install.sh: cancelled -- nothing uninstalled." >&2; return ;;
    esac
    return
  fi

  if [ ! -r /dev/tty ]; then
    echo "install.sh: no terminal to prompt on and UNINSTALL_COMPONENTS not set --" >&2
    echo "  found:$found -- set UNINSTALL_COMPONENTS explicitly to proceed non-interactively." >&2
    return
  fi

  echo "Found installed:$found" >&2
  echo "Which would you like to uninstall?" >&2
  echo >&2
  echo "  1) aggregator" >&2
  echo "  2) detector (agent)" >&2
  echo "  3) companion" >&2
  echo "  4) all three" >&2
  printf "Choose [1-4]: " >&2
  read -r choice < /dev/tty
  case "$choice" in
    1) echo "aggregator" ;;
    2) echo "agent" ;;
    3) echo "companion" ;;
    4) echo "aggregator,agent,companion" ;;
    *) echo "install.sh: invalid choice: $choice" >&2; exit 1 ;;
  esac
}

# assert_macos_components COMPONENTS -> fatal unless COMPONENTS is
# agent/companion-only. macOS has no Docker path and no aggregator port,
# so anything else (via INSTALL_COMPONENTS= or UNINSTALL_COMPONENTS=) is
# a usage error, not something to silently reinterpret.
assert_macos_components() {
  if is_macos; then
    case ",$1," in
      *,aggregator,*)
        echo "install.sh: the aggregator is not supported on macOS (got: $1) -- agent and companion only." >&2
        exit 1
        ;;
    esac
  fi
}

uninstall_requested=0
if [ "${1:-}" = "--uninstall" ] || [ -n "${UNINSTALL_COMPONENTS:-}" ]; then
  uninstall_requested=1
fi

# Checked unconditionally (cheap either way) so the dispatch condition
# below can ask "is there an agent here at all, native or Docker" without
# repeating this lookup.
existing_agent_native=0
native_unit_present update-detector && existing_agent_native=1
existing_agent_docker=$(docker_container_for '(^|/)update-detector(:|$)') || existing_agent_docker=""

if [ "$uninstall_requested" = "1" ]; then
  components=$(prompt_uninstall_components)
  if [ -z "$components" ]; then
    echo "install.sh: nothing to uninstall" >&2
    exit 0
  fi
  assert_macos_components "$components"
  # Reverse of the install order below -- companion first, so
  # uninstall_agent's "companion still installed" note only fires for the
  # genuinely useful case (removing just the agent while leaving
  # companion installed on purpose), not as noise during a full teardown.
  case ",$components," in *,companion,*) uninstall_companion ;; esac
  case ",$components," in *,agent,*) uninstall_agent ;; esac
  case ",$components," in *,aggregator,*) uninstall_aggregator ;; esac
# INSTALL_COMPONENTS is honored regardless of platform -- this is also how
# the companion's own self-update feature re-invokes this exact script to
# install a specific native component (see internal/companion/
# selfupdate.go) on whatever host it's actually running on. Beyond that,
# the interactive multi-component prompt (as opposed to the original
# companion-only default) triggers on is_wsl2 (no real Docker engine
# usually available there, see docker_available) or whenever no agent
# install -- native or Docker -- exists yet on this host at all: that's
# the "nothing set up here yet, so ask what to set up" case this whole
# feature is for. If an agent already exists, the original zero-config
# "just install the companion against it" default still applies.
elif [ -n "${INSTALL_COMPONENTS:-}" ] || is_wsl2 || { [ "$existing_agent_native" = "0" ] && [ -z "$existing_agent_docker" ]; }; then
  components=$(prompt_components)
  assert_macos_components "$components"
  case ",$components," in
    *,aggregator,*)
      if docker_available && [ "$(prompt_use_docker aggregator)" = "1" ]; then
        install_aggregator_docker
      else
        install_aggregator_native
      fi
      ;;
  esac
  case ",$components," in
    *,agent,*)
      if docker_available && [ "$(prompt_use_docker agent)" = "1" ]; then
        install_agent_docker
      else
        install_agent_native
      fi
      ;;
  esac
  case ",$components," in *,companion,*) install_companion ;; esac
elif is_macos; then
  # An agent already exists and macOS has no companion: re-running with
  # no explicit components updates that agent in place (re-download +
  # restart), mirroring what the Docker path does on Linux.
  install_agent_native
else
  install_companion
fi
