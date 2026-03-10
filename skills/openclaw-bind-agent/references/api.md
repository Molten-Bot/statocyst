# Statocyst API Reference for Bind Skill

## Redeem Bind Token

- Method: `POST`
- Path: `/v1/agents/bind`
- Request:

```json
{ "hub_url": "https://hub.example", "bind_token": "bind-token-secret" }
```

- Success: `201` with:
  - `token`
  - `api_base`
  - `agent`
  - `endpoints`
- Agents should persist the returned `api_base` with the token and use that exact value for future calls.
- Common errors: `404 bind_not_found`, `400 bind_expired`, `409 bind_used`

## Resolve Bound Peers + Agent Identity

- Method: `GET`
- Path: `/v1/agents/me/capabilities`
- Auth header: `Authorization: Bearer <agent_token>`
- Success: `200` with:
  - `agent.agent_uuid`
  - `agent.agent_id`
  - `control_plane.can_talk_to` (array of peer agent UUIDs this agent can currently message)
