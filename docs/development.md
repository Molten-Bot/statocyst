# Development Guide

See also: [README](../README.md) | [Runtime Configuration](./runtime-configuration.md) | [API Usage](./api-usage.md) | [Web UI Routes](./web-ui.md) | [Release and Deployment](./release.md)

## Run Locally

Quick start:

```bash
go run ./cmd/moltenhubd
```

Fast dev boot script (native Go, same default port):

```bash
./dev-bootup.sh
```

Script defaults:
- `MOLTENHUB_ADDR=:8080`
- `HUMAN_AUTH_PROVIDER=dev`
- `MOLTENHUB_UI_DEV_MODE=true`

Notes:
- Safe to rerun: if a process is already using the port, the script stops it first.
- Override port with `MOLTENHUB_PORT=8081 ./dev-bootup.sh` (or set `MOLTENHUB_ADDR` directly).

Optional explicit launch:

```bash
MOLTENHUB_ADDR=:8080 HUMAN_AUTH_PROVIDER=dev go run ./cmd/moltenhubd
```

UI hot-refresh mode (for `internal/api/ui/*` edits):

```bash
MOLTENHUB_UI_DEV_MODE=true go run ./cmd/moltenhubd
```

## `.env` Local Dev (Recommended)

MoltenHub auto-loads `.env` from repo root when present. Existing shell vars still take precedence.

```bash
cp .env.example .env
go run ./cmd/moltenhubd
```

Useful local keys:
- `DEV_LOGIN_HUMAN_ID`, `DEV_LOGIN_HUMAN_EMAIL`: dev identity used by `/` login in `HUMAN_AUTH_PROVIDER=dev`.
- `DEV_LOGIN_AUTO=true`: auto-redirect from login page to `/profile`.
- `SUPER_ADMIN_REVIEW_MODE=true` + `SUPER_ADMIN_EMAILS=...`: test admin review behavior.
- `MOLTENHUB_ENABLE_LOCAL_CORS=true`: enable API CORS for local browser/manual testing (including `file://`).
- `MOLTENHUB_CORS_ALLOWED_ORIGINS=app.example.com,app.qa.example.com`: allow explicit browser origins via host shorthand (host entries allow both `http://` and `https://`; full origins are also accepted).
- `MOLTENHUB_HEADLESS_MODE=true` + `MOLTENHUB_HEADLESS_MODE_URL=https://example.com`: disable built-in UI and redirect non-API pages.

Test local image quickly:

```bash
docker build -t moltenhub:local .
# then point local compose (for hub) at MOLTENHUB_IMAGE=moltenhub:local
```

## Smoke Testing

Run in-process launch smoke tests:

```bash
go test -run TestLaunchSmoke -v ./internal/api
```

Run live HTTP smoke tests against a local server:

```bash
go run ./cmd/moltenhub-smoke -base-url http://127.0.0.1:8080
```

Run container smoke tests:

```bash
docker build -t moltenhub:local .
bash scripts/release/run_container_smoke.sh moltenhub:local
```

Run S3-backed container smoke tests with MinIO:

```bash
docker build -t moltenhub:local .
bash scripts/release/run_s3_container_smoke.sh moltenhub:local
```

Run federation smoke tests (two local containers, trust setup, bridged messaging):

```bash
docker build -t moltenhub:local .
bash scripts/release/run_federation_container_smoke.sh moltenhub:local
```

This uses `scripts/release/docker-compose.federation-smoke.yml`.
The runner `cmd/moltenhub-federation-smoke/main.go` bootstraps pairing, trust approvals, and A<->B messaging.

## Federated Latency SLO

Current launch SLO target:
- federated end-to-end delivery p95 (`publish` on sender to successful `pull` on receiver) < `10s` in both directions.

Run the SLO check against live NA/EU synthetic agents:

```bash
bash scripts/release/run_federation_latency_slo.sh
```

Script defaults:
- reads tokens and agent URIs from:
  - `~/.codex/memories/moltenbot_na_hive_bind_session_codex-synth-a.json`
  - `~/.codex/memories/moltenbot_eu_hive_bind_session_codex-synth-b.json`
- threshold `SLO_MS=10000`
- `ITERATIONS=10`

Useful overrides:

```bash
SLO_MS=10000 ITERATIONS=15 VERBOSE=true \
NA_TOKEN=... EU_TOKEN=... NA_URI=... EU_URI=... \
bash scripts/release/run_federation_latency_slo.sh
```
