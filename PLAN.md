# Statocyst V2 Plan

## Summary

Statocyst V2 is an agent-first control plane and messaging API. The goal is to make agent bootstrap, discovery, and API calling succeed with minimal prompt glue and minimal out-of-band documentation.

V2 remains JSON-first for runtime correctness, adds markdown as a negotiated discovery format, and keeps the authenticated agent surface self-describing.

## Status Snapshot (Updated 2026-03-13)

Completed in PR #76 (`d0815c8`):

- added canonical discovery route `GET /v1/agents/me/manifest`
- implemented deterministic discovery negotiation:
  - JSON default
  - markdown via `Accept: text/markdown` or `?format=markdown`
- derived manifest, capabilities, and skill from shared typed discovery data
- expanded `GET /v1/agents/me/capabilities` into route/capability/constraint contract data
- added request correlation on API responses (`X-Request-ID`) and reflected IDs in error bodies
- added error metadata (`error_detail`, `retryable`, `next_action` for key classes)
- enforced media-type behavior for agent mutating routes (`415` for non-JSON)
- documented discovery/runtime changes in OpenAPI
- added/updated tests for manifest JSON+markdown, negotiation, and error correlation metadata

## Core Principles

- JSON is the source of truth for runtime APIs and all mutating requests.
- Markdown is a negotiated read format for discovery, help, and agent guidance.
- Discovery is built into the authenticated agent surface.
- Error handling is machine-usable and stable across agent routes.
- Public liveness/readiness routes remain minimal and stable.
- Human and agent credential classes remain strictly separated.

## Remaining V2 Work

- none for current V2 scope

## Competitive Features Backlog (From ClawForge Review)

Prioritized candidates to evaluate after V2 completion:

1. `P0` Emergency kill switch for agent/tool execution
   Add org-level kill switch state with optional operator message, and expose machine-readable kill-switch state in discovery/runtime status routes.

2. `P0` Authenticated service-account API keys
   Add issue/revoke flows for scoped machine-to-machine API keys, with expiry and optional IP allowlist constraints.

3. `P1` Fleet heartbeat + presence inventory
   Track active client heartbeats with online/offline reporting, and use version checks to trigger policy/config refresh.

4. `P1` Real-time policy/event push channel
   Add SSE push for policy changes and kill-switch propagation, with heartbeat fallback when streaming is unavailable.

5. `P1` Read-only operator role
   Introduce viewer RBAC for audit/read routes without mutation rights, while keeping policy/identity changes admin-only.

6. `P2` Enrollment token onboarding flow
   Support time-bounded and usage-bounded enrollment tokens for new users/agents, returning an initial credential bundle on success.

7. `P2` Audit retention controls + operational export
   Add configurable retention windows/cleanup jobs and an exportable audit query surface for SIEM and operations workflows.

## Non-Goals

- markdown request bodies for mutating runtime routes
- blending human and agent auth models
- moving liveness/readiness behavior into documentation logic
- relying on external docs as the only source of agent discovery

## Acceptance Criteria

V2 is successful when a newly bound agent can:

- fetch a canonical manifest in JSON
- fetch equivalent guidance in markdown
- determine which routes it may call and how
- publish and pull messages without out-of-band docs
- receive structured failures that identify what went wrong and what to do next

## Test Plan

Already covered:

- manifest JSON responses
- manifest markdown responses
- skill markdown behavior derived from shared discovery data
- capability contract route coverage
- discovery negotiation failures (`406`)
- request correlation metadata in error payloads
- `/ping` and `/health` regression protection via full test suite

Remaining additions:

- none; Milestone 2 and Milestone 3 contract/docs coverage landed

## Defaults

- V2 may refactor existing runtime routes directly (no parallel versioning required).
- JSON remains canonical for runtime protocol.
- Markdown is additive for discovery/help surfaces.
- Cloudflare Markdown for Agents is optional distribution enhancement, not the primary API contract.
