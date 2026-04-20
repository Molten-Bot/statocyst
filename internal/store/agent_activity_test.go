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
