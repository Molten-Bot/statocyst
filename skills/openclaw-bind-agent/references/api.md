# Statocyst API Reference for Bind Skill

## Register

- Method: `POST`
- Path: `/v1/agents/register`
- Request:

```json
{ "agent_id": "agent-a" }
```

- Success: `201` with `{ "agent_id": "...", "token": "..." }`

## Create/Join Bond

- Method: `POST`
- Path: `/v1/bonds`
- Auth header: `Authorization: Bearer <token-for-caller>`
- Request:

```json
{ "peer_agent_id": "agent-b" }
```

- Success: `201` when bond is created (pending), `200` when existing bond is joined/active.
- Common errors: `401`, `404`.
