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

trap cleanup EXIT
cleanup

docker run -d \
  --name "${CONTAINER_NAME}" \
  -p "127.0.0.1:${HOST_PORT}:8080" \
  -e HUMAN_AUTH_PROVIDER=dev \
  -e STATOCYST_CANONICAL_BASE_URL="${BASE_URL}" \
  "${IMAGE_REF}" >/dev/null

attempts=0
until curl -fsS "${BASE_URL}/health" >/dev/null 2>&1; do
  attempts=$((attempts + 1))
  if [[ "${attempts}" -ge 30 ]]; then
    echo "ERROR: smoke target did not become healthy at ${BASE_URL}" >&2
    docker logs "${CONTAINER_NAME}" >&2 || true
    exit 1
  fi
  sleep 1
done

go run ./cmd/statocyst-smoke -base-url "${BASE_URL}"
