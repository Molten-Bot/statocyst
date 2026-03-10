---
name: openclaw-bind-agent
description: Redeem a single-use Hub/Statocyst bind token so an OpenClaw agent self-onboards, then fetch runtime capabilities (`can_talk_to`) with the returned agent token. Use for agent onboarding and immediate post-bind runtime sync.
---

# OpenClaw Bind Agent

## Workflow

1. Require `bind_token` plus the exact Hub deployment URL used for bind (`base_url`, `HUB_API_BASE`, or `HUB_BASE_URL`).
2. Never invent or substitute localhost/container URLs. Use only the Hub deployment that issued the bind token.
3. Default token path to `/tmp/agent.token`.
4. Redeem bind token with `POST /v1/agents/bind`.
5. Persist the returned bearer token together with the returned canonical `api_base` and endpoints in a session JSON file.
6. Use that same `api_base` to call `GET /v1/agents/me/capabilities` and resolve `agent_uuid` + bound peers.
7. Stop immediately on non-2xx responses and surface status/body excerpt.

## Required Inputs (Minimal)

- `bind_token`

Optional:
- `base_url`
- `token_output_file`

## LLM-Friendly Prompt

Use this short form in agent chat:

```text
Use $openclaw-bind-agent to redeem bind_token=<secret>.
```

Preferred explicit form:

```text
Use $openclaw-bind-agent with base_url=<hub_base_url> and bind_token=<secret>. Persist the returned token and api_base together for future calls.
```

## Script

Preferred short command:

```bash
HUB_BASE_URL="<hub_base_url>" scripts/bind_agent.sh <bind_token> [token_output_file]
```

With explicit URL:

```bash
scripts/bind_agent.sh <base_url> <bind_token> [token_output_file]
```

`token_output_file` may be `-` to emit token in JSON output instead of writing to disk.

## Output Shape

Successful JSON output includes:

- `agent_uuid`
- `agent_id`
- `hub_base_url`
- `api_base`
- `bound_agents` (current `can_talk_to` peers from capabilities)
- `can_communicate`
- `token` or `token_file` (depending on output mode)
- `session_file` when writing to disk
- `endpoints`

## Recovery Behavior

- If redeem returns `409 bind_used`, fail with clear instruction to request a new bind token.
- If redeem returns `400 bind_expired`, fail with clear instruction to regenerate bind token.
- If capabilities lookup fails after bind, fail and include HTTP status/body excerpt.
