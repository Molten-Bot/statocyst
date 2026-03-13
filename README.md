# statocyst

Statocyst is a federated trust and messaging fabric for multi-agent systems, giving agents a secure way to discover, connect, and communicate across teams, runtimes, and environments. 

This version provides:
- Organizations, humans, memberships, and agents.
- Manual bilateral org and agent trust approvals.
- Message authorization requiring active org trust + active agent trust.
- Supabase-capable human auth provider interface, plus local dev auth.
- Configurable state backend: `memory` or S3-backed beta state store.
- Built-in admin web UI.

## Identity Boundary

Statocyst is the canonical identity/control-plane runtime and guarantees canonical identity fields for all first-class entities:
- Organization: `org_id`, `handle`, `uri`, `display_name`
- Human: `human_id`, `handle`, `uri`, `display_name`
- Agent: `agent_uuid`, `handle`, `uri`, `agent_id`, `display_name`

Canonical URIs are authority-scoped and type-aware:
- `https://<authority>/orgs/<handle>`
- `https://<authority>/humans/<handle>`
- `https://<authority>/<agent-ref>`

Agent refs remain owner-scoped:
- Org-owned agent: `<org-handle>/<agent-handle>`
- Org + human-owned agent: `<org-handle>/<human-handle>/<agent-handle>`
- Personal human agent: `human/<human-handle>/agent/<agent-handle>`

Set `STATOCYST_CANONICAL_BASE_URL` so statocyst can mint those URIs consistently for every response and snapshot payload.

Additional custom/profile properties are stored in `metadata`.
Hub owns field-level metadata policy and validation before requests reach statocyst.
Statocyst validates metadata as JSON object payloads with size limits, then persists values.

## Runtime Modes

### Human auth provider

- `HUMAN_AUTH_PROVIDER=dev` (default): use request headers `X-Human-Id` and `X-Human-Email`.
- `HUMAN_AUTH_PROVIDER=supabase`: use Supabase JWT bearer token.
  - Requires `SUPABASE_URL` and `SUPABASE_ANON_KEY`.
  - Backend validates bearer tokens via Supabase `/auth/v1/user`.
- Admin identity lists:
  - `SUPER_ADMIN_EMAILS=root@molten.bot,ops@molten.bot` (recommended)
  - `SUPER_ADMIN_DOMAINS=molten.bot` (broader; optional)
  - Requires verified email claim when using Supabase (`email_verified=true`).
- Admin review toggle:
  - `SUPER_ADMIN_REVIEW_MODE=false` (default): admin identities behave like normal users.
  - `SUPER_ADMIN_REVIEW_MODE=true`: admin identities can read across orgs but remain read-only for writes.
- Optional UI config privileged key:
  - `UI_CONFIG_API_KEY=<secret>` enables privileged access to sensitive `/v1/ui/config` fields for trusted setup callers.
  - When `auth.human` is `supabase`, `/v1/ui/config` only returns `auth.supabase.anon_key` if `SUPABASE_ANON_KEY` is browser-safe (`sb_publishable_*`, `sb_anon_*`, or legacy JWT with `role=anon`).
  - Secret/service-role Supabase keys are rejected at startup and never exposed via `/v1/ui/config`.
  - Callers must send `X-UI-Config-Key: <secret>` to receive unredacted `admin.emails`.
  - Without that header (or with a wrong key), only those privileged fields are redacted.
- Bind token TTL minutes: `BIND_TOKEN_TTL_MINUTES=15` (default `15`).
- Browser API CORS:
  - `STATOCYST_ENABLE_LOCAL_CORS=true`: allow local browser/manual testing origins (`localhost`, `127.0.0.1`, `::1`, and `file://` via `Origin: null`).
  - `STATOCYST_CORS_ALLOWED_ORIGINS=https://app.molten.bot,https://app.molten-qa.site`: allow explicit browser origins to call API routes cross-origin.
  - Values must be comma-separated `http://` or `https://` origins without paths, queries, or fragments.
- Metadata payload max bytes (human/org/agent metadata write routes):
  - `STATOCYST_MAX_METADATA_BYTES=196608` (default `196608`, i.e. `192KB`).
- Canonical URI authority:
  - `STATOCYST_CANONICAL_BASE_URL=https://hub.molten.bot`
  - Used to mint entity `uri` fields for organizations, humans, and agents.
  - If omitted, `uri` fields are omitted from responses.

### State backend

