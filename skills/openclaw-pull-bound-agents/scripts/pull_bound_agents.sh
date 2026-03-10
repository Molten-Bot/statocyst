#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<USAGE
Usage:
  pull_bound_agents.sh <agent_token>
  pull_bound_agents.sh <base_url> <agent_token>

Arguments:
  base_url    Optional Hub base URL or api_base. Example: https://hub.example or https://hub.example/v1
  agent_token Agent bearer token

Environment:
  HUB_API_BASE      Preferred canonical API base from bind/capabilities
  HUB_BASE_URL      Hub origin used when HUB_API_BASE is not set
  HUB_SESSION_FILE  Optional bind session JSON used to recover api_base when URL is omitted
USAGE
}

if [[ $# -lt 1 || $# -gt 2 ]]; then
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
  if [[ $# -ne 2 ]]; then
    usage >&2
    exit 1
  fi
  api_base="$(normalize_api_base "$1")"
  agent_token="$2"
else
  api_base="$(normalize_api_base "$default_api_input")"
  agent_token="$1"
fi

hub_base_url="$(derive_hub_base_url "$api_base")"

if [[ -z "$api_base" ]]; then
  echo "ERROR: base URL is required. Pass <base_url>, set HUB_API_BASE/HUB_BASE_URL, or provide HUB_SESSION_FILE." >&2
  exit 1
fi

tmp_caps="$(mktemp)"
trap 'rm -f "$tmp_caps"' EXIT

status="$(curl -sS -o "$tmp_caps" -w "%{http_code}" \
  -X GET "$api_base/agents/me/capabilities" \
  -H "Authorization: Bearer $agent_token")"

if [[ "$status" != "200" ]]; then
  node -e '
const fs = require("fs");
let code = "capabilities_failed";
let message = "failed to load bound agents from capabilities";
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
  message: message,
  http_status: Number(process.argv[2]),
}));
' "$tmp_caps" "$status"
  exit 1
fi

node -e '
const fs = require("fs");
const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const agent = payload && payload.agent ? payload.agent : {};
const cp = payload && payload.control_plane ? payload.control_plane : {};
const agentUUID = String(agent.agent_uuid || cp.agent_uuid || "");
const agentID = String(agent.agent_id || cp.agent_id || "");
const orgID = String(agent.org_id || cp.org_id || "");
if (!agentUUID) {
  console.log(JSON.stringify({
    status: "error",
    error: "invalid_response",
    message: "capabilities response missing agent_uuid",
  }));
  process.exit(2);
}
const bound = Array.isArray(cp.can_talk_to) ? cp.can_talk_to.map(String) : [];
console.log(JSON.stringify({
  status: "ok",
  hub_base_url: process.argv[2],
  api_base: process.argv[3],
  agent_uuid: agentUUID,
  agent_id: agentID,
  org_id: orgID,
  bound_agents: bound,
  bound_count: bound.length,
  can_communicate: bound.length > 0,
  endpoints: cp.endpoints || {},
}));
' "$tmp_caps" "$hub_base_url" "$api_base"
