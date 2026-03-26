package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"statocyst/internal/model"
)

type seqID struct {
	n int
}

func (s *seqID) next() (string, error) {
	s.n++
	return fmt.Sprintf("id-%d", s.n), nil
}

func (s *seqID) mustID(t *testing.T) string {
	t.Helper()
	id, err := s.next()
	if err != nil {
		t.Fatalf("id generation failed: %v", err)
	}
	return id
}

func mustCreateHuman(t *testing.T, mem *MemoryStore, ids *seqID, subject, email, handle string, now time.Time) model.Human {
	t.Helper()
	h, err := mem.UpsertHuman("dev", subject, email, true, now, ids.next)
	if err != nil {
		t.Fatalf("UpsertHuman(%q) failed: %v", subject, err)
	}
	if handle != "" {
		h, err = mem.UpdateHumanProfile(h.HumanID, handle, true, now)
		if err != nil {
			t.Fatalf("UpdateHumanProfile(%q) failed: %v", handle, err)
		}
	}
	return h
}

func mustCreateOrg(t *testing.T, mem *MemoryStore, ids *seqID, creator model.Human, handle, displayName string, now time.Time) model.Organization {
	t.Helper()
	org, _, err := mem.CreateOrg(handle, displayName, creator.HumanID, ids.mustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg(%q) failed: %v", handle, err)
	}
	return org
}

func TestMemoryStoreAgentCanonicalURIAndScopedUniqueness(t *testing.T) {
	now := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	bob := mustCreateHuman(t, mem, ids, "bob", "bob@b.test", "bob", now)
	orgA := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)
	orgB := mustCreateOrg(t, mem, ids, alice, "org-b", "Org B", now)
	orgC := mustCreateOrg(t, mem, ids, bob, "org-c", "Org C", now)

	humanOwnedA, err := mem.RegisterAgent(orgA.OrgID, "Alpha", &alice.HumanID, "tok-alpha-a", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register human-owned agent failed: %v", err)
	}
	if humanOwnedA.AgentUUID == "" {
		t.Fatalf("expected agent_uuid to be set")
	}
	if humanOwnedA.Handle != "alpha" {
		t.Fatalf("expected normalized handle alpha, got %q", humanOwnedA.Handle)
	}
	if humanOwnedA.AgentID != "org-a/alice/alpha" {
		t.Fatalf("expected canonical agent URI org-a/alice/alpha, got %q", humanOwnedA.AgentID)
	}
	uri, err := mem.GetAgentURI(humanOwnedA.AgentUUID)
	if err != nil {
		t.Fatalf("GetAgentURI failed: %v", err)
	}
	if uri != humanOwnedA.AgentID {
		t.Fatalf("expected uri %q got %q", humanOwnedA.AgentID, uri)
	}

	if _, err := mem.RegisterAgent(orgA.OrgID, "alpha", &alice.HumanID, "tok-alpha-a-2", alice.HumanID, now, false); !errors.Is(err, ErrAgentExists) {
		t.Fatalf("expected duplicate in same human scope to fail with ErrAgentExists, got %v", err)
	}

	humanOwnedB, err := mem.RegisterAgent(orgB.OrgID, "alpha", &alice.HumanID, "tok-alpha-b", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("same handle for same human across org should be allowed, got %v", err)
	}
	if humanOwnedB.AgentID != "org-b/alice/alpha" {
		t.Fatalf("expected canonical agent URI org-b/alice/alpha, got %q", humanOwnedB.AgentID)
	}

	if _, err := mem.RegisterAgent(orgC.OrgID, "alpha", &bob.HumanID, "tok-alpha-c", bob.HumanID, now, false); err != nil {
		t.Fatalf("same handle in a different human scope should be allowed, got %v", err)
	}

	orgOwnedA, err := mem.RegisterAgent(orgA.OrgID, "ops", nil, "tok-ops-a", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register org-owned agent failed: %v", err)
	}
	if orgOwnedA.AgentID != "org-a/ops" {
		t.Fatalf("expected canonical org-owned URI org-a/ops, got %q", orgOwnedA.AgentID)
	}

	if _, err := mem.RegisterAgent(orgA.OrgID, "OPS", nil, "tok-ops-a-2", alice.HumanID, now, false); !errors.Is(err, ErrAgentExists) {
		t.Fatalf("expected duplicate org-owned handle in same org to fail with ErrAgentExists, got %v", err)
	}

	if _, err := mem.RegisterAgent(orgB.OrgID, "ops", nil, "tok-ops-b", alice.HumanID, now, false); err != nil {
		t.Fatalf("same org-owned handle in different org should be allowed, got %v", err)
	}
}

func TestMemoryStoreAgentUUIDLookupAndTokenLifecycle(t *testing.T) {
	now := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	orgA := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)
	agent, err := mem.RegisterAgent(orgA.OrgID, "sender", &alice.HumanID, "tok-sender", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register sender failed: %v", err)
	}

	gotUUID, err := mem.AgentUUIDForTokenHash("tok-sender")
	if err != nil {
		t.Fatalf("AgentUUIDForTokenHash failed: %v", err)
	}
	if gotUUID != agent.AgentUUID {
		t.Fatalf("expected uuid %q got %q", agent.AgentUUID, gotUUID)
	}

	gotAgent, err := mem.GetAgentByUUID(agent.AgentUUID)
	if err != nil {
		t.Fatalf("GetAgentByUUID failed: %v", err)
	}
	if gotAgent.AgentID != agent.AgentID {
		t.Fatalf("expected agent uri %q got %q", agent.AgentID, gotAgent.AgentID)
	}

	if err := mem.RotateAgentToken(agent.AgentUUID, alice.HumanID, "tok-sender-rotated", now, false); err != nil {
		t.Fatalf("RotateAgentToken failed: %v", err)
	}
	if _, err := mem.AgentUUIDForTokenHash("tok-sender"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected old token invalid after rotation, got %v", err)
	}
	if uuid, err := mem.AgentUUIDForTokenHash("tok-sender-rotated"); err != nil || uuid != agent.AgentUUID {
		t.Fatalf("expected rotated token to resolve same uuid, got uuid=%q err=%v", uuid, err)
	}

	if err := mem.RevokeAgent(agent.AgentUUID, alice.HumanID, now, false); err != nil {
		t.Fatalf("RevokeAgent failed: %v", err)
	}
	if _, err := mem.AgentUUIDForTokenHash("tok-sender-rotated"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected revoked token invalid, got %v", err)
	}
	if _, err := mem.GetAgentByUUID(agent.AgentUUID); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected revoked agent not found, got %v", err)
	}
	if err := mem.RotateAgentToken(agent.AgentUUID, alice.HumanID, "tok-revoked", now, false); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected rotate token for revoked agent to fail with ErrAgentNotFound, got %v", err)
	}
	if _, err := mem.UpdateAgentMetadata(agent.AgentUUID, map[string]any{"public": false}, alice.HumanID, now, false); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected metadata update for revoked agent to fail with ErrAgentNotFound, got %v", err)
	}
	if _, err := mem.UpdateAgentMetadataSelf(agent.AgentUUID, map[string]any{"public": false}, now); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected self metadata update for revoked agent to fail with ErrAgentNotFound, got %v", err)
	}
}