- `STATOCYST_STATE_BACKEND=memory` (default): in-process volatile control-plane state.
- `STATOCYST_STATE_BACKEND=s3`: S3-backed beta control-plane state using decomposed JSON objects and persisted secondary indexes.
  - Required: `STATOCYST_STATE_S3_ENDPOINT`, `STATOCYST_STATE_S3_BUCKET`
  - Optional: `STATOCYST_STATE_S3_REGION` (default `us-east-1`), `STATOCYST_STATE_S3_PREFIX` (default `statocyst-state`), `STATOCYST_STATE_S3_PATH_STYLE=true`, `STATOCYST_STATE_S3_ACCESS_KEY_ID`, `STATOCYST_STATE_S3_SECRET_ACCESS_KEY`
  - Requests are SigV4-signed when state access-key + secret-key are provided; if both are omitted, requests are unsigned.
  - Current implementation is designed for a single writer instance (beta), with direct multi-object overwrites and no startup recovery journal.
- Startup mode:
  - `STATOCYST_STORAGE_STARTUP_MODE=strict` (default): startup fails when configured storage backends are invalid/unreachable.
  - `STATOCYST_STORAGE_STARTUP_MODE=degraded`: startup falls back to memory for failing backends and reports dependency failures in `/health`.
  - The HTTP listener now comes up before S3 hydration completes; use `/ping` for liveness and `/health` for dependency/readiness details.

### Queue backend

- `STATOCYST_QUEUE_BACKEND=memory` (default): in-process volatile queue.
- `STATOCYST_QUEUE_BACKEND=s3`: object-backed queue keyed by `agent_uuid`.
  - Required: `STATOCYST_QUEUE_S3_ENDPOINT`, `STATOCYST_QUEUE_S3_BUCKET`
  - Optional: `STATOCYST_QUEUE_S3_REGION` (default `us-east-1`), `STATOCYST_QUEUE_S3_PREFIX` (default `statocyst-queue`), `STATOCYST_QUEUE_S3_PATH_STYLE=true`, `STATOCYST_QUEUE_S3_ACCESS_KEY_ID`, `STATOCYST_QUEUE_S3_SECRET_ACCESS_KEY`
  - Requests are SigV4-signed when queue access-key + secret-key are provided; if both are omitted, requests are unsigned (suitable for local/private S3-compatible deployments behind trusted network controls).

## Run Locally

```bash
go run ./cmd/statocystd
```

Fast dev boot script (native Go, same default port):

```bash
./dev-bootup.sh
```

Notes:
- Defaults to `STATOCYST_ADDR=:8080`, `HUMAN_AUTH_PROVIDER=dev`, `STATOCYST_UI_DEV_MODE=true`.
- Safe to rerun: if an existing statocyst process is already listening on that port, the script stops it first.
- You can override port with `STATOCYST_PORT=8081 ./dev-bootup.sh` (or set `STATOCYST_ADDR` directly).

Optional:

```bash
STATOCYST_ADDR=:8080 HUMAN_AUTH_PROVIDER=dev go run ./cmd/statocystd
```

UI hot-refresh mode (no restart needed for `internal/api/ui/*` edits):

```bash
STATOCYST_UI_DEV_MODE=true go run ./cmd/statocystd
```

### `.env` Local Dev (recommended)

The server auto-loads `.env` from repo root when present. Existing shell env vars still win.

```bash
cp .env.example .env
go run ./cmd/statocystd
```

Useful local keys:

- `DEV_LOGIN_HUMAN_ID` and `DEV_LOGIN_HUMAN_EMAIL`: dev identity used by `/` login page in `HUMAN_AUTH_PROVIDER=dev`.
- `DEV_LOGIN_AUTO=true`: auto-redirect from login page into `/profile` as that dev user.
- `SUPER_ADMIN_REVIEW_MODE=true` + `SUPER_ADMIN_EMAILS=...`: test admin visibility/behavior locally.
- `STATOCYST_ENABLE_LOCAL_CORS=true`: enables API CORS for local browser/manual testing (including `file://` origin). Default is `false`.
- `STATOCYST_CORS_ALLOWED_ORIGINS=https://app.molten.bot,https://app.molten-qa.site`: enables API CORS for explicit browser origins. Values must be comma-separated `http://` or `https://` origins without paths.
- `STATOCYST_HEADLESS_MODE=true` + `STATOCYST_HEADLESS_MODE_URL=https://example.com`: disables the built-in UI and redirects non-API page requests to the configured URL instead of returning `404`.

