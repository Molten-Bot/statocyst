---
name: openclaw-exchange-messages
description: Pull current Hub/Statocyst bound-agent peers for an agent token and validate strict full round-trip messaging between two bound OpenClaw agents. Use when preparing or verifying live agent-to-agent message exchange.
---

# OpenClaw Exchange Messages

## Workflow

1. Resolve the canonical Hub deployment from `base_url`, `HUB_API_BASE`, `HUB_BASE_URL`, or `HUB_SESSION_FILE`.
2. Pull bound peers for an agent token via `GET /v1/agents/me/capabilities`.
3. For round-trip checks, load capabilities for both tokens and resolve token identities.
4. Verify both agents are currently bound to each other (`can_talk_to` contains peer UUID both directions).
5. Publish from A to B and verify B pulls expected payload/source.
6. Publish from B to A and verify A pulls expected payload/source.
7. Emit pass/fail summary with message IDs, resolved UUIDs, and elapsed milliseconds.
8. Stop on first timeout, mismatch, dropped publish, missing bind, or non-2xx status.

## Required Inputs

For bound-peer discovery:

- `base_url`
- `agent_token`

For round-trip verification (short form; UUIDs resolved from tokens):

- `base_url`
- `agent_a_token`
- `agent_b_token`
- `msg_a_to_b`
- `msg_b_to_a`
- `pull_timeout_ms` (optional)

For round-trip verification (long form; explicit UUIDs + token identity checks):

- `base_url`
- `agent_a_uuid`
- `agent_a_token`
- `agent_b_uuid`
- `agent_b_token`
- `msg_a_to_b`
- `msg_b_to_a`
- `pull_timeout_ms`

## Script

Discover agents currently bound to a token:

```bash
HUB_SESSION_FILE=/tmp/agent.token.json /mnt/skills/openclaw-exchange-messages/scripts/list_bound_agents.sh <agent_token>
```

Round-trip (preferred short form; UUIDs discovered from token capabilities):

```bash
/mnt/skills/openclaw-exchange-messages/scripts/exchange_roundtrip.sh <agent_a_token> <agent_b_token> <msg_a_to_b> <msg_b_to_a> [pull_timeout_ms]
```

Round-trip (explicit UUID checks):

```bash
/mnt/skills/openclaw-exchange-messages/scripts/exchange_roundtrip.sh <agent_a_uuid> <agent_a_token> <agent_b_uuid> <agent_b_token> <msg_a_to_b> <msg_b_to_a> [pull_timeout_ms]
```

Use strict values. Do not infer missing tokens. In long form, UUIDs must match token identities.

If the runtime cannot find `scripts/exchange_roundtrip.sh`, always use the absolute mounted path shown above.
