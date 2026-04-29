# Security Boundary Review - 2026-04-29

## Scope

Reviewed authentication, authorization, input validation, secrets management, dependency exposure, and data-flow trust boundaries for the Go service in this repository.

Material issues were found. This is not a no-op review.

## Findings

### High: Default dev human auth is spoofable and launch validation only warns

Any deployment that omits `HUMAN_AUTH_PROVIDER` defaults to `dev` auth. In that mode, any caller can become any human by sending `X-Human-Id` and can choose email through `X-Human-Email`; the provider also marks the email as verified. If `SUPER_ADMIN_EMAILS` or `SUPER_ADMIN_DOMAINS` is configured, forged dev-mode headers can satisfy super-admin checks.

References:
- `internal/auth/humans.go:29` chooses auth provider from env and falls back to dev.
- `internal/auth/humans.go:52` reads `X-Human-Id` as identity.
- `internal/auth/humans.go:57` reads caller-controlled `X-Human-Email`.
- `internal/auth/humans.go:61` returns `EmailVerified: true`.
- `cmd/moltenhubd/startup_config.go:70` defaults unset provider to `dev`.
- `cmd/moltenhubd/startup_config.go:75` emits warning only, not startup failure.
- `internal/auth/super_admin.go:34` grants admin only after email verification, which dev mode sets true.

Compromise path:
1. External caller reaches API while `HUMAN_AUTH_PROVIDER` is unset or unsupported.
2. Caller sends chosen `X-Human-Id` and `X-Human-Email`.
3. Service upserts that identity and treats email as verified.
4. If email or domain matches admin allowlist, caller gains admin-only actions and snapshot access.

Recommended fix:
Fail startup in non-local deployments unless `HUMAN_AUTH_PROVIDER=supabase`, or require an explicit `MOLTENHUB_ALLOW_DEV_AUTH=true` guard for dev mode. Do not treat header-supplied emails as verified outside local development.

### High: Request bodies and message payloads lack global size limits

Most JSON handlers call `decodeJSON` directly without `http.MaxBytesReader` or another body cap. Message publish accepts arbitrary `payload` strings and persists/enqueues them after auth and trust checks. This creates memory, storage, and queue exhaustion risk from any authenticated agent or spoofed dev-mode identity that can mint or control agents.

References:
- `internal/api/router.go:1023` decodes request body with `json.NewDecoder(r.Body)`.
- `internal/api/router.go:1026` disallows unknown fields but applies no byte limit.
- `internal/api/handlers_messages.go:117` decodes publish request.
- `internal/api/handlers_messages.go:136` validates content type only.
- `internal/api/handlers_messages.go:342` constructs persisted message object.
- `internal/api/handlers_messages.go:352` copies request content type into message.
- `internal/api/handlers_messages.go:353` copies unbounded payload into message.
- `internal/api/handlers_messages.go:380` enqueues message.
- `internal/api/handlers_v1.go:255` enforces metadata-specific limit, showing a bounded pattern exists but is not global.

Compromise path:
1. Authenticated agent submits large JSON body or large `payload`.
2. Decoder and message path allocate and retain attacker-controlled data.
3. Queue/state backend stores or attempts to store oversized messages.
4. Process memory, S3 costs, storage latency, or queue availability degrade.

Recommended fix:
Add per-route body limits with `http.MaxBytesReader`, plus explicit message payload and activity size limits. Return `413` for oversized requests.

### Medium: `/v1/admin/remote-agent-trusts` permits non-admin owners to create remote agent trust

The route name is admin-scoped, but create/list/delete remote-agent trust accepts any authenticated human who owns the local agent. For human-owned agents, explicit remote-agent trust is sufficient for federated publish, without org-level trust. This may be intended delegation, but it is an authorization boundary that lets a compromised normal user opt their owned agent into a configured remote peer relationship.

References:
- `internal/api/router.go:149` mounts `/v1/admin/remote-agent-trusts`.
- `internal/api/handlers_federation.go:259` authenticates human instead of requiring super admin.
- `internal/api/handlers_federation.go:300` allows non-admin actor if they own the local agent.
- `internal/api/handlers_federation.go:334` creates remote agent trust.
- `internal/api/handlers_federation.go:367` permits owner delete path for non-admins.
- `internal/api/handlers_federation.go:399` checks federated trust path.
- `internal/api/handlers_federation.go:403` treats human-owned/personal agents as not needing org scope.

