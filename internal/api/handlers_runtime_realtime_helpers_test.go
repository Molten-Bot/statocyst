package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"moltenhub/internal/auth"
	"moltenhub/internal/longpoll"
	"moltenhub/internal/model"
	"moltenhub/internal/store"
)

func TestOpenClawWSPullTimeout(t *testing.T) {
	timeout, err := openClawWSPullTimeout(nil)
	if err != nil {
		t.Fatalf("unexpected error for nil timeout: %v", err)
	}
	if timeout != openClawWebSocketPullTimeoutDefault {
		t.Fatalf("expected default timeout %v, got %v", openClawWebSocketPullTimeoutDefault, timeout)
	}

	zero := 0
	timeout, err = openClawWSPullTimeout(&zero)
	if err != nil {
		t.Fatalf("unexpected error for zero timeout: %v", err)
	}
	if timeout != 0 {
		t.Fatalf("expected zero timeout duration, got %v", timeout)
	}

	max := maxPullTimeoutMS
	timeout, err = openClawWSPullTimeout(&max)
	if err != nil {
		t.Fatalf("unexpected error for max timeout: %v", err)
	}
	if timeout != time.Duration(max)*time.Millisecond {
		t.Fatalf("expected max timeout duration %v, got %v", time.Duration(max)*time.Millisecond, timeout)
	}

	negative := -1
	if _, err := openClawWSPullTimeout(&negative); err == nil || !strings.Contains(err.Error(), "between 0 and 30000") {
		t.Fatalf("expected bounds error for negative timeout, got %v", err)
	}

	tooHigh := maxPullTimeoutMS + 1
	if _, err := openClawWSPullTimeout(&tooHigh); err == nil || !strings.Contains(err.Error(), "between 0 and 30000") {
		t.Fatalf("expected bounds error for timeout above max, got %v", err)
	}
}

func TestOpenClawPresenceFromMetadataAt(t *testing.T) {
	now := time.Date(2026, time.April, 17, 18, 0, 0, 0, time.UTC)
	fresh := now.Add(-openClawPresenceOfflineAfter + time.Minute).Format(time.RFC3339)
	stale := now.Add(-openClawPresenceOfflineAfter - time.Second).Format(time.RFC3339)

	t.Run("fresh online stays online", func(t *testing.T) {
		presence := openClawPresenceFromMetadataAt(map[string]any{
			"presence": map[string]any{
				"status":      "online",
				"ready":       true,
				"transport":   "websocket",
				"session_key": "main",
				"updated_at":  fresh,
			},
		}, now, openClawPresenceOfflineAfter)
		if got, _ := presence["status"].(string); got != "online" {
			t.Fatalf("expected online presence status, got %q payload=%v", got, presence)
		}
		if ready, _ := presence["ready"].(bool); !ready {
			t.Fatalf("expected ready=true while fresh, got payload=%v", presence)
		}
	})

	t.Run("stale online degrades to offline", func(t *testing.T) {
		presence := openClawPresenceFromMetadataAt(map[string]any{
			"presence": map[string]any{
				"status":      "online",
				"ready":       true,
				"session_key": "main",
				"updated_at":  stale,
			},
		}, now, openClawPresenceOfflineAfter)
		if got, _ := presence["status"].(string); got != "offline" {
			t.Fatalf("expected stale online status to degrade offline, got %q payload=%v", got, presence)
		}
		if ready, _ := presence["ready"].(bool); ready {
			t.Fatalf("expected stale online ready=false, got payload=%v", presence)
		}
	})

	t.Run("invalid updated_at keeps online", func(t *testing.T) {
		presence := openClawPresenceFromMetadataAt(map[string]any{
			"presence": map[string]any{
				"status":      "online",
				"ready":       true,
				"session_key": "main",
				"updated_at":  "not-a-time",
			},
		}, now, openClawPresenceOfflineAfter)
		if got, _ := presence["status"].(string); got != "online" {
			t.Fatalf("expected invalid timestamp to preserve online status, got %q payload=%v", got, presence)
		}
		if ready, _ := presence["ready"].(bool); !ready {
			t.Fatalf("expected ready=true with invalid timestamp, got payload=%v", presence)
		}
	})

	t.Run("stale threshold disabled", func(t *testing.T) {
		presence := openClawPresenceFromMetadataAt(map[string]any{
			"presence": map[string]any{
				"status":      "online",
				"ready":       true,
				"session_key": "main",
				"updated_at":  stale,
			},
		}, now, 0)
		if got, _ := presence["status"].(string); got != "online" {
			t.Fatalf("expected online status when stale threshold disabled, got %q payload=%v", got, presence)
		}
	})

	t.Run("missing or invalid presence metadata returns nil", func(t *testing.T) {
		if got := openClawPresenceFromMetadataAt(nil, now, openClawPresenceOfflineAfter); got != nil {
			t.Fatalf("expected nil presence for nil metadata, got %v", got)
		}
		if got := openClawPresenceFromMetadataAt(map[string]any{}, now, openClawPresenceOfflineAfter); got != nil {
			t.Fatalf("expected nil presence for missing presence key, got %v", got)
		}
		if got := openClawPresenceFromMetadataAt(map[string]any{"presence": "invalid"}, now, openClawPresenceOfflineAfter); got != nil {
			t.Fatalf("expected nil presence for invalid presence object, got %v", got)
		}
		if got := openClawPresenceFromMetadataAt(map[string]any{"presence": map[string]any{}}, now, openClawPresenceOfflineAfter); got != nil {
			t.Fatalf("expected nil presence for empty presence object, got %v", got)
		}
		if got := openClawPresenceFromMetadataAt(map[string]any{"presence": map[string]any{"note": "noop"}}, now, openClawPresenceOfflineAfter); got != nil {
			t.Fatalf("expected nil presence for metadata without supported fields, got %v", got)
		}
	})

	t.Run("zero now still renders presence", func(t *testing.T) {
		presence := openClawPresenceFromMetadataAt(map[string]any{
			"presence": map[string]any{
				"status":      "offline",
				"ready":       false,
				"session_key": "main",
				"updated_at":  stale,
			},
		}, time.Time{}, openClawPresenceOfflineAfter)
		if got, _ := presence["status"].(string); got != "offline" {
			t.Fatalf("expected status offline with zero now fallback, got %q payload=%v", got, presence)
		}
	})
}

