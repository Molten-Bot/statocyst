#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<USAGE
Usage:
  bind_agent.sh <agent_id> <from_agent_id> [token_output_file]
  bind_agent.sh <base_url> <agent_id> <from_agent_id> [token_output_file]

Arguments:
  agent_id          Agent to register and bond
  from_agent_id     Peer agent to bond with
  token_output_file Optional path to write token. Default: /tmp/<agent_id>.token. Use '-' to print token to stdout.

Environment:
  STATOCYST_BASE_URL  Default base URL when not passed explicitly. Default fallback: http://statocyst:8080
USAGE
}

if [[ $# -lt 2 || $# -gt 4 ]]; then
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
  if [[ $# -lt 3 || $# -gt 4 ]]; then
    usage >&2
    exit 1
  fi
  base_url="${1%/}"
  agent_id="$2"
  from_agent_id="$3"
  token_output_file="${4:-/tmp/${agent_id}.token}"
else
  base_url="${default_base_url%/}"
  agent_id="$1"
  from_agent_id="$2"
  token_output_file="${3:-/tmp/${agent_id}.token}"
fi

register_tmp="$(mktemp)"
bond_tmp="$(mktemp)"
trap 'rm -f "$register_tmp" "$bond_tmp"' EXIT

register_payload="$(node -e 'console.log(JSON.stringify({agent_id: process.argv[1]}))' "$agent_id")"

register_status="$(curl -sS -o "$register_tmp" -w "%{http_code}" \
  -X POST "$base_url/v1/agents/register" \
  -H "Content-Type: application/json" \
  --data "$register_payload")"

token=""
reused_registration="false"

if [[ "$register_status" == "201" ]]; then
  token="$(node -e 'const fs=require("fs"); const p=JSON.parse(fs.readFileSync(process.argv[1],"utf8")); if(!p.token){console.error("missing token in register response"); process.exit(1);} console.log(p.token);' "$register_tmp")"
elif [[ "$register_status" == "409" ]]; then
  token_file_candidate="$token_output_file"
  if [[ "$token_file_candidate" == "-" ]]; then
    token_file_candidate="/tmp/${agent_id}.token"
  fi
  if [[ -f "$token_file_candidate" ]]; then
    token="$(tr -d '\r\n' < "$token_file_candidate")"
    reused_registration="true"
  fi
  if [[ -z "$token" ]]; then
    excerpt="$(node -e 'const fs=require("fs"); const t=fs.readFileSync(process.argv[1],"utf8"); console.log(t.slice(0,300));' "$register_tmp")"
    echo "ERROR: register failed (HTTP 409) and no reusable token file found for ${agent_id}: $excerpt" >&2
    echo "Hint: provide token_output_file path for existing token, or restart statocyst for a clean registry." >&2
    exit 1
  fi
else
  excerpt="$(node -e 'const fs=require("fs"); const t=fs.readFileSync(process.argv[1],"utf8"); console.log(t.slice(0,300));' "$register_tmp")"
  echo "ERROR: register failed (HTTP $register_status): $excerpt" >&2
  exit 1
fi

if [[ "$token_output_file" == "-" ]]; then
  printf '%s\n' "$token"
else
  umask 077
  printf '%s\n' "$token" > "$token_output_file"
fi

bond_payload="$(node -e 'console.log(JSON.stringify({peer_agent_id: process.argv[1]}))' "$from_agent_id")"

bond_status="$(curl -sS -o "$bond_tmp" -w "%{http_code}" \
  -X POST "$base_url/v1/bonds" \
  -H "Authorization: Bearer $token" \
  -H "Content-Type: application/json" \
  --data "$bond_payload")"

if [[ "$bond_status" != "200" && "$bond_status" != "201" ]]; then
  excerpt="$(node -e 'const fs=require("fs"); const t=fs.readFileSync(process.argv[1],"utf8"); console.log(t.slice(0,300));' "$bond_tmp")"
  echo "ERROR: bond create/join failed (HTTP $bond_status): $excerpt" >&2
  exit 1
fi

if [[ "$token_output_file" == "-" ]]; then
  echo "OK: registered $agent_id and requested bond with $from_agent_id" >&2
else
  node -e '
const result = {
  status: "ok",
  base_url: process.argv[4],
  agent_id: process.argv[1],
  peer_agent_id: process.argv[2],
  token_file: process.argv[3],
  reused_registration: process.argv[5] === "true",
  bond: JSON.parse(require("fs").readFileSync(process.argv[6], "utf8")),
};
console.log(JSON.stringify(result));
' "$agent_id" "$from_agent_id" "$token_output_file" "$base_url" "$reused_registration" "$bond_tmp"
fi
