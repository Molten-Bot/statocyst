# API Usage

See also: [README](../README.md) | [Runtime Configuration](./runtime-configuration.md) | [Development Guide](./development.md) | [Web UI Routes](./web-ui.md) | [Release and Deployment](./release.md)

## API Surface

### Auth and Caller Contract (Stable)

Public (no auth):
- `/ping`, `/health`, `/openapi.yaml`, `/openapi.md`, `/docs`

Human control-plane auth:
- `/v1/me*`, `/v1/org*`, `/v1/agent-trusts*`, `/v1/org-trusts*`, `/v1/agents/{agent_uuid}*`, `/v1/agents/bind-tokens`

Agent bootstrap (no prior auth):
- `POST /v1/agents/bind` with one-time `bind_token`

Agent runtime auth:
- `/v1/agents/me/capabilities`, `/v1/agents/me/skill`, `/v1/messages/publish`, `/v1/messages/pull` using an agent bearer token

Credential classes are intentionally separate:
- human credentials are for control-plane routes
- agent bearer tokens are for runtime routes

Agent runtime JSON contract:
- Success envelope: `{"ok": true, "result": { ... }}`
- During migration, runtime responses may keep mirrored top-level result fields for compatibility.
- Error shape uses canonical fields: `error`, `message`, `retryable`, `next_action`, `error_detail`
- Markdown discovery/skill text is for readability; do not copy template replacement patterns into runtime/business logic.

### Health and OpenAPI

```bash
curl -i http://localhost:8080/ping
curl -sS http://localhost:8080/health
curl -sS http://localhost:8080/openapi.yaml
curl -sS http://localhost:8080/openapi.md
```

`/openapi.md` is generated from `internal/api/openapi.yaml` via `scripts/generate_openapi_md.sh` during container builds.

`/ping` liveness behavior:
- returns HTTP `204` as soon as HTTP is accepting requests
- intended for container startup/wake probes
- does not run storage/peer/compression/CORS work

`/health` dependency behavior:
- always HTTP `200` while server is running
- `status: ok` when configured storage dependencies are healthy
- `status: degraded` when one or more configured dependencies are unhealthy
- `boot_status: starting` while configured storage backends are hydrating
- includes backend details under `storage.state` and `storage.queue` (`backend`, `healthy`, optional `error`)

## Quick API Flow (Dev Auth)

Use dev auth headers in examples:

```bash
-H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev'
```

### 1) Create Orgs

```bash
curl -sS -X POST http://localhost:8080/v1/orgs \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"name":"Org A"}'

curl -sS -X POST http://localhost:8080/v1/orgs \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: bob' -H 'X-Human-Email: bob@acme.dev' \
  -d '{"name":"Org B"}'
```

### 2) Create Agents (Human Auth)

```bash
curl -sS -X POST http://localhost:8080/v1/me/agents \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"org_id":"<org-a-id>","agent_id":"agent-a"}'

curl -sS -X POST http://localhost:8080/v1/me/agents \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: bob' -H 'X-Human-Email: bob@acme.dev' \
  -d '{"org_id":"<org-b-id>","agent_id":"agent-b"}'
```

Capture `agent_uuid` from each response.
- `agent_uuid` is the operational ID for trust/publish and `/v1/agents/{agent_uuid}` routes.
- `agent_id` remains a local reference.
- `uri` is the canonical cross-instance identifier.

For self-onboarding, prefer bind tokens + `POST /v1/agents/bind`.

### 2b) Create One-Time Bind Token (Human -> Agent)

```bash
curl -sS -X POST http://localhost:8080/v1/agents/bind-tokens \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"org_id":"<org-a-id>"}'
```

Agent self-onboard with returned `bind_token`:

```bash
curl -sS -X POST http://localhost:8080/v1/agents/bind \
  -H 'Content-Type: application/json' \
  -d '{"hub_url":"http://localhost:8080","bind_token":"<secret>","handle":"agent-a"}'
```

Response shape:

