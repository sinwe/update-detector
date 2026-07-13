#!/bin/sh
# install.sh installs update-detector's pieces as native systemd services.
# Run as root:
#
#   curl -fsSL https://forgejo.winar.to/winarto/update-detector/raw/branch/main/install.sh | sudo sh
#
# On a normal Linux host, this installs just the companion
# (update-detector-companion), auto-discovering everything it needs from an
# already-running update-detector *container*: its bind-mounted state dir
# (for the Unix-socket token handoff), its AGGREGATOR_URL, and its published
# port. This is the same behavior install.sh has always had.
#
# On WSL2, this offers something different: an interactive choice of
# installing the aggregator, the detector (agent), the companion, or all
# three, each as a *native* systemd service with no Docker involved at all.
# This exists because WSL2 commonly has no real Docker engine inside the
# distro itself -- `docker` on PATH there is frequently just Docker
# Desktop's Windows-side CLI shim (confirmed live: resolves to
# /mnt/c/Program Files/Docker/...), which talks to Docker Desktop's own
# hidden VM, not this distro's filesystem. Running the agent containerized
# against that shim would silently detect updates for the wrong system,
# with no visible error -- so native is the safer default on WSL2. See
# docs/wsl2.md for the full explanation and how to tell if you actually
# have a real in-distro Docker engine (in which case the normal
# docker-compose.yml path works fine and this native install isn't needed).
#
# Set INSTALL_VERSION to pin a release instead of "latest". On WSL2, set
# INSTALL_COMPONENTS (comma-separated: aggregator,agent,companion) to skip
# the interactive prompt for scripted/non-interactive use.

set -eu

FORGEJO_API="https://forgejo.winar.to/api/v1/repos/winarto/update-detector"
INSTALL_VERSION="${INSTALL_VERSION:-latest}"

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh: must be run as root" >&2
  exit 1
fi

# docker is deliberately not required here (unlike earlier versions of this
# script) -- on WSL2 it's frequently present on PATH but not a working
# engine (see header), and requiring it would block native-only installs
# for no reason. Each Docker-dependent code path below handles its absence
# or failure gracefully instead.
for tool in curl systemctl; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "install.sh: $tool is required but not found on PATH" >&2
    exit 1
  fi
done

arch="$(uname -m)"
case "$arch" in
  x86_64) goarch=amd64 ;;
  aarch64|arm64) goarch=arm64 ;;
  *) echo "install.sh: unsupported architecture $arch" >&2; exit 1 ;;
esac

# Any one of these being true is sufficient; checking all three is just
# defense against any single one being absent on some WSL2 build.
is_wsl2() {
  [ -n "${WSL_DISTRO_NAME:-}" ] && return 0
  grep -qi microsoft /proc/version 2>/dev/null && return 0
  [ -e /proc/sys/fs/binfmt_misc/WSLInterop ] && return 0
  return 1
}

# resolve_asset_url ASSET_NAME -> prints that asset's browser_download_url
# for $INSTALL_VERSION, or nothing if not found in that release.
resolve_asset_url() {
  asset_name="$1"
  if [ "$INSTALL_VERSION" = "latest" ]; then
    release_url="$FORGEJO_API/releases/latest"
  else
    release_url="$FORGEJO_API/releases/tags/$INSTALL_VERSION"
  fi
  # Deliberately not `curl -f ... | jq ...`: same reasoning as
  # release.yml's own publish step -- a failed request here would silently
  # produce an empty result via a suppressed body, rather than a clear
  # error. grep+sed avoids needing jq on the *installing* host too, which
  # (unlike the release workflow's own runner) we don't control.
  curl -fsSL "$release_url" \
    | grep -o "\"browser_download_url\":[^,]*$asset_name\"" \
    | head -1 \
    | sed -E 's/.*"(https[^"]+)"$/\1/'
}

