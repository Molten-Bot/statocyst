#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 3 ]]; then
  echo "usage: $0 <image-ref> [alpha-host-port] [beta-host-port]" >&2
  exit 1
fi

IMAGE_REF="$1"
ALPHA_PORT="${2:-18080}"
BETA_PORT="${3:-18081}"
ALPHA_BASE_URL="http://127.0.0.1:${ALPHA_PORT}"
BETA_BASE_URL="http://127.0.0.1:${BETA_PORT}"
COMPOSE_FILE="scripts/release/docker-compose.federation-smoke.yml"
PROJECT_NAME="statocyst-federation-smoke"

cleanup() {
  STATOCYST_IMAGE="${IMAGE_REF}" \
  STATOCYST_ALPHA_PORT="${ALPHA_PORT}" \
  STATOCYST_BETA_PORT="${BETA_PORT}" \
  docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" down -v >/dev/null 2>&1 || true
}

trap cleanup EXIT
cleanup

STATOCYST_IMAGE="${IMAGE_REF}" \
STATOCYST_ALPHA_PORT="${ALPHA_PORT}" \
STATOCYST_BETA_PORT="${BETA_PORT}" \
docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" up -d >/dev/null

wait_for_ping() {
  local base_url="$1"
  local label="$2"
  local attempts=0
  while true; do
    local code
    code="$(curl -sS -o /dev/null -w "%{http_code}" "${base_url}/ping" || true)"
    if [[ "${code}" == "200" || "${code}" == "204" ]]; then
      return 0
    fi

    attempts=$((attempts + 1))
    if [[ "${attempts}" -ge 30 ]]; then
      echo "ERROR: ${label} did not become live at ${base_url}/ping" >&2
      STATOCYST_IMAGE="${IMAGE_REF}" \
      STATOCYST_ALPHA_PORT="${ALPHA_PORT}" \
      STATOCYST_BETA_PORT="${BETA_PORT}" \
      docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" logs >&2 || true
      exit 1
    fi
    sleep 1
  done
}

wait_for_ready_health() {
  local base_url="$1"
  local label="$2"
  local attempts=0
  local body_file
  body_file="$(mktemp)"
  trap 'rm -f "${body_file}"; cleanup' EXIT

  while true; do
    local code
    code="$(curl -sS -o "${body_file}" -w "%{http_code}" "${base_url}/health" || true)"
    if [[ "${code}" == "200" ]]; then
      if python3 - "${body_file}" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    payload = json.load(handle)

if str(payload.get("boot_status", "")).strip().lower() == "starting":
    raise SystemExit(1)
if str(payload.get("status", "")).strip().lower() != "ok":
    raise SystemExit(1)
PY
      then
        rm -f "${body_file}"
        trap cleanup EXIT
        return 0
      fi
    fi

    attempts=$((attempts + 1))
    if [[ "${attempts}" -ge 30 ]]; then
      echo "ERROR: ${label} did not become ready at ${base_url}/health" >&2
      if [[ -s "${body_file}" ]]; then
        head -c 512 "${body_file}" >&2 || true
        echo >&2
      fi
      STATOCYST_IMAGE="${IMAGE_REF}" \
      STATOCYST_ALPHA_PORT="${ALPHA_PORT}" \
      STATOCYST_BETA_PORT="${BETA_PORT}" \
      docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" logs >&2 || true
      exit 1
    fi
    sleep 1
  done
}

wait_for_ping "${ALPHA_BASE_URL}" "alpha"
wait_for_ping "${BETA_BASE_URL}" "beta"
wait_for_ready_health "${ALPHA_BASE_URL}" "alpha"
wait_for_ready_health "${BETA_BASE_URL}" "beta"

go run ./cmd/statocyst-federation-smoke \
  -alpha-base-url "${ALPHA_BASE_URL}" \
  -beta-base-url "${BETA_BASE_URL}"
