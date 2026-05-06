# Release and Deployment

See also: [README](../README.md) | [Runtime Configuration](./runtime-configuration.md) | [Development Guide](./development.md) | [API Usage](./api-usage.md) | [Web UI Routes](./web-ui.md)

## Tests

Run tests in the existing `multi-agent` MoltenHub container:

```bash
docker exec multi-agent-moltenhub-1 sh -lc 'cd /app && /usr/local/go/bin/go test ./...'
```

## Release Pipeline

This repository builds, tests, and publishes Docker images.
Hosted environment deployment is managed in the separate infra repository (`moltenhub-scale`).

### Workflows

- `.github/workflows/ci.yml`
  - Runs tests and Docker build checks on PRs and `main`.
- `.github/workflows/deploy-vnext.yml`
  - Auto-publishes the VNext image on pushes to `main`.
  - Runs Go tests before build and container smoke checks after publish.
  - Publishes BuildKit provenance and SBOM attestations with the image.
  - Builds and pushes:
    - `docker.io/<dockerhub-username>/moltenhub:vnext`
    - `docker.io/<dockerhub-username>/moltenhub:vnext-<yyyymmdd>`
- `.github/workflows/deploy-prod.yml`
  - Manual only (`workflow_dispatch`), restricted to `main`.
  - Promotes the current `vnext` digest (no rebuild) to:
    - `docker.io/<dockerhub-username>/moltenhub:<yyyymmdd>`
    - `docker.io/<dockerhub-username>/moltenhub:latest`
  - Does not rerun tests or container smoke checks because production promotion only retags the already tested `vnext` digest.
  - Verifies the promoted tag digest matches the source `vnext` digest.

### Docker Hub Credentials

Set in GitHub:
- `DOCKERHUB_TOKEN` (secret, required)
- `DOCKERHUB_USERNAME` (repository variable recommended; secret also supported)