# download_binary NAME DEST -> downloads NAME-linux-$goarch to DEST,
# atomically (via a .new + mv, so a partial download never replaces a
# working binary) and executable.
download_binary() {
  name="$1" dest="$2"
  asset_name="$name-linux-$goarch"
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

# install_unit NAME -> daemon-reload + enable + restart. Not `enable --now`
# -- its implicit `start` is a no-op if the service is already running
# (e.g. re-running this script to pick up a config change), silently
# leaving the old process running with its stale environment. `restart`
# unconditionally stops-then-starts regardless of current state.
install_unit() {
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

install_agent_native() {
  echo "install.sh: installing update-detector (agent) natively..."
  bin_path="/usr/local/bin/update-detector"
  download_binary update-detector "$bin_path"

  ensure_system_user update-detector
  state_dir="${STATE_DIR:-/var/lib/update-detector}"
  mkdir -p "$state_dir"
  chown update-detector:update-detector "$state_dir"

  # Every file-path var is set to its real absolute value here, never the
  # config package's Docker-oriented /host/... defaults -- there's no
  # container boundary to cross on a native install, so /host/etc/... etc.
  # would just be wrong. RELEASE_UPGRADES_FILE and REBOOT_REQUIRED_FILE
  # point at paths WSL2 may never populate, which internal/checker/reboot
  # and the release-upgrades reader already handle gracefully (treated as
  # false/empty, not an error).
  env_file="/etc/default/update-detector"
  cat > "$env_file" <<EOF
LISTEN_ADDR=${LISTEN_ADDR:-:8080}
HOSTNAME_OVERRIDE=${HOSTNAME_OVERRIDE:-}
CHECK_INTERVAL=${CHECK_INTERVAL:-6h}
APT_SOURCES_LIST=/etc/apt/sources.list
APT_SOURCES_LIST_D=/etc/apt/sources.list.d
DPKG_STATUS_FILE=/var/lib/dpkg/status
APT_LISTS_CACHE_DIR=$state_dir/apt/lists
OS_RELEASE_FILE=/etc/os-release
RELEASE_UPGRADES_FILE=/etc/update-manager/release-upgrades
REBOOT_REQUIRED_FILE=/var/run/reboot-required
STATE_FILE=$state_dir/state.json
TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN:-}
TELEGRAM_CHAT_ID=${TELEGRAM_CHAT_ID:-}
NOTIFY_ON_STARTUP=false
AGGREGATOR_URL=${AGGREGATOR_URL:-}
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
  env_file="/etc/default/update-aggregator"
  cat > "$env_file" <<EOF
LISTEN_ADDR=${AGGREGATOR_LISTEN_ADDR:-:9090}
REGISTRY_FILE=$data_dir/registry.json
TELEGRAM_BOT_TOKEN=${AGGREGATOR_TELEGRAM_BOT_TOKEN:-}
TELEGRAM_CHAT_ID=${AGGREGATOR_TELEGRAM_CHAT_ID:-}
ADMIN_APPLY_SHARED_SECRET=${ADMIN_APPLY_SHARED_SECRET:-}
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
}

install_companion() {
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
    # shellcheck disable=SC1090
    . "$agent_env_file"
    socket_path="${COMPANION_SOCKET_PATH:-$state_dir/companion.sock}"
    agg_url="${AGGREGATOR_URL:-}"
    agent_status_url="http://localhost:${LISTEN_ADDR#*:}/status"
  fi

  # docker is checked, but never required -- a broken/absent docker here
  # must not abort the script when native discovery already succeeded (or
  # even when it didn't: the "neither found" case below gives a clear
  # error either way).
  if command -v docker >/dev/null 2>&1; then
    container_id=$(docker ps --format '{{.ID}} {{.Image}}' 2>/dev/null \
      | awk '$2 ~ /(^|\/)update-detector(:|$)/ {print $1; exit}') || container_id=""
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
        if [ -z "$agg_url" ]; then
          echo "install.sh: container $container_id has no AGGREGATOR_URL set -- set it and restart the container first" >&2
          exit 1
        fi

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
            # where the container's own port already *is* the host's port,
            # unchanged.
            echo "install.sh: $agg_host is a local container with no published-port mapping (likely --network host) -- using localhost:$agg_port"
            agg_url="http://localhost:$agg_port"
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
  echo "install.sh: done. Check status with: systemctl status update-detector-companion"
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
  if [ ! -r /dev/tty ]; then
    echo "install.sh: no terminal to prompt on and INSTALL_COMPONENTS not set -- defaulting to companion only" >&2
    echo "companion"
    return
  fi
  echo "This looks like WSL2. Since there usually isn't a real Docker engine" >&2
  echo "in here (see docs/wsl2.md), install.sh can install any of these as" >&2
  echo "native systemd services instead -- no Docker needed for any of them:" >&2
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

if is_wsl2; then
  components=$(prompt_components)
  case ",$components," in *,aggregator,*) install_aggregator_native ;; esac
  case ",$components," in *,agent,*) install_agent_native ;; esac
  case ",$components," in *,companion,*) install_companion ;; esac
else
  install_companion
fi
