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
# (offline) are reported and skipped — a 409 from the push means exactly
# that, or an action already in flight.
#
# Auth: ADMIN_APPLY_SHARED_SECRET env var, or auto-read from the running
# aggregator container (never printed, never stored). Needs curl + jq +
# python3 + docker on this host.
set -eu

TARGET="${1:?usage: scripts/fleet-update.sh <target_version>}"
AGGREGATOR_URL="${AGGREGATOR_URL:-http://localhost:9090}"
AGG_CONTAINER="${AGG_CONTAINER:-$(docker ps --format '{{.Names}}' | grep -i aggregator | head -n 1)}"
SECRET="${ADMIN_APPLY_SHARED_SECRET:-$(docker exec "$AGG_CONTAINER" env 2>/dev/null | grep '^ADMIN_APPLY_SHARED_SECRET=' | cut -d= -f2-)}"
if [ -z "$SECRET" ]; then
  echo "error: no apply secret (set ADMIN_APPLY_SHARED_SECRET or run where the aggregator container is visible)" >&2
  exit 1
fi

api() { # api METHOD PATH [BODY] -> prints body; sets HTTP_CODE
  local method="$1" path="$2" body="${3:-}"
  local tmp
  tmp="$(mktemp)"
  if [ -n "$body" ]; then
    HTTP_CODE="$(curl -sS --max-time 30 -o "$tmp" -w "%{http_code}" -X "$method" \
      -H 'Content-Type: application/json' "$AGGREGATOR_URL$path" -d "$body")"
  else
    HTTP_CODE="$(curl -sS --max-time 30 -o "$tmp" -w "%{http_code}" -X "$method" \
      -H 'Content-Type: application/json' "$AGGREGATOR_URL$path")"
  fi
  cat "$tmp"
  rm -f "$tmp"
}

api_secret() { # same, plus the apply-secret header
  local method="$1" path="$2" body="${3:-}"
  local tmp
  tmp="$(mktemp)"
  HTTP_CODE="$(curl -sS --max-time 30 -o "$tmp" -w "%{http_code}" -X "$method" \
    -H 'Content-Type: application/json' -H "X-Admin-Apply-Secret: $SECRET" \
    "$AGGREGATOR_URL$path" ${body:+-d "$body"})"
  cat "$tmp"
  rm -f "$tmp"
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

at_version() { # at_version AGENT_ID FIELD -> true when that field == TARGET
  local id="$1" field="$2"
  [ "$(api GET "/admin/agents/$id/version" | jq -r --arg f "$field" '.[$f] // ""')" = "$TARGET" ]
}

push_update() { # push_update AGENT_ID COMPONENT -> 0 pushed+accepted, 1 skipped/failed
  local id="$1" component="$2"
  local resp
  resp="$(api_secret POST "/admin/agents/$id/self-update" \
    "{\"component\":\"$component\",\"target_version\":\"$TARGET\"}")"
  case "$HTTP_CODE" in
    202) echo "  pushed $component -> $TARGET"; return 0 ;;
    409) echo "  skip $component: no companion connected or action in flight"; return 1 ;;
    *)   echo "  ERROR pushing $component: http $HTTP_CODE $resp" >&2; return 1 ;;
  esac
}

echo "== aggregator at $AGGREGATOR_URL -> $TARGET =="
[ "$(api GET /healthz | jq -r .version)" != "" ] || { echo "error: aggregator not reachable" >&2; exit 1; }

# Refresh the release metadata synchronously (re-setting the same channel
# forces an immediate check) so the dashboard buttons agree with TARGET.
CHANNEL="$(docker exec "$AGG_CONTAINER" env 2>/dev/null | grep '^SELF_UPDATE_CHANNEL=' | cut -d= -f2-)"
CHANNEL="${CHANNEL:-release}"
api_secret POST /admin/self-update-channel "{\"channel\":\"$CHANNEL\"}" >/dev/null || true
echo "channel: $CHANNEL (refreshed)"

HOSTS_JSON="$(api GET /widgets/hosts)"
echo "fleet: $(echo "$HOSTS_JSON" | jq -r '.[].hostname' | tr '\n' ' ')"

# Which host runs the aggregator? Only its card renders the aggregator button.
AGG_HOST_ID="$(api GET /admin | python3 -c \
  "import re,sys; m = re.findall(r\"postSelfUpdate\\('([^']+)', 'aggregator'\", sys.stdin.read()); print(m[0] if m else '')")"
if [ -n "$AGG_HOST_ID" ]; then
  echo "== aggregator component on $AGG_HOST_ID =="
  if at_version "$AGG_HOST_ID" agent_version 2>/dev/null && \
     [ "$(api GET /healthz | jq -r .version)" = "$TARGET" ]; then
    echo "  aggregator already at $TARGET"
  else
    push_update "$AGG_HOST_ID" aggregator
    wait_for "aggregator healthy at $TARGET" 900 \
      bash -c "[ \"\$(curl -sS --max-time 10 $AGGREGATOR_URL/healthz | jq -r .version)\" = \"$TARGET\" ]"
  fi
else
  echo "warning: no host advertises the aggregator — skipping aggregator update" >&2
fi

PASS=0; SKIP=0; FAIL=0
while read -r id name; do
  [ -n "$id" ] || continue
  echo "== host $name ($id) =="
  info="$(api GET "/admin/agents/$id/version")"
  agent_v="$(echo "$info" | jq -r '.agent_version // ""')"
  comp_v="$(echo "$info" | jq -r '.companion_version // ""')"
  if [ -z "$comp_v" ]; then
    echo "  skip: no companion connected (offline?)"
    SKIP=$((SKIP + 1))
    continue
  fi
  ok=1
  if [ "$agent_v" = "$TARGET" ]; then
    echo "  agent already at $TARGET"
  else
    push_update "$id" agent && wait_for "agent at $TARGET" 600 at_version "$id" agent_version || ok=0
  fi
  # Re-read: the agent update may have taken a while; companion may connect late.
  comp_v="$(api GET "/admin/agents/$id/version" | jq -r '.companion_version // ""')"
  if [ "$comp_v" = "$TARGET" ]; then
    echo "  companion already at $TARGET"
  elif [ -z "$comp_v" ]; then
    echo "  skip companion: no companion connected" >&2
    ok=0
  else
    push_update "$id" companion && wait_for "companion at $TARGET" 600 at_version "$id" companion_version || ok=0
  fi
  if [ "$ok" = 1 ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); fi
done < <(echo "$HOSTS_JSON" | jq -r '.[] | "\(.agent_id) \(.hostname)"')

echo "== done: $PASS updated, $SKIP skipped (offline), $FAIL failed =="
[ "$FAIL" = 0 ]
