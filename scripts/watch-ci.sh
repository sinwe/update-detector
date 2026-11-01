#!/bin/bash
# watch-ci.sh — follow the GitHub Actions run(s) for a tag (or one run id)
# until they finish, printing only state changes.
#
# Usage: scripts/watch-ci.sh <tag|run-id> [timeout_minutes=45] [interval_seconds=20]
#
# Meant to be left running (foreground or `&`/nohup) instead of polling by
# hand: it prints one line per state transition, dumps the failing step and
# the tail of the failed log when something goes red, and exits with a
# usable code. For a tag it watches *every* run for that ref (a tag push
# triggers both CI and Release), so "done" means both.
#
# Env:
#   REPO=owner/name       default sinwe/update-detector
#   WORKFLOW=Release|CI   watch only that workflow (default: all for the ref)
#   QUIET=1               print nothing but the final summary
#
# Exit: 0 every watched run succeeded
#       1 a run failed, was cancelled, or timed out at the job level
#       2 nothing matched the tag before the timeout
#       3 gh missing / not authenticated, or an unreadable run id
#
# Needs: gh (authenticated) and jq. Complements monitor-release.sh, which
# asks the public API whether the release itself is published (and so also
# whether the ghcr.io images are up); this one answers "is the build still
# running, and what broke if it isn't".
set -eu

TARGET="${1:?usage: scripts/watch-ci.sh <tag|run-id> [timeout_minutes] [interval_seconds]}"
TIMEOUT_MIN="${2:-45}"
INTERVAL="${3:-20}"
REPO="${REPO:-sinwe/update-detector}"
WORKFLOW="${WORKFLOW:-}"
QUIET="${QUIET:-}"

command -v gh >/dev/null 2>&1 || { echo "error: gh is not installed" >&2; exit 3; }
command -v jq >/dev/null 2>&1 || { echo "error: jq is not installed" >&2; exit 3; }
gh auth status >/dev/null 2>&1 || { echo "error: gh is not authenticated (run: gh auth login)" >&2; exit 3; }

# A pure number is a run id; anything else is a tag/branch name.
BY_ID=0
case "$TARGET" in
  *[!0-9]*) ;;
  *) BY_ID=1 ;;
esac

say() { [ -n "$QUIET" ] || echo "$@"; }

# fetch_runs -> JSON array of {databaseId, workflowName, status, conclusion}
fetch_runs() {
  if [ "$BY_ID" = 1 ]; then
    gh run view "$TARGET" --repo "$REPO" \
      --json databaseId,workflowName,status,conclusion,url 2>/dev/null |
      jq -c '[.]'
    return
  fi
  gh run list --repo "$REPO" --limit 40 \
    --json databaseId,workflowName,status,conclusion,headBranch,url |
    jq -c --arg tag "$TARGET" --arg wf "$WORKFLOW" \
      '[.[] | select(.headBranch == $tag) | select($wf == "" or .workflowName == $wf)]'
}

# describe_run ID -> failing step names, one per line (empty when fine)
failing_steps() {
  gh run view "$1" --repo "$REPO" --json jobs --jq '
    .jobs[] | select(.conclusion == "failure" or .conclusion == "cancelled") |
    .name + ": " + ([.steps[] | select(.conclusion == "failure" or .conclusion == "cancelled") | .name] | join(", "))
  ' 2>/dev/null || true
}

deadline=$(( $(date +%s) + TIMEOUT_MIN * 60 ))
failed=0

# Run state is kept in prev_<id> shell variables rather than an associative
# array: /bin/bash on macOS is 3.2, which doesn't have them, and this is
# exactly the machine these scripts get run on. Ids are numeric, so the
# eval'd name is always a safe identifier.
state_of() { eval "echo \"\${prev_$1:-}\""; }
set_state() { eval "prev_$1=\$2"; }

while :; do
  runs="$(fetch_runs || echo '[]')"
  [ "$runs" = "null" ] && runs='[]'

  if [ "$BY_ID" = 1 ] && [ "$runs" = '[]' ]; then
    echo "error: run $TARGET not found in $REPO" >&2
    exit 3
  fi

  count="$(echo "$runs" | jq 'length')"
  pending=0
  while IFS=$'\t' read -r id name status conclusion url; do
    state="$status/$conclusion"
    if [ "$(state_of "$id")" != "$state" ]; then
      set_state "$id" "$state"
      if [ "$status" = "completed" ]; then
        say "$(date '+%H:%M:%S') $name ($id): $conclusion"
        if [ "$conclusion" != "success" ]; then
          failed=1
          say "  $url"
          steps="$(failing_steps "$id")"
          [ -n "$steps" ] && echo "$steps" | sed 's/^/  step: /'
          # Tail of the failed log: enough to see the actual error without
          # pulling the whole (very long) log down.
          gh run view "$id" --repo "$REPO" --log-failed 2>/dev/null |
            tail -n 20 | sed 's/^/  | /' || true
        fi
      else
        say "$(date '+%H:%M:%S') $name ($id): $status"
      fi
    fi
    [ "$status" = "completed" ] || pending=$((pending + 1))
  done < <(echo "$runs" | jq -r '.[] | [.databaseId, .workflowName, .status, (.conclusion // ""), .url] | @tsv')

  if [ "$count" -gt 0 ] && [ "$pending" -eq 0 ]; then
    if [ "$failed" -eq 0 ]; then
      echo "OK: $TARGET — all $count run(s) succeeded"
      exit 0
    fi
    echo "error: $TARGET — a run failed (see above)" >&2
    exit 1
  fi

  if [ "$(date +%s)" -ge "$deadline" ]; then
    if [ "$count" -eq 0 ]; then
      echo "error: no workflow run found for $TARGET in $REPO after ${TIMEOUT_MIN}m" >&2
      exit 2
    fi
    echo "error: timed out after ${TIMEOUT_MIN}m with $pending run(s) still going:" >&2
    echo "$runs" | jq -r '.[] | "  \(.workflowName) (\(.databaseId)): \(.status) \(.url)"' >&2
    exit 1
  fi

  sleep "$INTERVAL"
done