func TestMemoryStoreRevokeAgentPurgesTrustAndQueuedMessages(t *testing.T) {
	now := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	bob := mustCreateHuman(t, mem, ids, "bob", "bob@b.test", "bob", now)
	orgA := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)
	orgB := mustCreateOrg(t, mem, ids, bob, "org-b", "Org B", now)

	agentA, err := mem.RegisterAgent(orgA.OrgID, "agent-a", &alice.HumanID, "tok-agent-a", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register agent A failed: %v", err)
	}
	agentB, err := mem.RegisterAgent(orgB.OrgID, "agent-b", &bob.HumanID, "tok-agent-b", bob.HumanID, now, false)
	if err != nil {
		t.Fatalf("register agent B failed: %v", err)
	}

	orgTrust, _, err := mem.CreateOrJoinOrgTrust(orgA.OrgID, orgB.OrgID, alice.HumanID, ids.mustID(t), now, false)
	if err != nil {
		t.Fatalf("CreateOrJoinOrgTrust failed: %v", err)
	}
	if _, err := mem.ApproveOrgTrust(orgTrust.EdgeID, bob.HumanID, now, false); err != nil {
		t.Fatalf("ApproveOrgTrust failed: %v", err)
	}

	agentTrust, _, err := mem.CreateOrJoinAgentTrust(orgA.OrgID, agentA.AgentUUID, agentB.AgentUUID, alice.HumanID, ids.mustID(t), now, false)
	if err != nil {
		t.Fatalf("CreateOrJoinAgentTrust failed: %v", err)
	}
	if _, err := mem.ApproveAgentTrust(agentTrust.EdgeID, bob.HumanID, now, false); err != nil {
		t.Fatalf("ApproveAgentTrust failed: %v", err)
	}

	if err := mem.Enqueue(context.Background(), model.Message{
		MessageID:     ids.mustID(t),
		FromAgentUUID: agentA.AgentUUID,
		ToAgentUUID:   agentB.AgentUUID,
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("enqueue A->B failed: %v", err)
	}
	if err := mem.Enqueue(context.Background(), model.Message{
		MessageID:     ids.mustID(t),
		FromAgentUUID: agentB.AgentUUID,
		ToAgentUUID:   agentA.AgentUUID,
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("enqueue B->A failed: %v", err)
	}

	if err := mem.RevokeAgent(agentA.AgentUUID, alice.HumanID, now, false); err != nil {
		t.Fatalf("RevokeAgent failed: %v", err)
	}

	snapshot := mem.AdminSnapshot()
	foundRevokedAgentTrust := false
	for _, edge := range snapshot.AgentTrusts {
		if edge.EdgeID != agentTrust.EdgeID {
			continue
		}
		foundRevokedAgentTrust = true
		if edge.State != model.StatusRevoked {
			t.Fatalf("expected linked agent trust to be revoked, got %q", edge.State)
		}
	}
	if !foundRevokedAgentTrust {
		t.Fatalf("expected revoked trust edge %q to exist in snapshot", agentTrust.EdgeID)
	}

	if _, _, err := mem.CanPublish(agentA.AgentUUID, agentB.AgentUUID); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected CanPublish with revoked sender to fail with ErrAgentNotFound, got %v", err)
	}
	if _, _, err := mem.CreateOrJoinAgentTrust(orgA.OrgID, agentA.AgentUUID, agentB.AgentUUID, alice.HumanID, ids.mustID(t), now, false); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected trust create with revoked agent to fail with ErrAgentNotFound, got %v", err)
	}

	if _, ok, err := mem.Dequeue(context.Background(), agentB.AgentUUID); err != nil {
		t.Fatalf("dequeue B failed: %v", err)
	} else if ok {
		t.Fatalf("expected outbound messages from revoked agent to be purged")
	}
	if _, ok, err := mem.Dequeue(context.Background(), agentA.AgentUUID); err != nil {
		t.Fatalf("dequeue A failed: %v", err)
	} else if ok {
		t.Fatalf("expected inbound messages to revoked agent to be purged")
	}
}

func TestMemoryStoreMemberCanRequestAgentTrustWithoutAutoApproval(t *testing.T) {
	now := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	bob := mustCreateHuman(t, mem, ids, "bob", "bob@b.test", "bob", now)
	charlie := mustCreateHuman(t, mem, ids, "charlie", "charlie@c.test", "charlie", now)

	orgA := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)
	orgB := mustCreateOrg(t, mem, ids, bob, "org-b", "Org B", now)

	invite, err := mem.CreateInvite(
		orgA.OrgID,
		charlie.Email,
		model.RoleMember,
		alice.HumanID,
		ids.mustID(t),
		"invite-charlie-org-a",
		now.Add(24*time.Hour),
		now,
		false,
	)
	if err != nil {
		t.Fatalf("create invite for charlie failed: %v", err)
	}
	if _, err := mem.AcceptInvite(invite.InviteID, charlie.HumanID, charlie.Email, now.Add(time.Minute), ids.next); err != nil {
		t.Fatalf("accept invite for charlie failed: %v", err)
	}

	agentA, err := mem.RegisterAgent(orgA.OrgID, "agent-a", &alice.HumanID, "tok-agent-a", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register agent A failed: %v", err)
	}
	agentB, err := mem.RegisterAgent(orgB.OrgID, "agent-b", &bob.HumanID, "tok-agent-b", bob.HumanID, now, false)
	if err != nil {
		t.Fatalf("register agent B failed: %v", err)
	}

	edge, created, err := mem.CreateOrJoinAgentTrust(orgA.OrgID, agentA.AgentUUID, agentB.AgentUUID, charlie.HumanID, ids.mustID(t), now, false)
	if err != nil {
		t.Fatalf("member trust request failed: %v", err)
	}
	if !created {
		t.Fatalf("expected trust edge to be created")
	}
	if edge.State != model.StatusPending {
		t.Fatalf("expected member request state pending, got %q", edge.State)
	}
	if edge.LeftApproved || edge.RightApproved {
		t.Fatalf("expected member request to avoid auto-approval, got left=%v right=%v", edge.LeftApproved, edge.RightApproved)
	}

	memberEdges := mem.ListHumanAgentTrusts(charlie.HumanID)
	for _, candidate := range memberEdges {
		if candidate.EdgeID == edge.EdgeID {
			t.Fatalf("expected non-managing member not to see edge in managed trust list")
		}
	}

	ownerEdges := mem.ListHumanAgentTrusts(alice.HumanID)
	foundForOwner := false
	for _, candidate := range ownerEdges {
		if candidate.EdgeID == edge.EdgeID {
			foundForOwner = true
			break
		}
	}
	if !foundForOwner {
		t.Fatalf("expected org owner to see member-submitted request")
	}

	if _, err := mem.ApproveAgentTrust(edge.EdgeID, charlie.HumanID, now.Add(2*time.Minute), false); !errors.Is(err, ErrUnauthorizedRole) {
		t.Fatalf("expected member approve to fail with ErrUnauthorizedRole, got %v", err)
	}

	afterAliceApprove, err := mem.ApproveAgentTrust(edge.EdgeID, alice.HumanID, now.Add(3*time.Minute), false)
	if err != nil {
		t.Fatalf("owner approve failed: %v", err)
	}
	if afterAliceApprove.State != model.StatusPending {
		t.Fatalf("expected state pending after one-side approval, got %q", afterAliceApprove.State)
	}
	if afterAliceApprove.LeftApproved == afterAliceApprove.RightApproved {
		t.Fatalf("expected exactly one side approved after owner approval, got left=%v right=%v", afterAliceApprove.LeftApproved, afterAliceApprove.RightApproved)
	}

	afterBobApprove, err := mem.ApproveAgentTrust(edge.EdgeID, bob.HumanID, now.Add(4*time.Minute), false)
	if err != nil {
		t.Fatalf("peer owner approve failed: %v", err)
	}
	if afterBobApprove.State != model.StatusActive {
		t.Fatalf("expected state active after second-side approval, got %q", afterBobApprove.State)
	}
	if !afterBobApprove.LeftApproved || !afterBobApprove.RightApproved {
		t.Fatalf("expected bilateral approvals after second approval, got left=%v right=%v", afterBobApprove.LeftApproved, afterBobApprove.RightApproved)
	}
}

