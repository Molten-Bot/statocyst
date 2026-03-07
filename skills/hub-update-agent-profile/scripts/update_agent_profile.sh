#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<USAGE
Usage:
  update_agent_profile.sh <agent_token> <metadata_json_or_@file>
  update_agent_profile.sh <base_url> <agent_token> <metadata_json_or_@file>

Arguments:
  base_url                 Optional Hub/Statocyst base URL. Example: http://statocyst:8080
  agent_token              Agent bearer token
  metadata_json_or_@file   JSON object string or @path/to/metadata.json

Environment:
  STATOCYST_BASE_URL       Default base URL when omitted. Fallback: http://statocyst:8080
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

default_base_url="${STATOCYST_BASE_URL:-http://statocyst:8080}"
if [[ "$1" =~ ^https?:// ]]; then
  if [[ $# -ne 3 ]]; then
    usage >&2
    exit 1
  fi
  base_url="${1%/}"
  agent_token="$2"
  metadata_input="$3"
else
  base_url="${default_base_url%/}"
  agent_token="$1"
  metadata_input="$2"
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
  -X PATCH "$base_url/v1/agents/me/metadata" \
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
  base_url: process.argv[2],
  agent_uuid: String(agent.agent_uuid || ""),
  agent_id: String(agent.agent_id || ""),
  org_id: String(agent.org_id || ""),
  metadata: agent.metadata || {},
  agent,
}));
' "$tmp_response" "$base_url"
