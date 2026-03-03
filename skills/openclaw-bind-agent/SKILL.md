---
name: openclaw-bind-agent
description: Register an OpenClaw agent on the local Statocyst bus and configure one-way inbound allow rules. Use when setting up agent identity/token state and receiver-controlled sender permissions for local POC message exchange tests.
---

# OpenClaw Bind Agent

## Workflow

1. Prefer minimal inputs: `agent_id` and `from_agent_id`.
2. Default `base_url` from `STATOCYST_BASE_URL` or fallback `http://statocyst:8080`.
3. Default token path to `/tmp/<agent_id>.token`.
4. Register the agent with `POST /v1/agents/register`.
5. Capture the returned token.
6. Apply inbound allow with `POST /v1/agents/{agent_id}/allow-inbound`.
7. Stop immediately on non-2xx responses and surface status/body excerpt.

## Required Inputs (Minimal)

- `agent_id`
- `from_agent_id`

Optional:
- `base_url`
- `token_output_file`

## LLM-Friendly Prompt

Use this short form in agent chat:

```text
Use $openclaw-bind-agent to register agent_id=crab and allow inbound from_agent_id=shrimp.
```

If needed, include explicit URL:

```text
Use $openclaw-bind-agent with base_url=http://statocyst:8080, agent_id=crab, from_agent_id=shrimp.
```

## Script

Preferred short command:

```bash
scripts/bind_agent.sh <agent_id> <from_agent_id> [token_output_file]
```

Backward-compatible command:

```bash
scripts/bind_agent.sh <base_url> <agent_id> <from_agent_id> [token_output_file]
```

## Recovery Behavior

- If registration returns `409 agent_exists`, script reuses token from `token_output_file` (or `/tmp/<agent_id>.token` when available) and continues allow-inbound.
- If no token file is available on `409`, script fails with an actionable hint.