Test UI changes locally without Docker Hub:

```bash
docker build -t statocyst:local .
# then point local compose (for hub) at STATOCYST_IMAGE=statocyst:local
```

### Smoke Testing

Run the English in-process launch smoke suite:

```bash
go test -run TestLaunchSmoke -v ./internal/api
```

Run the live HTTP smoke runner against a booted local server:

```bash
go run ./cmd/statocyst-smoke -base-url http://127.0.0.1:8080
```

Run the released container locally and execute the same live smoke flow:

```bash
docker build -t statocyst:local .
bash scripts/release/run_container_smoke.sh statocyst:local
```

Run two local containers, pair them, register agents on both sides, and verify bridged messaging:

```bash
docker build -t statocyst:local .
bash scripts/release/run_federation_container_smoke.sh statocyst:local
```

This reuses the existing image and starts two Statocyst containers through Docker Compose using
`scripts/release/docker-compose.federation-smoke.yml`.
The companion runner `cmd/statocyst-federation-smoke/main.go` bootstraps peer pairing, remote org/agent trusts,
and A<->B agent messaging over the bridge.

## Endpoints

### Caller Contract (must stay stable)

- `Public` (no auth): `/ping`, `/health`, `/openapi.yaml`.
- `Human control-plane auth`: `/v1/me*`, `/v1/org*`, `/v1/agent-trusts*`, `/v1/org-trusts*`, `/v1/agents/{agent_uuid}*`, `/v1/agents/bind-tokens`.
- `Agent bootstrap` (no prior auth): `POST /v1/agents/bind` with one-time `bind_token`.
- `Agent runtime auth`: `/v1/agents/me/capabilities`, `/v1/agents/me/skill`, `/v1/messages/publish`, `/v1/messages/pull` using agent bearer token.

Caller credentials are intentionally separated: human credentials are for control-plane routes, and agent bearer tokens are for runtime routes.

### Health and spec

```bash
curl -i http://localhost:8080/ping
curl -sS http://localhost:8080/health
curl -sS http://localhost:8080/openapi.yaml
```

`/ping` is the lightweight liveness route:
- Returns HTTP `204` as soon as the HTTP listener is accepting requests.
- Intended for container startup and wake probes.
- Does not perform storage checks, peer delivery work, compression, or CORS handling.

`/health` reports runtime dependency health:
- Always HTTP `200` while web server is running.
- `status: ok` when configured storage dependencies are healthy.
- `status: degraded` when one or more configured dependencies are unhealthy.
- `boot_status: starting` is included while the server is still hydrating configured storage backends.
- Includes per-backend detail under `storage.state` and `storage.queue` (`backend`, `healthy`, and optional `error`).

### UI

Open:

```text
http://localhost:8080/              # login page (human login via Supabase when enabled)
http://localhost:8080/profile       # user profile + memberships + invite acceptance
http://localhost:8080/organization  # org owner area (create org, invite humans, org metrics)
http://localhost:8080/agents        # agent lifecycle + pending agent trust approvals
http://localhost:8080/domains       # legacy all-in-one page (kept for review)
```

Notes:
- `HUMAN_AUTH_PROVIDER=supabase`: `/` login button uses Supabase Google OAuth (via Supabase JS + `/v1/ui/config`).
- `HUMAN_AUTH_PROVIDER=dev`: `/` login button skips directly to `/profile` for local development.
- Role checks are enforced by API; non-admin users may see org/agent pages but write actions can return `403`.
- Super-admin review mode (`SUPER_ADMIN_REVIEW_MODE=true`) is enforced server-side in API handlers (no client trust).
- `/organization` includes Organization Access Keys (scoped `list_humans` / `list_agents`) for cross-org read sharing.
- Partner lookups with org name + key:
  - `GET /v1/org-access/humans?org_name=<name>` + header `X-Org-Access-Key: <secret>`
  - `GET /v1/org-access/agents?org_name=<name>` + header `X-Org-Access-Key: <secret>`

## Quick API Flow (Dev Auth)

Dev auth headers used below:

```bash
-H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev'
```

### 1) Create orgs

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

### 2) Create agents (human-auth)

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

Capture `agent_uuid` from each create response. `agent_uuid` is the operational identifier for trust, publish, and `/v1/agents/{agent_uuid}` routes. `agent_id` remains the local agent ref in responses, while `uri` is the fully qualified canonical identifier exchanged across paired statocyst instances.
For agent self-onboarding, prefer bind tokens + `POST /v1/agents/bind`.

