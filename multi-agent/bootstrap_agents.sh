#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-$SCRIPT_DIR/docker-compose.yml}"

CRAB_ID="${CRAB_ID:-crab}"
SHRIMP_ID="${SHRIMP_ID:-shrimp}"
BASE_URL="${STATOCYST_BASE_URL:-http://statocyst:8080}"
PULL_TIMEOUT_MS="${PULL_TIMEOUT_MS:-5000}"

wait_for_service() {
  local service="$1"
  local timeout="${2:-60}"
  local start_ts now cid running health
  start_ts="$(date +%s)"
  cid="$(docker compose -f "$COMPOSE_FILE" ps -q "$service")"
  if [[ -z "$cid" ]]; then
    echo "ERROR: service container not found: $service" >&2
    exit 1
  fi

  while true; do
    now="$(date +%s)"
    if (( now - start_ts > timeout )); then
      echo "ERROR: timeout waiting for $service (${timeout}s)" >&2
      exit 1
    fi

    running="$(docker inspect -f '{{.State.Running}}' "$cid" 2>/dev/null || true)"
    health="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$cid" 2>/dev/null || true)"

    if [[ "$running" == "true" ]]; then
      if [[ -z "$health" || "$health" == "healthy" ]]; then
        return 0
      fi
    fi
    sleep 1
  done
}

wait_for_statocyst_http() {
  local timeout="${1:-30}"
  local start_ts now
  start_ts="$(date +%s)"
  while true; do
    now="$(date +%s)"
    if (( now - start_ts > timeout )); then
      echo "ERROR: timeout waiting for statocyst HTTP readiness (${timeout}s)" >&2
      exit 1
    fi
    if docker exec multi-agent-crab-1 bash -lc "curl -sS -o /tmp/statocyst_health.json -w '%{http_code}' $BASE_URL/healthz | grep -q '^200$'" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
}

echo "[bootstrap] starting stack"
docker compose -f "$COMPOSE_FILE" up -d statocyst shrimp crab

echo "[bootstrap] recreating statocyst for clean in-memory registry"
docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps statocyst

wait_for_service statocyst 30
wait_for_service shrimp 90
wait_for_service crab 90

echo "[bootstrap] checking statocyst health from crab container"
wait_for_statocyst_http 45

echo "[bootstrap] registering $CRAB_ID"
CRAB_REG="$(docker exec multi-agent-crab-1 bash -lc "curl -sS -X POST $BASE_URL/v1/agents/register -H 'Content-Type: application/json' -d '{\"agent_id\":\"$CRAB_ID\"}'")"
CRAB_TOKEN="$(printf '%s' "$CRAB_REG" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')"
docker exec multi-agent-crab-1 bash -lc "umask 077; printf '%s\n' '$CRAB_TOKEN' > /tmp/${CRAB_ID}.token"

echo "[bootstrap] registering $SHRIMP_ID"
SHRIMP_REG="$(docker exec multi-agent-shrimp-1 bash -lc "curl -sS -X POST $BASE_URL/v1/agents/register -H 'Content-Type: application/json' -d '{\"agent_id\":\"$SHRIMP_ID\"}'")"
SHRIMP_TOKEN="$(printf '%s' "$SHRIMP_REG" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')"
docker exec multi-agent-shrimp-1 bash -lc "umask 077; printf '%s\n' '$SHRIMP_TOKEN' > /tmp/${SHRIMP_ID}.token"

echo "[bootstrap] applying mutual inbound allow rules"
docker exec multi-agent-crab-1 bash -lc "curl -sS -X POST $BASE_URL/v1/agents/$CRAB_ID/allow-inbound -H 'Authorization: Bearer $CRAB_TOKEN' -H 'Content-Type: application/json' -d '{\"from_agent_id\":\"$SHRIMP_ID\"}' >/tmp/${CRAB_ID}_allow.json"
docker exec multi-agent-shrimp-1 bash -lc "curl -sS -X POST $BASE_URL/v1/agents/$SHRIMP_ID/allow-inbound -H 'Authorization: Bearer $SHRIMP_TOKEN' -H 'Content-Type: application/json' -d '{\"from_agent_id\":\"$CRAB_ID\"}' >/tmp/${SHRIMP_ID}_allow.json"

echo "[bootstrap] running exchange smoke test"
MSG_A="bootstrap-ping-$(date +%s)"
MSG_B="bootstrap-pong-$(date +%s)"
EXCHANGE_JSON="$(docker exec multi-agent-crab-1 bash -lc "/mnt/skills/openclaw-exchange-messages/scripts/exchange_roundtrip.sh '$BASE_URL' '$CRAB_ID' '$CRAB_TOKEN' '$SHRIMP_ID' '$SHRIMP_TOKEN' '$MSG_A' '$MSG_B' '$PULL_TIMEOUT_MS'")"

python3 - <<PY
import json
print(json.dumps({
  "status": "ok",
  "base_url": "$BASE_URL",
  "crab_agent_id": "$CRAB_ID",
  "shrimp_agent_id": "$SHRIMP_ID",
  "crab_token_file": "/tmp/$CRAB_ID.token",
  "shrimp_token_file": "/tmp/$SHRIMP_ID.token",
  "exchange": json.loads('''$EXCHANGE_JSON'''),
}, indent=2))
PY
