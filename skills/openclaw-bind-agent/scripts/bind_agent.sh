#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<USAGE
Usage:
  bind_agent.sh <bind_token> [token_output_file]
  bind_agent.sh <base_url> <bind_token> [token_output_file]

Arguments:
  bind_token        One-time bind token for agent bootstrap
  token_output_file Optional path to write token. Default: /tmp/agent.token. Use '-' to return token in JSON output.

Environment:
  HUB_API_BASE      Preferred canonical API base from bind/capabilities. Example: https://hub.example/v1
  HUB_BASE_URL      Hub origin used when HUB_API_BASE is not set. Example: https://hub.example
  HUB_SESSION_FILE  Optional JSON session file from a previous bind; used to recover api_base when no explicit URL is passed
USAGE
}

if [[ $# -lt 1 || $# -gt 3 ]]; then
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
agent_id=""

if [[ "$1" =~ ^https?:// ]]; then
  api_base="$(normalize_api_base "$1")"
  hub_base_url="$(derive_hub_base_url "$api_base")"
  if [[ $# -eq 2 ]]; then
    bind_token="$2"
    token_output_file="/tmp/agent.token"
  elif [[ $# -eq 3 ]]; then
    bind_token="$2"
    token_output_file="$3"
  else
    usage >&2
    exit 1
  fi
else
  if [[ $# -eq 1 ]]; then
    api_base="$(normalize_api_base "$default_api_input")"
    hub_base_url="$(derive_hub_base_url "$api_base")"
    bind_token="$1"
    token_output_file="/tmp/agent.token"
  elif [[ $# -eq 2 ]]; then
    if [[ "$2" == "-" || "$2" == /* || "$2" == .* ]]; then
      api_base="$(normalize_api_base "$default_api_input")"
      hub_base_url="$(derive_hub_base_url "$api_base")"
      bind_token="$1"
      token_output_file="$2"
    else
      usage >&2
      exit 1
    fi
  else
    usage >&2
    exit 1
  fi
fi

if [[ -z "$api_base" || -z "$hub_base_url" ]]; then
  echo "ERROR: canonical Hub API base is required. Pass <base_url>, set HUB_API_BASE/HUB_BASE_URL, or provide HUB_SESSION_FILE." >&2
  exit 1
fi

redeem_tmp="$(mktemp)"
capabilities_tmp="$(mktemp)"
session_write_err="$(mktemp)"
trap 'rm -f "$redeem_tmp" "$capabilities_tmp" "$session_write_err"' EXIT

fail_json() {
  local code="$1"
  local message="$2"
  local http_status="${3:-}"
  node -e '
const payload = {
  status: "error",
  error: process.argv[1],
  message: process.argv[2],
};
if (process.argv[3]) payload.http_status = Number(process.argv[3]);
console.log(JSON.stringify(payload));
' "$code" "$message" "$http_status"
  exit 1
}

parse_error_field() {
  local file="$1"
  local field="$2"
  node -e '
const fs = require("fs");
try {
  const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  if (payload && payload[process.argv[2]] != null) {
    console.log(String(payload[process.argv[2]]));
    process.exit(0);
  }
} catch (_) {}
if (process.argv[2] === "message") {
  try {
    const text = fs.readFileSync(process.argv[1], "utf8");
    console.log(text.slice(0, 300));
    process.exit(0);
  } catch (_) {}
}
console.log("");
' "$file" "$field"
}

redeem_payload="$(node -e '
console.log(JSON.stringify({
  hub_url: process.argv[2],
  bind_token: process.argv[1],
}));
' "$bind_token" "$hub_base_url")"

redeem_status="$(curl -sS -o "$redeem_tmp" -w "%{http_code}" \
  -X POST "$api_base/agents/bind" \
  -H "Content-Type: application/json" \
  --data "$redeem_payload")"

if [[ "$redeem_status" != "201" ]]; then
  error_code="$(parse_error_field "$redeem_tmp" "error")"
  if [[ -z "$error_code" ]]; then
    error_code="redeem_failed"
  fi
  error_message="$(parse_error_field "$redeem_tmp" "message")"
  if [[ -z "$error_message" ]]; then
    error_message="bind redeem failed"
  fi
  fail_json "$error_code" "$error_message" "$redeem_status"
fi

token="$(node -e '
const fs = require("fs");
const p = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
if (!p.token) {
  process.exit(2);
}
console.log(p.token);
' "$redeem_tmp")" || fail_json "invalid_response" "redeem response missing token" "$redeem_status"

org_id="$(node -e '
const fs = require("fs");
const p = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const agent = p && p.agent ? p.agent : {};
console.log(String(agent.org_id || p.org_id || ""));
' "$redeem_tmp")"

bound_api_base="$(node -e '
const fs = require("fs");
const p = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
console.log(String(p.api_base || ""));
' "$redeem_tmp")"
if [[ -n "$bound_api_base" ]]; then
  api_base="$(normalize_api_base "$bound_api_base")"
  hub_base_url="$(derive_hub_base_url "$api_base")"
fi

endpoints_json="$(node -e '
const fs = require("fs");
const p = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const endpoints = p && p.endpoints && typeof p.endpoints === "object" ? p.endpoints : {};
process.stdout.write(JSON.stringify(endpoints));
' "$redeem_tmp")"

cap_status="$(curl -sS -o "$capabilities_tmp" -w "%{http_code}" \
  -X GET "$api_base/agents/me/capabilities" \
  -H "Authorization: Bearer $token")"
if [[ "$cap_status" != "200" ]]; then
  cap_code="$(parse_error_field "$capabilities_tmp" "error")"
  if [[ -z "$cap_code" ]]; then
    cap_code="capabilities_failed"
  fi
  cap_message="$(parse_error_field "$capabilities_tmp" "message")"
  if [[ -z "$cap_message" ]]; then
    cap_message="failed to fetch agent capabilities"
  fi
  fail_json "$cap_code" "$cap_message" "$cap_status"
fi

agent_uuid="$(node -e '
const fs = require("fs");
const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const fromAgent = payload && payload.agent ? payload.agent : {};
const fromCP = payload && payload.control_plane ? payload.control_plane : {};
const agentUUID = String(fromAgent.agent_uuid || fromCP.agent_uuid || "");
if (!agentUUID) {
  process.exit(2);
}
console.log(agentUUID);
' "$capabilities_tmp")" || fail_json "invalid_response" "capabilities response missing agent_uuid" "$cap_status"

discovered_agent_id="$(node -e '
const fs = require("fs");
const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const fromAgent = payload && payload.agent ? payload.agent : {};
const fromCP = payload && payload.control_plane ? payload.control_plane : {};
console.log(String(fromAgent.agent_id || fromCP.agent_id || ""));
' "$capabilities_tmp")"
if [[ -n "$discovered_agent_id" ]]; then
  agent_id="$discovered_agent_id"
fi

bound_agents_json="$(node -e '
const fs = require("fs");
const payload = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const cp = payload && payload.control_plane ? payload.control_plane : {};
const peers = Array.isArray(cp.can_talk_to) ? cp.can_talk_to.map(String) : [];
console.log(JSON.stringify(peers));
' "$capabilities_tmp")"

session_file=""
if [[ "$token_output_file" == "-" ]]; then
  node -e '
const out = {
  status: "ok",
  hub_base_url: process.argv[1],
  api_base: process.argv[2],
  agent_uuid: process.argv[3],
  agent_id: process.argv[4],
  org_id: process.argv[5],
  bound_agents: JSON.parse(process.argv[6]),
  can_communicate: JSON.parse(process.argv[6]).length > 0,
  token: process.argv[7],
  endpoints: JSON.parse(process.argv[8]),
};
console.log(JSON.stringify(out));
' "$hub_base_url" "$api_base" "$agent_uuid" "$agent_id" "$org_id" "$bound_agents_json" "$token" "$endpoints_json"
else
  umask 077
  printf '%s\n' "$token" > "$token_output_file"
  session_file="${token_output_file}.json"
  if ! node -e '
const fs = require("fs");
const payload = {
  hub_base_url: process.argv[1],
  api_base: process.argv[2],
  agent_uuid: process.argv[3],
  agent_id: process.argv[4],
  org_id: process.argv[5],
  bound_agents: JSON.parse(process.argv[6]),
  endpoints: JSON.parse(process.argv[7]),
  token_file: process.argv[8],
};
fs.writeFileSync(process.argv[9], JSON.stringify(payload, null, 2) + "\n", { mode: 0o600 });
' "$hub_base_url" "$api_base" "$agent_uuid" "$agent_id" "$org_id" "$bound_agents_json" "$endpoints_json" "$token_output_file" "$session_file" 2>"$session_write_err"; then
    fail_json "session_write_failed" "$(tr '\n' ' ' <"$session_write_err" | sed 's/[[:space:]]\+/ /g' | sed 's/^ //;s/ $//')" "0"
  fi
  node -e '
const result = {
  status: "ok",
  hub_base_url: process.argv[1],
  api_base: process.argv[2],
  agent_uuid: process.argv[3],
  agent_id: process.argv[4],
  org_id: process.argv[5],
  bound_agents: JSON.parse(process.argv[6]),
  can_communicate: JSON.parse(process.argv[6]).length > 0,
  token_file: process.argv[7],
  session_file: process.argv[8],
  endpoints: JSON.parse(process.argv[9]),
};
console.log(JSON.stringify(result));
' "$hub_base_url" "$api_base" "$agent_uuid" "$agent_id" "$org_id" "$bound_agents_json" "$token_output_file" "$session_file" "$endpoints_json"
fi