func TestParseOpenClawPresenceTimestamp(t *testing.T) {
	if _, ok := parseOpenClawPresenceTimestamp(""); ok {
		t.Fatalf("expected empty timestamp to fail parse")
	}
	if _, ok := parseOpenClawPresenceTimestamp("not-a-time"); ok {
		t.Fatalf("expected invalid timestamp to fail parse")
	}

	raw := "2026-04-17T10:11:12.123456789Z"
	parsed, ok := parseOpenClawPresenceTimestamp(raw)
	if !ok {
		t.Fatalf("expected timestamp parse success for %q", raw)
	}
	if got := parsed.Format(time.RFC3339Nano); got != raw {
		t.Fatalf("expected parsed timestamp %q, got %q", raw, got)
	}
}

func TestAgentMetadataForRenderStripsPresence(t *testing.T) {
	metadata := map[string]any{
		"presence": map[string]any{
			"status":      "online",
			"ready":       true,
			"updated_at":  "2000-01-01T00:00:00Z",
			"session_key": "main",
		},
	}
	rendered := agentMetadataForRender(metadata)
	if _, ok := rendered["presence"]; ok {
		t.Fatalf("expected rendered metadata to exclude server-managed presence, got %v", rendered)
	}
}

func TestTouchAgentPresenceOnlineMissingAgentUUID(t *testing.T) {
	h := NewHandler(
		store.NewMemoryStore(),
		store.NewMemoryStore(),
		longpoll.NewWaiters(),
		auth.NewDevHumanAuthProvider(),
		"https://hub.example.com",
		"",
		"",
		"",
		"",
		"example.com",
		true,
		15*time.Minute,
		false,
	)
	err := h.touchAgentPresenceOnline("", "", "")
	if err == nil {
		t.Fatalf("expected missing agent uuid to return runtime handler error")
	}
	if err.status != http.StatusUnauthorized || err.code != "unauthorized" {
		t.Fatalf("expected unauthorized error, got %+v", err)
	}
}

func TestTouchAgentPresenceOnlineAppliesSessionAndTransport(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(
		mem,
		mem,
		waiters,
		auth.NewDevHumanAuthProvider(),
		"https://hub.example.com",
		"",
		"",
		"",
		"",
		"example.com",
		true,
		15*time.Minute,
		false,
	)
	router := NewRouter(h)
	_, _, _, _, _, _, agentUUIDA, _ := setupTrustedAgents(t, router)

	if err := h.touchAgentPresenceOnline(agentUUIDA, "Session-Presence", "websocket"); err != nil {
		t.Fatalf("expected touchAgentPresenceOnline success, got %+v", err)
	}

	presence := h.currentAgentPresence(agentUUIDA, nil)
	if got, _ := presence["status"].(string); got != "online" {
		t.Fatalf("expected status online, got %q payload=%v", got, presence)
	}
	if ready, _ := presence["ready"].(bool); !ready {
		t.Fatalf("expected ready=true after touch, got payload=%v", presence)
	}
	if got, _ := presence["session_key"].(string); got != "session-presence" {
		t.Fatalf("expected normalized session key session-presence, got %q payload=%v", got, presence)
	}
	if got, _ := presence["transport"].(string); got != "websocket" {
		t.Fatalf("expected transport websocket, got %q payload=%v", got, presence)
	}
}

