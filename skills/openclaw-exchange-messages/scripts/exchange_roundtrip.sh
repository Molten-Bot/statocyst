#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<USAGE
Usage:
  exchange_roundtrip.sh <base_url> <agent_a_id> <agent_a_token> <agent_b_id> <agent_b_token> <msg_a_to_b> <msg_b_to_a> [pull_timeout_ms]

Arguments:
  base_url        Example: http://localhost:8080
  agent_a_id      Sender/receiver A
  agent_a_token   Bearer token for agent A
  agent_b_id      Sender/receiver B
  agent_b_token   Bearer token for agent B
  msg_a_to_b      Payload expected by B
  msg_b_to_a      Payload expected by A
  pull_timeout_ms Optional pull timeout (default: 5000)
USAGE
}

if [[ $# -lt 7 || $# -gt 8 ]]; then
  usage >&2
  exit 1
fi

for cmd in curl node; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "ERROR: missing required command: $cmd" >&2
    exit 1
  fi
done

base_url="${1%/}"
agent_a_id="$2"
agent_a_token="$3"
agent_b_id="$4"
agent_b_token="$5"
msg_a_to_b="$6"
msg_b_to_a="$7"
pull_timeout_ms="${8:-5000}"

if ! [[ "$pull_timeout_ms" =~ ^[0-9]+$ ]]; then
  echo "ERROR: pull_timeout_ms must be an integer" >&2
  exit 1
fi

start_ms="$(date +%s%3N)"

publish_tmp="$(mktemp)"
pull_tmp="$(mktemp)"
trap 'rm -f "$publish_tmp" "$pull_tmp"' EXIT

publish_message() {
  local sender_token="$1"
  local to_agent_id="$2"
  local payload="$3"

  local payload_json
  payload_json="$(node -e '
console.log(JSON.stringify({
  to_agent_id: process.argv[1],
  content_type: "text/plain",
  payload: process.argv[2],
}));
' "$to_agent_id" "$payload")"

  local status
  status="$(curl -sS -o "$publish_tmp" -w "%{http_code}" \
    -X POST "$base_url/v1/messages/publish" \
    -H "Authorization: Bearer $sender_token" \
    -H "Content-Type: application/json" \
    --data "$payload_json")"

  if [[ "$status" != "202" ]]; then
    local excerpt
    excerpt="$(node -e 'const fs=require("fs"); const t=fs.readFileSync(process.argv[1],"utf8"); console.log(t.slice(0,300));' "$publish_tmp")"
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
    -X GET "$base_url/v1/messages/pull?timeout_ms=$pull_timeout_ms" \
    -H "Authorization: Bearer $receiver_token")"

  if [[ "$status" != "200" ]]; then
    local excerpt
    excerpt="$(node -e 'const fs=require("fs"); const t=fs.readFileSync(process.argv[1],"utf8"); console.log(t.slice(0,300));' "$pull_tmp")"
    echo "ERROR: pull failed (HTTP $status): $excerpt" >&2
    exit 1
  fi

  node -e '
const fs = require("fs");
const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const message = payload.message || {};
if (message.from_agent_id !== process.argv[2]) {
  console.error(`pull verification failed: expected from_agent_id=${JSON.stringify(process.argv[2])}, got ${JSON.stringify(message.from_agent_id)}`);
  process.exit(1);
}
if (message.payload !== process.argv[3]) {
  console.error(`pull verification failed: expected payload=${JSON.stringify(process.argv[3])}, got ${JSON.stringify(message.payload)}`);
  process.exit(1);
}
console.log(message.message_id || "");
' "$pull_tmp" "$expected_from" "$expected_payload"
}

msg_id_a_to_b="$(publish_message "$agent_a_token" "$agent_b_id" "$msg_a_to_b")"
pulled_a_to_b="$(pull_and_verify "$agent_b_token" "$agent_a_id" "$msg_a_to_b")"
msg_id_b_to_a="$(publish_message "$agent_b_token" "$agent_a_id" "$msg_b_to_a")"
pulled_b_to_a="$(pull_and_verify "$agent_a_token" "$agent_b_id" "$msg_b_to_a")"

end_ms="$(date +%s%3N)"

node -e '
const result = {
  status: "ok",
  a_to_b_publish_message_id: process.argv[1],
  a_to_b_pulled_message_id: process.argv[2],
  b_to_a_publish_message_id: process.argv[3],
  b_to_a_pulled_message_id: process.argv[4],
  elapsed_ms: Number(process.argv[6]) - Number(process.argv[5]),
};
console.log(JSON.stringify(result));
' "$msg_id_a_to_b" "$pulled_a_to_b" "$msg_id_b_to_a" "$pulled_b_to_a" "$start_ms" "$end_ms"
