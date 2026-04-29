#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: $0 <image-ref> [host-port]" >&2
  exit 1
fi

IMAGE_REF="$1"
HOST_PORT="${2:-18081}"
BASE_URL="http://127.0.0.1:${HOST_PORT}"
NETWORK_NAME="moltenhub-s3-smoke-${HOST_PORT}"
MINIO_CONTAINER="moltenhub-s3-smoke-minio-${HOST_PORT}"
HUB_CONTAINER="moltenhub-s3-smoke-hub-${HOST_PORT}"
MINIO_IMAGE="${MINIO_IMAGE:-quay.io/minio/minio:latest}"
MC_IMAGE="${MC_IMAGE:-quay.io/minio/mc:latest}"
MINIO_ROOT_USER="${MINIO_ROOT_USER:-minioadmin}"
MINIO_ROOT_PASSWORD="${MINIO_ROOT_PASSWORD:-}"
if [[ -z "${MINIO_ROOT_PASSWORD}" ]]; then
  MINIO_ROOT_PASSWORD="$(python3 - <<'PY'
import secrets
import string

alphabet = string.ascii_letters + string.digits
print("".join(secrets.choice(alphabet) for _ in range(32)))
PY
)"
fi
STATE_BUCKET="${MOLTENHUB_STATE_S3_BUCKET:-moltenhub-state-smoke}"
QUEUE_BUCKET="${MOLTENHUB_QUEUE_S3_BUCKET:-moltenhub-queue-smoke}"

cleanup() {
  docker rm -f "${HUB_CONTAINER}" >/dev/null 2>&1 || true
  docker rm -f "${MINIO_CONTAINER}" >/dev/null 2>&1 || true
  docker network rm "${NETWORK_NAME}" >/dev/null 2>&1 || true
}

wait_for_minio() {
  local attempts=0
  while true; do
    if docker run --rm --network "${NETWORK_NAME}" \
      -e "MC_HOST_smoke=http://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@${MINIO_CONTAINER}:9000" \
      "${MC_IMAGE}" ls smoke >/dev/null 2>&1; then
      return 0
    fi
    attempts=$((attempts + 1))
    if [[ "${attempts}" -ge 60 ]]; then
      echo "ERROR: MinIO did not become ready" >&2
      docker logs "${MINIO_CONTAINER}" >&2 || true
      exit 1
    fi
    sleep 1
  done
}

create_buckets() {
  docker run --rm --network "${NETWORK_NAME}" \
    -e "MC_HOST_smoke=http://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@${MINIO_CONTAINER}:9000" \
    "${MC_IMAGE}" mb --ignore-existing "smoke/${STATE_BUCKET}" "smoke/${QUEUE_BUCKET}" >/dev/null
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
      echo "ERROR: S3 smoke target did not become live at ${BASE_URL}/ping" >&2
      docker logs "${HUB_CONTAINER}" >&2 || true
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

storage = payload.get("storage", {})
if storage.get("state", {}).get("backend") != "s3":
    raise SystemExit(1)
if storage.get("queue", {}).get("backend") != "s3":
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
      echo "ERROR: S3 smoke target did not become ready at ${BASE_URL}/health" >&2
      if [[ -s "${body_file}" ]]; then
        head -c 512 "${body_file}" >&2 || true
        echo >&2
      fi
      docker logs "${HUB_CONTAINER}" >&2 || true
      exit 1
    fi
    sleep 1
  done
}

trap cleanup EXIT
cleanup
docker network create "${NETWORK_NAME}" >/dev/null

docker run -d \
  --name "${MINIO_CONTAINER}" \
  --network "${NETWORK_NAME}" \
  -e "MINIO_ROOT_USER=${MINIO_ROOT_USER}" \
  -e "MINIO_ROOT_PASSWORD=${MINIO_ROOT_PASSWORD}" \
  "${MINIO_IMAGE}" server /data --address ":9000" >/dev/null

wait_for_minio
create_buckets

docker run -d \
  --name "${HUB_CONTAINER}" \
  --network "${NETWORK_NAME}" \
  -p "127.0.0.1:${HOST_PORT}:8080" \
  -e HUMAN_AUTH_PROVIDER=dev \
  -e MOLTENHUB_CANONICAL_BASE_URL="${BASE_URL}" \
  -e MOLTENHUB_STATE_BACKEND=s3 \
  -e MOLTENHUB_QUEUE_BACKEND=s3 \
  -e MOLTENHUB_STATE_S3_ENDPOINT="http://${MINIO_CONTAINER}:9000" \
  -e MOLTENHUB_STATE_S3_BUCKET="${STATE_BUCKET}" \
  -e MOLTENHUB_STATE_S3_ACCESS_KEY_ID="${MINIO_ROOT_USER}" \
  -e MOLTENHUB_STATE_S3_SECRET_ACCESS_KEY="${MINIO_ROOT_PASSWORD}" \
  -e MOLTENHUB_QUEUE_S3_ENDPOINT="http://${MINIO_CONTAINER}:9000" \
  -e MOLTENHUB_QUEUE_S3_BUCKET="${QUEUE_BUCKET}" \
  -e MOLTENHUB_QUEUE_S3_ACCESS_KEY_ID="${MINIO_ROOT_USER}" \
  -e MOLTENHUB_QUEUE_S3_SECRET_ACCESS_KEY="${MINIO_ROOT_PASSWORD}" \
  "${IMAGE_REF}" >/dev/null

wait_for_ping
wait_for_ready_health

go run ./cmd/moltenhub-smoke -base-url "${BASE_URL}"
