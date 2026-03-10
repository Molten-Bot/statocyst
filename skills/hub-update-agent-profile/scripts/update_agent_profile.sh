#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<USAGE
Usage:
  update_agent_profile.sh <agent_token> <metadata_json_or_@file>
  update_agent_profile.sh <base_url> <agent_token> <metadata_json_or_@file>

Arguments:
  base_url                 Optional Hub base URL or api_base. Example: https://hub.example or https://hub.example/v1
  agent_token              Agent bearer token
  metadata_json_or_@file   JSON object string or @path/to/metadata.json

Environment:
  HUB_API_BASE      Preferred canonical API base from bind/capabilities
  HUB_BASE_URL      Hub origin used when HUB_API_BASE is not set
  HUB_SESSION_FILE  Optional bind session JSON used to recover api_base when URL is omitted
USAGE
}

if [[ $# -lt 2 || $# -gt 3 ]]; then
  usage >&2
  exit 1
fi

for cmd in curl node; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "ERROR: missing required command: $cmd" >&2
    exit 1
  fi
done

read_session_api_base() {
  local session_file="${HUB_SESSION_FILE:-}"
  if [[ -z "$session_file" || ! -f "$session_file" ]]; then
    return 0
  fi
  node -e '
const fs = require("fs");
try {
  const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  const value = String(payload.api_base || "");
  if (value) process.stdout.write(value);
} catch (_) {}
' "$session_file"
}

normalize_api_base() {
  local value="${1%/}"
  if [[ -z "$value" ]]; then
    printf '%s' ""
    return 0
  fi
  if [[ "$value" == */v1 ]]; then
    printf '%s' "$value"
    return 0
  fi
  printf '%s/v1' "$value"
}

derive_hub_base_url() {
  local value="${1%/}"
  if [[ "$value" == */v1 ]]; then
    printf '%s' "${value%/v1}"
    return 0
  fi
  printf '%s' "$value"
}

session_api_base="$(read_session_api_base)"
default_api_input="${HUB_API_BASE:-${HUB_BASE_URL:-$session_api_base}}"

if [[ "$1" =~ ^https?:// ]]; then
  if [[ $# -ne 3 ]]; then
    usage >&2
    exit 1
  fi
  api_base="$(normalize_api_base "$1")"
  agent_token="$2"
  metadata_input="$3"
else
  api_base="$(normalize_api_base "$default_api_input")"
  agent_token="$1"
  metadata_input="$2"
fi

hub_base_url="$(derive_hub_base_url "$api_base")"

if [[ -z "$api_base" ]]; then
  echo "ERROR: base URL is required. Pass <base_url>, set HUB_API_BASE/HUB_BASE_URL, or provide HUB_SESSION_FILE." >&2
  exit 1
fi

metadata_err="$(mktemp)"
tmp_response="$(mktemp)"
trap 'rm -f "$metadata_err" "$tmp_response"' EXIT

resolve_metadata_json() {
  local input="$1"
  node -e '
const fs = require("fs");

const MAX_METADATA_BYTES = 65536;
const input = String(process.argv[1] || "");
let raw = input;

if (input.startsWith("@")) {
  const path = input.slice(1);
  if (!path) {
    console.error("metadata file path is empty");
    process.exit(2);
  }
  try {
    raw = fs.readFileSync(path, "utf8");
  } catch (_) {
    console.error(`metadata file not found: ${path}`);
    process.exit(2);
  }
}

let parsed;
try {
  parsed = JSON.parse(raw);
} catch (_) {
  console.error("metadata must be valid JSON");
  process.exit(2);
}

if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
  console.error("metadata must be a JSON object");
  process.exit(2);
}

const compact = JSON.stringify(parsed);
if (Buffer.byteLength(compact, "utf8") > MAX_METADATA_BYTES) {
  console.error(`metadata exceeds ${MAX_METADATA_BYTES} bytes`);
  process.exit(2);
}

process.stdout.write(compact);
' "$input"
}

if ! metadata_json="$(resolve_metadata_json "$metadata_input" 2>"$metadata_err")"; then
  parse_message="$(tr '\n' ' ' <"$metadata_err" | sed 's/[[:space:]]\+/ /g' | sed 's/^ //;s/ $//')"
  if [[ -z "$parse_message" ]]; then
    parse_message="invalid metadata input"
  fi
  node -e '
console.log(JSON.stringify({
  status: "error",
  error: "invalid_request",
  message: process.argv[1] || "invalid metadata input",
}));
' "$parse_message"
  exit 1
fi

payload="$(node -e '
const metadata = JSON.parse(process.argv[1]);
console.log(JSON.stringify({ metadata }));
' "$metadata_json")"

http_status="$(curl -sS -o "$tmp_response" -w "%{http_code}" \
  -X PATCH "$api_base/agents/me/metadata" \
  -H "Authorization: Bearer $agent_token" \
  -H "Content-Type: application/json" \
  --data "$payload")"

if [[ "$http_status" != "200" ]]; then
  node -e '
const fs = require("fs");
let code = "update_failed";
let message = "failed to update agent metadata";
try {
  const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  if (payload && payload.error) code = String(payload.error);
  if (payload && payload.message) message = String(payload.message);
} catch (_) {
  try {
    const text = fs.readFileSync(process.argv[1], "utf8");
    message = text.slice(0, 300);
  } catch (_) {}
}
console.log(JSON.stringify({
  status: "error",
  error: code,
  message,
  http_status: Number(process.argv[2]),
}));
' "$tmp_response" "$http_status"
  exit 1
fi

node -e '
const fs = require("fs");
const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const agent = payload && payload.agent ? payload.agent : {};
if (!agent.agent_uuid) {
  console.log(JSON.stringify({
    status: "error",
    error: "invalid_response",
    message: "response missing agent.agent_uuid",
  }));
  process.exit(2);
}
console.log(JSON.stringify({
  status: "ok",
  hub_base_url: process.argv[2],
  api_base: process.argv[3],
  agent_uuid: String(agent.agent_uuid || ""),
  agent_id: String(agent.agent_id || ""),
  org_id: String(agent.org_id || ""),
  metadata: agent.metadata || {},
  agent,
}));
' "$tmp_response" "$hub_base_url" "$api_base"