func TestMemoryStoreCanPublishBetweenOrgAndPersonalAgentsWithActiveAgentTrust(t *testing.T) {
	now := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	org := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)

	orgAgent, err := mem.RegisterAgent(org.OrgID, "org-agent", &alice.HumanID, "tok-org-agent", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register org agent failed: %v", err)
	}
	personalAgent, err := mem.RegisterAgent("", "personal-agent", &alice.HumanID, "tok-personal-agent", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register personal agent failed: %v", err)
	}

	edge, _, err := mem.CreateOrJoinAgentTrust(org.OrgID, orgAgent.AgentUUID, personalAgent.AgentUUID, alice.HumanID, ids.mustID(t), now.Add(time.Minute), false)
	if err != nil {
		t.Fatalf("create org->personal agent trust failed: %v", err)
	}
	if edge.State != model.StatusActive {
		t.Fatalf("expected org->personal trust to be active, got %q", edge.State)
	}

	if _, _, err := mem.CanPublish(orgAgent.AgentUUID, personalAgent.AgentUUID); err != nil {
		t.Fatalf("expected org->personal publish to be allowed, got %v", err)
	}
	if _, _, err := mem.CanPublish(personalAgent.AgentUUID, orgAgent.AgentUUID); err != nil {
		t.Fatalf("expected personal->org publish to be allowed, got %v", err)
	}
}

func TestMemoryStoreDeleteAgentAuthorizationMatrix(t *testing.T) {
	now := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	bob := mustCreateHuman(t, mem, ids, "bob", "bob@b.test", "bob", now)
	charlie := mustCreateHuman(t, mem, ids, "charlie", "charlie@c.test", "charlie", now)
	org := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)

	addMember := func(human model.Human, role string) {
		t.Helper()
		invite, err := mem.CreateInvite(
			org.OrgID,
			human.Email,
			role,
			alice.HumanID,
			ids.mustID(t),
			"invite-hash-"+strings.ToLower(role)+"-"+human.HumanID,
			now.Add(24*time.Hour),
			now,
			false,
		)
		if err != nil {
			t.Fatalf("CreateInvite(%q) failed: %v", role, err)
		}
		if _, err := mem.AcceptInvite(invite.InviteID, human.HumanID, human.Email, now, ids.next); err != nil {
			t.Fatalf("AcceptInvite(%q) failed: %v", role, err)
		}
	}

	addMember(bob, model.RoleMember)
	addMember(charlie, model.RoleAdmin)

	agent, err := mem.RegisterAgent(org.OrgID, "owned-bot", &bob.HumanID, "tok-owned-bot", bob.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	if err := mem.DeleteAgent(agent.AgentUUID, charlie.HumanID, now.Add(time.Minute), false); !errors.Is(err, ErrUnauthorizedRole) {
		t.Fatalf("expected org admin delete to fail with ErrUnauthorizedRole, got %v", err)
	}
	if err := mem.DeleteAgent(agent.AgentUUID, bob.HumanID, now.Add(2*time.Minute), false); !errors.Is(err, ErrUnauthorizedRole) {
		t.Fatalf("expected non-owner member delete to fail with ErrUnauthorizedRole, got %v", err)
	}
	if err := mem.DeleteAgent(agent.AgentUUID, alice.HumanID, now.Add(3*time.Minute), false); err != nil {
		t.Fatalf("expected org owner delete to succeed, got %v", err)
	}
	if _, err := mem.GetAgentByUUID(agent.AgentUUID); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected deleted agent not found, got %v", err)
	}

	agentByOwner, err := mem.RegisterAgent(org.OrgID, "owned-bot-2", &bob.HumanID, "tok-owned-bot-2", bob.HumanID, now.Add(4*time.Minute), false)
	if err != nil {
		t.Fatalf("RegisterAgent second agent failed: %v", err)
	}
	if err := mem.DeleteAgent(agentByOwner.AgentUUID, alice.HumanID, now.Add(5*time.Minute), false); err != nil {
		t.Fatalf("expected org owner delete to succeed, got %v", err)
	}
	if agents := mem.ListHumanAgents(alice.HumanID); len(agents) != 0 {
		t.Fatalf("expected org owner managed list to exclude deleted agent, got %d entries", len(agents))
	}

	personalAgent, err := mem.RegisterAgent("", "owned-bot-personal", &bob.HumanID, "tok-owned-bot-personal", bob.HumanID, now.Add(6*time.Minute), false)
	if err != nil {
		t.Fatalf("RegisterAgent personal agent failed: %v", err)
	}
	if err := mem.DeleteAgent(personalAgent.AgentUUID, alice.HumanID, now.Add(7*time.Minute), false); !errors.Is(err, ErrUnauthorizedRole) {
		t.Fatalf("expected non-owner delete of personal agent to fail with ErrUnauthorizedRole, got %v", err)
	}
	if err := mem.DeleteAgent(personalAgent.AgentUUID, bob.HumanID, now.Add(8*time.Minute), false); err != nil {
		t.Fatalf("expected personal owner delete to succeed, got %v", err)
	}
	if _, err := mem.GetAgentByUUID(personalAgent.AgentUUID); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected deleted personal agent not found, got %v", err)
	}
}

