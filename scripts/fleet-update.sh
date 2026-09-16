#!/bin/bash
# fleet-update.sh — roll the whole fleet to one version via the aggregator.
#
# Usage: scripts/fleet-update.sh <target_version>
#
# MUST run on the aggregator host itself (default AGGREGATOR_URL is
# http://localhost:9090). For each approved host with a connected
# companion it pushes agent then companion self-updates; the aggregator
# itself goes first since the server rejects component updates newer
# than its own running version. Hosts with no connected companion
# (offline) are reported and skipped — a 409 from a push means exactly
# that, or an action already in flight (e.g. from an earlier interrupted
# run), in which case we just wait for the version to land.
#
# Auth: ADMIN_APPLY_SHARED_SECRET env var, or auto-read from the running
# aggregator container (never printed, never stored). Needs curl + jq +
# python3 + docker on this host. Safe to re-run: hosts already at the
# target are skipped.
set -eu

TARGET="${1:?usage: scripts/fleet-update.sh <target_version>}"
AGGREGATOR_URL="${AGGREGATOR_URL:-http://localhost:9090}"
AGG_CONTAINER="${AGG_CONTAINER:-$(docker ps --format '{{.Names}}' | grep -i aggregator | head -n 1)}"
SECRET="${ADMIN_APPLY_SHARED_SECRET:-$(docker exec "$AGG_CONTAINER" env 2>/dev/null | grep '^ADMIN_APPLY_SHARED_SECRET=' | cut -d= -f2-)}"
if [ -z "$SECRET" ]; then
  echo "error: no apply secret (set ADMIN_APPLY_SHARED_SECRET or run where the aggregator container is visible)" >&2
  exit 1
fi

# NOTE: these helpers echo the HTTP code and write the body to the file
# given as $1 — never the other way round. Capturing a helper's stdout
# with $(...) runs it in a subshell, so a "global" for the status code
# would silently keep its previous value (this exact bug once reported a
# successful push as failed and aborted a run).
call() { # call OUTFILE METHOD PATH [BODY] — echoes http code
  local outfile="$1" method="$2" path="$3" body="${4:-}"
  local args=(-sS --max-time 30 -o "$outfile" -w "%{http_code}" -X "$method"
    -H 'Content-Type: application/json'
    -H "X-Admin-Apply-Secret: $SECRET")
  if [ -n "$body" ]; then
    args+=(-d "$body")
  fi
  curl "${args[@]}" "$AGGREGATOR_URL$path"
}

GET() { # GET PATH — prints body, fails unless 2xx
  local path="$1" tmp code
  tmp="$(mktemp)"
  code="$(call "$tmp" GET "$path")"
  cat "$tmp"
  rm -f "$tmp"
  case "$code" in
    2*) return 0 ;;
    *) echo "GET $path -> http $code" >&2; return 1 ;;
  esac
}

wait_for() { # wait_for DESC TIMEOUT_SECS CMD... (succeeds when CMD exits 0)
  local desc="$1" timeout_s="$2"
  shift 2
  local deadline=$(( $(date +%s) + timeout_s ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if "$@" >/dev/null 2>&1; then
      echo "  $desc: OK"
      return 0
    fi
    sleep 10
  done
  echo "  $desc: TIMEOUT after ${timeout_s}s" >&2
  return 1
}

version_field() { # version_field AGENT_ID FIELD — prints the field ("" if unset)
  local id="$1" field="$2" tmp
  tmp="$(mktemp)"
  if [ "$(call "$tmp" GET "/admin/agents/$id/version")" = "200" ]; then
    jq -r --arg f "$field" '.[$f] // ""' "$tmp"
  else
    echo ""
  fi
  rm -f "$tmp"
}

# push_update AGENT_ID COMPONENT — echoes ok|skip|error, always exits 0.
push_update() {
  local id="$1" component="$2" tmp code body
  tmp="$(mktemp)"
  code="$(call "$tmp" POST "/admin/agents/$id/self-update" \
    "{\"component\":\"$component\",\"target_version\":\"$TARGET\"}")"
  body="$(cat "$tmp")"
  rm -f "$tmp"
  case "$code" in
    202) echo "  pushed $component -> $TARGET"; echo ok ;;
    409) echo "  $component not pushed (http 409: no companion connected or action already in flight)"; echo skip ;;
    *)   echo "  ERROR pushing $component: http $code $body" >&2; echo error ;;
  esac
  return 0
}

