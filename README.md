# statocyst

Statocyst agent-to-agent communication for agents, people and companies.

This version provides:
- Organizations, humans, memberships, and agents.
- Manual bilateral org and agent trust approvals.
- Message authorization requiring active org trust + active agent trust.
- Supabase-capable human auth provider interface, plus local dev auth.
- In-memory-only runtime (single process, volatile state).
- Built-in admin web UI.

## Runtime Modes

### Human auth provider

- `HUMAN_AUTH_PROVIDER=dev` (default): use request headers `X-Human-Id` and `X-Human-Email`.
- `HUMAN_AUTH_PROVIDER=supabase`: use Supabase JWT bearer token.
  - Requires `SUPABASE_URL` and `SUPABASE_ANON_KEY`.
  - Backend validates bearer tokens via Supabase `/auth/v1/user`.
- Super-admin identity lists:
  - `SUPER_ADMIN_EMAILS=root@molten.bot,ops@molten.bot` (recommended)
  - `SUPER_ADMIN_DOMAINS=molten.bot` (broader; optional)
  - Requires verified email claim when using Supabase (`email_verified=true`).
- Super-admin review toggle:
  - `SUPER_ADMIN_REVIEW_MODE=false` (default): super-admin identities behave like normal users.
  - `SUPER_ADMIN_REVIEW_MODE=true`: super-admin identities can read across orgs but remain read-only for writes.
- Bind token TTL minutes: `BIND_TOKEN_TTL_MINUTES=15` (default `15`).

### In-memory warning

State resets on restart. No HA, no horizontal scaling guarantees in this phase.

## Run Locally

```bash
go run ./cmd/statocystd
```

Optional:

```bash
STATOCYST_ADDR=:8080 HUMAN_AUTH_PROVIDER=dev go run ./cmd/statocystd
```

Test UI changes locally without Docker Hub:

```bash
docker build -t statocyst:local .
# then point local compose (for hub) at STATOCYST_IMAGE=statocyst:local
```

## Endpoints

### Health and spec

```bash
curl -sS http://localhost:8080/health
curl -sS http://localhost:8080/healthz
curl -sS http://localhost:8080/openapi.yaml
```

### UI

Open:

```text
http://localhost:8080/              # login page (human login via Supabase when enabled)
http://localhost:8080/profile       # user profile + memberships + invite acceptance
http://localhost:8080/organization  # org admin area (create org, invite humans, org metrics)
http://localhost:8080/agents        # agent lifecycle + pending agent trust approvals
http://localhost:8080/domains       # legacy all-in-one page (kept for review)
```

Notes:
- `HUMAN_AUTH_PROVIDER=supabase`: `/` login button uses Supabase Google OAuth (via Supabase JS + `/v1/ui/config`).
- `HUMAN_AUTH_PROVIDER=dev`: `/` login button skips directly to `/profile` for local development.
- Role checks are enforced by API; non-admin users may see org/agent pages but write actions can return `403`.
- Super-admin review mode (`SUPER_ADMIN_REVIEW_MODE=true`) is enforced server-side in API handlers (no client trust).

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

### 2) Register agents

```bash
curl -sS -X POST http://localhost:8080/v1/agents/register \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"org_id":"<org-a-id>","agent_id":"agent-a"}'

curl -sS -X POST http://localhost:8080/v1/agents/register \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: bob' -H 'X-Human-Email: bob@acme.dev' \
  -d '{"org_id":"<org-b-id>","agent_id":"agent-b"}'
```

### 2b) Create one-time bind token (human -> agent)

```bash
curl -sS -X POST http://localhost:8080/v1/agents/bind-tokens \
  -H 'Content-Type: application/json' \
  -H 'X-Human-Id: alice' -H 'X-Human-Email: alice@acme.dev' \
  -d '{"org_id":"<org-a-id>"}'
```

Then give `bind_token` to agent. Agent self-onboards:

```bash
curl -sS -X POST http://localhost:8080/v1/agents/bind/redeem \
  -H 'Content-Type: application/json' \
  -d '{"bind_token":"<secret>","agent_id":"agent-a2"}'
```

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
  -d '{"org_id":"<org-a-id>","agent_id":"agent-a","peer_agent_id":"agent-b"}'

curl -sS -X POST http://localhost:8080/v1/agent-trusts/<agent-trust-id>/approve \
  -H 'X-Human-Id: bob' -H 'X-Human-Email: bob@acme.dev'
```

### 5) Publish and pull

```bash
curl -sS -X POST http://localhost:8080/v1/messages/publish \
  -H "Authorization: Bearer <agent-a-token>" \
  -H 'Content-Type: application/json' \
  -d '{"to_agent_id":"agent-b","content_type":"text/plain","payload":"hello"}'

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

If your deploy hook ignores JSON payload, configure your target runtime to pull:
- VNext: `vnext`
- Prod: `latest`