func TestMemoryStoreDeleteAgentPurgesTrustAndQueuedMessages(t *testing.T) {
	now := time.Date(2026, 3, 8, 1, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	bob := mustCreateHuman(t, mem, ids, "bob", "bob@b.test", "bob", now)
	orgA := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)
	orgB := mustCreateOrg(t, mem, ids, bob, "org-b", "Org B", now)

	agentA, err := mem.RegisterAgent(orgA.OrgID, "agent-a", &alice.HumanID, "tok-agent-a", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register agent A failed: %v", err)
	}
	agentB, err := mem.RegisterAgent(orgB.OrgID, "agent-b", &bob.HumanID, "tok-agent-b", bob.HumanID, now, false)
	if err != nil {
		t.Fatalf("register agent B failed: %v", err)
	}

	orgTrust, _, err := mem.CreateOrJoinOrgTrust(orgA.OrgID, orgB.OrgID, alice.HumanID, ids.mustID(t), now, false)
	if err != nil {
		t.Fatalf("CreateOrJoinOrgTrust failed: %v", err)
	}
	if _, err := mem.ApproveOrgTrust(orgTrust.EdgeID, bob.HumanID, now, false); err != nil {
		t.Fatalf("ApproveOrgTrust failed: %v", err)
	}

	agentTrust, _, err := mem.CreateOrJoinAgentTrust(orgA.OrgID, agentA.AgentUUID, agentB.AgentUUID, alice.HumanID, ids.mustID(t), now, false)
	if err != nil {
		t.Fatalf("CreateOrJoinAgentTrust failed: %v", err)
	}
	if _, err := mem.ApproveAgentTrust(agentTrust.EdgeID, bob.HumanID, now, false); err != nil {
		t.Fatalf("ApproveAgentTrust failed: %v", err)
	}

	if err := mem.Enqueue(context.Background(), model.Message{
		MessageID:     ids.mustID(t),
		FromAgentUUID: agentA.AgentUUID,
		ToAgentUUID:   agentB.AgentUUID,
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("enqueue A->B failed: %v", err)
	}
	if err := mem.Enqueue(context.Background(), model.Message{
		MessageID:     ids.mustID(t),
		FromAgentUUID: agentB.AgentUUID,
		ToAgentUUID:   agentA.AgentUUID,
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("enqueue B->A failed: %v", err)
	}

	if err := mem.DeleteAgent(agentA.AgentUUID, alice.HumanID, now.Add(time.Minute), false); err != nil {
		t.Fatalf("DeleteAgent failed: %v", err)
	}

	snapshot := mem.AdminSnapshot()
	for _, agent := range snapshot.Agents {
		if agent.AgentUUID == agentA.AgentUUID {
			t.Fatalf("expected deleted agent %q to be absent from snapshot", agentA.AgentUUID)
		}
	}
	for _, edge := range snapshot.AgentTrusts {
		if edge.EdgeID == agentTrust.EdgeID {
			t.Fatalf("expected deleted agent trust %q to be absent from snapshot", agentTrust.EdgeID)
		}
	}

	if _, err := mem.AgentUUIDForTokenHash("tok-agent-a"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected deleted token invalid, got %v", err)
	}
	if _, _, err := mem.CanPublish(agentA.AgentUUID, agentB.AgentUUID); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected CanPublish with deleted sender to fail with ErrAgentNotFound, got %v", err)
	}
	if _, _, err := mem.CreateOrJoinAgentTrust(orgA.OrgID, agentA.AgentUUID, agentB.AgentUUID, alice.HumanID, ids.mustID(t), now, false); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected trust create with deleted agent to fail with ErrAgentNotFound, got %v", err)
	}
	if _, ok, err := mem.Dequeue(context.Background(), agentB.AgentUUID); err != nil {
		t.Fatalf("dequeue B failed: %v", err)
	} else if ok {
		t.Fatalf("expected outbound messages from deleted agent to be purged")
	}
	if _, ok, err := mem.Dequeue(context.Background(), agentA.AgentUUID); err != nil {
		t.Fatalf("dequeue A failed: %v", err)
	} else if ok {
		t.Fatalf("expected deleted agent queue to be removed")
	}
}

func TestMemoryStoreHandleValidationAcrossEntities(t *testing.T) {
	now := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)

	if _, err := mem.UpdateHumanProfile(alice.HumanID, "a", true, now); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("expected short human handle to fail with ErrInvalidHandle, got %v", err)
	}
	if _, err := mem.UpdateHumanProfile(alice.HumanID, "fuck", true, now); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("expected blocked human handle to fail with ErrInvalidHandle, got %v", err)
	}

	if _, _, err := mem.CreateOrg("a", "Org Too Short", alice.HumanID, ids.mustID(t), now); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("expected short org handle to fail with ErrInvalidHandle, got %v", err)
	}
	if _, _, err := mem.CreateOrg("shit", "Org Blocked", alice.HumanID, ids.mustID(t), now); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("expected blocked org handle to fail with ErrInvalidHandle, got %v", err)
	}

	org := mustCreateOrg(t, mem, ids, alice, "org-good", "Org Good", now)
	if _, err := mem.RegisterAgent(org.OrgID, "x", nil, "tok-short", alice.HumanID, now, false); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("expected short agent handle to fail with ErrInvalidHandle, got %v", err)
	}
	if _, err := mem.RegisterAgent(org.OrgID, "f.u.c.k", nil, "tok-blocked", alice.HumanID, now, false); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("expected blocked agent handle to fail with ErrInvalidHandle, got %v", err)
	}

	bind, err := mem.CreateBindToken(org.OrgID, nil, alice.HumanID, ids.mustID(t), "bind-hash-1", now.Add(time.Hour), now, false)
	if err != nil {
		t.Fatalf("CreateBindToken failed: %v", err)
	}
	if bind.BindID == "" {
		t.Fatalf("expected bind token to be created")
	}

	if _, err := mem.RedeemBindToken("bind-hash-1", "x", "tok-redeem-short", now); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("expected short redeem agent handle to fail with ErrInvalidHandle, got %v", err)
	}
}

func TestMemoryStoreGeneratedAgentUUIDLooksUUIDLike(t *testing.T) {
	now := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	org := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)
	agent, err := mem.RegisterAgent(org.OrgID, "agent-a", &alice.HumanID, "tok-a", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	parts := strings.Split(agent.AgentUUID, "-")
	if len(parts) != 5 {
		t.Fatalf("expected uuid-like shape, got %q", agent.AgentUUID)
	}
}

