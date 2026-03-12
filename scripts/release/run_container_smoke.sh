#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: $0 <image-ref> [host-port]" >&2
  exit 1
fi

IMAGE_REF="$1"
HOST_PORT="${2:-18080}"
BASE_URL="http://127.0.0.1:${HOST_PORT}"
CONTAINER_NAME="statocyst-smoke-${HOST_PORT}"

cleanup() {
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
}

wait_for_ping() {
  local attempts=0
  while true; do
    local code
    code="$(curl -sS -o /dev/null -w "%{http_code}" "${BASE_URL}/ping" || true)"
    if [[ "${code}" == "200" || "${code}" == "204" ]]; then
      return 0
    fi

    attempts=$((attempts + 1))
    if [[ "${attempts}" -ge 30 ]]; then
      echo "ERROR: smoke target did not become live at ${BASE_URL}/ping" >&2
      docker logs "${CONTAINER_NAME}" >&2 || true
      exit 1
    fi
    sleep 1
  done
}

wait_for_ready_health() {
  local attempts=0
  local body_file
  body_file="$(mktemp)"
  trap 'rm -f "${body_file}"; cleanup' EXIT

  while true; do
    local code
    code="$(curl -sS -o "${body_file}" -w "%{http_code}" "${BASE_URL}/health" || true)"
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
      echo "ERROR: smoke target did not become ready at ${BASE_URL}/health" >&2
      if [[ -s "${body_file}" ]]; then
        head -c 512 "${body_file}" >&2 || true
        echo >&2
      fi
      docker logs "${CONTAINER_NAME}" >&2 || true
      exit 1
    fi
    sleep 1
  done
}

trap cleanup EXIT
cleanup

docker run -d \
  --name "${CONTAINER_NAME}" \
  -p "127.0.0.1:${HOST_PORT}:8080" \
  -e HUMAN_AUTH_PROVIDER=dev \
  -e STATOCYST_CANONICAL_BASE_URL="${BASE_URL}" \
  "${IMAGE_REF}" >/dev/null

wait_for_ping
wait_for_ready_health

go run ./cmd/statocyst-smoke -base-url "${BASE_URL}"
