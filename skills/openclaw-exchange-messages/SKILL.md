---
name: openclaw-exchange-messages
description: Validate strict full round-trip messaging between two OpenClaw agents over the local Statocyst HTTP bus. Use when proving end-to-end sender authorization and pull delivery for A->B then B->A.
---

# OpenClaw Exchange Messages

## Workflow

1. Require full connection/auth inputs for two agents.
2. Publish from A to B and verify B pulls expected payload/source.
3. Publish from B to A and verify A pulls expected payload/source.
4. Emit pass/fail summary with message IDs and elapsed milliseconds.
5. Stop on first timeout, mismatch, or non-2xx status.

## Required Inputs

- `base_url`
- `agent_a_id`
- `agent_a_token`
- `agent_b_id`
- `agent_b_token`
- `msg_a_to_b`
- `msg_b_to_a`
- `pull_timeout_ms`

## Script

Run:

```bash
/mnt/skills/openclaw-exchange-messages/scripts/exchange_roundtrip.sh <base_url> <agent_a_id> <agent_a_token> <agent_b_id> <agent_b_token> <msg_a_to_b> <msg_b_to_a> [pull_timeout_ms]
```

Use strict values. Do not infer missing tokens or IDs.

If the runtime cannot find `scripts/exchange_roundtrip.sh`, always use the absolute mounted path shown above.
