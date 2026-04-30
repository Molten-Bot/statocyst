# Moltnet Feature Gap Analysis

Date: 2026-04-30

## Follow-Up Context

Previous local task `local-1777588377-000020` completed with `stage=git status=no_changes`.
The log shows the agent treated the request as investigation-only, returned the comparison in
stdout, and wrote no repository artifact. That made the work non-durable and left the harness
without a pull request even though the prompt asked for a full deep-dive comparison.

This document persists that analysis in-repo so the result is reviewable, branchable, and not lost
as terminal-only output.

## Sources Reviewed

- Moltnet README: `../repo-01-moltnet/README.md`
- Moltnet protocol types: `../repo-01-moltnet/pkg/protocol/types.go`
- Moltnet HTTP transport: `../repo-01-moltnet/internal/transport/http.go`
- Moltnet runtime and storage docs:
  - `../repo-01-moltnet/website/src/content/docs/reference/runtime-capabilities.md`
  - `../repo-01-moltnet/website/src/content/docs/reference/storage-and-durability.md`
- MoltenHub README: `../repo-02-moltenhub/README.md`
- MoltenHub API routes: `../repo-02-moltenhub/internal/api/router.go`
- MoltenHub message model: `../repo-02-moltenhub/internal/model/types.go`
- MoltenHub API usage docs: `../repo-02-moltenhub/docs/api-usage.md`
- MoltenHub OpenAPI source: `../repo-02-moltenhub/internal/api/openapi.yaml`

`na.hub.molten.bot.openapi.yaml` was not present in this workspace. The local canonical OpenAPI
source reviewed here is `internal/api/openapi.yaml`.

## Product Shape

Moltnet is a chat-network runtime. Its core abstractions are rooms, threads, direct conversations,
message parts, artifacts, local runtime attachments, and durable conversation history. It is built
for agents and humans sharing conversation surfaces.

MoltenHub is an identity, trust, and delivery control plane. Its core abstractions are humans,
organizations, agents, bind tokens, trust edges, presence, OpenClaw/A2A integration routes, and
directed message queues. It is built for managed agent-to-agent messaging rather than shared chat
workspaces.

The systems overlap at "agents send messages", but they diverge above that layer.

## Moltnet Features Missing In MoltenHub

1. Rooms as first-class group conversations.
   Moltnet exposes room create, membership patch, room history, and room-targeted messages.
   MoltenHub has orgs, agents, trust edges, and publish/pull queues, but no room or channel model.

2. Threads as first-class conversations.
   Moltnet has thread targets, thread listing, and thread message history. MoltenHub records message
   delivery state but does not expose threaded conversation surfaces.

3. Direct conversation objects.
   Moltnet stores and lists DMs with participant IDs, message counts, and history. MoltenHub supports
   directed publish/pull to an agent UUID/URI, but not a durable DM object with shared history.

4. Cursor-paged conversation history APIs.
   Moltnet exposes room, thread, and DM message pages. MoltenHub exposes queue pull/status/records
   mostly around delivery lifecycle, not shared readable conversation history.

5. Multipart message model.
   Moltnet messages use `parts` for text, URL, data, file, image, and audio payloads. MoltenHub
   messages use `content_type` plus string `payload`. Skill activation can carry JSON, but generic
   multipart messages are not modeled.

6. Artifact index.
   Moltnet exposes `/v1/artifacts`. MoltenHub has no comparable artifact catalog.

7. Generic runtime attachment protocol.
   Moltnet exposes `/v1/attach` WebSocket for runtime attachment identity, event flow, and ACKs.
   MoltenHub has an OpenClaw-specific WebSocket adapter, not a generic runtime attachment gateway.

8. Runtime node supervisor.
   Moltnet can start a node and attach OpenClaw, PicoClaw, TinyClaw, Codex, and Claude Code runtimes.
   MoltenHub does not spawn or supervise local runtimes.

9. Conversation-level read/reply policies.
   Moltnet supports read policies such as `all`, `mentions`, and `thread_only`, and reply policies
   such as `auto`, `manual`, and `never`. MoltenHub trust controls who may talk, but has no per-room
   or per-conversation read/reply policy layer.

10. End-user local chat CLI.
    Moltnet includes `connect`, `conversations`, `read`, `participants`, `send`, `skill install`,
    `node`, and `bridge` commands. MoltenHub has server and smoke-test utilities, not a comparable
    local chat client.

11. Local-first conversation storage options.
    Moltnet defaults to SQLite WAL and supports Postgres, JSON, and memory backends for conversation
    state. MoltenHub currently centers on in-memory/S3 state and queue storage for hub operation.

12. Explicit chat network identity model.
    Moltnet uses network IDs and FQIDs for rooms, DMs, threads, agents, and events. MoltenHub uses
    canonical hub URIs for humans, orgs, and agents plus trust edges.

## MoltenHub Strengths Moltnet Lacks

- Human and organization membership management.
- Bind-token onboarding for agents.
- Bilateral organization/agent trust edges.
- Supabase-backed human authentication option.
- Agent discovery, manifests, and generated skill guides.
- A2A JSON-RPC endpoint surface.
- OpenClaw HTTP and WebSocket adapter routes.
- Presence/activity tracking for managed agents.
- Peer-signed federation delivery.
- Agent-facing failure envelopes with `Failure:` and `Error details:` compatibility aliases.

## OpenAPI Integration Behaviors

The local OpenAPI source documents the runtime offline contract at
`POST /v1/openclaw/messages/offline`:

- Caller must use agent bearer auth.
- Request accepts `session_key` and optional `reason`.
- Success marks `metadata.presence.status=offline`, `ready=false`, and records the session key.
- Success appends `agent_presence` activity with `offline` action.
- UI/integrations should render the agent offline when that presence is returned by agent reads.
- Offline does not revoke the agent token.

Implementation matches that contract in `internal/api/handlers_openclaw_realtime.go`:

- `handleOpenClawOffline` authenticates the agent, decodes JSON, normalizes session key, updates
  presence, records OpenClaw adapter usage as `ws_offline`, and returns the agent plus presence.
- `setOpenClawWebSocketPresence` owns presence metadata and activity write behavior.
- WebSocket connect/disconnect paths use the same presence helper, keeping HTTP and WS state
  semantics aligned.

Smoke coverage exists in `cmd/moltenhub-smoke/main.go`:

- `stepOpenClawQueuedOfflineWebSocketDelivery` marks an agent offline, publishes while offline, then
  reconnects websocket and verifies queued delivery.
- `stepOpenClawPresenceHeartbeat` verifies HTTP pull can refresh presence behavior after offline.

## Parity Implication

MoltenHub should not chase Moltnet feature parity by small patches. The missing pieces are product
slabs: shared conversation data model, readable history APIs, multipart/artifact payload model,
runtime attachment gateway, local runtime supervisor, and local chat CLI.

The clean path is to decide whether MoltenHub should remain a trust/delivery hub or grow a chat
network layer. If it grows that layer, rooms, threads, DMs, message parts, artifacts, and history
storage should be introduced together behind explicit API contracts rather than folded into the
current delivery queue model.
