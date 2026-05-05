package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"moltenhub/internal/model"
)

func TestRecordAgentSystemActivity_AppendsAndDedupesByEventID(t *testing.T) {
	mem := NewMemoryStore()
	ids := &idGen{}
	now := time.Date(2026, 3, 27, 9, 0, 0, 0, time.UTC)

	_, _, agent := seedOrgAndAgent(t, mem, ids, now, "alice", "alice@a.test", "org-a", "Org A", "agent-a")

	entry := map[string]any{
		"activity": "moltenhub openclaw plugin registered",
		"category": "openclaw_plugin",
		"action":   "register",
		"event_id": "evt-register-1",
	}

	updated, err := mem.RecordAgentSystemActivity(agent.AgentUUID, entry, now.Add(1*time.Minute))
	if err != nil {
		t.Fatalf("RecordAgentSystemActivity returned error: %v", err)
	}
	log := parseActivityEntries(updated.Metadata[model.AgentMetadataKeySystemActivityLog])
	if len(log) < 1 {
		t.Fatalf("expected at least 1 activity entry, got %d: %+v", len(log), log)
	}
	last := log[len(log)-1]
	if got := stringValue(last["source"]); got != "system" {
		t.Fatalf("expected source=system, got %q", got)
	}
	if got := stringValue(last["activity"]); got != "moltenhub openclaw plugin registered" {
		t.Fatalf("unexpected activity text: %q", got)
	}
	if got := stringValue(last["event_id"]); got != "evt-register-1" {
		t.Fatalf("expected event_id to persist, got %q", got)
	}

	updated, err = mem.RecordAgentSystemActivity(agent.AgentUUID, map[string]any{
		"activity": "ignored duplicate event",
		"event_id": "evt-register-1",
	}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("RecordAgentSystemActivity (duplicate) returned error: %v", err)
	}
	log = parseActivityEntries(updated.Metadata[model.AgentMetadataKeySystemActivityLog])
	eventCount := 0
	for _, row := range log {
		if stringValue(row["event_id"]) == "evt-register-1" {
			eventCount++
		}
	}
	if eventCount != 1 {
		t.Fatalf("expected duplicate event_id to be deduped, got count=%d entries=%+v", eventCount, log)
	}
}

func TestRecordAgentSystemActivity_UnknownAgent(t *testing.T) {
	mem := NewMemoryStore()
	now := time.Date(2026, 3, 27, 9, 5, 0, 0, time.UTC)

	_, err := mem.RecordAgentSystemActivity("missing-agent", map[string]any{"activity": "test"}, now)
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected ErrAgentNotFound, got %v", err)
	}
}

func TestRecordAgentSystemActivity_UsesServerTimestampAndTruncates(t *testing.T) {
	mem := NewMemoryStore()
	ids := &idGen{}
	now := time.Date(2026, 3, 27, 9, 15, 0, 0, time.UTC)

	_, _, agent := seedOrgAndAgent(t, mem, ids, now, "alice", "alice@a.test", "org-a", "Org A", "agent-a")

	updated, err := mem.RecordAgentSystemActivity(agent.AgentUUID, map[string]any{
		"activity": strings.Repeat("x", maxAgentActivityChars+40),
		"at":       "2001-09-09T01:46:40Z",
	}, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("RecordAgentSystemActivity returned error: %v", err)
	}

	log := parseActivityEntries(updated.Metadata[model.AgentMetadataKeySystemActivityLog])
	if len(log) == 0 {
		t.Fatalf("expected non-empty system activity log, got %v", updated.Metadata[model.AgentMetadataKeySystemActivityLog])
	}
	last := log[len(log)-1]
	expectedAt := now.Add(3 * time.Minute).UTC().Format(time.RFC3339)
	if got := stringValue(last["at"]); got != expectedAt {
		t.Fatalf("expected server timestamp %q, got %q entry=%v", expectedAt, got, last)
	}
	if gotLen := len([]rune(stringValue(last["activity"]))); gotLen != maxAgentActivityChars {
		t.Fatalf("expected activity length %d, got %d entry=%v", maxAgentActivityChars, gotLen, last)
	}
}

