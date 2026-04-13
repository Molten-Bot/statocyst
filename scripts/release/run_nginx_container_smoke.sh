#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: $0 <image-ref> [host-port]" >&2
  exit 1
fi

IMAGE_REF="$1"
HOST_PORT="${2:-18082}"
BASE_URL="http://127.0.0.1:${HOST_PORT}"
COMPOSE_FILE="scripts/release/docker-compose.nginx-smoke.yml"
PROJECT_NAME="moltenhub-nginx-smoke-${HOST_PORT}"
REPO_ROOT="$(pwd)"

cleanup() {
  MOLTENHUB_IMAGE="${IMAGE_REF}" \
  MOLTENHUB_NGINX_PORT="${HOST_PORT}" \
  MOLTENHUB_REPO_ROOT="${REPO_ROOT}" \
  docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" down -v >/dev/null 2>&1 || true
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
      echo "ERROR: nginx smoke target did not become live at ${BASE_URL}/ping" >&2
      MOLTENHUB_IMAGE="${IMAGE_REF}" \
      MOLTENHUB_NGINX_PORT="${HOST_PORT}" \
      MOLTENHUB_REPO_ROOT="${REPO_ROOT}" \
      docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" logs >&2 || true
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
      echo "ERROR: nginx smoke target did not become ready at ${BASE_URL}/health" >&2
      if [[ -s "${body_file}" ]]; then
        head -c 512 "${body_file}" >&2 || true
        echo >&2
      fi
      MOLTENHUB_IMAGE="${IMAGE_REF}" \
      MOLTENHUB_NGINX_PORT="${HOST_PORT}" \
      MOLTENHUB_REPO_ROOT="${REPO_ROOT}" \
      docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" logs >&2 || true
      exit 1
    fi
    sleep 1
  done
}

verify_rate_limit() {
  local attempts=0
  local body_file
  local header_file
  body_file="$(mktemp)"
  header_file="$(mktemp)"
  trap 'rm -f "${body_file}" "${header_file}"; cleanup' EXIT

  while [[ "${attempts}" -lt 650 ]]; do
    local code
    code="$(curl -sS -D "${header_file}" -o "${body_file}" -w "%{http_code}" "${BASE_URL}/health" || true)"
    if [[ "${code}" == "429" ]]; then
      if ! grep -q "Retry-After:" "${header_file}"; then
        echo "ERROR: rate-limited response from ${BASE_URL}/health was missing Retry-After" >&2
        cat "${header_file}" >&2
        cat "${body_file}" >&2
        exit 1
      fi
      if ! grep -q 'rate_limited' "${body_file}"; then
        echo "ERROR: rate-limited response from ${BASE_URL}/health did not include canonical error details" >&2
        cat "${header_file}" >&2
        cat "${body_file}" >&2
        exit 1
      fi
      rm -f "${body_file}" "${header_file}"
      trap cleanup EXIT
      return 0
    fi
    attempts=$((attempts + 1))
  done

  echo "ERROR: expected rate limiting at ${BASE_URL}/health but never observed a 429 response" >&2
  MOLTENHUB_IMAGE="${IMAGE_REF}" \
  MOLTENHUB_NGINX_PORT="${HOST_PORT}" \
  MOLTENHUB_REPO_ROOT="${REPO_ROOT}" \
  docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" logs >&2 || true
  exit 1
}

trap cleanup EXIT
cleanup

MOLTENHUB_IMAGE="${IMAGE_REF}" \
MOLTENHUB_NGINX_PORT="${HOST_PORT}" \
MOLTENHUB_REPO_ROOT="${REPO_ROOT}" \
docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" up -d >/dev/null

wait_for_ping
wait_for_ready_health

go run ./cmd/moltenhub-smoke -base-url "${BASE_URL}"
verify_rate_limit
