#!/bin/sh
# install.sh installs update-detector-companion as a systemd service on this
# host. Run as root, on a host that's already running the update-detector
# container with AGGREGATOR_URL configured:
#
#   curl -fsSL https://forgejo.winar.to/winarto/update-detector/raw/branch/main/install.sh | sh
#
# Everything else is auto-discovered from that running container: its
# bind-mounted state dir (for the Unix-socket token handoff), its
# AGGREGATOR_URL, and its published port (for the companion to reach the
# agent's own GET /status when validating an action). Set INSTALL_VERSION to
# pin a release instead of "latest".

set -eu

FORGEJO_API="https://forgejo.winar.to/api/v1/repos/winarto/update-detector"
INSTALL_VERSION="${INSTALL_VERSION:-latest}"
BIN_PATH="/usr/local/bin/update-detector-companion"
UNIT_PATH="/etc/systemd/system/update-detector-companion.service"

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh: must be run as root" >&2
  exit 1
fi

for tool in docker curl systemctl; do
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

if [ "$INSTALL_VERSION" = "latest" ]; then
  release_url="$FORGEJO_API/releases/latest"
else
  release_url="$FORGEJO_API/releases/tags/$INSTALL_VERSION"
fi
asset_name="update-detector-companion-linux-$goarch"

echo "install.sh: resolving $asset_name from release $INSTALL_VERSION..."
download_url=$(curl -fsSL "$release_url" \
  | grep -o "\"browser_download_url\":[^,]*$asset_name\"" \
  | head -1 \
  | sed -E 's/.*"(https[^"]+)"$/\1/')
if [ -z "$download_url" ]; then
  echo "install.sh: could not find asset $asset_name in release $INSTALL_VERSION" >&2
  exit 1
fi

echo "install.sh: downloading $download_url"
curl -fsSL "$download_url" -o "$BIN_PATH.new"
chmod 0755 "$BIN_PATH.new"
mv "$BIN_PATH.new" "$BIN_PATH"

# Find the running update-detector container -- never update-aggregator, in
# case both happen to run on this same host.
container_id=$(docker ps --format '{{.ID}} {{.Image}}' \
  | awk '$2 ~ /(^|\/)update-detector(:|$)/ {print $1; exit}')
if [ -z "$container_id" ]; then
  echo "install.sh: no running update-detector container found on this host -- start it first" >&2
  exit 1
fi

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

# Empty for --network host containers (no published-port mapping to read),
# which is exactly when localhost:8080 is already correct anyway.
host_port=$(docker inspect --format \
  '{{with index .NetworkSettings.Ports "8080/tcp"}}{{(index . 0).HostPort}}{{end}}' \
  "$container_id" 2>/dev/null || true)
agent_status_url="http://localhost:${host_port:-8080}/status"

echo "install.sh: socket=$socket_path aggregator=$agg_url agent_status=$agent_status_url"

cat > "$UNIT_PATH" <<EOF
[Unit]
Description=update-detector companion (applies pending package upgrades on trigger)
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BIN_PATH
Environment=COMPANION_SOCKET_PATH=$socket_path
Environment=AGGREGATOR_URL=$agg_url
Environment=AGENT_STATUS_URL=$agent_status_url
Restart=on-failure
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now update-detector-companion

echo "install.sh: done. Check status with: systemctl status update-detector-companion"
