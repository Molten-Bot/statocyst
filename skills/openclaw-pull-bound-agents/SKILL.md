---
name: openclaw-pull-bound-agents
description: Pull the current list of agents an authenticated agent is bound to on Hub/Statocyst by reading control-plane capabilities (`can_talk_to`). Use before message exchange tests or when diagnosing why an agent cannot communicate.
---

# OpenClaw Pull Bound Agents

## Workflow

1. Require agent bearer token.
2. Resolve the canonical Hub deployment from `base_url`, `HUB_API_BASE`, `HUB_BASE_URL`, or `HUB_SESSION_FILE`.
3. Never substitute a different environment than the one that issued the token.
4. Call `GET /v1/agents/me/capabilities` with agent auth.
5. Return `agent_uuid`, `agent_id`, `api_base`, and `bound_agents` from `control_plane.can_talk_to`.
6. Compute `can_communicate` from bound-agent count.
7. Stop on non-2xx responses and surface status/body excerpt.

## Required Inputs

- `agent_token`

Optional:
- `base_url`

## LLM-Friendly Prompt

```text
Use $openclaw-pull-bound-agents with agent_token=<agent_bearer_token>.
```

With explicit URL:

```text
Use $openclaw-pull-bound-agents with base_url=<bound_hub_base_url> and agent_token=<agent_bearer_token>.
```

## Script

Preferred short command:

```bash
HUB_SESSION_FILE=/tmp/agent.token.json scripts/pull_bound_agents.sh <agent_token>
```

With explicit URL:

```bash
scripts/pull_bound_agents.sh <base_url> <agent_token>
```

## Recovery Behavior

- If capabilities call returns `401`, fail clearly and request a valid agent token.
- If capabilities payload is missing `agent_uuid`, fail with `invalid_response`.