func TestAgentCreationAudit_RegisterAddsCanonicalCreateEvent(t *testing.T) {
	mem := NewMemoryStore()
	ids := &idGen{}
	now := recentAuditTestTime()

	_, _, agent := seedOrgAndAgent(t, mem, ids, now, "alice", "alice@a.test", "org-a", "Org A", "agent-a")

	snapshot := mem.AdminSnapshot()
	event, ok := findAuditEvent(snapshot.ActivityFeed, "agent", "create", agent.AgentUUID)
	if !ok {
		t.Fatalf("expected agent/create event for %q, got=%v", agent.AgentUUID, snapshot.ActivityFeed)
	}
	if got := stringValue(event.Details["creation_flow"]); got != "register" {
		t.Fatalf("expected creation_flow=register, got %q event=%v", got, event)
	}
	if got := stringValue(event.Details["agent_uuid"]); got != agent.AgentUUID {
		t.Fatalf("expected agent_uuid detail %q, got %q event=%v", agent.AgentUUID, got, event)
	}
	snapshotAgent, ok := findSnapshotAgent(snapshot, agent.AgentUUID)
	if !ok {
		t.Fatalf("expected created agent in snapshot, got=%v", snapshot.Agents)
	}
	if !hasStoredSystemActivity(snapshotAgent, "created agent", "agent", "create") {
		t.Fatalf("expected created agent system activity, got metadata=%v", snapshotAgent.Metadata)
	}
}

func TestAdminSnapshotActivityFeedIncludesOnlyPast31Days(t *testing.T) {
	mem := NewMemoryStore()
	ids := &idGen{}
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	recent := time.Now().UTC().Add(-24 * time.Hour)

	_, _, agent := seedOrgAndAgent(t, mem, ids, old, "alice", "alice@a.test", "org-a", "Org A", "agent-a")
	_, err := mem.RecordAgentSystemActivity(agent.AgentUUID, map[string]any{
		"activity": "recent activity",
		"category": "test",
		"action":   "record",
	}, recent)
	if err != nil {
		t.Fatalf("RecordAgentSystemActivity returned error: %v", err)
	}

	snapshot := mem.AdminSnapshot()
	if _, ok := findAuditEvent(snapshot.ActivityFeed, "agent_activity", "record", agent.AgentUUID); !ok {
		t.Fatalf("expected recent agent_activity event in activity_feed, got=%v", snapshot.ActivityFeed)
	}
	if _, ok := findAuditEvent(snapshot.ActivityFeed, "agent", "create", agent.AgentUUID); ok {
		t.Fatalf("expected old agent/create event to be omitted from activity_feed, got=%v", snapshot.ActivityFeed)
	}
	cutoff := time.Now().UTC().Add(-adminSnapshotActivityFeedLimit)
	for _, event := range snapshot.ActivityFeed {
		if event.CreatedAt.Before(cutoff) {
			t.Fatalf("expected activity_feed event within past 31 days, got event=%v cutoff=%s", event, cutoff.Format(time.RFC3339))
		}
	}
}