func TestMemoryStoreHumanScopedAgentAndBindWithoutOrg(t *testing.T) {
	now := time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	bob := mustCreateHuman(t, mem, ids, "bob", "bob@b.test", "bob", now)

	agent, err := mem.RegisterAgent("", "alpha", &alice.HumanID, "tok-alpha", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register human-scoped agent failed: %v", err)
	}
	if agent.OrgID != "" {
		t.Fatalf("expected empty org_id for human-scoped agent, got %q", agent.OrgID)
	}
	if agent.AgentID != "human/alice/agent/alpha" {
		t.Fatalf("expected human-scoped URI, got %q", agent.AgentID)
	}
	if _, err := mem.RegisterAgent("", "alpha", &alice.HumanID, "tok-alpha-2", alice.HumanID, now, false); !errors.Is(err, ErrAgentExists) {
		t.Fatalf("expected duplicate human-scoped handle to fail with ErrAgentExists, got %v", err)
	}

	bind, err := mem.CreateBindToken("", &alice.HumanID, alice.HumanID, ids.mustID(t), "bind-human", now.Add(time.Hour), now, false)
	if err != nil {
		t.Fatalf("create human-scoped bind token failed: %v", err)
	}
	if bind.OrgID != "" {
		t.Fatalf("expected bind token org_id empty, got %q", bind.OrgID)
	}
	redeemed, err := mem.RedeemBindToken("bind-human", "beta", "tok-beta", now)
	if err != nil {
		t.Fatalf("redeem human-scoped bind token failed: %v", err)
	}
	if redeemed.AgentID != "human/alice/agent/beta" {
		t.Fatalf("expected human-scoped redeemed URI, got %q", redeemed.AgentID)
	}
	if redeemed.OrgID != "" {
		t.Fatalf("expected redeemed agent org_id empty, got %q", redeemed.OrgID)
	}

	if _, err := mem.CreateBindToken("", &alice.HumanID, bob.HumanID, ids.mustID(t), "bind-unauthorized", now.Add(time.Hour), now, false); !errors.Is(err, ErrUnauthorizedRole) {
		t.Fatalf("expected non-owner create human-scoped bind token to fail with ErrUnauthorizedRole, got %v", err)
	}
}

func TestMemoryStoreAcceptInviteTransfersHumanScopedAgentsToOrg(t *testing.T) {
	now := time.Date(2026, 3, 6, 1, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	bob := mustCreateHuman(t, mem, ids, "bob", "bob@b.test", "bob", now)
	org := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)

	personalAgent, err := mem.RegisterAgent("", "bob-personal", &bob.HumanID, "tok-bob-personal", bob.HumanID, now, false)
	if err != nil {
		t.Fatalf("register personal agent failed: %v", err)
	}
	if personalAgent.OrgID != "" {
		t.Fatalf("expected personal agent org_id empty before invite, got %q", personalAgent.OrgID)
	}

	invite, err := mem.CreateInvite(org.OrgID, bob.Email, model.RoleMember, alice.HumanID, ids.mustID(t), "invite-bob-transfer", now.Add(24*time.Hour), now, false)
	if err != nil {
		t.Fatalf("create invite failed: %v", err)
	}
	if _, err := mem.AcceptInvite(invite.InviteID, bob.HumanID, bob.Email, now.Add(time.Minute), ids.next); err != nil {
		t.Fatalf("accept invite failed: %v", err)
	}

	updatedAgent, err := mem.GetAgentByUUID(personalAgent.AgentUUID)
	if err != nil {
		t.Fatalf("load transferred agent failed: %v", err)
	}
	if updatedAgent.OrgID != org.OrgID {
		t.Fatalf("expected transferred agent org_id %q, got %q", org.OrgID, updatedAgent.OrgID)
	}
	if updatedAgent.OwnerHumanID == nil || *updatedAgent.OwnerHumanID != bob.HumanID {
		t.Fatalf("expected transferred agent owner_human_id %q, got %v", bob.HumanID, updatedAgent.OwnerHumanID)
	}
	if !strings.HasPrefix(updatedAgent.AgentID, org.Handle+"/") {
		t.Fatalf("expected transferred agent URI to use org handle prefix %q, got %q", org.Handle+"/", updatedAgent.AgentID)
	}

	orgAgents, err := mem.ListOrgAgents(org.OrgID, bob.HumanID, false)
	if err != nil {
		t.Fatalf("list org agents failed: %v", err)
	}
	foundInOrg := false
	for _, agent := range orgAgents {
		if agent.AgentUUID == updatedAgent.AgentUUID {
			foundInOrg = true
			break
		}
	}
	if !foundInOrg {
		t.Fatalf("expected transferred agent in org list")
	}

	bobAgents := mem.ListHumanAgents(bob.HumanID)
	foundInBobList := false
	for _, agent := range bobAgents {
		if agent.AgentUUID == updatedAgent.AgentUUID {
			foundInBobList = true
			if agent.OrgID != org.OrgID {
				t.Fatalf("expected transferred agent in bob list with org_id %q, got %q", org.OrgID, agent.OrgID)
			}
			break
		}
	}
	if !foundInBobList {
		t.Fatalf("expected transferred agent in bob manageable list")
	}
}