Compromise path:
1. Attacker controls a normal human account.
2. Attacker owns or creates a personal/owned agent.
3. Attacker creates remote-agent trust to a configured peer.
4. Agent can send to that remote peer when peer and target URI satisfy trust checks.

Recommended fix:
Decide policy. If only admins should manage federation, call `requireSuperAdmin` here. If owners may manage remote trust, move route out of `/v1/admin`, document policy, and add explicit audit events.

### Medium: Peer and S3 endpoints allow cleartext HTTP

Canonical peer URLs, delivery URLs, CORS origins, and S3 endpoints accept `http://`. Peer federation carries signed message bodies; S3 carries state, queue data, and optional AWS-style authorization headers. Cleartext transport exposes metadata and payloads to network observers and allows active tampering attempts against availability.

References:
- `internal/api/identity_uri.go:24` accepts `http` and `https` canonical bases.
- `internal/api/handlers_federation.go:130` normalizes admin-supplied peer canonical URL.
- `internal/api/handlers_federation.go:131` normalizes admin-supplied delivery URL.
- `internal/api/handlers_federation.go:427` builds outbound peer delivery URL.
- `internal/api/handlers_federation.go:433` sets JSON content type.
- `internal/api/handlers_federation.go:434` signs outbound message body.
- `internal/store/s3_connection.go:85` accepts `http://` and `https://` S3 endpoints.
- `internal/store/s3_signing.go:86` sets `Authorization` header for signed S3 requests.
- `internal/api/router.go:350` accepts HTTP CORS origins.

Compromise path:
1. Admin configures `http://` peer or S3 endpoint, or local network attacker observes traffic to such endpoint.
2. Message payloads, metadata, object keys, and signed request headers cross network in cleartext.
3. Attacker learns sensitive data and can disrupt delivery/storage.

Recommended fix:
Require HTTPS by default for peer delivery, canonical base, CORS origins, and S3 endpoints. Keep `http://localhost` or explicit `MOLTENHUB_ALLOW_INSECURE_TRANSPORT=true` for local development only.

### Medium: Federation shared secrets are persisted in plaintext

Peer shared secrets are hidden from JSON responses, but they are stored as plaintext in memory and persisted to the S3 state store through full-state persistence. Any state-store reader can recover peer HMAC secrets and forge inbound peer messages inside the allowed timestamp window.

References:
- `internal/model/types.go:99` defines `PeerInstance`.
- `internal/model/types.go:103` hides `SharedSecret` from JSON output only.
- `internal/store/memory.go:2558` creates peer instance.
- `internal/store/memory.go:2579` stores trimmed shared secret.
- `internal/store/s3_state.go:1821` persists created peer through S3 state store.
- `internal/store/s3_state.go:1829` calls `persistAll`.
- `internal/api/handlers_federation.go:570` verifies HMAC using stored shared secret.

Compromise path:
1. Attacker gains read access to state backend or memory dump.
2. Attacker extracts peer shared secret.
3. Attacker signs request to `/v1/peer/messages` with known `peer_id`.
4. Server accepts message if trust path and timestamp checks pass.

Recommended fix:
Store only key material encrypted with KMS or a deployment secret unavailable to state readers. Add key IDs and rotation. Consider hashing only if protocol can verify without needing raw secret; current outbound signing needs recoverable secret.

### Low: Public snapshot defaults entities to public

`/v1/public/snapshot` treats missing or non-boolean `metadata.public` as public. Output is filtered to selected fields, but handles, IDs, memberships, org display names, and selected metadata become publicly enumerable by default. This is a privacy boundary and may surprise operators expecting opt-in public discovery.

References:
- `internal/api/handlers_v1.go:1763` starts public-default logic.
- `internal/api/handlers_v1.go:1765` returns public when metadata key is missing.
- `internal/api/handlers_v1.go:1768` checks boolean type.
- `internal/api/handlers_v1.go:1770` returns public when value is non-boolean.
- `internal/api/handlers_v1.go:2096` exposes public snapshot without auth.
- `internal/api/handlers_v1.go:2106` applies public-default logic to orgs.
- `internal/api/handlers_v1.go:2130` applies public-default logic to humans.
- `internal/api/handlers_v1.go:2207` applies public-default logic to agents.

Compromise path:
1. Operator or caller creates entity without `metadata.public=false`.
2. Entity appears in unauthenticated public snapshot if surrounding membership/org filters pass.
3. External caller enumerates public graph metadata.