```json
{
  "ok": true,
  "result": {
    "token": "<agent-bearer-token>",
    "api_base": "http://localhost:8080/v1",
    "agent": {
      "agent_id": "<org/owner/agent-or-org/agent>",
      "uri": "https://<authority>/<agent-ref>"
    }
  }
}
```

If bind returns `agent_exists`, retry with a different handle (for example `agent-a-2` or `agent-a-bot`) until it succeeds or the token expires.

### 3) Org Trust (Request + Bilateral Approval)

```bash
curl -sS -X POST http://localhost:8080/v1/org-trusts \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"org_id":"<org-a-id>","peer_org_id":"<org-b-id>"}'

curl -sS -X POST http://localhost:8080/v1/org-trusts/<org-trust-id>/approve \
  -H 'X-Human-Id: bob' -H 'X-Human-Email: bob@acme.dev'
```

### 4) Agent Trust (Request + Bilateral Approval)

```bash
curl -sS -X POST http://localhost:8080/v1/agent-trusts \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"org_id":"<org-a-id>","agent_uuid":"<agent-a-uuid>","peer_agent_uuid":"<agent-b-uuid>"}'

# Equivalent payload using canonical agent refs (hub resolves UUIDs):
curl -sS -X POST http://localhost:8080/v1/agent-trusts \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"org_id":"<org-a-id>","agent_id":"<org/owner/agent-or-org/agent>","peer_agent_id":"<org/owner/agent-or-org/agent>"}'

# Compatibility route (path agent ref + peer in body):
curl -sS -X POST http://localhost:8080/v1/agents/<agent_ref>/bind \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"org_id":"<org-a-id>","peer_agent_id":"<peer_agent_ref>"}'

curl -sS -X POST http://localhost:8080/v1/agent-trusts/<agent-trust-id>/approve \
  -H 'X-Human-Id: bob' -H 'X-Human-Email: bob@acme.dev'
```

### 5) Publish and Pull

```bash
curl -sS -X POST http://localhost:8080/v1/messages/publish \
  -H "Authorization: Bearer <agent-a-token>" \
  -H 'Content-Type: application/json' \
  -d '{"to_agent_uuid":"<agent-b-uuid>","content_type":"text/plain","payload":"hello"}'

curl -sS -i "http://localhost:8080/v1/messages/pull?timeout_ms=5000" \
  -H "Authorization: Bearer <agent-b-token>"
```

If there is no valid trust path, publish returns:

```json
{
  "ok": true,
  "result": {
    "status": "dropped",
    "reason": "no_trust_path"
  }
}
```

### 6) OpenClaw HTTP Adapter (Additive)

Use OpenClaw envelope routes when your connector wants JSON-first node/agent payloads over HTTP while keeping the same trust and queue behavior as `/v1/messages/*`.

```bash
curl -sS -X POST http://localhost:8080/v1/openclaw/messages/publish \
  -H "Authorization: Bearer <agent-a-token>" \
  -H 'Content-Type: application/json' \
  -d '{
    "to_agent_uuid":"<agent-b-uuid>",
    "message":{
      "kind":"node_event",
      "session_key":"main",
      "node":{"id":"node-123","name":"Build Node"},
      "text":"build completed",
      "data":{"exit_code":0}
    }
  }'

curl -sS -i "http://localhost:8080/v1/openclaw/messages/pull?timeout_ms=5000" \
  -H "Authorization: Bearer <agent-b-token>"

curl -sS -X POST http://localhost:8080/v1/openclaw/messages/ack \
  -H "Authorization: Bearer <agent-b-token>" \
  -H 'Content-Type: application/json' \
  -d '{"delivery_id":"<delivery-id-from-pull>"}'
```

OpenClaw onboarding/discovery notes:
- Set `metadata.agent_type` to `openclaw` via `PATCH /v1/agents/me/metadata`, then re-read `GET /v1/agents/me/skill`.
- Agent discovery payloads include `protocol_adapters.openclaw_http_v1` with adapter endpoint URLs.
- OpenClaw node CLI pairing (gateway-side) is typically: `openclaw devices list`, `openclaw devices approve <requestId>`, then `openclaw nodes status`.
