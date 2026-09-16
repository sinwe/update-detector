#!/bin/bash
# rollout.sh — release a version and roll the fleet to it, end to end.
#
# Usage: scripts/rollout.sh <tag> [branch]
#   <tag>    e.g. v0.15.3-alpha3 (same convention as scripts/release.sh)
#   [branch] branch to release (default: current branch)
#
# Cascades the fleet-ops stages consecutively in one terminal:
#   1. scripts/release.sh — merge branch into main, push, tag (this
#      triggers .github/workflows/release.yml). Runs HERE: needs the
#      working tree committed and GitHub reachable (SSH fallback built in).
#   2. scripts/monitor-release.sh — wait for the release + ghcr.io
#      images. Runs on the aggregator host over SSH (it has direct
#      internet; a proxied dev machine usually can't poll api.github.com).
#   3. scripts/fleet-update.sh — push aggregator/agent/companion
#      self-updates to every online host. Runs on the aggregator host.
#
# The nuc-side stages run in the foreground so their logs stream here.
# Any stage failing aborts the cascade: a red release never reaches the
# fleet, and a failed monitor never triggers updates.
#
# Env:
#   AGG_SSH_HOST        ssh target of the aggregator host (default: nuc)
#   MONITOR_TIMEOUT_MIN monitor timeout in minutes (default: 45)
#   MONITOR_INTERVAL_SEC poll interval in seconds (default: 90)
set -eu
cd "$(dirname "$0")/.."

TAG="${1:?usage: scripts/rollout.sh <tag> [branch]}"
BRANCH="${2:-$(git branch --show-current)}"
AGG_SSH_HOST="${AGG_SSH_HOST:-nuc}"

rssh() {
  ssh -o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=60 "$AGG_SSH_HOST" "$@"
}

echo "== rollout $TAG (branch: $BRANCH, aggregator host: $AGG_SSH_HOST) =="

# 0. Aggregator host reachable before we tag anything.
rssh true || { echo "error: cannot ssh to $AGG_SSH_HOST" >&2; exit 1; }

# 1. Merge, push, tag.
scripts/release.sh "$TAG" "$BRANCH"

# 2+3. Ship the stage scripts over (post-merge tree == what the tag
# points at) and run them there in the foreground.
scp scripts/monitor-release.sh scripts/fleet-update.sh "$AGG_SSH_HOST:/tmp/"
rssh "chmod +x /tmp/monitor-release.sh /tmp/fleet-update.sh && /tmp/monitor-release.sh '$TAG' '${MONITOR_TIMEOUT_MIN:-45}' '${MONITOR_INTERVAL_SEC:-90}'" || {
  echo "error: release $TAG did not publish — fleet untouched" >&2
  exit 1
}
rssh "/tmp/fleet-update.sh '$TAG'" || {
  echo "error: fleet update to $TAG finished with failures (see above)" >&2
  exit 1
}

echo "== rollout $TAG complete =="
