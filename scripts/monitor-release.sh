#!/bin/bash
# monitor-release.sh — wait until a pushed tag becomes a published release.
#
# Usage: scripts/monitor-release.sh <tag> [timeout_minutes=45] [interval_seconds=90]
#
# Polls the public GitHub API (curl + jq only; GH_TOKEN optional and only
# raises the rate limit). Succeeds when the release exists and isn't a
# draft — the workflow publishes the release dead last (after the
# multi-arch ghcr.io images), so that also means the images are up.
# Fails fast if the workflow run for the tag concludes unsuccessfully.
#
# Runs anywhere with internet access (e.g. NOT behind the office proxy —
# run it on the aggregator host).
set -eu

TAG="${1:?usage: scripts/monitor-release.sh <tag> [timeout_minutes] [interval_seconds]}"
TIMEOUT_MIN="${2:-45}"
INTERVAL="${3:-90}"
REPO="${REPO:-sinwe/update-detector}"
API="https://api.github.com/repos/$REPO"

auth_args=()
if [ -n "${GH_TOKEN:-}" ]; then
  auth_args=(-H "Authorization: Bearer $GH_TOKEN")
fi

deadline=$(( $(date +%s) + TIMEOUT_MIN * 60 ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  # Fail fast on a red workflow run for this tag.
  run_json="$(curl -sS --max-time 30 "${auth_args[@]}" \
    "$API/actions/runs?event=push&per_page=10" || true)"
  conclusion="$(echo "$run_json" | jq -r \
    --arg tag "$TAG" '[.workflow_runs[]? | select(.head_branch == $tag)] | .[0] | "\(.status // "?")/\(.conclusion // "?")"' 2>/dev/null || echo "?/?")"
  case "$conclusion" in
    completed/failure|completed/cancelled|completed/timed_out)
      echo "error: release workflow for $TAG ended: $conclusion" >&2
      exit 1
      ;;
  esac

  # Release published? (created last in the workflow, after the images)
  rel_json="$(curl -sS --max-time 30 "${auth_args[@]}" -o /dev/null -w "%{http_code}" \
    "$API/releases/tags/$TAG" || echo 000)"
  if [ "$rel_json" = "200" ]; then
    echo "release $TAG is published"
    for img in update-detector update-aggregator; do
      echo "checking ghcr.io/sinwe/$img:$TAG ..."
      token="$(curl -sS --max-time 30 "https://ghcr.io/token?service=ghcr.io&scope=repository:sinwe/$img:pull" | jq -r .token)"
      if curl -sS --max-time 30 -o /dev/null -w "%{http_code}" \
          -H "Authorization: Bearer $token" \
          "https://ghcr.io/v2/sinwe/$img/tags/list" | grep -q 200; then
        echo "  $img image list reachable"
      else
        echo "  warning: couldn't verify $img image (continuing anyway)"
      fi
    done
    echo "OK: $TAG released"
    exit 0
  fi

  echo "$(date '+%H:%M:%S') waiting for $TAG (workflow: $conclusion, release: http $rel_json) — retry in ${INTERVAL}s"
  sleep "$INTERVAL"
done

echo "error: timed out after ${TIMEOUT_MIN}m waiting for $TAG" >&2
exit 1
