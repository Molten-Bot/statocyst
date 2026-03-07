package store

import (
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
