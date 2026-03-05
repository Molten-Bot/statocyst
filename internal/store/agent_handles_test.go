package store

import (
	"errors"
	"fmt"
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
		h, err = mem.UpdateHumanProfile(h.HumanID, handle, nil, true, now)
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
	if humanOwnedA.Handle != "alpha" {
		t.Fatalf("expected normalized handle alpha, got %q", humanOwnedA.Handle)
	}
	if humanOwnedA.AgentID != "org-a/alice/alpha" {
		t.Fatalf("expected canonical agent URI org-a/alice/alpha, got %q", humanOwnedA.AgentID)
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

func TestMemoryStoreAgentReferenceResolutionAndAmbiguity(t *testing.T) {
	now := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)
	bob := mustCreateHuman(t, mem, ids, "bob", "bob@b.test", "bob", now)
	orgA := mustCreateOrg(t, mem, ids, alice, "org-a", "Org A", now)
	orgB := mustCreateOrg(t, mem, ids, bob, "org-b", "Org B", now)

	sender, err := mem.RegisterAgent(orgA.OrgID, "sender", &alice.HumanID, "tok-sender", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register sender failed: %v", err)
	}
	dupA, err := mem.RegisterAgent(orgA.OrgID, "dup", nil, "tok-dup-a", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("register dupA failed: %v", err)
	}
	dupB, err := mem.RegisterAgent(orgB.OrgID, "dup", nil, "tok-dup-b", bob.HumanID, now, false)
	if err != nil {
		t.Fatalf("register dupB failed: %v", err)
	}

	if _, err := mem.GetAgent("dup"); !errors.Is(err, ErrAgentAmbiguous) {
		t.Fatalf("expected GetAgent by local handle to be ambiguous, got %v", err)
	}

	gotDupA, err := mem.GetAgent(dupA.AgentID)
	if err != nil {
		t.Fatalf("expected GetAgent by canonical URI to succeed, got %v", err)
	}
	if gotDupA.AgentID != dupA.AgentID {
		t.Fatalf("unexpected resolved agent, got %q want %q", gotDupA.AgentID, dupA.AgentID)
	}

	if _, err := mem.SetAgentVisibility("dup", false, alice.HumanID, now, false); !errors.Is(err, ErrAgentAmbiguous) {
		t.Fatalf("expected SetAgentVisibility by ambiguous local handle to fail with ErrAgentAmbiguous, got %v", err)
	}

	if _, _, err := mem.CanPublish(sender.AgentID, "dup"); !errors.Is(err, ErrAgentAmbiguous) {
		t.Fatalf("expected CanPublish with ambiguous receiver to fail with ErrAgentAmbiguous, got %v", err)
	}

	if err := mem.RevokeAgent(dupB.AgentID, bob.HumanID, now, false); err != nil {
		t.Fatalf("revoke dupB failed: %v", err)
	}

	resolvedAfterRevoke, err := mem.GetAgent("dup")
	if err != nil {
		t.Fatalf("expected GetAgent(dup) to resolve after one duplicate revoked, got %v", err)
	}
	if resolvedAfterRevoke.AgentID != dupA.AgentID {
		t.Fatalf("unexpected resolved agent after revoke, got %q want %q", resolvedAfterRevoke.AgentID, dupA.AgentID)
	}

	if err := mem.RevokeAgent(dupA.AgentID, alice.HumanID, now, false); err != nil {
		t.Fatalf("revoke dupA failed: %v", err)
	}
	if _, err := mem.GetAgent("dup"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected GetAgent(dup) to be not found after all dups revoked, got %v", err)
	}
}

func TestMemoryStoreHandleValidationAcrossEntities(t *testing.T) {
	now := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	ids := &seqID{}
	mem := NewMemoryStore()

	alice := mustCreateHuman(t, mem, ids, "alice", "alice@a.test", "alice", now)

	if _, err := mem.UpdateHumanProfile(alice.HumanID, "a", nil, true, now); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("expected short human handle to fail with ErrInvalidHandle, got %v", err)
	}
	if _, err := mem.UpdateHumanProfile(alice.HumanID, "fuck", nil, true, now); !errors.Is(err, ErrInvalidHandle) {
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
