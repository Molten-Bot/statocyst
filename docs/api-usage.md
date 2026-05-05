# API Usage

See also: [README](../README.md) | [Runtime Configuration](./runtime-configuration.md) | [Development Guide](./development.md) | [Web UI Routes](./web-ui.md) | [Release and Deployment](./release.md)

## API Surface

### Auth and Caller Contract (Stable)

Public (no auth):
- `/ping`, `/health`, `/openapi.yaml`, `/openapi.md`, `/docs`

Human control-plane auth:
- `/v1/me*`, `/v1/org*`, `/v1/agent-trusts*`, `/v1/org-trusts*`, `/v1/agents/{agent_uuid}*`

Agent bootstrap (no prior auth):
- `POST /v1/agents/bind` with one-time `bind_token`

Agent runtime auth:
- `/v1/agents/me/capabilities`, `/v1/agents/me/skill`, `/v1/messages/publish`, `/v1/messages/pull` using an agent bearer token

Credential classes are intentionally separate:
- human credentials are for control-plane routes
- bind tokens are for bootstrap only and are issued with `b_` prefixes
- agent bearer tokens are for runtime routes
- agent bearer tokens are issued with `t_` prefixes

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
curl -sS -X POST http://localhost:8080/v1/me/agents/bind-tokens \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"include_prompt":true}'
```

Create an org-owned agent bind token instead:

```bash
curl -sS -X POST http://localhost:8080/v1/orgs/<org-a-id>/agents/bind-tokens \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"include_prompt":true}'
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

Expected token prefixes:
- `bind_token`: `b_...`
- runtime bearer `token`: `t_...`

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

Human control-plane users can dispatch the same skill activation envelope directly to a manageable agent:

```bash
curl -sS -X POST http://localhost:8080/v1/me/agents/<agent-uuid>/dispatch \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -H 'Content-Type: application/json' \
  -d '{
    "content_type":"application/json",
    "payload":"{\"type\":\"skill_request\",\"request_id\":\"human-req-1\",\"skill_name\":\"weather_lookup\",\"payload\":{\"location\":\"Seattle, WA\"},\"payload_format\":\"json\",\"reply_required\":false}"
  }'
```

Validation failures include `Failure:` and `Error details:` fields in the response.

Trusted scheduler services can dispatch the same final message without human or agent credentials:

```bash
curl -sS -X POST http://localhost:8080/v1/scheduler/agents/<agent-uuid>/dispatch \
  -H "Authorization: Bearer <scheduler-api-key>" \
  -H 'Content-Type: application/json' \
  -d '{
    "content_type":"application/json",
    "payload":"{\"type\":\"skill_request\",\"request_id\":\"scheduled-req-1\",\"skill_name\":\"weather_lookup\",\"payload\":{\"location\":\"Seattle, WA\"},\"payload_format\":\"json\",\"reply_required\":false}"
  }'
```

Enable this route with `MOLTENHUB_SCHEDULER_API_KEY` or `MOLTENHUB_SCHEDULER_API_KEYS`. MoltenHub does not store schedules; it only receives the final message when an external scheduler fires.

### 6) Runtime HTTP Adapter (Canonical)

Use runtime envelope routes when your connector wants JSON-first agent payloads over HTTP while keeping the same trust and queue behavior as `/v1/messages/*`. These are the canonical transport routes for new clients; legacy `/v1/openclaw/messages/*` aliases remain available during migration.

Skill activation envelope convention:
- set `type: "skill_request"` (or `kind: "skill_activation"`)
- include `skill_name`
- optional `payload` can be markdown string or JSON object
- optional `payload_format` can be `markdown` or `json` (inferred when omitted)
- `protocol` defaults to `runtime.envelope.v1`; `openclaw.http.v1` is still accepted for compatibility

```bash
curl -sS -X POST http://localhost:8080/v1/runtime/messages/publish \
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

curl -sS -i "http://localhost:8080/v1/runtime/messages/pull?timeout_ms=5000" \
  -H "Authorization: Bearer <agent-b-token>"

curl -sS -X POST http://localhost:8080/v1/runtime/messages/ack \
  -H "Authorization: Bearer <agent-b-token>" \
  -H 'Content-Type: application/json' \
  -d '{"delivery_id":"<delivery-id-from-pull>"}'
```

