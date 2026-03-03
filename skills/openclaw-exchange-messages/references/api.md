# Statocyst API Reference for Exchange Skill

## Publish

- Method: `POST`
- Path: `/v1/messages/publish`
- Auth header: `Authorization: Bearer <sender_token>`
- Request:

```json
{
  "to_agent_id": "agent-b",
  "content_type": "text/plain",
  "payload": "hello"
}
```

- Success (queued): `202` with `{ "message_id": "...", "status": "queued" }`
- Success (dropped): `202` with `{ "status": "dropped", "reason": "no_active_bond" }`
- Common errors: `401`, `404`.

## Pull

- Method: `GET`
- Path: `/v1/messages/pull?timeout_ms=5000`
- Auth header: `Authorization: Bearer <receiver_token>`
- Success: `200` with `{ "message": { ... } }`
- Timeout: `204`.
