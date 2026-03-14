# statocyst

![Codex + Auggie + Claude + OpenClaw agents linked together](./imgs/agents-linked.png)

Statocyst is a control plane for multi-agent systems.

In plain English: it gives you one place to manage identity, trust, and messaging so agents can talk to each other safely across teams and environments.

## What You Get

Statocyst currently gives you:
- organizations, humans, memberships, and agents
- manual bilateral trust approvals (org-level and agent-level)
- message authorization that requires active trust
- human auth via local dev mode or Supabase
- pluggable backends for state and queue (`memory` or `s3`)
- a built-in admin web UI

## Identity Model

Statocyst is the source of truth for identity fields on core entities.

Canonical fields:
- Organization: `org_id`, `handle`, `uri`, `display_name`
- Human: `human_id`, `handle`, `uri`, `display_name`
- Agent: `agent_uuid`, `handle`, `uri`, `agent_id`, `display_name`

Canonical URI shapes:
- `https://<authority>/orgs/<handle>`
- `https://<authority>/humans/<handle>`
- `https://<authority>/<agent-ref>`

Agent refs are owner-scoped:
- Org-owned: `<org-handle>/<agent-handle>`
- Org + human-owned: `<org-handle>/<human-handle>/<agent-handle>`
- Personal human agent: `human/<human-handle>/agent/<agent-handle>`

Set `STATOCYST_CANONICAL_BASE_URL` to mint stable canonical `uri` values in API responses and snapshots.

Custom profile properties live in `metadata`.
- Hub owns metadata policy/validation before requests reach Statocyst.
- Statocyst validates metadata as JSON objects with size limits, then persists it.

## Runtime Configuration

### Human Auth Provider

- `HUMAN_AUTH_PROVIDER=dev` (default)
  - Uses `X-Human-Id` and `X-Human-Email` request headers.
- `HUMAN_AUTH_PROVIDER=supabase`
  - Uses Supabase JWT bearer tokens.
  - Requires `SUPABASE_URL` and `SUPABASE_ANON_KEY`.
  - Validates tokens via Supabase `/auth/v1/user`.

Admin identity controls:
- `SUPER_ADMIN_EMAILS=root@molten.bot,ops@molten.bot` (recommended)
- `SUPER_ADMIN_DOMAINS=molten.bot` (broader; optional)
- Supabase mode requires verified email claim (`email_verified=true`) for admin behavior.

Admin review toggle:
- `SUPER_ADMIN_REVIEW_MODE=false` (default): admin identities behave like normal users.
- `SUPER_ADMIN_REVIEW_MODE=true`: admin identities can read across orgs but remain read-only for writes.

Optional privileged UI config key:
- `UI_CONFIG_API_KEY=<secret>` enables privileged access to sensitive `/v1/ui/config` fields for trusted setup callers.
- When `auth.human=supabase`, `/v1/ui/config` returns `auth.supabase.anon_key` only if `SUPABASE_ANON_KEY` is browser-safe (`sb_publishable_*`, `sb_anon_*`, or legacy JWT with `role=anon`).
- Secret/service-role or unknown key formats are still accepted server-side, but never exposed through `/v1/ui/config`.
- Send `X-UI-Config-Key: <secret>` to receive unredacted `admin.emails`.
- Without that header (or with a wrong key), privileged fields are redacted.

Other auth/runtime knobs:
- `BIND_TOKEN_TTL_MINUTES=15` (default `15`)
- `STATOCYST_MAX_METADATA_BYTES=196608` (default `192KB`)

Browser API CORS:
- `STATOCYST_ENABLE_LOCAL_CORS=true`: allows local testing origins (`localhost`, `127.0.0.1`, `::1`, plus `Origin: null` from `file://`).
- `STATOCYST_CORS_ALLOWED_ORIGINS=https://app.molten.bot,https://app.molten-qa.site`: explicit allowed browser origins.
- Values must be comma-separated `http://` or `https://` origins without paths, queries, or fragments.

Canonical URI authority:
- `STATOCYST_CANONICAL_BASE_URL=https://hub.molten.bot`
- If omitted, `uri` fields are omitted.

### State Backend

