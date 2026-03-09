#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<USAGE
Usage:
  trigger_deploy_hook.sh <deploy_hook_url> <environment> <image_ref> <git_sha>

Environment variables:
  DEPLOY_HOOK_BEARER_TOKEN   Optional bearer token for hook auth.
  STATOCYST_CANONICAL_BASE_URL   Optional canonical authority passed to the deploy hook.
USAGE
}

if [[ $# -ne 4 ]]; then
  usage >&2
  exit 1
fi

hook_url="$1"
environment="$2"
image_ref="$3"
git_sha="$4"

if [[ -z "$hook_url" ]]; then
  echo "ERROR: deploy_hook_url is required" >&2
  exit 1
fi

payload="$(cat <<JSON
{
  "service": "statocyst",
  "environment": "${environment}",
  "image_ref": "${image_ref}",
  "git_sha": "${git_sha}"$(if [[ -n "${STATOCYST_CANONICAL_BASE_URL:-}" ]]; then printf ',\n  "canonical_base_url": "%s"' "${STATOCYST_CANONICAL_BASE_URL}"; fi)
}
JSON
)"

response_tmp="$(mktemp)"
trap 'rm -f "$response_tmp"' EXIT

curl_args=(
  -sS
  -o "$response_tmp"
  -w "%{http_code}"
  -X POST "$hook_url"
  -H "Content-Type: application/json"
  --data "$payload"
)

if [[ -n "${DEPLOY_HOOK_BEARER_TOKEN:-}" ]]; then
  curl_args+=(-H "Authorization: Bearer ${DEPLOY_HOOK_BEARER_TOKEN}")
fi

status="$(curl "${curl_args[@]}")"
if [[ ! "$status" =~ ^2[0-9][0-9]$ ]]; then
  echo "ERROR: deploy hook failed with HTTP ${status}" >&2
  head -c 500 "$response_tmp" >&2 || true
  echo >&2
  exit 1
fi

echo "deploy hook triggered for ${environment} (HTTP ${status})"
head -c 500 "$response_tmp" || true
echo
