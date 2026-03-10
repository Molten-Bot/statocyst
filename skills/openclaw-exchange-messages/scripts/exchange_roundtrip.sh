#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<USAGE
Usage:
  exchange_roundtrip.sh <agent_a_token> <agent_b_token> <msg_a_to_b> <msg_b_to_a> [pull_timeout_ms]
  exchange_roundtrip.sh <base_url> <agent_a_token> <agent_b_token> <msg_a_to_b> <msg_b_to_a> [pull_timeout_ms]
  exchange_roundtrip.sh <agent_a_uuid> <agent_a_token> <agent_b_uuid> <agent_b_token> <msg_a_to_b> <msg_b_to_a> [pull_timeout_ms]
  exchange_roundtrip.sh <base_url> <agent_a_uuid> <agent_a_token> <agent_b_uuid> <agent_b_token> <msg_a_to_b> <msg_b_to_a> [pull_timeout_ms]

Arguments:
  base_url        Example: https://hub.example or https://hub.example/v1
  agent_a_uuid    Optional explicit sender/receiver A UUID (long form only)
  agent_a_token   Bearer token for agent A
  agent_b_uuid    Optional explicit sender/receiver B UUID (long form only)
  agent_b_token   Bearer token for agent B
  msg_a_to_b      Payload expected by B
  msg_b_to_a      Payload expected by A
  pull_timeout_ms Optional pull timeout (default: 5000)

Environment:
  HUB_API_BASE      Preferred canonical API base from bind/capabilities
  HUB_BASE_URL      Hub origin used when HUB_API_BASE is not set
  HUB_SESSION_FILE  Optional bind session JSON used to recover api_base when URL is omitted
USAGE
}