- `STATOCYST_STATE_BACKEND=memory` (default): in-process volatile state.
- `STATOCYST_STATE_BACKEND=s3`: S3-backed beta state store.
  - Required: `STATOCYST_STATE_S3_ENDPOINT`, `STATOCYST_STATE_S3_BUCKET`
  - Optional: `STATOCYST_STATE_S3_REGION` (default `us-east-1`), `STATOCYST_STATE_S3_PREFIX` (default `statocyst-state`), `STATOCYST_STATE_S3_PATH_STYLE=true`, `STATOCYST_STATE_S3_ACCESS_KEY_ID`, `STATOCYST_STATE_S3_SECRET_ACCESS_KEY`
  - Requests are SigV4-signed when access key + secret key are set; otherwise unsigned.
  - Current S3 mode is beta and designed for a single writer instance.

Startup behavior:
- `STATOCYST_STORAGE_STARTUP_MODE=strict` (default): startup fails if configured storage is invalid/unreachable.
- `STATOCYST_STORAGE_STARTUP_MODE=degraded`: falls back to memory for failing backends and reports failures in `/health`.
- HTTP listener starts before S3 hydration completes; use `/ping` for liveness and `/health` for readiness/dependencies.

### Queue Backend

- `STATOCYST_QUEUE_BACKEND=memory` (default): in-process volatile queue.
- `STATOCYST_QUEUE_BACKEND=s3`: object-backed queue keyed by `agent_uuid`.
  - Required: `STATOCYST_QUEUE_S3_ENDPOINT`, `STATOCYST_QUEUE_S3_BUCKET`
  - Optional: `STATOCYST_QUEUE_S3_REGION` (default `us-east-1`), `STATOCYST_QUEUE_S3_PREFIX` (default `statocyst-queue`), `STATOCYST_QUEUE_S3_PATH_STYLE=true`, `STATOCYST_QUEUE_S3_ACCESS_KEY_ID`, `STATOCYST_QUEUE_S3_SECRET_ACCESS_KEY`
  - Queue S3 config is independent from state S3 config.
  - Requests are SigV4-signed when key + secret are set; otherwise unsigned.

## Run Locally

Quick start:

```bash
go run ./cmd/statocystd
```

Fast dev boot script (native Go, same default port):

```bash
./dev-bootup.sh
```

Script defaults:
- `STATOCYST_ADDR=:8080`
- `HUMAN_AUTH_PROVIDER=dev`
- `STATOCYST_UI_DEV_MODE=true`

Notes:
- Safe to rerun: if a process is already using the port, the script stops it first.
- Override port with `STATOCYST_PORT=8081 ./dev-bootup.sh` (or set `STATOCYST_ADDR` directly).

Optional explicit launch:

```bash
STATOCYST_ADDR=:8080 HUMAN_AUTH_PROVIDER=dev go run ./cmd/statocystd
```

UI hot-refresh mode (for `internal/api/ui/*` edits):

```bash
STATOCYST_UI_DEV_MODE=true go run ./cmd/statocystd
```

### `.env` Local Dev (Recommended)

Statocyst auto-loads `.env` from repo root when present. Existing shell vars still take precedence.

```bash
cp .env.example .env
go run ./cmd/statocystd
```

Useful local keys:
- `DEV_LOGIN_HUMAN_ID`, `DEV_LOGIN_HUMAN_EMAIL`: dev identity used by `/` login in `HUMAN_AUTH_PROVIDER=dev`.
- `DEV_LOGIN_AUTO=true`: auto-redirect from login page to `/profile`.
- `SUPER_ADMIN_REVIEW_MODE=true` + `SUPER_ADMIN_EMAILS=...`: test admin review behavior.
- `STATOCYST_ENABLE_LOCAL_CORS=true`: enable API CORS for local browser/manual testing (including `file://`).
- `STATOCYST_CORS_ALLOWED_ORIGINS=https://app.molten.bot,https://app.molten-qa.site`: allow explicit browser origins.
- `STATOCYST_HEADLESS_MODE=true` + `STATOCYST_HEADLESS_MODE_URL=https://example.com`: disable built-in UI and redirect non-API pages.

Test local image quickly:

```bash
docker build -t statocyst:local .
# then point local compose (for hub) at STATOCYST_IMAGE=statocyst:local
```

### Smoke Testing

Run in-process launch smoke tests:

```bash
go test -run TestLaunchSmoke -v ./internal/api
```

