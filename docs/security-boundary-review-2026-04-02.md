# Security Boundary Review (2026-04-02)

## Scope

This review focused on actor boundary enforcement across:

- Humans (control-plane authenticated users)
- Organizations (membership and role-scoped resources)
- Agents (runtime bearer-token authenticated principals)
- Federated peers (peer-signed cross-hub delivery)

Primary objective: ensure no actor can assume another actor's role or identity through API interactions.

## Method

- Reviewed authentication entry points (`internal/auth`, API auth helpers).
- Traced authorization flows in control-plane and runtime handlers (`internal/api`).
- Audited policy enforcement in store layer (`internal/store/memory.go`), where most role checks execute.
- Added regression tests for discovered boundary issues.

## Findings And Fixes

### 1) Org member could assign `owner_human_id` to other humans via bind tokens

Severity: High

Impact:

- A non-admin org member could create org-scoped bind tokens that claim ownership for another human.
- This enabled identity confusion by binding an agent that appears human-owned by someone else.

Fix:

- Tightened `CreateBindToken` authorization in the store:
  - Non-admins may only assign `owner_human_id` to themselves.
  - Admin/owner (or super-admin) can assign ownership to other active org members.

Changed file:

- `internal/store/memory.go`

Regression test:

- `TestMemoryStoreOrgBindTokenOwnerAssignmentRequiresAdminForOthers` in `internal/store/agent_handles_test.go`

### 2) Invite listing leaked data when caller email was empty

Severity: Medium

Impact:

- `ListInvitesForHuman` filtered by email only when email was non-empty.
- A non-admin human with empty email could see all invites.

Fix:

- Enforced strict recipient email match for non-admin invite listing.
- Non-admin callers with empty email now receive no invites.

Changed file:

- `internal/store/memory.go`

Regression test:

- `TestMemoryStoreListInvitesForHumanRequiresRecipientEmailWhenNotAdmin` in `internal/store/agent_handles_test.go`

### 3) Federated org-trust namespace parsing conflated `human/*` prefixes

Severity: Medium

Impact:

- Remote org handle derivation treated any `human/...` agent ref as personal namespace.
- Org handles starting with `human` could be mis-scoped during federated trust checks.

Fix:

- Updated remote org derivation to treat only canonical personal refs
  (`human/<owner>/agent/<agent>`) as personal namespace.
- Other refs now use first path segment as org handle.

Changed file:

- `internal/api/handlers_federation.go`

Regression test:

- `TestRemoteOrgHandleFromAgentRefRecognizesOnlyPersonalPattern` in `internal/api/federation_test.go`

## Additional Observations

- Human and agent credential classes are correctly separated by route classes and auth methods.
- Agent runtime routes consistently bind caller identity from bearer token lookup, not request body fields.
- Dev human auth (`X-Human-Id`, `X-Human-Email`) is intentionally non-production and should remain environment-restricted.

## Validation

Run:

```bash
go test ./...
```

This should exercise the new regression tests and existing authorization/caller contract tests.