func TestAgentCreationAudit_BindRedeemAddsCanonicalCreateEventAndKeepsRedeem(t *testing.T) {
	mem := NewMemoryStore()
	ids := &idGen{}
	now := recentAuditTestTime()

	alice, err := mem.UpsertHuman("dev", "alice", "alice@a.test", true, now, ids.Next)
	if err != nil {
		t.Fatalf("UpsertHuman failed: %v", err)
	}
	org, _, err := mem.CreateOrg("org-a", "Org A", alice.HumanID, ids.MustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}
	bind, err := mem.CreateBindToken(org.OrgID, nil, alice.HumanID, ids.MustID(t), "bind-hash", now.Add(time.Hour), now, false)
	if err != nil {
		t.Fatalf("CreateBindToken failed: %v", err)
	}
	agent, err := mem.RedeemBindToken("bind-hash", "agent-a", "agent-token-hash", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("RedeemBindToken failed: %v", err)
	}

	snapshot := mem.AdminSnapshot()
	event, ok := findAuditEvent(snapshot.ActivityFeed, "agent", "create", agent.AgentUUID)
	if !ok {
		t.Fatalf("expected agent/create event for bind-created agent %q, got=%v", agent.AgentUUID, snapshot.ActivityFeed)
	}
	if got := stringValue(event.Details["creation_flow"]); got != "bind" {
		t.Fatalf("expected creation_flow=bind, got %q event=%v", got, event)
	}
	if got := stringValue(event.Details["bind_id"]); got != bind.BindID {
		t.Fatalf("expected bind_id detail %q, got %q event=%v", bind.BindID, got, event)
	}
	if _, ok := findAuditEvent(snapshot.ActivityFeed, "agent_bind", "redeem", bind.BindID); !ok {
		t.Fatalf("expected existing agent_bind/redeem event to remain, got=%v", snapshot.ActivityFeed)
	}
	snapshotAgent, ok := findSnapshotAgent(snapshot, agent.AgentUUID)
	if !ok {
		t.Fatalf("expected redeemed agent in snapshot, got=%v", snapshot.Agents)
	}
	if !hasStoredSystemActivity(snapshotAgent, "created agent", "agent", "create") {
		t.Fatalf("expected created agent system activity, got metadata=%v", snapshotAgent.Metadata)
	}
}

func TestAgentCreationAudit_PersonalAgentUsesGlobalActivityFeed(t *testing.T) {
	mem := NewMemoryStore()
	ids := &idGen{}
	now := recentAuditTestTime()

	alice, err := mem.UpsertHuman("dev", "alice", "alice@a.test", true, now, ids.Next)
	if err != nil {
		t.Fatalf("UpsertHuman failed: %v", err)
	}
	alice, err = mem.UpdateHumanProfile(alice.HumanID, "alice", true, now)
	if err != nil {
		t.Fatalf("UpdateHumanProfile failed: %v", err)
	}
	ownerHumanID := alice.HumanID
	agent, err := mem.RegisterAgent("", "personal-agent", &ownerHumanID, "personal-token-hash", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent personal failed: %v", err)
	}

	snapshot := mem.AdminSnapshot()
	event, ok := findAuditEvent(snapshot.ActivityFeed, "agent", "create", agent.AgentUUID)
	if !ok {
		t.Fatalf("expected personal agent/create event for %q, got=%v", agent.AgentUUID, snapshot.ActivityFeed)
	}
	if event.OrgID != "" {
		t.Fatalf("expected personal agent/create event in global org scope, got org_id=%q event=%v", event.OrgID, event)
	}
}

func TestDeleteAgentArchivesAgentForAdminSnapshot(t *testing.T) {
	mem := NewMemoryStore()
	ids := &idGen{}
	now := recentAuditTestTime()

	alice, _, agent := seedOrgAndAgent(t, mem, ids, now, "alice", "alice@a.test", "org-a", "Org A", "agent-a")
	if err := mem.DeleteAgent(agent.AgentUUID, alice.HumanID, now.Add(time.Minute), false); err != nil {
		t.Fatalf("DeleteAgent failed: %v", err)
	}

	snapshot := mem.AdminSnapshot()
	if _, ok := findSnapshotAgent(snapshot, agent.AgentUUID); ok {
		t.Fatalf("expected deleted agent to be absent from active snapshot agents, got=%v", snapshot.Agents)
	}
	archived, ok := findArchivedSnapshotAgent(snapshot, agent.AgentUUID)
	if !ok {
		t.Fatalf("expected deleted agent in archived_agents, got=%v", snapshot.ArchivedAgents)
	}
	if archived.Status != model.StatusDeleted || archived.RevokedAt == nil {
		t.Fatalf("expected archived agent status=deleted with revoked_at, got=%+v", archived)
	}
	if !hasStoredSystemActivity(archived, "created agent", "agent", "create") {
		t.Fatalf("expected archived agent activity_log to retain creation activity, got metadata=%v", archived.Metadata)
	}
	if _, ok := findAuditEvent(snapshot.ActivityFeed, "agent", "delete", agent.AgentUUID); !ok {
		t.Fatalf("expected agent/delete event to remain in activity_feed, got=%v", snapshot.ActivityFeed)
	}
}

