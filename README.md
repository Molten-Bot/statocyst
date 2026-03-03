# statocyst

Local POC backplane service for OpenClaw agents.

## What This POC Does

- Registers agents with client-provided IDs.
- Issues per-agent bearer tokens.
- Enforces explicit bilateral bonds between agents.
- Supports HTTP publish and long-poll pull using an in-memory FIFO queue.

Delivery model: at-most-once, best-effort, in-memory only.

## Run the Service

```bash
go run ./cmd/statocystd
```

Optional bind address:

```bash
STATOCYST_ADDR=:8080 go run ./cmd/statocystd
```

## API Quick Reference

### OpenAPI spec

```bash
curl -sS http://localhost:8080/openapi.yaml
```

### Register agent

```bash
curl -sS -X POST http://localhost:8080/v1/agents/register \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"agent-a"}'
```

### Create/join bond

Run this once from each side. The first call creates a pending bond, the second activates it.

```bash
curl -sS -X POST http://localhost:8080/v1/bonds \
  -H "Authorization: Bearer <token-for-agent-a>" \
  -H 'Content-Type: application/json' \
  -d '{"peer_agent_id":"agent-b"}'
```

### Publish

```bash
curl -sS -X POST http://localhost:8080/v1/messages/publish \
  -H "Authorization: Bearer <token-for-agent-a>" \
  -H 'Content-Type: application/json' \
  -d '{"to_agent_id":"agent-b","content_type":"text/plain","payload":"hello"}'
```

If no active bond exists, publish returns `202` with:

```json
{"status":"dropped","reason":"no_active_bond"}
```

### Pull (long-poll)

```bash
curl -sS -i -X GET 'http://localhost:8080/v1/messages/pull?timeout_ms=5000' \
  -H "Authorization: Bearer <token-for-agent-b>"
```

## Manual Two-Agent Validation Runbook

1. Register `agent-a` and `agent-b`; save each token.
2. Create the same bond from both sides:
- `agent-a` joins with `peer_agent_id=agent-b`.
- `agent-b` joins with `peer_agent_id=agent-a`.
3. Start pull requests for both agents in separate terminals.
4. Publish `agent-a -> agent-b` and verify `agent-b` receives.
5. Publish `agent-b -> agent-a` and verify `agent-a` receives.
6. Negative test: delete bond (`DELETE /v1/bonds/{id}`), then publish and verify dropped response.

## OpenClaw Skills

Project-owned skills live in:

- `skills/openclaw-bind-agent`
- `skills/openclaw-exchange-messages`

Scripts:

```bash
skills/openclaw-bind-agent/scripts/bind_agent.sh
skills/openclaw-exchange-messages/scripts/exchange_roundtrip.sh
```

## LLM-Agnostic Setup (Recommended)

For real-world environments where model behavior varies, initialize agents without any LLM prompt:

```bash
./multi-agent/bootstrap_agents.sh
```

What it does:
- Recreates `statocyst` for a clean in-memory registry.
- Registers `crab` and `shrimp` directly via API.
- Creates and activates a mutual bond.
- Writes tokens to:
  - `multi-agent-crab-1:/tmp/crab.token`
  - `multi-agent-shrimp-1:/tmp/shrimp.token`
- Runs a round-trip message smoke test.

After this succeeds, LLM prompts are optional rather than required for bring-up.