if [[ $# -lt 4 || $# -gt 8 ]]; then
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
mode=""
if [[ "$1" =~ ^https?:// ]]; then
  api_base="$(normalize_api_base "$1")"
  if [[ $# -eq 5 || $# -eq 6 ]]; then
    mode="short"
    agent_a_uuid=""
    agent_a_token="$2"
    agent_b_uuid=""
    agent_b_token="$3"
    msg_a_to_b="$4"
    msg_b_to_a="$5"
    pull_timeout_ms="${6:-5000}"
  elif [[ $# -eq 7 || $# -eq 8 ]]; then
    mode="long"
    agent_a_uuid="$2"
    agent_a_token="$3"
    agent_b_uuid="$4"
    agent_b_token="$5"
    msg_a_to_b="$6"
    msg_b_to_a="$7"
    pull_timeout_ms="${8:-5000}"
  else
    usage >&2
    exit 1
  fi
elif [[ $# -eq 4 || $# -eq 5 ]]; then
  mode="short"
  api_base="$(normalize_api_base "$default_api_input")"
  agent_a_uuid=""
  agent_a_token="$1"
  agent_b_uuid=""
  agent_b_token="$2"
  msg_a_to_b="$3"
  msg_b_to_a="$4"
  pull_timeout_ms="${5:-5000}"
elif [[ $# -eq 6 || $# -eq 7 ]]; then
  mode="long"
  api_base="$(normalize_api_base "$default_api_input")"
  agent_a_uuid="$1"
  agent_a_token="$2"
  agent_b_uuid="$3"
  agent_b_token="$4"
  msg_a_to_b="$5"
  msg_b_to_a="$6"
  pull_timeout_ms="${7:-5000}"
else
  usage >&2
  exit 1
fi

hub_base_url="$(derive_hub_base_url "$api_base")"

if ! [[ "$pull_timeout_ms" =~ ^[0-9]+$ ]]; then
  echo "ERROR: pull_timeout_ms must be an integer" >&2
  exit 1
fi

if [[ -z "$api_base" ]]; then
  echo "ERROR: base URL is required. Pass <base_url>, set HUB_API_BASE/HUB_BASE_URL, or provide HUB_SESSION_FILE." >&2
  exit 1
fi

start_ms="$(date +%s%3N)"

publish_tmp="$(mktemp)"
pull_tmp="$(mktemp)"
caps_a_tmp="$(mktemp)"
caps_b_tmp="$(mktemp)"
trap 'rm -f "$publish_tmp" "$pull_tmp" "$caps_a_tmp" "$caps_b_tmp"' EXIT

error_excerpt() {
  local file="$1"
  node -e '
const fs = require("fs");
try {
  const p = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  if (p.error || p.message) {
    console.log(JSON.stringify({ error: p.error || null, message: p.message || null }));
    process.exit(0);
  }
} catch (_) {}
try {
  const t = fs.readFileSync(process.argv[1], "utf8");
  console.log(t.slice(0, 300));
} catch (_) {
  console.log("unknown error");
}
' "$file"
}

fetch_capabilities() {
  local token="$1"
  local out_file="$2"
  local status
  status="$(curl -sS -o "$out_file" -w "%{http_code}" \
    -X GET "$api_base/agents/me/capabilities" \
    -H "Authorization: Bearer $token")"
  if [[ "$status" != "200" ]]; then
    local excerpt
    excerpt="$(error_excerpt "$out_file")"
    echo "ERROR: capabilities lookup failed (HTTP $status): $excerpt" >&2
    exit 1
  fi
}

extract_agent_uuid() {
  local file="$1"
  node -e '
const fs = require("fs");
const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const fromAgent = payload && payload.agent ? payload.agent : {};
const fromCP = payload && payload.control_plane ? payload.control_plane : {};
const agentUUID = String(fromAgent.agent_uuid || fromCP.agent_uuid || "");
if (!agentUUID) {
  process.exit(2);
}
console.log(agentUUID);
' "$file"
}

extract_peer_list() {
  local file="$1"
  node -e '
const fs = require("fs");
const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const cp = payload && payload.control_plane ? payload.control_plane : {};
const peers = Array.isArray(cp.can_talk_to) ? cp.can_talk_to.map(String) : [];
for (const peer of peers) {
  console.log(peer);
}
' "$file"
}

publish_message() {
  local sender_token="$1"
  local to_agent_uuid="$2"
  local payload="$3"

  local payload_json
  payload_json="$(node -e '
console.log(JSON.stringify({
  to_agent_uuid: process.argv[1],
  content_type: "text/plain",
  payload: process.argv[2],
}));
' "$to_agent_uuid" "$payload")"

  local status
  status="$(curl -sS -o "$publish_tmp" -w "%{http_code}" \
    -X POST "$api_base/messages/publish" \
    -H "Authorization: Bearer $sender_token" \
    -H "Content-Type: application/json" \
    --data "$payload_json")"

  if [[ "$status" != "202" ]]; then
    local excerpt
    excerpt="$(error_excerpt "$publish_tmp")"
    echo "ERROR: publish failed (HTTP $status): $excerpt" >&2
    exit 1
  fi

  node -e '
const fs = require("fs");
const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
if (payload.status === "dropped") {
  console.error(`publish dropped: reason=${JSON.stringify(payload.reason || "unknown")}`);
  process.exit(2);
}
if (payload.status !== "queued" || !payload.message_id) {
  console.error(`unexpected publish response: ${JSON.stringify(payload)}`);
  process.exit(1);
}
console.log(payload.message_id);
' "$publish_tmp"
}

pull_and_verify() {
  local receiver_token="$1"
  local expected_from="$2"
  local expected_payload="$3"

  local status
  status="$(curl -sS -o "$pull_tmp" -w "%{http_code}" \
    -X GET "$api_base/messages/pull?timeout_ms=$pull_timeout_ms" \
    -H "Authorization: Bearer $receiver_token")"

  if [[ "$status" != "200" ]]; then
    local excerpt
    excerpt="$(error_excerpt "$pull_tmp")"
    echo "ERROR: pull failed (HTTP $status): $excerpt" >&2
    exit 1
  fi

  node -e '
const fs = require("fs");
const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const message = payload.message || {};
if (message.from_agent_uuid !== process.argv[2]) {
  console.error(`pull verification failed: expected from_agent_uuid=${JSON.stringify(process.argv[2])}, got ${JSON.stringify(message.from_agent_uuid)}`);
  process.exit(1);
}
if (message.payload !== process.argv[3]) {
  console.error(`pull verification failed: expected payload=${JSON.stringify(process.argv[3])}, got ${JSON.stringify(message.payload)}`);
  process.exit(1);
}
console.log(message.message_id || "");
' "$pull_tmp" "$expected_from" "$expected_payload"
}

fetch_capabilities "$agent_a_token" "$caps_a_tmp"
fetch_capabilities "$agent_b_token" "$caps_b_tmp"

discovered_agent_a_uuid="$(extract_agent_uuid "$caps_a_tmp")" || {
  echo "ERROR: capabilities response for agent A is missing agent_uuid" >&2
  exit 1
}
discovered_agent_b_uuid="$(extract_agent_uuid "$caps_b_tmp")" || {
  echo "ERROR: capabilities response for agent B is missing agent_uuid" >&2
  exit 1
}

if [[ "$mode" == "long" ]]; then
  if [[ "$agent_a_uuid" != "$discovered_agent_a_uuid" ]]; then
    echo "ERROR: provided agent_a_uuid does not match token identity ($agent_a_uuid != $discovered_agent_a_uuid)" >&2
    exit 1
  fi
  if [[ "$agent_b_uuid" != "$discovered_agent_b_uuid" ]]; then
    echo "ERROR: provided agent_b_uuid does not match token identity ($agent_b_uuid != $discovered_agent_b_uuid)" >&2
    exit 1
  fi
else
  agent_a_uuid="$discovered_agent_a_uuid"
  agent_b_uuid="$discovered_agent_b_uuid"
fi

agent_a_bound_agents="$(extract_peer_list "$caps_a_tmp")"
agent_b_bound_agents="$(extract_peer_list "$caps_b_tmp")"

if ! grep -Fxq "$agent_b_uuid" <<<"$agent_a_bound_agents"; then
  echo "ERROR: agent A is not currently bound to agent B (A cannot talk to B)" >&2
  exit 1
fi
if ! grep -Fxq "$agent_a_uuid" <<<"$agent_b_bound_agents"; then
  echo "ERROR: agent B is not currently bound to agent A (B cannot talk to A)" >&2
  exit 1
fi

a_bound_json="$(node -e '
const peers = String(process.argv[1] || "")
  .split(/\n+/)
  .map((v) => v.trim())
  .filter(Boolean);
console.log(JSON.stringify(peers));
' "$agent_a_bound_agents")"

b_bound_json="$(node -e '
const peers = String(process.argv[1] || "")
  .split(/\n+/)
  .map((v) => v.trim())
  .filter(Boolean);
console.log(JSON.stringify(peers));
' "$agent_b_bound_agents")"

msg_id_a_to_b="$(publish_message "$agent_a_token" "$agent_b_uuid" "$msg_a_to_b")"
pulled_a_to_b="$(pull_and_verify "$agent_b_token" "$agent_a_uuid" "$msg_a_to_b")"
msg_id_b_to_a="$(publish_message "$agent_b_token" "$agent_a_uuid" "$msg_b_to_a")"
pulled_b_to_a="$(pull_and_verify "$agent_a_token" "$agent_b_uuid" "$msg_b_to_a")"

end_ms="$(date +%s%3N)"

node -e '
const result = {
  status: "ok",
  mode: process.argv[1],
  hub_base_url: process.argv[2],
  api_base: process.argv[3],
  agent_a_uuid: process.argv[4],
  agent_b_uuid: process.argv[5],
  agent_a_bound_agents: JSON.parse(process.argv[6]),
  agent_b_bound_agents: JSON.parse(process.argv[7]),
  bound_peer_check: "passed",
  a_to_b_publish_message_id: process.argv[8],
  a_to_b_pulled_message_id: process.argv[9],
  b_to_a_publish_message_id: process.argv[10],
  b_to_a_pulled_message_id: process.argv[11],
  elapsed_ms: Number(process.argv[13]) - Number(process.argv[12]),
};
console.log(JSON.stringify(result));
' "$mode" "$hub_base_url" "$api_base" "$agent_a_uuid" "$agent_b_uuid" "$a_bound_json" "$b_bound_json" "$msg_id_a_to_b" "$pulled_a_to_b" "$msg_id_b_to_a" "$pulled_b_to_a" "$start_ms" "$end_ms"