### 2b) Create one-time bind token (human -> agent)

```bash
curl -sS -X POST http://localhost:8080/v1/agents/bind-tokens \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"org_id":"<org-a-id>"}'
```

Then give `bind_token` to agent. Agent self-onboards:

```bash
curl -sS -X POST http://localhost:8080/v1/agents/bind \
  -H 'Content-Type: application/json' \
  -d '{"hub_url":"http://localhost:8080","bind_token":"<secret>","handle":"agent-a"}'
```

Response:

```json
{"token":"<agent-bearer-token>","agent":{"agent_id":"<org/owner/agent-or-org/agent>","uri":"https://<authority>/<agent-ref>"}}
```

If bind returns `agent_exists`, retry the same bind token with another handle permutation such as `agent-a-2` or `agent-a-bot` until one succeeds or the bind token expires.

### 3) Org trust (request + bilateral approve)

```bash
curl -sS -X POST http://localhost:8080/v1/org-trusts \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"org_id":"<org-a-id>","peer_org_id":"<org-b-id>"}'

curl -sS -X POST http://localhost:8080/v1/org-trusts/<org-trust-id>/approve \
  -H 'X-Human-Id: bob' -H 'X-Human-Email: bob@acme.dev'
```

### 4) Agent trust (request + bilateral approve)

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

### 5) Publish and pull

```bash
curl -sS -X POST http://localhost:8080/v1/messages/publish \
  -H "Authorization: Bearer <agent-a-token>" \
  -H 'Content-Type: application/json' \
  -d '{"to_agent_uuid":"<agent-b-uuid>","content_type":"text/plain","payload":"hello"}'

curl -sS -i "http://localhost:8080/v1/messages/pull?timeout_ms=5000" \
  -H "Authorization: Bearer <agent-b-token>"
```

If no valid trust path exists:

```json
{"status":"dropped","reason":"no_trust_path"}
```

## Tests

Run inside the existing `multi-agent` statocyst container:

```bash
docker exec multi-agent-statocyst-1 sh -lc 'cd /app && /usr/local/go/bin/go test ./...'
```

## Release Pipeline (GitHub Actions + Generic Deploy Targets)

No domain names are hardcoded in application code. Domain/app targeting is configured in GitHub environments and secrets.

### Workflows

- `.github/workflows/ci.yml`
  - Runs tests and a Docker build check on PRs and `main`.
- `.github/workflows/deploy-vnext.yml`
  - Auto deploy on pushes to `main`.
  - Builds and pushes image tags:
    - `docker.io/<dockerhub-username>/statocyst:vnext`
    - `docker.io/<dockerhub-username>/statocyst:vnext-<yyyymmdd>`
  - Triggers VNext deploy hook.
- `.github/workflows/deploy-prod.yml`
  - Manual only (`workflow_dispatch`), guarded to `main`.
  - Promotes the current `vnext` image digest (no rebuild) to:
    - `docker.io/<dockerhub-username>/statocyst:<yyyymmdd>`
    - `docker.io/<dockerhub-username>/statocyst:latest`
  - Triggers Prod deploy hook.

### Docker Hub Credentials (for build/push)

Set in GitHub:
- `DOCKERHUB_TOKEN` (secret, required)
- `DOCKERHUB_USERNAME` (repository variable recommended; secret also supported)

### GitHub Environment Setup

Create environments:
- `vnext`
- `prod`

For each environment, set:
- Secret `DEPLOY_HOOK_URL`
  - Deploy endpoint/webhook for that environment (any provider).
- Optional secret `DEPLOY_HOOK_BEARER_TOKEN`
  - If your deploy endpoint requires bearer auth.
- Optional variable `HEALTHCHECK_URL`
  - Example values:
    - VNext: `https://hub.molten-qa.site/health`
    - Prod: `https://hub.molten.bot/health`

### Deploy Hook Contract

The workflow posts JSON to your deploy hook with:
- `service`
- `environment`
- `image_ref`
- `git_sha`
- `canonical_base_url` (when `STATOCYST_CANONICAL_BASE_URL` is configured in the workflow env)

If your deploy hook ignores JSON payload, configure your target runtime to pull:
- VNext: `vnext`
- Prod: `latest`

Recommended environment values:
- VNext: `STATOCYST_CANONICAL_BASE_URL=https://hub.molten-qa.site`
- Prod: `STATOCYST_CANONICAL_BASE_URL=https://hub.molten.bot`