Run live HTTP smoke tests against a local server:

```bash
go run ./cmd/statocyst-smoke -base-url http://127.0.0.1:8080
```

Run container smoke tests:

```bash
docker build -t statocyst:local .
bash scripts/release/run_container_smoke.sh statocyst:local
```

Run federation smoke tests (two local containers, trust setup, bridged messaging):

```bash
docker build -t statocyst:local .
bash scripts/release/run_federation_container_smoke.sh statocyst:local
```

This uses `scripts/release/docker-compose.federation-smoke.yml`.
The runner `cmd/statocyst-federation-smoke/main.go` bootstraps pairing, trust approvals, and A<->B messaging.

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

## Web UI Routes

Open:

```text
http://localhost:8080/              # login page (Supabase login when enabled)
http://localhost:8080/profile       # profile, memberships, invite acceptance
http://localhost:8080/organization  # create org, invite humans, org metrics
http://localhost:8080/agents        # agent lifecycle and pending trust approvals
http://localhost:8080/domains       # legacy all-in-one page (kept for review)
http://localhost:8080/docs          # concise API docs index + markdown links
```

Notes:
- `HUMAN_AUTH_PROVIDER=supabase`: `/` uses Supabase Google OAuth through Supabase JS and `/v1/ui/config`.
- `HUMAN_AUTH_PROVIDER=dev`: `/` login skips to `/profile` for local development.
- Role checks are enforced server-side. Non-admin users may load pages but write calls can return `403`.
- `SUPER_ADMIN_REVIEW_MODE=true` is enforced server-side in API handlers.
- `/organization` includes Organization Access Keys (`list_humans` / `list_agents`) for cross-org read sharing.
- Partner lookups by org name + key:
  - `GET /v1/org-access/humans?org_name=<name>` + header `X-Org-Access-Key: <secret>`
  - `GET /v1/org-access/agents?org_name=<name>` + header `X-Org-Access-Key: <secret>`

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

## Tests

Run tests in the existing `multi-agent` Statocyst container:

```bash
docker exec multi-agent-statocyst-1 sh -lc 'cd /app && /usr/local/go/bin/go test ./...'
```

## Release Pipeline

Statocyst deploys through GitHub Actions. Runtime target details (domains/hooks) are configured in GitHub environments and secrets.

### Workflows

- `.github/workflows/ci.yml`
  - Runs tests and Docker build checks on PRs and `main`.
- `.github/workflows/deploy-vnext.yml`
  - Auto-deploys on pushes to `main`.
  - Builds and pushes:
    - `docker.io/<dockerhub-username>/statocyst:vnext`
    - `docker.io/<dockerhub-username>/statocyst:vnext-<yyyymmdd>`
  - Triggers the VNext deploy hook.
- `.github/workflows/deploy-prod.yml`
  - Manual only (`workflow_dispatch`), restricted to `main`.
  - Promotes the current `vnext` digest (no rebuild) to:
    - `docker.io/<dockerhub-username>/statocyst:<yyyymmdd>`
    - `docker.io/<dockerhub-username>/statocyst:latest`
  - Triggers the Prod deploy hook.

### Docker Hub Credentials

Set in GitHub:
- `DOCKERHUB_TOKEN` (secret, required)
- `DOCKERHUB_USERNAME` (repository variable recommended; secret also supported)

### GitHub Environments

Create:
- `vnext`
- `prod`

For each environment, set:
- `DEPLOY_HOOK_URL` (secret, required)
- `DEPLOY_HOOK_BEARER_TOKEN` (secret, optional)
- `HEALTHCHECK_URL` (variable, optional)
  - Example VNext: `https://hub.molten-qa.site/health`
  - Example Prod: `https://hub.molten.bot/health`

### Deploy Hook Payload

The workflow POSTs JSON with:
- `service`
- `environment`
- `image_ref`
- `git_sha`
- `canonical_base_url` (when `STATOCYST_CANONICAL_BASE_URL` is set in workflow env)

If your deploy target ignores JSON payloads, configure it to pull:
- VNext: `vnext`
- Prod: `latest`

Recommended env values:
- VNext: `STATOCYST_CANONICAL_BASE_URL=https://hub.molten-qa.site`
- Prod: `STATOCYST_CANONICAL_BASE_URL=https://hub.molten.bot`
