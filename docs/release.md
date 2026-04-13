# Release and Deployment

See also: [README](../README.md) | [Runtime Configuration](./runtime-configuration.md) | [Development Guide](./development.md) | [API Usage](./api-usage.md) | [Web UI Routes](./web-ui.md)

## Tests

Run tests in the existing `multi-agent` MoltenHub container:

```bash
docker exec multi-agent-moltenhub-1 sh -lc 'cd /app && /usr/local/go/bin/go test ./...'
```

## nginx Layering

Recommended deployment shape:
- keep the existing MoltenHub Docker image as the application container
- place nginx in front of it as a sidecar or ingress layer when you need edge buffering, TLS termination, or an outer proxy shield

Why this shape is preferred over making nginx the base image:
- the app stays a single-process Go runtime with direct health/startup semantics
- websocket upgrades and runtime error contracts remain owned by MoltenHub
- you avoid bundling process supervision for nginx plus the app into one container
- nginx can still add a first-pass edge limit while MoltenHub keeps the canonical per-IP runtime limit

Example companion config:
- `deploy/nginx/default.conf`

If nginx is forwarding client IPs, set `MOLTENHUB_RATE_LIMIT_TRUST_PROXY_HEADERS=true` in the MoltenHub container so the app keys rate limiting off `X-Forwarded-For` / `X-Real-IP` instead of the proxy address.

## Release Pipeline

This repository builds, tests, and publishes Docker images.
Hosted environment deployment is managed in the separate infra repository (`moltenhub-scale`).

### Workflows

- `.github/workflows/ci.yml`
  - Runs on PRs, `main`, and `moltenhub-*` branch pushes so remediation branches publish checks before merge.
  - Validates unit/integration tests, Docker buildability, direct-container smoke, nginx-fronted smoke, and federation smoke.
- `.github/workflows/deploy-vnext.yml`
  - Auto-publishes the VNext image on pushes to `main`.
  - Builds and pushes:
    - `docker.io/<dockerhub-username>/moltenhub:vnext`
    - `docker.io/<dockerhub-username>/moltenhub:vnext-<yyyymmdd>`
  - Runs container smoke checks before publish.
- `.github/workflows/deploy-prod.yml`
  - Manual only (`workflow_dispatch`), restricted to `main`.
  - Promotes the current `vnext` digest (no rebuild) to:
    - `docker.io/<dockerhub-username>/moltenhub:<yyyymmdd>`
    - `docker.io/<dockerhub-username>/moltenhub:latest`
  - Runs container smoke checks before promotion.

### Docker Hub Credentials

Set in GitHub:
- `DOCKERHUB_TOKEN` (secret, required)
- `DOCKERHUB_USERNAME` (repository variable recommended; secret also supported)