Recommended fix:
Make public discovery opt-in (`metadata.public == true`) or document default-public policy prominently in API and runtime docs.

### Low: CORS preflight echoes requested headers for allowlisted origins

For allowlisted origins, CORS middleware copies `Access-Control-Request-Headers` into `Access-Control-Allow-Headers`. Origin validation is explicit, so this is not a standalone bypass, but it broadens browser-callable header surface for any compromised or over-broad allowlisted origin.

References:
- `internal/api/router.go:237` reads request origin.
- `internal/api/router.go:238` checks origin against local/allowlist policy.
- `internal/api/router.go:243` reads requested headers.
- `internal/api/router.go:245` echoes requested headers.
- `internal/api/router.go:308` starts CORS allow check.
- `internal/api/router.go:323` permits local origins when local CORS is enabled.

Compromise path:
1. Trusted web origin is compromised or misconfigured.
2. Browser preflight asks for sensitive custom headers.
3. API allows those headers for that origin.
4. Attacker-controlled JavaScript can call authenticated/keyed APIs if it has access to corresponding tokens or keys.

Recommended fix:
Return a fixed allowed-header set even for allowed origins, or restrict echo to known headers.

## Boundary Notes

Authentication:
- Human auth has two modes: Supabase bearer validation and dev headers. Supabase calls `/auth/v1/user` with `Authorization` and anon key; missing Supabase config fails only when `HUMAN_AUTH_PROVIDER=supabase`.
- Agent auth is bearer token hash lookup. Raw agent tokens are generated with 32 random bytes and stored as SHA-256 hashes.
- Peer auth is HMAC over timestamp, method, path, and body hash with 5 minute skew.

Authorization:
- Org and agent writes depend on authenticated human ownership, membership, or super-admin status.
- Message publish requires sender agent token plus active local or federated trust path.
- Admin snapshot allows either super-admin human or shared `X-Admin-Snapshot-Key`.
- Entity metadata keyed endpoint requires shared `X-Entities-Metadata-Key`.

Input validation:
- Handles and UUIDs use central validation.
- Metadata writes require JSON object shape and max byte limit.
- Most other JSON request bodies lack global byte limits.
- Message content types are restricted to `text/plain` and `application/json`, but payload size is not capped.

Secrets management:
- Agent tokens and bind tokens are random; agent tokens are hashed at rest.
- Invite secrets are hidden from JSON output.
- Peer shared secrets are hidden from JSON output but stored recoverably.
- Supabase anon key is only exposed to UI config when recognized as browser-safe.
- Startup logging redacts env names containing secret/token/key/password/private/bearer.

Dependency exposure:
- Runtime dependencies are small: `github.com/a2aproject/a2a-go/v2`, `github.com/gorilla/websocket`, `github.com/google/uuid`, and `golang.org/x/mod`.
- WebSocket upgrader accepts any origin, but route still requires agent bearer auth before upgrade.
- Local validation could not run Go tests because `go` is unavailable in this runtime.

Data-flow trust boundaries:
- Browser to API: CORS is deny-by-default unless local or configured origins are enabled.
- Human to API: Supabase or dev-header auth decides identity.
- Agent to API: bearer token controls runtime routes and message publish/pull.
- Peer to API: HMAC signed federation ingress controls `/v1/peer/messages`.
- API to peer: outbound federation sends signed message bodies to configured delivery base URLs.
- API to storage: memory or S3 stores control-plane state and queue data.
- Public internet to discovery: public snapshot and public peers are unauthenticated read boundaries.

## Open Questions

- Should production startup fail when human auth is unset or `dev`?
- Should remote-agent trust be admin-only, owner-managed, or split into separate admin and owner routes?
- What maximum message payload size should be enforced for local and federated messages?
- Are `http://` peer/S3 endpoints required outside local development?
- Should public snapshot be opt-in only?
- What rotation and storage model is expected for peer shared secrets?
- Should shared API keys (`X-Admin-Snapshot-Key`, `X-Entities-Metadata-Key`, `X-UI-Config-Key`) support key IDs, expiration, and audit logging?

## Validation

- Read source files with line-numbered references.
- Ran `go test ./...`; validation could not complete because this runtime returned `/bin/sh: go: not found`.
- Tried direct Go vulnerability database lookup with `curl`; validation could not complete because this runtime returned `/bin/sh: curl: not found`.
- No code paths changed; review deliverable only.
