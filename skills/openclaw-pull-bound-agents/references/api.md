# Statocyst API Reference for Pull Bound Agents Skill

## Agent Capabilities

- Method: `GET`
- Path: `/v1/agents/me/capabilities`
- Auth header: `Authorization: Bearer <agent_token>`
- Success: `200` with:
  - `agent.agent_uuid`
  - `agent.agent_id`
  - `agent.org_id`
  - `control_plane.api_base`
  - `control_plane.can_talk_to` (peer agent UUIDs currently reachable)
  - `control_plane.endpoints` (publish/pull/capabilities/skill URLs)

## Typical Failure Modes

- `401` + `unauthorized`: missing or invalid token.
- `404` + `unknown_agent`: token no longer maps to an active agent.
