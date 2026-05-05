# Runtime Configuration

See also: [README](../README.md) | [Development Guide](./development.md) | [API Usage](./api-usage.md) | [Web UI Routes](./web-ui.md) | [Release and Deployment](./release.md)

## Human Auth Provider

- `HUMAN_AUTH_PROVIDER=dev` (default)
  - Uses `X-Human-Id` and `X-Human-Email` request headers.
- `HUMAN_AUTH_PROVIDER=supabase`
  - Uses Supabase JWT bearer tokens.
  - Requires `SUPABASE_URL` and `SUPABASE_ANON_KEY`.
  - Validates tokens via Supabase `/auth/v1/user`.

Admin identity controls:
- `SUPER_ADMIN_EMAILS=root@example.com,ops@example.com` (recommended)
- `SUPER_ADMIN_DOMAINS=example.com` (broader; optional)
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
- `MOLTENHUB_MAX_METADATA_BYTES=196608` (default `192KB`)

Scheduler machine auth:
- `MOLTENHUB_SCHEDULER_API_KEY=<secret>` enables `POST /v1/scheduler/agents/{agent_uuid}/dispatch` for one trusted scheduler service.
- `MOLTENHUB_SCHEDULER_API_KEYS=<secret1>,<secret2>` accepts multiple scheduler keys for rotation. Values may be comma or newline separated.
- Scheduler keys are bearer tokens and are never exposed through `/v1/ui/config`. When no scheduler key is configured, scheduler dispatch stays disabled.
- This endpoint only accepts the final dispatch message. Schedule creation, persistence, cancellation, and timing live outside MoltenHub.

Browser API CORS:
- `MOLTENHUB_ENABLE_LOCAL_CORS=true`: allows local testing origins (`localhost`, `127.0.0.1`, `::1`, plus `Origin: null` from `file://`).
- `MOLTENHUB_CORS_ALLOWED_ORIGINS=app.example.com,app.qa.example.com`: explicit allowed browser origins via host shorthand.
- Host shorthand entries allow both `http://` and `https://` for that host. You can also provide full `http://` or `https://` origins; values must be comma-separated and must not include paths, queries, or fragments.

Canonical URI authority:
- `MOLTENHUB_CANONICAL_BASE_URL=https://hub.example.com`
- If omitted, `uri` fields are omitted.

## State Backend

- `MOLTENHUB_STATE_BACKEND=memory` (default): in-process volatile state.
- `MOLTENHUB_STATE_BACKEND=s3`: S3-backed beta state store.
  - Required: `MOLTENHUB_STATE_S3_ENDPOINT`, `MOLTENHUB_STATE_S3_BUCKET`
  - Optional: `MOLTENHUB_STATE_S3_REGION` (default `us-east-1`), `MOLTENHUB_STATE_S3_PREFIX` (default `moltenhub-state`), `MOLTENHUB_STATE_S3_PATH_STYLE=true`, `MOLTENHUB_STATE_S3_ACCESS_KEY_ID`, `MOLTENHUB_STATE_S3_SECRET_ACCESS_KEY`
  - Requests are SigV4-signed when access key + secret key are set; otherwise unsigned.
  - Current S3 mode is beta and designed for a single writer instance.

Startup behavior:
- `MOLTENHUB_STORAGE_STARTUP_MODE=strict` (default): startup fails if configured storage is invalid/unreachable.
- `MOLTENHUB_STORAGE_STARTUP_MODE=degraded`: falls back to memory for failing backends and reports failures in `/health`.
- HTTP listener starts before S3 hydration completes.
  - Early routes while booting: `/ping`, `/health`, `/openapi.yaml`, `/openapi.md`, `/v1/ui/config`, `/v1/me`.
  - `/v1/me` serves identity-only startup payload while booting; writes remain unavailable until ready.
  - Use `/ping` for liveness and `/health` for readiness/dependencies.

S3 state hydration tuning:
- `MOLTENHUB_S3_HYDRATION_TIMEOUT_SEC=20` (default): upper bound for strict startup hydration.
- `MOLTENHUB_S3_HYDRATION_LIST_CONCURRENCY=6` (default): parallel list workers during hydration.
- `MOLTENHUB_S3_HYDRATION_GET_CONCURRENCY=24` (default): parallel object fetch workers during hydration.

## Queue Backend

- `MOLTENHUB_QUEUE_BACKEND=memory` (default): in-process volatile queue.
- `MOLTENHUB_QUEUE_BACKEND=s3`: object-backed queue keyed by `agent_uuid`.
  - Required: `MOLTENHUB_QUEUE_S3_ENDPOINT`, `MOLTENHUB_QUEUE_S3_BUCKET`
  - Optional: `MOLTENHUB_QUEUE_S3_REGION` (default `us-east-1`), `MOLTENHUB_QUEUE_S3_PREFIX` (default `moltenhub-queue`), `MOLTENHUB_QUEUE_S3_PATH_STYLE=true`, `MOLTENHUB_QUEUE_S3_ACCESS_KEY_ID`, `MOLTENHUB_QUEUE_S3_SECRET_ACCESS_KEY`
  - Queue S3 config is independent from state S3 config.
  - Requests are SigV4-signed when key + secret are set; otherwise unsigned.
  - In `strict` startup mode, moltenhub now preflights queue bucket reachability before reporting ready.