echo "== aggregator at $AGGREGATOR_URL -> $TARGET =="
[ "$(GET /healthz | jq -r .version)" != "" ] || { echo "error: aggregator not reachable" >&2; exit 1; }

# Refresh the release metadata synchronously (re-setting the same channel
# forces an immediate check) so the dashboard buttons agree with TARGET.
CHANNEL="$(docker exec "$AGG_CONTAINER" env 2>/dev/null | grep '^SELF_UPDATE_CHANNEL=' | cut -d= -f2-)"
CHANNEL="${CHANNEL:-release}"
tmp="$(mktemp)"
call "$tmp" POST /admin/self-update-channel "{\"channel\":\"$CHANNEL\"}" >/dev/null || true
rm -f "$tmp"
echo "channel: $CHANNEL (refreshed)"

HOSTS_JSON="$(GET /widgets/hosts)"
echo "fleet: $(echo "$HOSTS_JSON" | jq -r '.[].hostname' | tr '\n' ' ')"

# Which host runs the aggregator? Only its card renders the aggregator button.
AGG_HOST_ID="$(GET /admin | python3 -c \
  "import re,sys; m = re.findall(r\"postSelfUpdate\\('([^']+)', 'aggregator'\", sys.stdin.read()); print(m[0] if m else '')")"
if [ -n "$AGG_HOST_ID" ]; then
  echo "== aggregator component on $AGG_HOST_ID =="
  if [ "$(GET /healthz | jq -r .version)" = "$TARGET" ]; then
    echo "  aggregator already at $TARGET"
  else
    case "$(push_update "$AGG_HOST_ID" aggregator | tail -n 1)" in
      ok|skip) wait_for "aggregator healthy at $TARGET" 900 \
        bash -c "[ \"\$(curl -sS --max-time 10 $AGGREGATOR_URL/healthz | jq -r .version)\" = \"$TARGET\" ]" || true ;;
    esac
    if [ "$(GET /healthz | jq -r .version)" != "$TARGET" ]; then
      echo "error: aggregator did not reach $TARGET — aborting host updates" >&2
      exit 1
    fi
  fi
else
  echo "warning: no host advertises the aggregator — skipping aggregator update" >&2
fi

PASS=0; SKIP=0; FAIL=0
while read -r id name; do
  [ -n "$id" ] || continue
  echo "== host $name ($id) =="
  agent_v="$(version_field "$id" agent_version)"
  comp_v="$(version_field "$id" companion_version)"
  if [ -z "$comp_v" ]; then
    echo "  skip: no companion connected (offline?)"
    SKIP=$((SKIP + 1))
    continue
  fi
  ok=1
  if [ "$agent_v" = "$TARGET" ]; then
    echo "  agent already at $TARGET"
  else
    case "$(push_update "$id" agent | tail -n 1)" in
      ok|skip)
        if ! wait_for "agent at $TARGET" 600 \
            bash -c "[ \"\$(curl -sS --max-time 10 $AGGREGATOR_URL/admin/agents/$id/version | jq -r .agent_version)\" = \"$TARGET\" ]"; then
          ok=0
        fi ;;
      *) ok=0 ;;
    esac
  fi
  comp_v="$(version_field "$id" companion_version)"
  if [ "$comp_v" = "$TARGET" ]; then
    echo "  companion already at $TARGET"
  elif [ -z "$comp_v" ]; then
    echo "  skip companion: no companion connected" >&2
    ok=0
  else
    case "$(push_update "$id" companion | tail -n 1)" in
      ok|skip)
        if ! wait_for "companion at $TARGET" 600 \
            bash -c "[ \"\$(curl -sS --max-time 10 $AGGREGATOR_URL/admin/agents/$id/version | jq -r .companion_version)\" = \"$TARGET\" ]"; then
          ok=0
        fi ;;
      *) ok=0 ;;
    esac
  fi
  if [ "$ok" = 1 ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); fi
done < <(echo "$HOSTS_JSON" | jq -r '.[] | "\(.agent_id) \(.hostname)"')

echo "== done: $PASS updated, $SKIP skipped (offline), $FAIL failed =="
[ "$FAIL" = 0 ]