func TestMemoryStoreListOrgHumanAgentsWithinOrgContext(t *testing.T) {
	now := time.Date(2026, 3, 7, 3, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	bob := mustCreateHuman(t, mem, ids, "bob", "bob@b.test", "bob", now)
	charlie := mustCreateHuman(t, mem, ids, "charlie", "charlie@c.test", "charlie", now)

	orgA := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)
	invite, err := mem.CreateInvite(orgA.OrgID, bob.Email, model.RoleMember, alice.HumanID, ids.mustID(t), "invite-bob-org-a", now.Add(24*time.Hour), now, false)
	if err != nil {
		t.Fatalf("create invite failed: %v", err)
	}
	if _, err := mem.AcceptInvite(invite.InviteID, bob.HumanID, bob.Email, now.Add(time.Minute), ids.next); err != nil {
		t.Fatalf("accept invite failed: %v", err)
	}

	personalAgent, err := mem.RegisterAgent("", "bob-personal", &bob.HumanID, "tok-bob-personal", bob.HumanID, now.Add(2*time.Minute), false)
	if err != nil {
		t.Fatalf("register personal bob agent failed: %v", err)
	}
	if personalAgent.OrgID != "" {
		t.Fatalf("expected bob personal agent org_id empty, got %q", personalAgent.OrgID)
	}

	orgAAgent, err := mem.RegisterAgent(orgA.OrgID, "bob-org-a", &bob.HumanID, "tok-bob-org-a", bob.HumanID, now.Add(3*time.Minute), false)
	if err != nil {
		t.Fatalf("register bob org-a agent failed: %v", err)
	}

	orgB := mustCreateOrg(t, mem, ids, bob, "org-b", "Org B", now.Add(4*time.Minute))
	orgBAgent, err := mem.RegisterAgent(orgB.OrgID, "bob-org-b", &bob.HumanID, "tok-bob-org-b", bob.HumanID, now.Add(5*time.Minute), false)
	if err != nil {
		t.Fatalf("register bob org-b agent failed: %v", err)
	}

	agents, err := mem.ListOrgHumanAgents(orgA.OrgID, bob.HumanID, alice.HumanID, false)
	if err != nil {
		t.Fatalf("ListOrgHumanAgents failed: %v", err)
	}
	seen := map[string]bool{}
	for _, agent := range agents {
		seen[agent.AgentUUID] = true
	}
	if !seen[personalAgent.AgentUUID] {
		t.Fatalf("expected personal agent %q in org human list", personalAgent.AgentUUID)
	}
	if !seen[orgAAgent.AgentUUID] {
		t.Fatalf("expected org-scoped agent %q in org human list", orgAAgent.AgentUUID)
	}
	if seen[orgBAgent.AgentUUID] {
		t.Fatalf("did not expect other-org agent %q in org human list", orgBAgent.AgentUUID)
	}

	if _, err := mem.ListOrgHumanAgents(orgA.OrgID, bob.HumanID, bob.HumanID, false); !errors.Is(err, ErrUnauthorizedRole) {
		t.Fatalf("expected member requester to fail with ErrUnauthorizedRole, got %v", err)
	}
	if _, err := mem.ListOrgHumanAgents(orgA.OrgID, charlie.HumanID, alice.HumanID, false); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("expected non-member target to fail with ErrMembershipNotFound, got %v", err)
	}
}

func TestMemoryStoreFinalizeAgentHandleSelfOnce(t *testing.T) {
	now := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	org := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)

	bindA, err := mem.CreateBindToken(org.OrgID, &alice.HumanID, alice.HumanID, ids.mustID(t), "bind-a", now.Add(time.Hour), now, false)
	if err != nil {
		t.Fatalf("create bind A failed: %v", err)
	}
	agentA, err := mem.RedeemBindToken(bindA.TokenHash, "tmp-a", "tok-a", now)
	if err != nil {
		t.Fatalf("redeem bind A failed: %v", err)
	}
	if agentA.HandleFinalizedAt != nil {
		t.Fatalf("expected bound agent handle to start unlocked, got %v", agentA.HandleFinalizedAt)
	}

	finalizedAt := now.Add(time.Minute)
	finalizedA, err := mem.FinalizeAgentHandleSelf(agentA.AgentUUID, "stable-a", finalizedAt)
	if err != nil {
		t.Fatalf("finalize agent A handle failed: %v", err)
	}
	if finalizedA.Handle != "stable-a" {
		t.Fatalf("expected finalized handle stable-a, got %q", finalizedA.Handle)
	}
	if finalizedA.HandleFinalizedAt == nil {
		t.Fatalf("expected finalized handle timestamp to be set")
	}
	if !strings.HasSuffix(finalizedA.AgentID, "/stable-a") {
		t.Fatalf("expected finalized agent URI to end with /stable-a, got %q", finalizedA.AgentID)
	}

	if _, err := mem.FinalizeAgentHandleSelf(agentA.AgentUUID, "stable-a", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("expected idempotent finalize with same handle to succeed, got %v", err)
	}
	if _, err := mem.FinalizeAgentHandleSelf(agentA.AgentUUID, "stable-a-2", now.Add(3*time.Minute)); !errors.Is(err, ErrAgentHandleLocked) {
		t.Fatalf("expected second different finalize to fail with ErrAgentHandleLocked, got %v", err)
	}

	bindB, err := mem.CreateBindToken(org.OrgID, &alice.HumanID, alice.HumanID, ids.mustID(t), "bind-b", now.Add(time.Hour), now, false)
	if err != nil {
		t.Fatalf("create bind B failed: %v", err)
	}
	agentB, err := mem.RedeemBindToken(bindB.TokenHash, "tmp-b", "tok-b", now)
	if err != nil {
		t.Fatalf("redeem bind B failed: %v", err)
	}
	if _, err := mem.FinalizeAgentHandleSelf(agentB.AgentUUID, "stable-a", now.Add(4*time.Minute)); !errors.Is(err, ErrAgentExists) {
		t.Fatalf("expected duplicate finalized handle in same scope to fail with ErrAgentExists, got %v", err)
	}
}

