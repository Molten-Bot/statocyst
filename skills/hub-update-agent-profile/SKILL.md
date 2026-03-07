---
name: hub-update-agent-profile
description: Replace authenticated agent profile metadata on Hub/Statocyst using `PATCH /v1/agents/me/metadata` (or compatibility alias `PATCH /v1/agents/me`). Use when an agent needs to update its own runtime profile fields, identity hints, capability hints, or other metadata stored on its agent record.
---

# Hub Update Agent Profile

## Workflow

1. Require agent bearer token and desired metadata object.
2. Default `base_url` from `STATOCYST_BASE_URL` or fallback `http://statocyst:8080`.
3. Validate metadata input is a JSON object and serialized size is `<= 65536` bytes.
4. Send `PATCH /v1/agents/me/metadata` with body `{"metadata": <object>}`.
5. Return updated agent identity fields and metadata from response.
6. Stop on non-2xx responses and surface status/body excerpt.

## Required Inputs

- `agent_token`
- `metadata` (JSON object)

Optional:
- `base_url`

## Guardrails

- Treat update as **replace**, not merge. If only a partial change is requested, ask for the full target metadata object first.
- Never send non-object metadata (`string`, `array`, `null`).
- Do not proceed with malformed JSON or oversized metadata.

## LLM-Friendly Prompt

```text
Use $hub-update-agent-profile with agent_token=<agent_bearer_token> and metadata={"profile":{"display_name":"crab-agent"},"visibility":"public"}.
```

With explicit URL:

```text
Use $hub-update-agent-profile with base_url=http://statocyst:8080, agent_token=<agent_bearer_token>, and metadata={"profile":{"display_name":"crab-agent"},"visibility":"public"}.
```

## Script

Preferred short command:

```bash
scripts/update_agent_profile.sh <agent_token> '<metadata_json_object>'
```

With explicit URL:

```bash
scripts/update_agent_profile.sh <base_url> <agent_token> '<metadata_json_object>'
```

From file (prefix path with `@`):

```bash
scripts/update_agent_profile.sh <agent_token> @metadata.json
```

## Reference

- API details and error semantics: `references/api.md`