func TestDeleteOrgArchivesOrgAgentsAndKeepsActivityFeed(t *testing.T) {
	mem := NewMemoryStore()
	ids := &idGen{}
	now := recentAuditTestTime()

	alice, org, agent := seedOrgAndAgent(t, mem, ids, now, "alice", "alice@a.test", "org-a", "Org A", "agent-a")
	if err := mem.DeleteOrg(org.OrgID, alice.HumanID, false, now.Add(time.Minute)); err != nil {
		t.Fatalf("DeleteOrg failed: %v", err)
	}

	snapshot := mem.AdminSnapshot()
	if _, ok := findArchivedSnapshotOrg(snapshot, org.OrgID); !ok {
		t.Fatalf("expected deleted org in archived_organizations, got=%v", snapshot.ArchivedOrganizations)
	}
	if archived, ok := findArchivedSnapshotAgent(snapshot, agent.AgentUUID); !ok {
		t.Fatalf("expected org-deleted agent in archived_agents, got=%v", snapshot.ArchivedAgents)
	} else if archived.Status != model.StatusDeleted || archived.RevokedAt == nil {
		t.Fatalf("expected org-deleted archived agent status=deleted with revoked_at, got=%+v", archived)
	}
	if _, ok := findAuditEvent(snapshot.ActivityFeed, "org", "create", org.OrgID); !ok {
		t.Fatalf("expected org/create event to remain after delete, got=%v", snapshot.ActivityFeed)
	}
	if _, ok := findAuditEvent(snapshot.ActivityFeed, "org", "delete", org.OrgID); !ok {
		t.Fatalf("expected org/delete event to remain after delete, got=%v", snapshot.ActivityFeed)
	}
}

func recentAuditTestTime() time.Time {
	return time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
}

func findAuditEvent(events []model.AuditEvent, category, action, subjectID string) (model.AuditEvent, bool) {
	for _, event := range events {
		if event.Category == category && event.Action == action && event.SubjectID == subjectID {
			return event, true
		}
	}
	return model.AuditEvent{}, false
}

func findSnapshotAgent(snapshot model.AdminSnapshot, agentUUID string) (model.Agent, bool) {
	for _, agent := range snapshot.Agents {
		if agent.AgentUUID == agentUUID {
			return agent, true
		}
	}
	return model.Agent{}, false
}

func findArchivedSnapshotAgent(snapshot model.AdminSnapshot, agentUUID string) (model.Agent, bool) {
	for _, agent := range snapshot.ArchivedAgents {
		if agent.AgentUUID == agentUUID {
			return agent, true
		}
	}
	return model.Agent{}, false
}

func findArchivedSnapshotOrg(snapshot model.AdminSnapshot, orgID string) (model.Organization, bool) {
	for _, org := range snapshot.ArchivedOrganizations {
		if org.OrgID == orgID {
			return org, true
		}
	}
	return model.Organization{}, false
}

func hasStoredSystemActivity(agent model.Agent, activity, category, action string) bool {
	for _, row := range parseActivityEntries(agent.Metadata[model.AgentMetadataKeySystemActivityLog]) {
		if stringValue(row["activity"]) != activity {
			continue
		}
		if stringValue(row["source"]) != "system" {
			continue
		}
		if stringValue(row["category"]) != category {
			continue
		}
		if stringValue(row["action"]) != action {
			continue
		}
		if stringValue(row["event_id"]) == "" || stringValue(row["subject_id"]) != agent.AgentUUID {
			continue
		}
		return true
	}
	return false
}