func TestMemoryStoreAgentMetadataNormalizesAgentType(t *testing.T) {
	now := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	org := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)
	agent, err := mem.RegisterAgent(org.OrgID, "agent-a", &alice.HumanID, "tok-a", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register agent failed: %v", err)
	}
	if got := agentTypeFromMetadata(agent.Metadata); got != model.AgentTypeUnknown {
		t.Fatalf("expected default agent_type=%q, got %q", model.AgentTypeUnknown, got)
	}

	updated, err := mem.UpdateAgentMetadataSelf(agent.AgentUUID, map[string]any{
		"agent_type": "CoDeX",
		"public":     true,
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("UpdateAgentMetadataSelf valid agent_type failed: %v", err)
	}
	if got := updated.Metadata[model.AgentMetadataKeyType]; got != "codex" {
		t.Fatalf("expected normalized metadata.agent_type=codex, got %v", got)
	}
	if got := updated.Metadata["public"]; got != true {
		t.Fatalf("expected metadata.public=true, got %v", got)
	}

	codexType, err := mem.UpdateAgentMetadataSelf(agent.AgentUUID, map[string]any{
		"public": false,
	}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("UpdateAgentMetadataSelf without agent_type failed: %v", err)
	}
	if got := codexType.Metadata[model.AgentMetadataKeyType]; got != "codex" {
		t.Fatalf("expected missing agent_type update to preserve existing value codex, got %v", got)
	}
	clearedPublic, err := mem.UpdateAgentMetadataSelf(agent.AgentUUID, map[string]any{
		"public": nil,
	}, now.Add(130*time.Second))
	if err != nil {
		t.Fatalf("UpdateAgentMetadataSelf null-delete public failed: %v", err)
	}
	if _, ok := clearedPublic.Metadata["public"]; ok {
		t.Fatalf("expected metadata.public to be removed via null delete, got %v", clearedPublic.Metadata["public"])
	}
	withActivityA, err := mem.UpdateAgentMetadataSelf(agent.AgentUUID, map[string]any{
		"activities": []any{"bound to hub"},
	}, now.Add(140*time.Second))
	if err != nil {
		t.Fatalf("UpdateAgentMetadataSelf first activities append failed: %v", err)
	}
	withActivityB, err := mem.UpdateAgentMetadataSelf(agent.AgentUUID, map[string]any{
		"activities": []any{"published first message"},
	}, now.Add(150*time.Second))
	if err != nil {
		t.Fatalf("UpdateAgentMetadataSelf second activities append failed: %v", err)
	}
	rawActivities, ok := withActivityB.Metadata[model.AgentMetadataKeyActivities].([]map[string]any)
	if !ok {
		t.Fatalf("expected metadata.activities to be normalized []map[string]any, got %T %v", withActivityB.Metadata[model.AgentMetadataKeyActivities], withActivityB.Metadata[model.AgentMetadataKeyActivities])
	}
	if len(rawActivities) < 2 {
		t.Fatalf("expected additive metadata.activities entries, got %v", rawActivities)
	}
	if rawActivities[0]["activity"] != "bound to hub" || rawActivities[1]["activity"] != "published first message" {
		t.Fatalf("expected additive activity ordering preserved, got %v", rawActivities)
	}
	if rawActivities[0]["source"] != "agent" || rawActivities[1]["source"] != "agent" {
		t.Fatalf("expected metadata.activities entries normalized with source=agent, got %v", rawActivities)
	}
	rawActivitiesA, ok := withActivityA.Metadata[model.AgentMetadataKeyActivities].([]map[string]any)
	if !ok || len(rawActivitiesA) == 0 {
		t.Fatalf("expected first activity append to persist, got %v", withActivityA.Metadata[model.AgentMetadataKeyActivities])
	}
	updatedSkills, err := mem.UpdateAgentMetadataSelf(agent.AgentUUID, map[string]any{
		"agent_type": "codex",
		"skills": []map[string]any{
			{"name": "Weather.Lookup", "description": "Get current weather for a location."},
			{"name": "math.add", "description": "Add two numbers."},
		},
	}, now.Add(250*time.Second))
	if err != nil {
		t.Fatalf("UpdateAgentMetadataSelf valid skills failed: %v", err)
	}
	rawSkills, ok := updatedSkills.Metadata[model.AgentMetadataKeySkills].([]map[string]any)
	if !ok || len(rawSkills) != 2 {
		t.Fatalf("expected normalized metadata.skills with 2 entries, got %T %v", updatedSkills.Metadata[model.AgentMetadataKeySkills], updatedSkills.Metadata[model.AgentMetadataKeySkills])
	}
	if rawSkills[0]["name"] != "math.add" || rawSkills[1]["name"] != "weather.lookup" {
		t.Fatalf("expected normalized sorted skill names [math.add weather.lookup], got %v", rawSkills)
	}

	if _, err := mem.UpdateAgentMetadataSelf(agent.AgentUUID, map[string]any{
		"skills": []map[string]any{
			{"name": "weather_lookup", "description": "Use API key ABC123 to query upstream"},
		},
	}, now.Add(260*time.Second)); !errors.Is(err, ErrInvalidSkillDescription) {
		t.Fatalf("expected secret-like skill description to fail with ErrInvalidSkillDescription, got %v", err)
	}

	if _, err := mem.UpdateAgentMetadataSelf(agent.AgentUUID, map[string]any{
		"skills": []map[string]any{
			{"name": "bad skill!", "description": "invalid name"},
		},
	}, now.Add(270*time.Second)); !errors.Is(err, ErrInvalidAgentSkills) {
		t.Fatalf("expected invalid skills shape/name to fail with ErrInvalidAgentSkills, got %v", err)
	}

	if _, err := mem.UpdateAgentMetadataSelf(agent.AgentUUID, map[string]any{
		"agent_type": "bad type!",
	}, now.Add(3*time.Minute)); !errors.Is(err, ErrInvalidAgentType) {
		t.Fatalf("expected invalid agent_type to fail with ErrInvalidAgentType, got %v", err)
	}
	if _, err := mem.UpdateAgentMetadata(agent.AgentUUID, map[string]any{
		"agent_type": 42,
	}, alice.HumanID, now.Add(4*time.Minute), false); !errors.Is(err, ErrInvalidAgentType) {
		t.Fatalf("expected non-string agent_type to fail with ErrInvalidAgentType, got %v", err)
	}
}