func TestRuntimeHandlerErrorForPresenceUpdateMappings(t *testing.T) {
	notFound := runtimeHandlerErrorForPresenceUpdate(store.ErrAgentNotFound)
	if notFound.status != http.StatusNotFound || notFound.code != "unknown_agent" {
		t.Fatalf("expected unknown_agent 404, got %+v", notFound)
	}

	invalidType := runtimeHandlerErrorForPresenceUpdate(store.ErrInvalidAgentType)
	if invalidType.status != http.StatusBadRequest || invalidType.code != "invalid_agent_type" {
		t.Fatalf("expected invalid_agent_type 400, got %+v", invalidType)
	}

	storeErr := runtimeHandlerErrorForPresenceUpdate(errors.New("boom"))
	if storeErr.status != http.StatusInternalServerError || storeErr.code != "store_error" {
		t.Fatalf("expected store_error 500, got %+v", storeErr)
	}
	if detail, _ := storeErr.extras["detail"].(string); strings.TrimSpace(detail) == "" {
		t.Fatalf("expected store_error extras.detail to be populated, got %+v", storeErr.extras)
	}

	nilErr := runtimeHandlerErrorForPresenceUpdate(nil)
	if nilErr.status != http.StatusInternalServerError || nilErr.code != "store_error" {
		t.Fatalf("expected nil error to map to store_error 500, got %+v", nilErr)
	}
	if nilErr.extras != nil {
		t.Fatalf("expected nil error extras to remain nil, got %+v", nilErr.extras)
	}
}

func TestSetOpenClawWebSocketPresenceInvalidStatusFallsBackOffline(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(
		mem,
		mem,
		waiters,
		auth.NewDevHumanAuthProvider(),
		"https://hub.example.com",
		"",
		"",
		"",
		"",
		"example.com",
		true,
		15*time.Minute,
		false,
	)
	router := NewRouter(h)
	_, _, _, _, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	agent, err := h.setOpenClawWebSocketPresence(agentUUIDB, "main", "invalid-status", "forced invalid")
	if err != nil {
		t.Fatalf("expected setOpenClawWebSocketPresence success, got %v", err)
	}
	presence := h.currentAgentPresence(agentUUIDB, agent.Metadata)
	if got, _ := presence["status"].(string); got != "offline" {
		t.Fatalf("expected invalid status to degrade offline, got %q payload=%v", got, presence)
	}
	if ready, _ := presence["ready"].(bool); ready {
		t.Fatalf("expected ready=false for invalid status fallback, got payload=%v", presence)
	}

	if _, err := h.setOpenClawWebSocketPresence("", "main", "online", "missing"); !errors.Is(err, store.ErrAgentNotFound) {
		t.Fatalf("expected empty agent uuid to return ErrAgentNotFound, got %v", err)
	}
}

type failPresenceActivityStore struct {
	*store.MemoryStore
	failRecordActivity bool
}

func (s *failPresenceActivityStore) RecordAgentSystemActivity(agentUUID string, entry map[string]any, now time.Time) (model.Agent, error) {
	if s.failRecordActivity {
		return model.Agent{}, errors.New("activity write failed")
	}
	return s.MemoryStore.RecordAgentSystemActivity(agentUUID, entry, now)
}

func TestSetOpenClawWebSocketPresenceActivityFailure(t *testing.T) {
	stateStore := &failPresenceActivityStore{MemoryStore: store.NewMemoryStore()}
	waiters := longpoll.NewWaiters()
	h := NewHandler(
		stateStore,
		stateStore,
		waiters,
		auth.NewDevHumanAuthProvider(),
		"https://hub.example.com",
		"",
		"",
		"",
		"",
		"example.com",
		true,
		15*time.Minute,
		false,
	)
	router := NewRouter(h)
	_, _, _, _, _, _, agentUUIDA, _ := setupTrustedAgents(t, router)

	stateStore.failRecordActivity = true
	agent, err := h.setOpenClawWebSocketPresence(agentUUIDA, "main", "online", "")
	if err != nil {
		t.Fatalf("expected setOpenClawWebSocketPresence to ignore activity write failure, got %v", err)
	}
	presence := h.currentAgentPresence(agentUUIDA, agent.Metadata)
	if got, _ := presence["status"].(string); got != "online" {
		t.Fatalf("expected status online despite activity write failure, got %q payload=%v", got, presence)
	}
}

func TestTouchAgentPresenceOnlineStoreFailure(t *testing.T) {
	stateStore := &flakyStateWriteStore{MemoryStore: store.NewMemoryStore()}
	waiters := longpoll.NewWaiters()
	h := NewHandler(
		stateStore,
		stateStore,
		waiters,
		auth.NewDevHumanAuthProvider(),
		"https://hub.example.com",
		"",
		"",
		"",
		"",
		"example.com",
		true,
		15*time.Minute,
		false,
	)
	router := NewRouter(h)
	_, _, _, _, _, _, agentUUIDA, _ := setupTrustedAgents(t, router)

	stateStore.mu.Lock()
	stateStore.failMetadataWriteLeft = 1
	stateStore.mu.Unlock()

	err := h.touchAgentPresenceOnline(agentUUIDA, "", "")
	if err != nil {
		t.Fatalf("expected touchAgentPresenceOnline to fail open when presence writes fail, got %+v", err)
	}
	if presence := h.currentAgentPresence(agentUUIDA, nil); presence != nil {
		t.Fatalf("expected no presence to be persisted when write fails, got %v", presence)
	}
}
