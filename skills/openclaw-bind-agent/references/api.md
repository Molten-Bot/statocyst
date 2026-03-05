# Statocyst API Reference for Bind Skill

## Redeem Bind Token

- Method: `POST`
- Path: `/v1/agents/bind`
- Request:

```json
{ "hub_url": "https://hub.example", "bind_token": "secret-from-human" }
```

- Success: `201` with `{ "token":"..." }`
- Common errors:
  - `404` + `bind_not_found`
  - `400` + `bind_expired`
  - `409` + `bind_used`
  - `409` + `agent_exists`