func TestMemoryStoreAdminSnapshotMessageMetricsArchivesAndRollups(t *testing.T) {
	now := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	bob := mustCreateHuman(t, mem, ids, "bob", "bob@b.test", "bob", now)
	orgA := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)
	orgB := mustCreateOrg(t, mem, ids, bob, "org-b", "Org B", now)

	agentA, err := mem.RegisterAgent(orgA.OrgID, "agent-a", &alice.HumanID, "tok-a", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register agent A failed: %v", err)
	}
	agentB, err := mem.RegisterAgent(orgB.OrgID, "agent-b", &bob.HumanID, "tok-b", bob.HumanID, now, false)
	if err != nil {
		t.Fatalf("register agent B failed: %v", err)
	}
	agentOrg, err := mem.RegisterAgent(orgA.OrgID, "org-agent", nil, "tok-org", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register org-owned agent failed: %v", err)
	}
	if _, err := mem.UpdateAgentMetadataSelf(agentA.AgentUUID, map[string]any{"agent_type": "codex"}, now.Add(10*time.Second)); err != nil {
		t.Fatalf("set agent A type failed: %v", err)
	}
	if _, err := mem.UpdateAgentMetadataSelf(agentB.AgentUUID, map[string]any{"agent_type": "claude"}, now.Add(11*time.Second)); err != nil {
		t.Fatalf("set agent B type failed: %v", err)
	}
	if _, err := mem.UpdateAgentMetadataSelf(agentOrg.AgentUUID, map[string]any{"agent_type": "openclaw"}, now.Add(12*time.Second)); err != nil {
		t.Fatalf("set org-owned agent type failed: %v", err)
	}

	msgAB := model.Message{
		MessageID:     "msg-a-b",
		FromAgentUUID: agentA.AgentUUID,
		ToAgentUUID:   agentB.AgentUUID,
		FromAgentID:   agentA.AgentID,
		ToAgentID:     agentB.AgentID,
		SenderOrgID:   orgA.OrgID,
		ReceiverOrgID: orgB.OrgID,
		ContentType:   "text/plain",
		Payload:       "secret-ab",
		CreatedAt:     now.Add(time.Minute),
	}
	if _, replay, err := mem.CreateOrGetMessageRecord(msgAB, msgAB.CreatedAt); err != nil || replay {
		t.Fatalf("CreateOrGetMessageRecord(msgAB) failed: replay=%v err=%v", replay, err)
	}
	deliveryAB1, _, err := mem.LeaseMessage(msgAB.MessageID, agentB.AgentUUID, "delivery-ab-1", now.Add(61*time.Second), now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("LeaseMessage first msgAB failed: %v", err)
	}
	if _, _, err := mem.ReleaseMessageDelivery(agentB.AgentUUID, deliveryAB1.DeliveryID, now.Add(62*time.Second), "receiver_nack"); err != nil {
		t.Fatalf("ReleaseMessageDelivery msgAB failed: %v", err)
	}
	if _, _, err := mem.LeaseMessage(msgAB.MessageID, agentB.AgentUUID, "delivery-ab-2", now.Add(63*time.Second), now.Add(2*time.Minute)); err != nil {
		t.Fatalf("LeaseMessage second msgAB failed: %v", err)
	}

	msgBA := model.Message{
		MessageID:     "msg-b-a",
		FromAgentUUID: agentB.AgentUUID,
		ToAgentUUID:   agentA.AgentUUID,
		FromAgentID:   agentB.AgentID,
		ToAgentID:     agentA.AgentID,
		SenderOrgID:   orgB.OrgID,
		ReceiverOrgID: orgA.OrgID,
		ContentType:   "application/json",
		Payload:       "{\"secret\":\"ba\"}",
		CreatedAt:     now.Add(3 * time.Minute),
	}
	if _, replay, err := mem.CreateOrGetMessageRecord(msgBA, msgBA.CreatedAt); err != nil || replay {
		t.Fatalf("CreateOrGetMessageRecord(msgBA) failed: replay=%v err=%v", replay, err)
	}

	msgOrgA := model.Message{
		MessageID:     "msg-org-a",
		FromAgentUUID: agentOrg.AgentUUID,
		ToAgentUUID:   agentA.AgentUUID,
		FromAgentID:   agentOrg.AgentID,
		ToAgentID:     agentA.AgentID,
		SenderOrgID:   orgA.OrgID,
		ReceiverOrgID: orgA.OrgID,
		ContentType:   "text/plain",
		Payload:       "secret-org-a",
		CreatedAt:     now.Add(4 * time.Minute),
	}
	if _, replay, err := mem.CreateOrGetMessageRecord(msgOrgA, msgOrgA.CreatedAt); err != nil || replay {
		t.Fatalf("CreateOrGetMessageRecord(msgOrgA) failed: replay=%v err=%v", replay, err)
	}
	if _, _, err := mem.LeaseMessage(msgOrgA.MessageID, agentA.AgentUUID, "delivery-org-a-1", now.Add(5*time.Minute), now.Add(6*time.Minute)); err != nil {
		t.Fatalf("LeaseMessage msgOrgA failed: %v", err)
	}

	snapshot := mem.AdminSnapshot()
	agentMetrics := make(map[string]model.AgentMessageMetrics, len(snapshot.MessageMetrics.Agents))
	for _, metric := range snapshot.MessageMetrics.Agents {
		agentMetrics[metric.AgentUUID] = metric
	}
	metricA, ok := agentMetrics[agentA.AgentUUID]
	if !ok {
		t.Fatalf("expected message metrics for agent A")
	}
	if metricA.OutboxMessages != 1 || metricA.InboxMessages != 1 {
		t.Fatalf("expected agent A outbox=1 inbox=1, got outbox=%d inbox=%d", metricA.OutboxMessages, metricA.InboxMessages)
	}
	if len(metricA.Archive.From) != 1 || metricA.Archive.From[0].MessageID != msgAB.MessageID {
		t.Fatalf("expected agent A from archive to include msgAB, got %+v", metricA.Archive.From)
	}
	if len(metricA.Archive.To) != 1 || metricA.Archive.To[0].MessageID != msgOrgA.MessageID {
		t.Fatalf("expected agent A to archive to include msgOrgA, got %+v", metricA.Archive.To)
	}

	metricB, ok := agentMetrics[agentB.AgentUUID]
	if !ok {
		t.Fatalf("expected message metrics for agent B")
	}
	if metricB.OutboxMessages != 1 || metricB.InboxMessages != 1 {
		t.Fatalf("expected agent B outbox=1 inbox=1, got outbox=%d inbox=%d", metricB.OutboxMessages, metricB.InboxMessages)
	}
	if len(metricB.Archive.To) != 1 || metricB.Archive.To[0].MessageID != msgAB.MessageID {
		t.Fatalf("expected agent B to archive to include msgAB, got %+v", metricB.Archive.To)
	}
	if metricB.Archive.To[0].FirstReceivedAt == nil || !metricB.Archive.To[0].FirstReceivedAt.Equal(deliveryAB1.LeasedAt) {
		t.Fatalf("expected agent B first_received_at=%v, got %+v", deliveryAB1.LeasedAt, metricB.Archive.To[0].FirstReceivedAt)
	}

	metricOrg, ok := agentMetrics[agentOrg.AgentUUID]
	if !ok {
		t.Fatalf("expected message metrics for org-owned agent")
	}
	if metricOrg.OutboxMessages != 1 || metricOrg.InboxMessages != 0 {
		t.Fatalf("expected org-owned agent outbox=1 inbox=0, got outbox=%d inbox=%d", metricOrg.OutboxMessages, metricOrg.InboxMessages)
	}

	humanMetrics := make(map[string]model.HumanMessageMetrics, len(snapshot.MessageMetrics.Humans))
	for _, metric := range snapshot.MessageMetrics.Humans {
		humanMetrics[metric.HumanID] = metric
	}
	aliceMetrics := humanMetrics[alice.HumanID]
	if aliceMetrics.LinkedAgents != 1 || aliceMetrics.OutboxMessages != 1 || aliceMetrics.InboxMessages != 1 {
		t.Fatalf("expected alice linked=1 outbox=1 inbox=1, got %+v", aliceMetrics)
	}
	bobMetrics := humanMetrics[bob.HumanID]
	if bobMetrics.LinkedAgents != 1 || bobMetrics.OutboxMessages != 1 || bobMetrics.InboxMessages != 1 {
		t.Fatalf("expected bob linked=1 outbox=1 inbox=1, got %+v", bobMetrics)
	}

	orgMetrics := make(map[string]model.OrganizationMessageMetrics, len(snapshot.MessageMetrics.Organizations))
	for _, metric := range snapshot.MessageMetrics.Organizations {
		orgMetrics[metric.OrgID] = metric
	}
	orgAMetrics := orgMetrics[orgA.OrgID]
	if orgAMetrics.LinkedAgents != 2 || orgAMetrics.OutboxMessages != 2 || orgAMetrics.InboxMessages != 1 {
		t.Fatalf("expected orgA linked=2 outbox=2 inbox=1, got %+v", orgAMetrics)
	}
	orgBMetrics := orgMetrics[orgB.OrgID]
	if orgBMetrics.LinkedAgents != 1 || orgBMetrics.OutboxMessages != 1 || orgBMetrics.InboxMessages != 1 {
		t.Fatalf("expected orgB linked=1 outbox=1 inbox=1, got %+v", orgBMetrics)
	}
}