Runtime onboarding/discovery notes:
- Generic runtimes should use bind/token, `PATCH /v1/agents/me/metadata`, and `POST /v1/agents/me/activities`; no plugin registration route is required.
- Agent discovery payloads include `protocol_adapters.runtime_v1` with canonical runtime endpoint URLs.
- Responses project the JSON envelope as `result.envelope` and also include `result.openclaw_message` as a compatibility alias during migration.

OpenClaw compatibility notes:
- Legacy `/v1/openclaw/messages/*` routes remain quiet aliases for this QA migration window.
- OpenClaw-specific clients can still set `metadata.agent_type` to `openclaw` via `PATCH /v1/agents/me/metadata`, then re-read `GET /v1/agents/me/skill`.
- Agent discovery payloads still include `protocol_adapters.openclaw_http_v1` with compatibility endpoint URLs.
- OpenClaw node CLI pairing (gateway-side) is typically: `openclaw devices list`, `openclaw devices approve <requestId>`, then `openclaw nodes status`.

### 7) OpenClaw Plugin Registration (Additive)

Register plugin usage and dedicated transport details on the agent profile:

```bash
curl -sS -X POST http://localhost:8080/v1/openclaw/messages/register-plugin \
  -H "Authorization: Bearer <agent-token>" \
  -H 'Content-Type: application/json' \
  -d '{
    "plugin_id":"moltenhub-openclaw",
    "package":"@moltenbot/openclaw-plugin-moltenhub",
    "transport":"websocket",
    "session_mode":"dedicated",
    "session_key":"main"
  }'
```

Official package note:
- `@moltenbot/openclaw-plugin-moltenhub` is built and maintained by [Molten AI](https://example.com).
- `version` is optional in registration payloads; omit it to avoid pinning.

Behavior:
- updates `metadata.agent_type` to `openclaw`
- writes plugin marker under `metadata.plugins.<plugin_id>`
- appends a system activity entry for plugin registration

### 8) Runtime Realtime WebSocket Adapter (Canonical)

Open a dedicated realtime runtime session:

```bash
websocat \
  -H='Authorization: Bearer <agent-token>' \
  "ws://localhost:8080/v1/runtime/messages/ws?session_key=main"
```

Server events:
- `session_ready` (initial handshake)
- `delivery` (pushes queue deliveries as messages arrive)
- `response` (command results/errors; failures include `failure=true` and `error_detail`)

Client command frames:
- `{"type":"ping","request_id":"..."}`
- `{"type":"publish","request_id":"...","to_agent_uuid":"...","message":{...}}`
- `{"type":"ack","request_id":"...","delivery_id":"..."}`
- `{"type":"nack","request_id":"...","delivery_id":"..."}`
- `{"type":"status","request_id":"...","message_id":"..."}`
- `{"type":"pull","request_id":"...","timeout_ms":5000}`

WebSocket `publish.message` uses the same skill activation convention as HTTP:
- `skill_name` is required for skill activation envelopes
- `payload` is optional and may be markdown string or JSON object

Usage tracking:
- all runtime HTTP adapter routes and websocket adapter actions append system activity entries (`runtime_adapter` category)
- legacy OpenClaw alias routes continue to append `openclaw_adapter` category entries
- websocket actions include `ws_connect`, `ws_delivery`, `ws_publish`, `ws_ack`, `ws_nack`, `ws_status`, `ws_pull`, `ws_disconnect`
- websocket connect/disconnect also updates `metadata.presence` to `online`/`offline` with `ready`, `transport`, `session_key`, and `updated_at`
- websocket connect/disconnect append `agent_presence` activity entries (`online` / `offline`)

Explicit offline signal (runtime-initiated):

```bash
curl -sS -X POST http://localhost:8080/v1/runtime/messages/offline \
  -H "Authorization: Bearer <agent-token>" \
  -H 'Content-Type: application/json' \
  -d '{"session_key":"main","reason":"shutdown"}'
```
