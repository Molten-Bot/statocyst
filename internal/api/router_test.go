package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"moltenhub/internal/auth"
	"moltenhub/internal/longpoll"
	"moltenhub/internal/model"
	"moltenhub/internal/store"
)

func newTestRouter() http.Handler {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	return NewRouter(h)
}

func TestHandlerWiringWithInterfaceStores(t *testing.T) {
	mem := store.NewMemoryStore()
	var control store.ControlPlaneStore = mem
	var queue store.MessageQueueStore = mem
	waiters := longpoll.NewWaiters()
	h := NewHandler(control, queue, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	router := NewRouter(h)

	health := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if health.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", health.Code, health.Body.String())
	}
	var healthPayload map[string]any
	if err := json.Unmarshal(health.Body.Bytes(), &healthPayload); err != nil {
		t.Fatalf("decode /health response: %v", err)
	}
	if status, _ := healthPayload["status"].(string); status != "ok" {
		t.Fatalf("expected /health status ok, got %v payload=%v", healthPayload["status"], healthPayload)
	}
	storageObj, _ := healthPayload["storage"].(map[string]any)
	if storageObj == nil {
		t.Fatalf("expected /health storage object, got payload=%v", healthPayload)
	}

	me := doJSONRequest(t, router, http.MethodGet, "/v1/me", nil, humanHeaders("alice", "alice@a.test"))
	if me.Code != http.StatusOK {
		t.Fatalf("expected /v1/me 200, got %d %s", me.Code, me.Body.String())
	}
}

func TestPingReturnsNoContent(t *testing.T) {
	router := newTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("expected /ping 204, got %d %s", resp.Code, resp.Body.String())
	}
	if resp.Body.Len() != 0 {
		t.Fatalf("expected /ping empty body, got %q", resp.Body.String())
	}
}

func TestHealthReportsDegradedStorageStatus(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	h.SetStorageHealth(store.StorageHealthStatus{
		StartupMode: store.StorageStartupModeDegraded,
		State: store.StorageBackendHealth{
			Backend: "s3",
			Healthy: false,
			Error:   "state backend unavailable",
		},
		Queue: store.StorageBackendHealth{
			Backend: "memory",
			Healthy: true,
		},
	})
	router := NewRouter(h)

	health := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if health.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", health.Code, health.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(health.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode /health response: %v", err)
	}
	if got, _ := payload["status"].(string); got != "degraded" {
		t.Fatalf("expected /health status degraded, got %q payload=%v", got, payload)
	}
	storageObj, _ := payload["storage"].(map[string]any)
	if storageObj == nil {
		t.Fatalf("expected /health storage object, got payload=%v", payload)
	}
	stateObj, _ := storageObj["state"].(map[string]any)
	if stateObj == nil {
		t.Fatalf("expected /health storage.state object, got payload=%v", payload)
	}
	if healthy, ok := stateObj["healthy"].(bool); !ok || healthy {
		t.Fatalf("expected storage.state.healthy=false, got %v payload=%v", stateObj["healthy"], payload)
	}
	if _, ok := stateObj["error"].(string); !ok {
		t.Fatalf("expected storage.state.error string, got %v payload=%v", stateObj["error"], payload)
	}
}

func TestHealthIncludesStartupSummaryWhenProvided(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	h.SetStartupSummary(map[string]any{
		"boot_status": "ready",
		"startup": map[string]any{
			"total_ms": 1234,
		},
	})
	router := NewRouter(h)

	health := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if health.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", health.Code, health.Body.String())
	}
	payload := decodeJSONMap(t, health.Body.Bytes())
	if got, _ := payload["boot_status"].(string); got != "ready" {
		t.Fatalf("expected boot_status=ready, got %q payload=%v", got, payload)
	}
	startupObj, _ := payload["startup"].(map[string]any)
	if startupObj == nil {
		t.Fatalf("expected startup object in /health payload=%v", payload)
	}
}

func TestHealthSanitizesBackendErrorDetails(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	h.SetStorageHealth(store.StorageHealthStatus{
		StartupMode: store.StorageStartupModeDegraded,
		State: store.StorageBackendHealth{
			Backend: "s3",
			Healthy: false,
			Error:   "list objects status 403: <Error><Code>SignatureDoesNotMatch</Code></Error>",
		},
		Queue: store.StorageBackendHealth{
			Backend: "s3",
			Healthy: false,
			Error:   "get object status 500: https://example.invalid/internal",
		},
	})
	router := NewRouter(h)

	health := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if health.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", health.Code, health.Body.String())
	}
	payload := decodeJSONMap(t, health.Body.Bytes())
	storageObj, _ := payload["storage"].(map[string]any)
	stateObj, _ := storageObj["state"].(map[string]any)
	queueObj, _ := storageObj["queue"].(map[string]any)
	if got, _ := stateObj["error"].(string); got != "authorization failed" {
		t.Fatalf("expected sanitized state error, got %q payload=%v", got, payload)
	}
	if got, _ := stateObj["error_detail"].(string); got != "status=403, s3_code=SignatureDoesNotMatch" {
		t.Fatalf("expected sanitized state error detail, got %q payload=%v", got, payload)
	}
	if got, _ := queueObj["error"].(string); got != "request failed" {
		t.Fatalf("expected sanitized queue error, got %q payload=%v", got, payload)
	}
	if got, _ := queueObj["error_detail"].(string); got != "status=500" {
		t.Fatalf("expected sanitized queue error detail, got %q payload=%v", got, payload)
	}
}

func TestParseCORSAllowedOrigins(t *testing.T) {
	origins, err := ParseCORSAllowedOrigins(" https://app.example.com,https://app.qa.example.com/\nhttp://localhost:3000 ")
	if err != nil {
		t.Fatalf("ParseCORSAllowedOrigins returned error: %v", err)
	}

	for _, origin := range []string{
		"https://app.example.com",
		"https://app.qa.example.com",
		"http://localhost:3000",
	} {
		if _, ok := origins[origin]; !ok {
			t.Fatalf("expected origin %q in parsed set, got %v", origin, origins)
		}
	}
}

func TestParseCORSAllowedOrigins_AcceptsHostShorthand(t *testing.T) {
	origins, err := ParseCORSAllowedOrigins(" x.site.com,y.site.com:8443 ")
	if err != nil {
		t.Fatalf("ParseCORSAllowedOrigins returned error: %v", err)
	}

	for _, origin := range []string{
		"https://x.site.com",
		"http://x.site.com",
		"https://y.site.com:8443",
		"http://y.site.com:8443",
	} {
		if _, ok := origins[origin]; !ok {
			t.Fatalf("expected shorthand origin %q in parsed set, got %v", origin, origins)
		}
	}
}

func TestAPICORSAllowsExplicitOrigin(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	router := NewRouterWithOptions(h, RouterOptions{
		AllowedCORSOrigins: map[string]struct{}{
			"https://app.example.com": {},
		},
	})

	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("expected OPTIONS /health 204, got %d %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("expected explicit CORS origin to be allowed, got %q", got)
	}
}

func TestAPICORSRejectsUnknownOrigin(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	router := NewRouterWithOptions(h, RouterOptions{
		AllowedCORSOrigins: map[string]struct{}{
			"https://app.example.com": {},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected unknown origin to be blocked, got %q", got)
	}
}

func TestAPICORSAllowsHostShorthandOrigin(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	allowedOrigins, err := ParseCORSAllowedOrigins("x.site.com,y.site.com")
	if err != nil {
		t.Fatalf("ParseCORSAllowedOrigins returned error: %v", err)
	}
	router := NewRouterWithOptions(h, RouterOptions{
		AllowedCORSOrigins: allowedOrigins,
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://y.site.com")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "https://y.site.com" {
		t.Fatalf("expected host shorthand origin to be allowed, got %q", got)
	}
}

type eofTrackingBody struct {
	reader     io.Reader
	reachedEOF bool
}

func (b *eofTrackingBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err == io.EOF {
		b.reachedEOF = true
	}
	return n, err
}

func (b *eofTrackingBody) Close() error {
	return nil
}

func TestDecodeJSONConsumesBodyToEOF(t *testing.T) {
	body := &eofTrackingBody{reader: strings.NewReader(`{"value":"ok"}   `)}
	req := httptest.NewRequest(http.MethodPost, "/v1/test", nil)
	req.Body = body

	var payload struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(req, &payload); err != nil {
		t.Fatalf("decodeJSON failed: %v", err)
	}
	if payload.Value != "ok" {
		t.Fatalf("expected decoded value ok, got %q", payload.Value)
	}
	if !body.reachedEOF {
		t.Fatalf("expected decodeJSON to read request body to EOF")
	}
}

func TestDecodeJSONRejectsMultipleJSONValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(`{"value":"ok"} {"value":"second"}`))

	var payload struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(req, &payload); err == nil {
		t.Fatalf("expected decodeJSON to reject multiple JSON values")
	}
}

type failOnceQueue struct {
	base            store.MessageQueueStore
	mu              sync.Mutex
	failNextEnqueue bool
	failNextDequeue bool
}

type flakyBindTokenStore struct {
	*store.MemoryStore
	mu                 sync.Mutex
	failCreateBindLeft int
}

type flakyStateWriteStore struct {
	*store.MemoryStore
	mu                    sync.Mutex
	failMetadataWriteLeft int
}

func (s *flakyStateWriteStore) consumeWriteFailure() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failMetadataWriteLeft <= 0 {
		return false
	}
	s.failMetadataWriteLeft--
	return true
}

func (s *flakyBindTokenStore) CreateBindToken(orgID string, ownerHumanID *string, actorHumanID, bindID, bindTokenHash string, expiresAt, now time.Time, isSuperAdmin bool) (model.BindToken, error) {
	s.mu.Lock()
	shouldFail := s.failCreateBindLeft > 0
	if shouldFail {
		s.failCreateBindLeft--
	}
	s.mu.Unlock()
	if shouldFail {
		return model.BindToken{}, context.DeadlineExceeded
	}
	return s.MemoryStore.CreateBindToken(orgID, ownerHumanID, actorHumanID, bindID, bindTokenHash, expiresAt, now, isSuperAdmin)
}

func (s *flakyStateWriteStore) UpdateAgentMetadataSelf(agentUUID string, metadata map[string]any, now time.Time) (model.Agent, error) {
	if s.consumeWriteFailure() {
		return model.Agent{}, context.DeadlineExceeded
	}
	return s.MemoryStore.UpdateAgentMetadataSelf(agentUUID, metadata, now)
}

func (s *flakyStateWriteStore) SetAgentPresence(agentUUID string, presence map[string]any, now time.Time) (model.Agent, bool, error) {
	if s.consumeWriteFailure() {
		return model.Agent{}, false, context.DeadlineExceeded
	}
	return s.MemoryStore.SetAgentPresence(agentUUID, presence, now)
}

func (s *flakyStateWriteStore) UpdateAgentMetadataSelfBestEffort(agentUUID string, metadata map[string]any, now time.Time) (model.Agent, error) {
	return s.MemoryStore.UpdateAgentMetadataSelf(agentUUID, metadata, now)
}

func (q *failOnceQueue) Enqueue(ctx context.Context, message model.Message) error {
	q.mu.Lock()
	fail := q.failNextEnqueue
	if q.failNextEnqueue {
		q.failNextEnqueue = false
	}
	q.mu.Unlock()
	if fail {
		return errors.New("enqueue unavailable")
	}
	return q.base.Enqueue(ctx, message)
}

func (q *failOnceQueue) Dequeue(ctx context.Context, agentUUID string) (model.Message, bool, error) {
	q.mu.Lock()
	fail := q.failNextDequeue
	if q.failNextDequeue {
		q.failNextDequeue = false
	}
	q.mu.Unlock()
	if fail {
		return model.Message{}, false, errors.New("dequeue unavailable")
	}
	return q.base.Dequeue(ctx, agentUUID)
}

type slowPeerOutboxStore struct {
	*store.MemoryStore
	listDelay time.Duration
	listCalls atomic.Int32
}

func (s *slowPeerOutboxStore) ListDuePeerOutbounds(now time.Time, limit int) []model.PeerOutboundMessage {
	s.listCalls.Add(1)
	time.Sleep(s.listDelay)
	return nil
}

func TestPeerOutboxProcessingCoalescesConcurrentKicks(t *testing.T) {
	st := &slowPeerOutboxStore{
		MemoryStore: store.NewMemoryStore(),
		listDelay:   150 * time.Millisecond,
	}
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	h.peerOutboxTimeout = 2 * time.Second

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.kickPeerOutboxProcessing(16)
		}()
	}
	wg.Wait()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		got := st.listCalls.Load()
		if got > 1 {
			t.Fatalf("expected one outbox drain while worker is active, got %d", got)
		}
		if got == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := st.listCalls.Load(); got != 1 {
		t.Fatalf("expected a single outbox drain call, got %d", got)
	}
}

func TestHealthReportsRuntimeQueueFailureAndRecovery(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	queue := &failOnceQueue{
		base:            mem,
		failNextEnqueue: true,
	}
	h := NewHandler(mem, queue, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	h.SetStorageHealth(store.StorageHealthStatus{
		StartupMode: store.StorageStartupModeDegraded,
		State: store.StorageBackendHealth{
			Backend: "s3",
			Healthy: true,
		},
		Queue: store.StorageBackendHealth{
			Backend: "s3",
			Healthy: true,
		},
	})
	router := NewRouter(h)

	_, _, tokenA, _, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	failedPublish := publish(t, router, tokenA, agentUUIDB, "first")
	if failedPublish.Code != http.StatusInternalServerError {
		t.Fatalf("expected publish 500 on enqueue failure, got %d %s", failedPublish.Code, failedPublish.Body.String())
	}

	healthAfterFailure := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if healthAfterFailure.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", healthAfterFailure.Code, healthAfterFailure.Body.String())
	}
	payload := decodeJSONMap(t, healthAfterFailure.Body.Bytes())
	if got, _ := payload["status"].(string); got != "degraded" {
		t.Fatalf("expected status degraded after runtime enqueue failure, got %q payload=%v", got, payload)
	}
	storageObj, _ := payload["storage"].(map[string]any)
	queueObj, _ := storageObj["queue"].(map[string]any)
	if healthy, _ := queueObj["healthy"].(bool); healthy {
		t.Fatalf("expected queue health false after runtime enqueue failure, got %v payload=%v", queueObj["healthy"], payload)
	}
	queueErr, _ := queueObj["error"].(string)
	if !strings.Contains(queueErr, "queue enqueue failed") {
		t.Fatalf("expected runtime queue error summary after enqueue failure, got %q payload=%v", queueErr, payload)
	}
	if !strings.Contains(queueErr, "enqueue unavailable") {
		t.Fatalf("expected runtime queue error to preserve safe enqueue detail, got %q payload=%v", queueErr, payload)
	}
	queueRuntimeErr, _ := queueObj["runtime_error"].(string)
	if !strings.Contains(queueRuntimeErr, "queue enqueue failed") {
		t.Fatalf("expected queue runtime error after enqueue failure, got %q payload=%v", queueRuntimeErr, payload)
	}

	successfulPublish := publish(t, router, tokenA, agentUUIDB, "second")
	if successfulPublish.Code != http.StatusAccepted {
		t.Fatalf("expected publish 202 after queue recovers, got %d %s", successfulPublish.Code, successfulPublish.Body.String())
	}

	healthAfterRecovery := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if healthAfterRecovery.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", healthAfterRecovery.Code, healthAfterRecovery.Body.String())
	}
	recoveryPayload := decodeJSONMap(t, healthAfterRecovery.Body.Bytes())
	if got, _ := recoveryPayload["status"].(string); got != "ok" {
		t.Fatalf("expected status ok after queue recovers, got %q payload=%v", got, recoveryPayload)
	}
	recoveryStorage, _ := recoveryPayload["storage"].(map[string]any)
	recoveryQueue, _ := recoveryStorage["queue"].(map[string]any)
	if healthy, _ := recoveryQueue["healthy"].(bool); !healthy {
		t.Fatalf("expected queue health true after successful enqueue, got %v payload=%v", recoveryQueue["healthy"], recoveryPayload)
	}
	if _, exists := recoveryQueue["error"]; exists {
		t.Fatalf("expected queue error cleared after recovery, got payload=%v", recoveryPayload)
	}
	if _, exists := recoveryQueue["runtime_error"]; exists {
		t.Fatalf("expected queue runtime error cleared after recovery, got payload=%v", recoveryPayload)
	}
}

func TestHealthReportsRuntimeDequeueFailureAndRecovery(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	queue := &failOnceQueue{
		base:            mem,
		failNextDequeue: true,
	}
	h := NewHandler(mem, queue, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	h.SetStorageHealth(store.StorageHealthStatus{
		StartupMode: store.StorageStartupModeDegraded,
		State: store.StorageBackendHealth{
			Backend: "s3",
			Healthy: true,
		},
		Queue: store.StorageBackendHealth{
			Backend: "s3",
			Healthy: true,
		},
	})
	router := NewRouter(h)

	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	publishResp := publish(t, router, tokenA, agentUUIDB, "queued-before-pull")
	if publishResp.Code != http.StatusAccepted {
		t.Fatalf("expected publish 202, got %d %s", publishResp.Code, publishResp.Body.String())
	}

	failedPull := pull(t, router, tokenB, 10)
	if failedPull.Code != http.StatusInternalServerError {
		t.Fatalf("expected pull 500 on dequeue failure, got %d %s", failedPull.Code, failedPull.Body.String())
	}

	healthAfterFailure := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if healthAfterFailure.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", healthAfterFailure.Code, healthAfterFailure.Body.String())
	}
	payload := decodeJSONMap(t, healthAfterFailure.Body.Bytes())
	if got, _ := payload["status"].(string); got != "degraded" {
		t.Fatalf("expected status degraded after runtime dequeue failure, got %q payload=%v", got, payload)
	}
	storageObj, _ := payload["storage"].(map[string]any)
	queueObj, _ := storageObj["queue"].(map[string]any)
	if healthy, _ := queueObj["healthy"].(bool); healthy {
		t.Fatalf("expected queue health false after runtime dequeue failure, got %v payload=%v", queueObj["healthy"], payload)
	}
	queueErr, _ := queueObj["error"].(string)
	if !strings.Contains(queueErr, "queue dequeue failed") {
		t.Fatalf("expected runtime queue error summary after dequeue failure, got %q payload=%v", queueErr, payload)
	}
	if !strings.Contains(queueErr, "dequeue unavailable") {
		t.Fatalf("expected runtime queue error to preserve safe dequeue detail, got %q payload=%v", queueErr, payload)
	}
	queueRuntimeErr, _ := queueObj["runtime_error"].(string)
	if !strings.Contains(queueRuntimeErr, "queue dequeue failed") {
		t.Fatalf("expected queue runtime error after dequeue failure, got %q payload=%v", queueRuntimeErr, payload)
	}

	recoveredPull := pull(t, router, tokenB, 10)
	if recoveredPull.Code != http.StatusOK {
		t.Fatalf("expected pull 200 after queue recovers, got %d %s", recoveredPull.Code, recoveredPull.Body.String())
	}

	healthAfterRecovery := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if healthAfterRecovery.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", healthAfterRecovery.Code, healthAfterRecovery.Body.String())
	}
	recoveryPayload := decodeJSONMap(t, healthAfterRecovery.Body.Bytes())
	if got, _ := recoveryPayload["status"].(string); got != "ok" {
		t.Fatalf("expected status ok after dequeue recovers, got %q payload=%v", got, recoveryPayload)
	}
	recoveryStorage, _ := recoveryPayload["storage"].(map[string]any)
	recoveryQueue, _ := recoveryStorage["queue"].(map[string]any)
	if healthy, _ := recoveryQueue["healthy"].(bool); !healthy {
		t.Fatalf("expected queue health true after successful dequeue, got %v payload=%v", recoveryQueue["healthy"], recoveryPayload)
	}
	if _, exists := recoveryQueue["error"]; exists {
		t.Fatalf("expected queue error cleared after dequeue recovery, got payload=%v", recoveryPayload)
	}
	if _, exists := recoveryQueue["runtime_error"]; exists {
		t.Fatalf("expected queue runtime error cleared after dequeue recovery, got payload=%v", recoveryPayload)
	}
}

func TestAgentMetadataPatchUsesBestEffortFallbackWhenStateWriteFails(t *testing.T) {
	stateStore := &flakyStateWriteStore{
		MemoryStore:           store.NewMemoryStore(),
		failMetadataWriteLeft: 1,
	}
	waiters := longpoll.NewWaiters()
	h := NewHandler(stateStore, stateStore, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	h.SetStorageHealth(store.StorageHealthStatus{
		StartupMode: store.StorageStartupModeDegraded,
		State: store.StorageBackendHealth{
			Backend: "s3",
			Healthy: true,
		},
		Queue: store.StorageBackendHealth{
			Backend: "memory",
			Healthy: true,
		},
	})
	router := NewRouter(h)

	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)

	updateResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"agent_type":       "openclaw",
			"profile_markdown": "# Ready",
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("expected metadata patch 200 with degraded fallback, got %d %s", updateResp.Code, updateResp.Body.String())
	}
	updatePayload := decodeJSONMap(t, updateResp.Body.Bytes())
	result := requireAgentRuntimeSuccessEnvelope(t, updatePayload)
	agent, _ := result["agent"].(map[string]any)
	metadata, _ := agent["metadata"].(map[string]any)
	if got := readStringPath(metadata, "agent_type"); got != "openclaw" {
		t.Fatalf("expected metadata.agent_type=openclaw, got %q payload=%v", got, updatePayload)
	}

	healthAfterFallback := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if healthAfterFallback.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", healthAfterFallback.Code, healthAfterFallback.Body.String())
	}
	fallbackPayload := decodeJSONMap(t, healthAfterFallback.Body.Bytes())
	if got := readStringPath(fallbackPayload, "status"); got != "degraded" {
		t.Fatalf("expected health status degraded after runtime state failure, got %q payload=%v", got, fallbackPayload)
	}
	storageObj, _ := fallbackPayload["storage"].(map[string]any)
	stateObj, _ := storageObj["state"].(map[string]any)
	if healthy, _ := stateObj["healthy"].(bool); healthy {
		t.Fatalf("expected state health false after runtime state failure, got %v payload=%v", stateObj["healthy"], fallbackPayload)
	}
	stateErr, _ := stateObj["error"].(string)
	if stateErr != "request timed out" {
		t.Fatalf("expected sanitized runtime state error after metadata fallback, got %q payload=%v", stateErr, fallbackPayload)
	}
	if stateRuntimeErr, _ := stateObj["runtime_error"].(string); stateRuntimeErr != "state agent metadata update failed: request timed out" {
		t.Fatalf("expected state runtime error after metadata fallback, got %q payload=%v", stateRuntimeErr, fallbackPayload)
	}

	retryResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"profile_markdown": "# Recovered",
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if retryResp.Code != http.StatusOK {
		t.Fatalf("expected metadata retry 200 after state write recovery, got %d %s", retryResp.Code, retryResp.Body.String())
	}

	healthAfterRecovery := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if healthAfterRecovery.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", healthAfterRecovery.Code, healthAfterRecovery.Body.String())
	}
	recoveryPayload := decodeJSONMap(t, healthAfterRecovery.Body.Bytes())
	if got := readStringPath(recoveryPayload, "status"); got != "ok" {
		t.Fatalf("expected health status ok after metadata state recovery, got %q payload=%v", got, recoveryPayload)
	}
	recoveryStorage, _ := recoveryPayload["storage"].(map[string]any)
	recoveryState, _ := recoveryStorage["state"].(map[string]any)
	if healthy, _ := recoveryState["healthy"].(bool); !healthy {
		t.Fatalf("expected state health true after metadata state recovery, got %v payload=%v", recoveryState["healthy"], recoveryPayload)
	}
	if _, exists := recoveryState["error"]; exists {
		t.Fatalf("expected state runtime error cleared after successful strict metadata write, got payload=%v", recoveryPayload)
	}
	if _, exists := recoveryState["runtime_error"]; exists {
		t.Fatalf("expected state runtime details cleared after successful strict metadata write, got payload=%v", recoveryPayload)
	}
}

func TestOpenClawRegisterPluginUsesBestEffortFallbackWhenStateWriteFails(t *testing.T) {
	stateStore := &flakyStateWriteStore{
		MemoryStore:           store.NewMemoryStore(),
		failMetadataWriteLeft: 1,
	}
	waiters := longpoll.NewWaiters()
	h := NewHandler(stateStore, stateStore, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	h.SetStorageHealth(store.StorageHealthStatus{
		StartupMode: store.StorageStartupModeDegraded,
		State: store.StorageBackendHealth{
			Backend: "s3",
			Healthy: true,
		},
		Queue: store.StorageBackendHealth{
			Backend: "memory",
			Healthy: true,
		},
	})
	router := NewRouter(h)

	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)

	registerResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/register-plugin", map[string]any{
		"plugin_id":    "moltenhub-openclaw",
		"package":      "@moltenbot/openclaw-plugin-moltenhub",
		"version":      "0.1.0-test",
		"transport":    "websocket",
		"session_key":  "dedicated-main",
		"session_mode": "dedicated",
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if registerResp.Code != http.StatusOK {
		t.Fatalf("expected register-plugin 200 with degraded fallback, got %d %s", registerResp.Code, registerResp.Body.String())
	}
	registerPayload := decodeJSONMap(t, registerResp.Body.Bytes())
	registerResult := requireAgentRuntimeSuccessEnvelope(t, registerPayload)
	agent, _ := registerResult["agent"].(map[string]any)
	metadata, _ := agent["metadata"].(map[string]any)
	if got := readStringPath(metadata, "agent_type"); got != "openclaw" {
		t.Fatalf("expected metadata.agent_type=openclaw, got %q payload=%v", got, registerPayload)
	}

	healthAfterFallback := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if healthAfterFallback.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", healthAfterFallback.Code, healthAfterFallback.Body.String())
	}
	fallbackPayload := decodeJSONMap(t, healthAfterFallback.Body.Bytes())
	if got := readStringPath(fallbackPayload, "status"); got != "degraded" {
		t.Fatalf("expected health status degraded after register fallback, got %q payload=%v", got, fallbackPayload)
	}
	storageObj, _ := fallbackPayload["storage"].(map[string]any)
	stateObj, _ := storageObj["state"].(map[string]any)
	stateErr, _ := stateObj["error"].(string)
	if stateErr != "request timed out" {
		t.Fatalf("expected sanitized runtime state error after register fallback, got %q payload=%v", stateErr, fallbackPayload)
	}
	if stateRuntimeErr, _ := stateObj["runtime_error"].(string); stateRuntimeErr != "state agent metadata update failed: request timed out" {
		t.Fatalf("expected state runtime error after register fallback, got %q payload=%v", stateRuntimeErr, fallbackPayload)
	}

	registerRetry := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/register-plugin", map[string]any{
		"plugin_id":    "moltenhub-openclaw",
		"package":      "@moltenbot/openclaw-plugin-moltenhub",
		"version":      "0.1.1-test",
		"transport":    "websocket",
		"session_key":  "dedicated-main",
		"session_mode": "dedicated",
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if registerRetry.Code != http.StatusOK {
		t.Fatalf("expected register-plugin retry 200 after state write recovery, got %d %s", registerRetry.Code, registerRetry.Body.String())
	}

	healthAfterRecovery := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if healthAfterRecovery.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", healthAfterRecovery.Code, healthAfterRecovery.Body.String())
	}
	recoveryPayload := decodeJSONMap(t, healthAfterRecovery.Body.Bytes())
	if got := readStringPath(recoveryPayload, "status"); got != "ok" {
		t.Fatalf("expected health status ok after register state recovery, got %q payload=%v", got, recoveryPayload)
	}
	recoveryStorage, _ := recoveryPayload["storage"].(map[string]any)
	recoveryState, _ := recoveryStorage["state"].(map[string]any)
	if healthy, _ := recoveryState["healthy"].(bool); !healthy {
		t.Fatalf("expected state health true after register state recovery, got %v payload=%v", recoveryState["healthy"], recoveryPayload)
	}
	if _, exists := recoveryState["error"]; exists {
		t.Fatalf("expected state runtime error cleared after successful strict register write, got payload=%v", recoveryPayload)
	}
	if _, exists := recoveryState["runtime_error"]; exists {
		t.Fatalf("expected state runtime details cleared after successful strict register write, got payload=%v", recoveryPayload)
	}
}

func TestUIConfigExposesAuthAndRedactsPrivilegedFields(t *testing.T) {
	t.Setenv("DEV_LOGIN_HUMAN_ID", "dev-human")
	t.Setenv("DEV_LOGIN_HUMAN_EMAIL", "dev@local.test")

	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(
		mem,
		mem,
		waiters,
		auth.NewDevHumanAuthProvider(),
		"https://hub.example.com",
		"https://example.supabase.co",
		"should-not-leak",
		"",
		"admin1@example.com,admin2@example.com",
		"example.com",
		true,
		15*time.Minute,
		false,
	)
	router := NewRouter(h)

	resp := doJSONRequest(t, router, http.MethodGet, "/v1/ui/config", nil, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected /v1/ui/config 200, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	authObj, _ := payload["auth"].(map[string]any)
	if got, _ := authObj["human"].(string); got != "dev" {
		t.Fatalf("expected auth.human=dev, got %q payload=%v", got, payload)
	}
	devObj, ok := authObj["dev"].(map[string]any)
	if !ok {
		t.Fatalf("expected auth.dev object for dev provider, payload=%v", payload)
	}
	if got, _ := devObj["human_id"].(string); got != "dev-human" {
		t.Fatalf("expected auth.dev.human_id=dev-human, got %q payload=%v", got, payload)
	}
	if got, _ := devObj["human_email"].(string); got != "dev@local.test" {
		t.Fatalf("expected auth.dev.human_email=dev@local.test, got %q payload=%v", got, payload)
	}
	if _, exists := authObj["supabase"]; exists {
		t.Fatalf("did not expect auth.supabase for dev provider, payload=%v", payload)
	}

	adminObj, ok := payload["admin"].(map[string]any)
	if !ok {
		t.Fatalf("expected admin object, got %T payload=%v", payload["admin"], payload)
	}
	if _, exists := adminObj["emails"]; exists {
		t.Fatalf("expected admin.emails redacted/omitted, got payload=%v", payload)
	}
	domains, ok := adminObj["domains"].([]any)
	if !ok || len(domains) != 1 || domains[0] != "example.com" {
		t.Fatalf("expected admin.domains preserved, got %v payload=%v", adminObj["domains"], payload)
	}
}

func TestUIConfigReturnsSensitiveFieldsWithPrivilegedKey(t *testing.T) {
	t.Setenv("DEV_LOGIN_HUMAN_ID", "dev-human")
	t.Setenv("DEV_LOGIN_HUMAN_EMAIL", "dev@local.test")
	t.Setenv("UI_CONFIG_API_KEY", "ui-config-secret")

	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(
		mem,
		mem,
		waiters,
		auth.NewDevHumanAuthProvider(),
		"https://hub.example.com",
		"https://example.supabase.co",
		"should-leak-only-to-privileged-caller",
		"",
		"admin1@example.com,admin2@example.com",
		"example.com",
		true,
		15*time.Minute,
		false,
	)
	router := NewRouter(h)

	resp := doJSONRequest(t, router, http.MethodGet, "/v1/ui/config", nil, map[string]string{
		"X-UI-Config-Key": "ui-config-secret",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected /v1/ui/config 200, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	authObj, _ := payload["auth"].(map[string]any)
	if got, _ := authObj["human"].(string); got != "dev" {
		t.Fatalf("expected auth.human=dev, got %q payload=%v", got, payload)
	}

	adminObj, ok := payload["admin"].(map[string]any)
	if !ok {
		t.Fatalf("expected admin object, got %T payload=%v", payload["admin"], payload)
	}
	adminEmails, ok := adminObj["emails"].([]any)
	if !ok {
		t.Fatalf("expected admin.emails array for privileged caller, got %T payload=%v", adminObj["emails"], payload)
	}
	if len(adminEmails) != 2 || adminEmails[0] != "admin1@example.com" || adminEmails[1] != "admin2@example.com" {
		t.Fatalf("expected admin.emails for privileged caller, got %v", adminEmails)
	}
}

func TestUIConfigKeepsPrivilegedFieldsRedactedWithWrongPrivilegedKey(t *testing.T) {
	t.Setenv("DEV_LOGIN_HUMAN_EMAIL", "dev@local.test")
	t.Setenv("UI_CONFIG_API_KEY", "ui-config-secret")

	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(
		mem,
		mem,
		waiters,
		auth.NewDevHumanAuthProvider(),
		"https://hub.example.com",
		"https://example.supabase.co",
		"should-not-leak",
		"",
		"admin1@example.com,admin2@example.com",
		"example.com",
		true,
		15*time.Minute,
		false,
	)
	router := NewRouter(h)

	resp := doJSONRequest(t, router, http.MethodGet, "/v1/ui/config", nil, map[string]string{
		"X-UI-Config-Key": "wrong-key",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected /v1/ui/config 200, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	adminObj, ok := payload["admin"].(map[string]any)
	if !ok {
		t.Fatalf("expected admin object, got %T payload=%v", payload["admin"], payload)
	}
	if _, exists := adminObj["emails"]; exists {
		t.Fatalf("expected redacted admin.emails for wrong key, got %v", adminObj["emails"])
	}
}

func TestUIConfigSupabaseOmitsSecretKey(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(
		mem,
		mem,
		waiters,
		auth.NewSupabaseAuthProvider("https://example.supabase.co", "sb_secret_should_not_leak"),
		"https://hub.example.com",
		"https://example.supabase.co",
		"sb_secret_should_not_leak",
		"",
		"",
		"",
		false,
		15*time.Minute,
		false,
	)
	router := NewRouter(h)

	resp := doJSONRequest(t, router, http.MethodGet, "/v1/ui/config", nil, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected /v1/ui/config 200, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	authObj, _ := payload["auth"].(map[string]any)
	supabaseObj, ok := authObj["supabase"].(map[string]any)
	if !ok {
		t.Fatalf("expected auth.supabase object, payload=%v", payload)
	}
	if _, exists := supabaseObj["anon_key"]; exists {
		t.Fatalf("expected auth.supabase.anon_key omitted for secret key, payload=%v", payload)
	}
}

func TestUIConfigSupabaseIncludesBrowserSafeKey(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(
		mem,
		mem,
		waiters,
		auth.NewSupabaseAuthProvider("https://example.supabase.co", "sb_publishable_safe"),
		"https://hub.example.com",
		"https://example.supabase.co",
		"sb_publishable_safe",
		"",
		"",
		"",
		false,
		15*time.Minute,
		false,
	)
	router := NewRouter(h)

	resp := doJSONRequest(t, router, http.MethodGet, "/v1/ui/config", nil, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected /v1/ui/config 200, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	authObj, _ := payload["auth"].(map[string]any)
	supabaseObj, ok := authObj["supabase"].(map[string]any)
	if !ok {
		t.Fatalf("expected auth.supabase object, payload=%v", payload)
	}
	if got, _ := supabaseObj["anon_key"].(string); got != "sb_publishable_safe" {
		t.Fatalf("expected safe anon_key in ui config, got %q payload=%v", got, payload)
	}
}

func doJSONRequest(t *testing.T, router http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func humanHeaders(humanID, email string) map[string]string {
	return map[string]string{
		"X-Human-Id":    humanID,
		"X-Human-Email": email,
	}
}

func ensureHandleConfirmed(t *testing.T, router http.Handler, humanID, email string) {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
		"handle": humanID,
	}, humanHeaders(humanID, email))
	if resp.Code != http.StatusOK {
		t.Fatalf("confirm handle failed: %d %s", resp.Code, resp.Body.String())
	}
}

func createOrg(t *testing.T, router http.Handler, humanID, email, name string) string {
	t.Helper()
	ensureHandleConfirmed(t, router, humanID, email)
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{
		"handle":       normalizeHandle(name),
		"display_name": name,
	}, humanHeaders(humanID, email))
	if resp.Code != http.StatusCreated {
		t.Fatalf("create org failed: %d %s", resp.Code, resp.Body.String())
	}
	var payload map[string]map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create org: %v", err)
	}
	orgID, _ := payload["organization"]["org_id"].(string)
	if orgID == "" {
		t.Fatalf("missing org_id")
	}
	return orgID
}

func currentHumanID(t *testing.T, router http.Handler, humanID, email string) string {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodGet, "/v1/me", nil, humanHeaders(humanID, email))
	if resp.Code != http.StatusOK {
		t.Fatalf("get me failed: %d %s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode me response: %v", err)
	}
	humanObj, _ := payload["human"].(map[string]any)
	id, _ := humanObj["human_id"].(string)
	if id == "" {
		t.Fatalf("missing human_id in /v1/me response")
	}
	return id
}

func createInvite(t *testing.T, router http.Handler, humanID, email, orgID, inviteeEmail, role string) string {
	t.Helper()
	ensureHandleConfirmed(t, router, humanID, email)
	inviteID, _ := createInviteWithCode(t, router, humanID, email, orgID, inviteeEmail, role)
	return inviteID
}

func createInviteWithCode(t *testing.T, router http.Handler, humanID, email, orgID, inviteeEmail, role string) (string, string) {
	t.Helper()
	ensureHandleConfirmed(t, router, humanID, email)
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/orgs/"+orgID+"/invites", map[string]string{
		"email": inviteeEmail,
		"role":  role,
	}, humanHeaders(humanID, email))
	if resp.Code != http.StatusCreated {
		t.Fatalf("create invite failed: %d %s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode invite: %v", err)
	}
	inviteObj, _ := payload["invite"].(map[string]any)
	inviteID, _ := inviteObj["invite_id"].(string)
	if inviteID == "" {
		t.Fatalf("missing invite_id")
	}
	// Invite secret is no longer returned on create; redeem flows should use invite_id.
	return inviteID, ""
}

func acceptInvite(t *testing.T, router http.Handler, humanID, email, inviteID string) {
	t.Helper()
	ensureHandleConfirmed(t, router, humanID, email)
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/org-invites/"+inviteID+"/accept", nil, humanHeaders(humanID, email))
	if resp.Code != http.StatusOK {
		t.Fatalf("accept invite failed: %d %s", resp.Code, resp.Body.String())
	}
}

func registerAgentWithUUID(t *testing.T, router http.Handler, humanID, email, orgID, agentID, ownerHumanID string) (string, string) {
	t.Helper()
	ensureHandleConfirmed(t, router, humanID, email)
	return bindAgentWithUUIDForOwner(t, router, humanID, email, orgID, agentID, ownerHumanID)
}

func bindAgentWithUUID(t *testing.T, router http.Handler, humanID, email, orgID, agentID string) (string, string) {
	t.Helper()
	return bindAgentWithUUIDForOwner(t, router, humanID, email, orgID, agentID, "")
}

func bindAgentWithUUIDForOwner(t *testing.T, router http.Handler, humanID, email, orgID, agentID, ownerHumanID string) (string, string) {
	t.Helper()
	bindReq := map[string]any{
		"org_id": orgID,
	}
	if strings.TrimSpace(ownerHumanID) != "" {
		bindReq["owner_human_id"] = ownerHumanID
	}
	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind-tokens", bindReq, humanHeaders(humanID, email))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp.Body.Bytes())
	bindToken, _ := createPayload["bind_token"].(string)
	if bindToken == "" {
		t.Fatalf("bind token missing")
	}

	redeemResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]any{
		"bind_token": bindToken,
		"handle":     agentID,
	}, nil)
	if redeemResp.Code != http.StatusCreated {
		t.Fatalf("bind redeem failed: %d %s", redeemResp.Code, redeemResp.Body.String())
	}
	redeemPayload := decodeJSONMap(t, redeemResp.Body.Bytes())
	token, _ := redeemPayload["token"].(string)
	if token == "" {
		t.Fatalf("bind response missing token")
	}

	meResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if meResp.Code != http.StatusOK {
		t.Fatalf("agent me failed: %d %s", meResp.Code, meResp.Body.String())
	}
	mePayload := decodeJSONMap(t, meResp.Body.Bytes())
	agent, _ := mePayload["agent"].(map[string]any)
	agentUUID, _ := agent["agent_uuid"].(string)
	if agentUUID == "" {
		t.Fatalf("agent me missing agent_uuid")
	}
	gotAgentID, _ := agent["agent_id"].(string)
	expectedURI := "https://hub.example.com/" + strings.ReplaceAll(url.PathEscape(gotAgentID), "%2F", "/")
	if gotURI, _ := agent["uri"].(string); gotURI != expectedURI {
		t.Fatalf("expected canonical agent uri, got %q payload=%v", gotURI, agent)
	}
	return token, agentUUID
}

func registerAgent(t *testing.T, router http.Handler, humanID, email, orgID, agentID, ownerHumanID string) string {
	t.Helper()
	token, _ := registerAgentWithUUID(t, router, humanID, email, orgID, agentID, ownerHumanID)
	return token
}

func registerMyAgent(t *testing.T, router http.Handler, humanID, email, orgID, agentID string) (string, string, string) {
	t.Helper()
	ensureHandleConfirmed(t, router, humanID, email)
	createBody := map[string]any{
		"org_id": orgID,
	}
	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/bind-tokens", createBody, humanHeaders(humanID, email))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create my bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp.Body.Bytes())
	bindToken, _ := createPayload["bind_token"].(string)
	if bindToken == "" {
		t.Fatalf("bind token missing")
	}

	redeemResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]any{
		"bind_token": bindToken,
		"handle":     agentID,
	}, nil)
	if redeemResp.Code != http.StatusCreated {
		t.Fatalf("redeem my bind token failed: %d %s", redeemResp.Code, redeemResp.Body.String())
	}
	redeemPayload := decodeJSONMap(t, redeemResp.Body.Bytes())
	token, _ := redeemPayload["token"].(string)
	if token == "" {
		t.Fatalf("redeem response missing token")
	}

	meResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if meResp.Code != http.StatusOK {
		t.Fatalf("agent me failed: %d %s", meResp.Code, meResp.Body.String())
	}
	mePayload := decodeJSONMap(t, meResp.Body.Bytes())
	agentObj, _ := mePayload["agent"].(map[string]any)
	returnedOrgID, _ := agentObj["org_id"].(string)
	agentUUID, _ := agentObj["agent_uuid"].(string)
	if token == "" || agentUUID == "" {
		t.Fatalf("missing token or agent_uuid")
	}
	if strings.TrimSpace(orgID) != "" && strings.TrimSpace(returnedOrgID) != strings.TrimSpace(orgID) {
		t.Fatalf("expected response org_id %q, got %q", orgID, returnedOrgID)
	}
	return token, returnedOrgID, agentUUID
}

func createOrgAccessKey(t *testing.T, router http.Handler, humanID, email, orgID, label string, scopes []string) (string, string) {
	t.Helper()
	ensureHandleConfirmed(t, router, humanID, email)
	body := map[string]any{
		"label":  label,
		"scopes": scopes,
	}
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/orgs/"+orgID+"/access-keys", body, humanHeaders(humanID, email))
	if resp.Code != http.StatusCreated {
		t.Fatalf("create org access key failed: %d %s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create org access key: %v", err)
	}
	keyObj, _ := payload["access_key"].(map[string]any)
	keyID, _ := keyObj["key_id"].(string)
	secret, _ := payload["key"].(string)
	if keyID == "" || secret == "" {
		t.Fatalf("missing key_id or key secret")
	}
	return keyID, secret
}

func publish(t *testing.T, router http.Handler, senderToken, toAgentUUID, payload string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSONRequest(t, router, http.MethodPost, "/v1/messages/publish", map[string]string{
		"to_agent_uuid": toAgentUUID,
		"content_type":  "text/plain",
		"payload":       payload,
	}, map[string]string{"Authorization": "Bearer " + senderToken})
}

func publishWithClientMsgID(t *testing.T, router http.Handler, senderToken, toAgentUUID, payload, clientMsgID string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSONRequest(t, router, http.MethodPost, "/v1/messages/publish", map[string]string{
		"to_agent_uuid": toAgentUUID,
		"content_type":  "text/plain",
		"payload":       payload,
		"client_msg_id": clientMsgID,
	}, map[string]string{"Authorization": "Bearer " + senderToken})
}

func pull(t *testing.T, router http.Handler, token string, timeoutMS int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/messages/pull?timeout_ms=%d", timeoutMS), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func ackDelivery(t *testing.T, router http.Handler, token, deliveryID string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSONRequest(t, router, http.MethodPost, "/v1/messages/ack", map[string]string{
		"delivery_id": deliveryID,
	}, map[string]string{"Authorization": "Bearer " + token})
}

func nackDelivery(t *testing.T, router http.Handler, token, deliveryID string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSONRequest(t, router, http.MethodPost, "/v1/messages/nack", map[string]string{
		"delivery_id": deliveryID,
	}, map[string]string{"Authorization": "Bearer " + token})
}

func messageStatus(t *testing.T, router http.Handler, token, messageID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/messages/"+messageID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func decodeJSONMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode json map: %v body=%s", err, string(body))
	}
	return m
}

func requireAgentRuntimeSuccessEnvelope(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	okValue, ok := payload["ok"].(bool)
	if !ok || !okValue {
		t.Fatalf("expected ok=true success envelope, got payload=%v", payload)
	}
	result, ok := payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object in success envelope, got %T payload=%v", payload["result"], payload)
	}
	return result
}

func setupTrustedAgents(t *testing.T, router http.Handler) (string, string, string, string, string, string, string, string) {
	t.Helper()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Org A")
	orgB := createOrg(t, router, "bob", "bob@b.test", "Org B")

	tokenA, agentUUIDA := registerAgentWithUUID(t, router, "alice", "alice@a.test", orgA, "agent-a", aliceHumanID)
	tokenB, agentUUIDB := registerAgentWithUUID(t, router, "bob", "bob@b.test", orgB, "agent-b", bobHumanID)

	orgTrustResp := doJSONRequest(t, router, http.MethodPost, "/v1/org-trusts", map[string]string{
		"org_id":      orgA,
		"peer_org_id": orgB,
	}, humanHeaders("alice", "alice@a.test"))
	if orgTrustResp.Code != http.StatusCreated {
		t.Fatalf("org trust create failed: %d %s", orgTrustResp.Code, orgTrustResp.Body.String())
	}
	orgTrust := decodeJSONMap(t, orgTrustResp.Body.Bytes())
	orgTrustObj := orgTrust["trust"].(map[string]any)
	orgTrustID := orgTrustObj["edge_id"].(string)
	orgApprove := doJSONRequest(t, router, http.MethodPost, "/v1/org-trusts/"+orgTrustID+"/approve", nil, humanHeaders("bob", "bob@b.test"))
	if orgApprove.Code != http.StatusOK {
		t.Fatalf("org trust approve failed: %d %s", orgApprove.Code, orgApprove.Body.String())
	}

	agentTrustResp := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts", map[string]string{
		"org_id":          orgA,
		"agent_uuid":      agentUUIDA,
		"peer_agent_uuid": agentUUIDB,
	}, humanHeaders("alice", "alice@a.test"))
	if agentTrustResp.Code != http.StatusCreated {
		t.Fatalf("agent trust create failed: %d %s", agentTrustResp.Code, agentTrustResp.Body.String())
	}
	agentTrust := decodeJSONMap(t, agentTrustResp.Body.Bytes())
	agentTrustObj := agentTrust["trust"].(map[string]any)
	agentTrustID := agentTrustObj["edge_id"].(string)
	agentApprove := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts/"+agentTrustID+"/approve", nil, humanHeaders("bob", "bob@b.test"))
	if agentApprove.Code != http.StatusOK {
		t.Fatalf("agent trust approve failed: %d %s", agentApprove.Code, agentApprove.Body.String())
	}

	return orgA, orgB, tokenA, tokenB, orgTrustID, agentTrustID, agentUUIDA, agentUUIDB
}

func TestInviteAcceptFlow(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org A")
	inviteID := createInvite(t, router, "alice", "alice@a.test", orgID, "bob@b.test", "member")
	acceptInvite(t, router, "bob", "bob@b.test", inviteID)

	resp := doJSONRequest(t, router, http.MethodGet, "/v1/orgs/"+orgID+"/humans", nil, humanHeaders("alice", "alice@a.test"))
	if resp.Code != http.StatusOK {
		t.Fatalf("list humans failed: %d %s", resp.Code, resp.Body.String())
	}
	body := decodeJSONMap(t, resp.Body.Bytes())
	humans := body["humans"].([]any)
	if len(humans) < 2 {
		t.Fatalf("expected at least 2 humans, got %d", len(humans))
	}
}

func TestInviteAcceptTransfersPersonalAgentIntoOrgAndKeepsOwnerManagement(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org Transfer")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")

	_, personalOrgID, bobAgentUUID := registerMyAgent(t, router, "bob", "bob@b.test", "", "bob-personal-agent")
	if personalOrgID != "" {
		t.Fatalf("expected personal agent org_id empty before invite acceptance, got %q", personalOrgID)
	}

	inviteID := createInvite(t, router, "alice", "alice@a.test", orgID, "bob@b.test", "member")
	acceptInvite(t, router, "bob", "bob@b.test", inviteID)

	orgAgentsResp := doJSONRequest(t, router, http.MethodGet, "/v1/orgs/"+orgID+"/agents", nil, humanHeaders("alice", "alice@a.test"))
	if orgAgentsResp.Code != http.StatusOK {
		t.Fatalf("expected org agents list 200 after invite accept, got %d %s", orgAgentsResp.Code, orgAgentsResp.Body.String())
	}
	orgAgentsPayload := decodeJSONMap(t, orgAgentsResp.Body.Bytes())
	orgAgents, _ := orgAgentsPayload["agents"].([]any)
	foundTransferredInOrg := false
	for _, raw := range orgAgents {
		agent, _ := raw.(map[string]any)
		if gotAgentUUID, _ := agent["agent_uuid"].(string); gotAgentUUID != bobAgentUUID {
			continue
		}
		foundTransferredInOrg = true
		if gotOrgID, _ := agent["org_id"].(string); gotOrgID != orgID {
			t.Fatalf("expected transferred agent org_id %q, got %q payload=%v", orgID, gotOrgID, agent)
		}
		owner, _ := agent["owner"].(map[string]any)
		if gotOwnerHumanID, _ := owner["human_id"].(string); gotOwnerHumanID != bobHumanID {
			t.Fatalf("expected transferred agent owner.human_id %q, got %q payload=%v", bobHumanID, gotOwnerHumanID, agent)
		}
	}
	if !foundTransferredInOrg {
		t.Fatalf("expected transferred agent %q in org agent list", bobAgentUUID)
	}

	bobAgentsResp := doJSONRequest(t, router, http.MethodGet, "/v1/me/agents", nil, humanHeaders("bob", "bob@b.test"))
	if bobAgentsResp.Code != http.StatusOK {
		t.Fatalf("expected bob /v1/me/agents 200, got %d %s", bobAgentsResp.Code, bobAgentsResp.Body.String())
	}
	bobAgentsPayload := decodeJSONMap(t, bobAgentsResp.Body.Bytes())
	bobAgents, _ := bobAgentsPayload["agents"].([]any)
	foundTransferredInBobList := false
	for _, raw := range bobAgents {
		agent, _ := raw.(map[string]any)
		if gotAgentUUID, _ := agent["agent_uuid"].(string); gotAgentUUID != bobAgentUUID {
			continue
		}
		foundTransferredInBobList = true
		if gotOrgID, _ := agent["org_id"].(string); gotOrgID != orgID {
			t.Fatalf("expected bob-managed transferred agent org_id %q, got %q payload=%v", orgID, gotOrgID, agent)
		}
	}
	if !foundTransferredInBobList {
		t.Fatalf("expected transferred agent %q in bob /v1/me/agents", bobAgentUUID)
	}

	rotateResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/"+bobAgentUUID+"/rotate-token", nil, humanHeaders("bob", "bob@b.test"))
	if rotateResp.Code != http.StatusOK {
		t.Fatalf("expected transferred agent owner rotate token to succeed, got %d %s", rotateResp.Code, rotateResp.Body.String())
	}
}

func TestOrgOwnerCanListMemberAgentsWithinOrgContext(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org Member Agent Visibility")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")
	charlieHumanID := currentHumanID(t, router, "charlie", "charlie@c.test")

	inviteID := createInvite(t, router, "alice", "alice@a.test", orgID, "bob@b.test", "member")
	acceptInvite(t, router, "bob", "bob@b.test", inviteID)

	otherOrgID := createOrg(t, router, "bob", "bob@b.test", "Bob Other Org")
	_, bobOtherOrgAgentUUID := registerAgentWithUUID(t, router, "bob", "bob@b.test", otherOrgID, "bob-other-org", bobHumanID)
	revokeOtherOrgAgent := doJSONRequest(
		t,
		router,
		http.MethodDelete,
		"/v1/agents/"+bobOtherOrgAgentUUID,
		nil,
		humanHeaders("bob", "bob@b.test"),
	)
	if revokeOtherOrgAgent.Code != http.StatusOK {
		t.Fatalf("expected bob other-org revoke to succeed, got %d %s", revokeOtherOrgAgent.Code, revokeOtherOrgAgent.Body.String())
	}

	_, bobPersonalOrgID, bobPersonalAgentUUID := registerMyAgent(t, router, "bob", "bob@b.test", "", "bob-personal-visible")
	if bobPersonalOrgID != "" {
		t.Fatalf("expected bob personal agent org_id empty, got %q", bobPersonalOrgID)
	}
	_, bobOrgScopedAgentUUID := registerAgentWithUUID(t, router, "bob", "bob@b.test", orgID, "bob-org-visible", bobHumanID)

	ownerList := doJSONRequest(
		t,
		router,
		http.MethodGet,
		"/v1/orgs/"+orgID+"/humans/"+bobHumanID+"/agents",
		nil,
		humanHeaders("alice", "alice@a.test"),
	)
	if ownerList.Code != http.StatusOK {
		t.Fatalf("expected owner member-agent list 200, got %d %s", ownerList.Code, ownerList.Body.String())
	}
	ownerPayload := decodeJSONMap(t, ownerList.Body.Bytes())
	ownerAgents, _ := ownerPayload["agents"].([]any)
	foundPersonal := false
	foundOrgScoped := false
	foundOtherOrg := false
	for _, raw := range ownerAgents {
		agent, _ := raw.(map[string]any)
		agentUUID, _ := agent["agent_uuid"].(string)
		switch agentUUID {
		case bobPersonalAgentUUID:
			foundPersonal = true
		case bobOrgScopedAgentUUID:
			foundOrgScoped = true
		case bobOtherOrgAgentUUID:
			foundOtherOrg = true
		}
	}
	if !foundPersonal {
		t.Fatalf("expected owner list to include bob personal agent %q", bobPersonalAgentUUID)
	}
	if !foundOrgScoped {
		t.Fatalf("expected owner list to include bob org-scoped agent %q", bobOrgScopedAgentUUID)
	}
	if foundOtherOrg {
		t.Fatalf("did not expect owner list to include bob other-org agent %q", bobOtherOrgAgentUUID)
	}

	memberList := doJSONRequest(
		t,
		router,
		http.MethodGet,
		"/v1/orgs/"+orgID+"/humans/"+bobHumanID+"/agents",
		nil,
		humanHeaders("bob", "bob@b.test"),
	)
	if memberList.Code != http.StatusForbidden {
		t.Fatalf("expected member list to be forbidden, got %d %s", memberList.Code, memberList.Body.String())
	}

	unknownMembership := doJSONRequest(
		t,
		router,
		http.MethodGet,
		"/v1/orgs/"+orgID+"/humans/"+charlieHumanID+"/agents",
		nil,
		humanHeaders("alice", "alice@a.test"),
	)
	if unknownMembership.Code != http.StatusNotFound {
		t.Fatalf("expected unknown member list to return 404, got %d %s", unknownMembership.Code, unknownMembership.Body.String())
	}
}

func TestInviteCodeRedeemFlow(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org Invite Codes")
	inviteID, _ := createInviteWithCode(t, router, "alice", "alice@a.test", orgID, "bob@b.test", "member")
	ensureHandleConfirmed(t, router, "bob", "bob@b.test")

	listInvites := doJSONRequest(t, router, http.MethodGet, "/v1/orgs/"+orgID+"/invites", nil, humanHeaders("alice", "alice@a.test"))
	if listInvites.Code != http.StatusOK {
		t.Fatalf("list org invites failed: %d %s", listInvites.Code, listInvites.Body.String())
	}
	listPayload := decodeJSONMap(t, listInvites.Body.Bytes())
	invites, _ := listPayload["invites"].([]any)
	if len(invites) != 1 {
		t.Fatalf("expected exactly 1 invite, got %d", len(invites))
	}

	redeem := doJSONRequest(t, router, http.MethodPost, "/v1/org-invites/redeem", map[string]string{
		"invite_id": inviteID,
	}, humanHeaders("bob", "bob@b.test"))
	if redeem.Code != http.StatusOK {
		t.Fatalf("redeem invite code failed: %d %s", redeem.Code, redeem.Body.String())
	}

	humansResp := doJSONRequest(t, router, http.MethodGet, "/v1/orgs/"+orgID+"/humans", nil, humanHeaders("alice", "alice@a.test"))
	if humansResp.Code != http.StatusOK {
		t.Fatalf("list humans failed: %d %s", humansResp.Code, humansResp.Body.String())
	}
	humansPayload := decodeJSONMap(t, humansResp.Body.Bytes())
	humans, _ := humansPayload["humans"].([]any)
	if len(humans) < 2 {
		t.Fatalf("expected at least 2 humans after redeem, got %d", len(humans))
	}
}

func TestInviteCodeRedeemRejectsWrongEmail(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org Invite Security")
	inviteID, _ := createInviteWithCode(t, router, "alice", "alice@a.test", orgID, "bob@b.test", "member")
	ensureHandleConfirmed(t, router, "charlie", "charlie@c.test")

	redeem := doJSONRequest(t, router, http.MethodPost, "/v1/org-invites/redeem", map[string]string{
		"invite_id": inviteID,
	}, humanHeaders("charlie", "charlie@c.test"))
	if redeem.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for wrong-email invite redeem, got %d %s", redeem.Code, redeem.Body.String())
	}
}

func TestOwnerCanRevokeHumanMembership(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "owner", "owner@a.test", "Org Human Revoke")
	inviteID := createInvite(t, router, "owner", "owner@a.test", orgID, "member@a.test", "member")
	acceptInvite(t, router, "member", "member@a.test", inviteID)
	memberHumanID := currentHumanID(t, router, "member", "member@a.test")

	revoke := doJSONRequest(
		t,
		router,
		http.MethodDelete,
		"/v1/orgs/"+orgID+"/humans/"+memberHumanID,
		nil,
		humanHeaders("owner", "owner@a.test"),
	)
	if revoke.Code != http.StatusOK {
		t.Fatalf("revoke human failed: %d %s", revoke.Code, revoke.Body.String())
	}

	memberList := doJSONRequest(t, router, http.MethodGet, "/v1/orgs/"+orgID+"/humans", nil, humanHeaders("member", "member@a.test"))
	if memberList.Code != http.StatusForbidden {
		t.Fatalf("expected revoked member access to be forbidden, got %d %s", memberList.Code, memberList.Body.String())
	}
}

func TestOnlyOwnerCanDeleteOrganization(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "owner", "owner@a.test", "Org Owner Delete Guard")
	inviteMember := createInvite(t, router, "owner", "owner@a.test", orgID, "member@a.test", "member")
	acceptInvite(t, router, "member", "member@a.test", inviteMember)
	inviteAdmin := createInvite(t, router, "owner", "owner@a.test", orgID, "admin@a.test", "admin")
	acceptInvite(t, router, "admin", "admin@a.test", inviteAdmin)

	ownerHumanID := currentHumanID(t, router, "owner", "owner@a.test")
	_, agentUUID := registerAgentWithUUID(t, router, "owner", "owner@a.test", orgID, "owner-agent", ownerHumanID)

	memberDeleteOrg := doJSONRequest(t, router, http.MethodDelete, "/v1/orgs/"+orgID, nil, humanHeaders("member", "member@a.test"))
	if memberDeleteOrg.Code != http.StatusForbidden {
		t.Fatalf("expected member org delete to be forbidden, got %d %s", memberDeleteOrg.Code, memberDeleteOrg.Body.String())
	}

	adminDeleteOrg := doJSONRequest(t, router, http.MethodDelete, "/v1/orgs/"+orgID, nil, humanHeaders("admin", "admin@a.test"))
	if adminDeleteOrg.Code != http.StatusForbidden {
		t.Fatalf("expected admin org delete to be forbidden, got %d %s", adminDeleteOrg.Code, adminDeleteOrg.Body.String())
	}

	listAgents := doJSONRequest(t, router, http.MethodGet, "/v1/orgs/"+orgID+"/agents", nil, humanHeaders("owner", "owner@a.test"))
	if listAgents.Code != http.StatusOK {
		t.Fatalf("expected owner agent list after forbidden deletes to succeed, got %d %s", listAgents.Code, listAgents.Body.String())
	}
	listPayload := decodeJSONMap(t, listAgents.Body.Bytes())
	agents, _ := listPayload["agents"].([]any)
	foundAgent := false
	for _, raw := range agents {
		agent, _ := raw.(map[string]any)
		if gotAgentUUID, _ := agent["agent_uuid"].(string); gotAgentUUID == agentUUID {
			foundAgent = true
			break
		}
	}
	if !foundAgent {
		t.Fatalf("expected org agent to remain after forbidden org delete attempts")
	}

	ownerDeleteOrg := doJSONRequest(t, router, http.MethodDelete, "/v1/orgs/"+orgID, nil, humanHeaders("owner", "owner@a.test"))
	if ownerDeleteOrg.Code != http.StatusOK {
		t.Fatalf("expected owner org delete to succeed, got %d %s", ownerDeleteOrg.Code, ownerDeleteOrg.Body.String())
	}
}

func TestRoleEnforcementOnInvites(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "owner", "owner@a.test", "Org A")
	inviteID := createInvite(t, router, "owner", "owner@a.test", orgID, "viewer@a.test", "viewer")
	acceptInvite(t, router, "viewer", "viewer@a.test", inviteID)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/orgs/"+orgID+"/invites", map[string]string{
		"email": "x@a.test",
		"role":  "member",
	}, humanHeaders("viewer", "viewer@a.test"))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer invite creation, got %d", resp.Code)
	}
}

func TestOrgAccessKeyScopedReadsByOrgName(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org Share")
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	registerAgent(t, router, "alice", "alice@a.test", orgID, "agent-share", aliceHumanID)

	_, humansOnlyKey := createOrgAccessKey(t, router, "alice", "alice@a.test", orgID, "Humans Only", []string{"list_humans"})
	humansResp := doJSONRequest(
		t,
		router,
		http.MethodGet,
		"/v1/org-access/humans?org_name=Org%20Share",
		nil,
		map[string]string{"X-Org-Access-Key": humansOnlyKey},
	)
	if humansResp.Code != http.StatusOK {
		t.Fatalf("org-access humans failed: %d %s", humansResp.Code, humansResp.Body.String())
	}

	agentsDenied := doJSONRequest(
		t,
		router,
		http.MethodGet,
		"/v1/org-access/agents?org_name=Org%20Share",
		nil,
		map[string]string{"X-Org-Access-Key": humansOnlyKey},
	)
	if agentsDenied.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing list_agents scope, got %d body=%s", agentsDenied.Code, agentsDenied.Body.String())
	}

	keyID, fullKey := createOrgAccessKey(
		t,
		router,
		"alice",
		"alice@a.test",
		orgID,
		"Humans + Agents",
		[]string{"list_humans", "list_agents"},
	)
	agentsAllowed := doJSONRequest(
		t,
		router,
		http.MethodGet,
		"/v1/org-access/agents?org_name=Org%20Share",
		nil,
		map[string]string{"X-Org-Access-Key": fullKey},
	)
	if agentsAllowed.Code != http.StatusOK {
		t.Fatalf("expected 200 for list_agents scope, got %d body=%s", agentsAllowed.Code, agentsAllowed.Body.String())
	}

	revokeResp := doJSONRequest(t, router, http.MethodDelete, "/v1/orgs/"+orgID+"/access-keys/"+keyID, nil, humanHeaders("alice", "alice@a.test"))
	if revokeResp.Code != http.StatusOK {
		t.Fatalf("revoke org access key failed: %d %s", revokeResp.Code, revokeResp.Body.String())
	}

	agentsAfterRevoke := doJSONRequest(
		t,
		router,
		http.MethodGet,
		"/v1/org-access/agents?org_name=Org%20Share",
		nil,
		map[string]string{"X-Org-Access-Key": fullKey},
	)
	if agentsAfterRevoke.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked key, got %d body=%s", agentsAfterRevoke.Code, agentsAfterRevoke.Body.String())
	}
}

func TestAgentRegisterHumanAndOrgOwned(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org A")
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	registerAgent(t, router, "alice", "alice@a.test", orgID, "a-human", aliceHumanID)
	registerAgent(t, router, "alice", "alice@a.test", orgID, "a-org", "")
}

func TestMyAgentsAndImmediateSelfBond(t *testing.T) {
	router := newTestRouter()
	tokenA, orgA, agentUUIDA := registerMyAgent(t, router, "alice", "alice@a.test", "", "alice-agent-a")
	_, orgB, agentUUIDB := registerMyAgent(t, router, "alice", "alice@a.test", "", "alice-agent-b")
	if orgA != "" || orgB != "" {
		t.Fatalf("expected human-scoped agents without org, got %q and %q", orgA, orgB)
	}

	listResp := doJSONRequest(t, router, http.MethodGet, "/v1/me/agents", nil, humanHeaders("alice", "alice@a.test"))
	if listResp.Code != http.StatusOK {
		t.Fatalf("list my agents failed: %d %s", listResp.Code, listResp.Body.String())
	}
	listPayload := decodeJSONMap(t, listResp.Body.Bytes())
	agents, ok := listPayload["agents"].([]any)
	if !ok || len(agents) < 2 {
		t.Fatalf("expected at least 2 managed agents, got %v", listPayload["agents"])
	}

	bondResp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agent-trusts", map[string]any{
		"agent_uuid":      agentUUIDA,
		"peer_agent_uuid": agentUUIDB,
	}, humanHeaders("alice", "alice@a.test"))
	if bondResp.Code != http.StatusCreated {
		t.Fatalf("create self bond failed: %d %s", bondResp.Code, bondResp.Body.String())
	}
	bondPayload := decodeJSONMap(t, bondResp.Body.Bytes())
	trust, ok := bondPayload["trust"].(map[string]any)
	if !ok {
		t.Fatalf("missing trust payload: %v", bondPayload)
	}
	if trust["state"] != "active" {
		t.Fatalf("expected immediate active trust, got %v", trust["state"])
	}
	if trust["left_approved"] != true || trust["right_approved"] != true {
		t.Fatalf("expected bilateral approvals true, got left=%v right=%v", trust["left_approved"], trust["right_approved"])
	}

	graphResp := doJSONRequest(t, router, http.MethodGet, "/v1/me/agent-trusts", nil, humanHeaders("alice", "alice@a.test"))
	if graphResp.Code != http.StatusOK {
		t.Fatalf("list my agent trusts failed: %d %s", graphResp.Code, graphResp.Body.String())
	}
	graphPayload := decodeJSONMap(t, graphResp.Body.Bytes())
	edges, ok := graphPayload["agent_trusts"].([]any)
	if !ok || len(edges) == 0 {
		t.Fatalf("expected non-empty agent_trusts list, got %v", graphPayload["agent_trusts"])
	}

	pubResp := publish(t, router, tokenA, agentUUIDB, "hello-self-bond")
	if pubResp.Code != http.StatusAccepted {
		t.Fatalf("publish should be accepted after self-bond: %d %s", pubResp.Code, pubResp.Body.String())
	}
	pubPayload := decodeJSONMap(t, pubResp.Body.Bytes())
	if pubPayload["status"] != "queued" {
		t.Fatalf("expected queued message, got %v", pubPayload["status"])
	}
}

func TestOrgMemberCanRequestAgentTrustForOwnerReview(t *testing.T) {
	router := newTestRouter()
	orgA := createOrg(t, router, "alice", "alice@a.test", "Org A")
	orgB := createOrg(t, router, "bob", "bob@b.test", "Org B")

	inviteID := createInvite(t, router, "alice", "alice@a.test", orgA, "charlie@c.test", "member")
	acceptInvite(t, router, "charlie", "charlie@c.test", inviteID)

	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")
	_, agentUUIDA := registerAgentWithUUID(t, router, "alice", "alice@a.test", orgA, "agent-a", aliceHumanID)
	_, agentUUIDB := registerAgentWithUUID(t, router, "bob", "bob@b.test", orgB, "agent-b", bobHumanID)

	requestResp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agent-trusts", map[string]any{
		"org_id":          orgA,
		"agent_uuid":      agentUUIDA,
		"peer_agent_uuid": agentUUIDB,
	}, humanHeaders("charlie", "charlie@c.test"))
	if requestResp.Code != http.StatusCreated {
		t.Fatalf("member trust request failed: %d %s", requestResp.Code, requestResp.Body.String())
	}
	requestPayload := decodeJSONMap(t, requestResp.Body.Bytes())
	requestedTrust, _ := requestPayload["trust"].(map[string]any)
	edgeID, _ := requestedTrust["edge_id"].(string)
	if edgeID == "" {
		t.Fatalf("expected trust edge_id in response")
	}
	if requestedTrust["state"] != "pending" {
		t.Fatalf("expected pending trust from member request, got %v", requestedTrust["state"])
	}
	if requestedTrust["left_approved"] != false || requestedTrust["right_approved"] != false {
		t.Fatalf("expected no auto-approval for member request, got left=%v right=%v", requestedTrust["left_approved"], requestedTrust["right_approved"])
	}

	memberApprove := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts/"+edgeID+"/approve", nil, humanHeaders("charlie", "charlie@c.test"))
	if memberApprove.Code != http.StatusForbidden {
		t.Fatalf("expected member approve forbidden, got %d %s", memberApprove.Code, memberApprove.Body.String())
	}

	ownerList := doJSONRequest(t, router, http.MethodGet, "/v1/me/agent-trusts", nil, humanHeaders("alice", "alice@a.test"))
	if ownerList.Code != http.StatusOK {
		t.Fatalf("owner list trusts failed: %d %s", ownerList.Code, ownerList.Body.String())
	}
	ownerPayload := decodeJSONMap(t, ownerList.Body.Bytes())
	ownerEdges, ok := ownerPayload["agent_trusts"].([]any)
	if !ok {
		t.Fatalf("expected agent_trusts array, got %T", ownerPayload["agent_trusts"])
	}
	ownerSeesRequest := false
	for _, entry := range ownerEdges {
		edge, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if edge["edge_id"] == edgeID {
			ownerSeesRequest = true
			break
		}
	}
	if !ownerSeesRequest {
		t.Fatalf("expected org owner to see member-submitted request edge=%s", edgeID)
	}

	ownerApprove := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts/"+edgeID+"/approve", nil, humanHeaders("alice", "alice@a.test"))
	if ownerApprove.Code != http.StatusOK {
		t.Fatalf("owner approve failed: %d %s", ownerApprove.Code, ownerApprove.Body.String())
	}
	ownerApprovePayload := decodeJSONMap(t, ownerApprove.Body.Bytes())
	ownerApproveTrust, _ := ownerApprovePayload["trust"].(map[string]any)
	if ownerApproveTrust["state"] != "pending" {
		t.Fatalf("expected pending after one-side approve, got %v", ownerApproveTrust["state"])
	}

	peerApprove := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts/"+edgeID+"/approve", nil, humanHeaders("bob", "bob@b.test"))
	if peerApprove.Code != http.StatusOK {
		t.Fatalf("peer owner approve failed: %d %s", peerApprove.Code, peerApprove.Body.String())
	}
	peerApprovePayload := decodeJSONMap(t, peerApprove.Body.Bytes())
	peerApproveTrust, _ := peerApprovePayload["trust"].(map[string]any)
	if peerApproveTrust["state"] != "active" {
		t.Fatalf("expected active after second-side approve, got %v", peerApproveTrust["state"])
	}
	if peerApproveTrust["left_approved"] != true || peerApproveTrust["right_approved"] != true {
		t.Fatalf("expected bilateral approvals true after second-side approve, got left=%v right=%v", peerApproveTrust["left_approved"], peerApproveTrust["right_approved"])
	}
}

func TestMyAgentBindTokenRedeemUsesRequestedHandle(t *testing.T) {
	router := newTestRouter()
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")
	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/bind-tokens", map[string]any{}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create my bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp.Body.Bytes())
	bindToken, _ := createPayload["bind_token"].(string)
	if bindToken == "" {
		t.Fatalf("bind_token missing")
	}

	redeemResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]string{
		"hub_url":    "https://hub.qa.example.com",
		"bind_token": bindToken,
		"handle":     "alice-agent-picked-name",
	}, nil)
	if redeemResp.Code != http.StatusCreated {
		t.Fatalf("redeem bind token failed: %d %s", redeemResp.Code, redeemResp.Body.String())
	}
	redeemPayload := decodeJSONMap(t, redeemResp.Body.Bytes())
	token, _ := redeemPayload["token"].(string)
	if token == "" {
		t.Fatalf("expected bind response token")
	}
	apiBase, _ := redeemPayload["api_base"].(string)
	if apiBase != "http://example.com/v1" {
		t.Fatalf("expected bind response api_base, got %q", apiBase)
	}
	endpoints, _ := redeemPayload["endpoints"].(map[string]any)
	if got, _ := endpoints["publish"].(string); got != "http://example.com/v1/messages/publish" {
		t.Fatalf("expected bind response publish endpoint, got %q", got)
	}

	capsResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/capabilities", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if capsResp.Code != http.StatusOK {
		t.Fatalf("agent capabilities failed: %d %s", capsResp.Code, capsResp.Body.String())
	}
	capsPayload := decodeJSONMap(t, capsResp.Body.Bytes())
	capsAgent, _ := capsPayload["agent"].(map[string]any)
	agentUUID, _ := capsAgent["agent_uuid"].(string)
	if strings.TrimSpace(agentUUID) == "" {
		t.Fatalf("expected capabilities payload agent_uuid")
	}
	initialAgentID, _ := capsAgent["agent_id"].(string)
	if !strings.HasSuffix(initialAgentID, "/alice-agent-picked-name") {
		t.Fatalf("expected bind redeem to use requested handle, got %q", initialAgentID)
	}
	initialURI, _ := capsAgent["uri"].(string)
	if initialURI != "https://hub.example.com/human/alice/agent/alice-agent-picked-name" {
		t.Fatalf("expected fully qualified canonical agent uri, got %q", initialURI)
	}

	firstFinalize := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me", map[string]any{
		"handle": "alice-agent-picked-name",
	}, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if firstFinalize.Code != http.StatusOK {
		t.Fatalf("expected first finalize patch to return 200, got %d %s", firstFinalize.Code, firstFinalize.Body.String())
	}
	firstFinalizePayload := decodeJSONMap(t, firstFinalize.Body.Bytes())
	firstFinalizeAgent, _ := firstFinalizePayload["agent"].(map[string]any)
	if firstFinalizeAgent["agent_uuid"] != agentUUID {
		t.Fatalf("expected finalize to update authenticated agent %q, got %v", agentUUID, firstFinalizeAgent["agent_uuid"])
	}
	if firstFinalizeAgent["handle"] != "alice-agent-picked-name" {
		t.Fatalf("expected finalized handle alice-agent-picked-name, got %v", firstFinalizeAgent["handle"])
	}
	firstFinalizeAgentID, _ := firstFinalizeAgent["agent_id"].(string)
	if !strings.HasSuffix(firstFinalizeAgentID, "/alice-agent-picked-name") {
		t.Fatalf("expected finalized URI to end with /alice-agent-picked-name, got %q", firstFinalizeAgentID)
	}

	secondFinalize := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me", map[string]any{
		"handle": "alice-agent-second-name",
	}, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if secondFinalize.Code != http.StatusConflict {
		t.Fatalf("expected second finalize patch to return 409, got %d %s", secondFinalize.Code, secondFinalize.Body.String())
	}
	secondFinalizePayload := decodeJSONMap(t, secondFinalize.Body.Bytes())
	if secondFinalizePayload["error"] != "agent_handle_locked" {
		t.Fatalf("expected second finalize error agent_handle_locked, got %v payload=%v", secondFinalizePayload["error"], secondFinalizePayload)
	}
}

func TestMyAgentBindTokenRedeemDuplicateHandleReturnsSuggestions(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Bind Duplicate")
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	registerAgentWithUUID(t, router, "alice", "alice@a.test", orgID, "launch-agent-a", aliceHumanID)

	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/bind-tokens", map[string]any{
		"org_id": orgID,
	}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create my bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	bindToken, _ := decodeJSONMap(t, createResp.Body.Bytes())["bind_token"].(string)
	if bindToken == "" {
		t.Fatalf("bind_token missing")
	}

	redeemResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]any{
		"bind_token": bindToken,
		"handle":     "launch-agent-a",
	}, nil)
	if redeemResp.Code != http.StatusConflict {
		t.Fatalf("expected duplicate handle bind to return 409, got %d %s", redeemResp.Code, redeemResp.Body.String())
	}
	redeemPayload := decodeJSONMap(t, redeemResp.Body.Bytes())
	if redeemPayload["error"] != "agent_exists" {
		t.Fatalf("expected agent_exists, got %v payload=%v", redeemPayload["error"], redeemPayload)
	}
	if retryable, _ := redeemPayload["retryable"].(bool); !retryable {
		t.Fatalf("expected retryable duplicate bind response, got %v payload=%v", redeemPayload["retryable"], redeemPayload)
	}
	nextAction, _ := redeemPayload["next_action"].(string)
	if strings.TrimSpace(nextAction) == "" {
		t.Fatalf("expected next_action guidance for duplicate bind response, got %v", redeemPayload["next_action"])
	}
	suggestedHandles, _ := redeemPayload["suggested_handles"].([]any)
	if len(suggestedHandles) == 0 {
		t.Fatalf("expected suggested_handles in duplicate bind response, got %v", redeemPayload["suggested_handles"])
	}
	detail, _ := redeemPayload["error_detail"].(map[string]any)
	if detail == nil {
		t.Fatalf("expected error_detail object, got %v", redeemPayload["error_detail"])
	}
	detailSuggestions, _ := detail["suggested_handles"].([]any)
	if len(detailSuggestions) == 0 {
		t.Fatalf("expected error_detail.suggested_handles in duplicate bind response, got %v", detail["suggested_handles"])
	}
	got, _ := suggestedHandles[0].(string)
	if got = strings.TrimSpace(got); got == "" || got == "launch-agent-a" {
		t.Fatalf("expected first suggested handle to differ from original, got %q", got)
	}
}

func TestMyAgentBindTokenCreateIncludesConnectPrompt(t *testing.T) {
	router := newTestRouter()
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/bind-tokens", map[string]any{}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create my bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp.Body.Bytes())
	bindToken, _ := createPayload["bind_token"].(string)
	if bindToken == "" {
		t.Fatalf("bind_token missing")
	}
	connectPrompt, _ := createPayload["connect_prompt"].(string)
	if !strings.Contains(connectPrompt, bindToken) {
		t.Fatalf("expected connect prompt to contain bind token, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "http://example.com/v1/agents/bind") {
		t.Fatalf("expected connect prompt to contain bind api url, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "http://example.com/v1/agents/me/skill") {
		t.Fatalf("expected connect prompt to contain skill url, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "Bind Scope: Personal") {
		t.Fatalf("expected personal scope in connect prompt, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "\"handle\":\"<your-agent-handle>\"") {
		t.Fatalf("expected connect prompt to instruct agent to pass desired handle, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "agent_exists") || !strings.Contains(connectPrompt, "<your-agent-handle>-2") {
		t.Fatalf("expected connect prompt to include duplicate-handle retry guidance, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "token") || !strings.Contains(connectPrompt, "api_base") || !strings.Contains(connectPrompt, "endpoints") {
		t.Fatalf("expected connect prompt to require persisting token/api_base/endpoints, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "GET {api_base}/agents/me/skill") {
		t.Fatalf("expected connect prompt to include skill read step, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "display_name") || !strings.Contains(connectPrompt, "emoji") || !strings.Contains(connectPrompt, "agent_type") || !strings.Contains(connectPrompt, "llm") || !strings.Contains(connectPrompt, "harness") {
		t.Fatalf("expected connect prompt to include profile metadata guidance, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "metadata.presence") || !strings.Contains(connectPrompt, "server-managed") {
		t.Fatalf("expected connect prompt to describe server-managed presence, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "control_plane.can_communicate=true") || !strings.Contains(connectPrompt, "POST {api_base}/messages/publish") {
		t.Fatalf("expected connect prompt to include readiness + first publish guidance, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "Optional OpenClaw-only hints (not required):") {
		t.Fatalf("expected connect prompt to include optional OpenClaw hints heading, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "@moltenbot/openclaw-plugin-moltenhub") {
		t.Fatalf("expected connect prompt to include OpenClaw plugin package hint, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "workspace/.moltenhub/config.json") {
		t.Fatalf("expected connect prompt to include optional workspace config path hint, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "\"baseUrl\":\"<api_base>\"") || !strings.Contains(connectPrompt, "\"sessionKey\":\"main\"") || !strings.Contains(connectPrompt, "\"timeoutMs\":20000") {
		t.Fatalf("expected connect prompt to include optional config shape hint, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "continue with core `/v1/messages/*` routes") {
		t.Fatalf("expected connect prompt to preserve non-plugin fallback guidance, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "Treat both the bind token and returned bearer token as secrets.") {
		t.Fatalf("expected connect prompt to include token secrecy guidance, got %q", connectPrompt)
	}
}

func TestMyAgentBindTokenCreateWithoutPromptOmitsConnectPrompt(t *testing.T) {
	router := newTestRouter()
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/bind-token", map[string]any{}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create my bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp.Body.Bytes())
	if bindToken, _ := createPayload["bind_token"].(string); strings.TrimSpace(bindToken) == "" {
		t.Fatalf("bind_token missing")
	}
	if _, ok := createPayload["connect_prompt"]; ok {
		t.Fatalf("expected connect_prompt to be omitted, payload=%v", createPayload)
	}
}

func TestCreateBindTokenWithoutPromptOmitsConnectPrompt(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Alpha Org")

	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind-token", map[string]any{
		"org_id": orgID,
	}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp.Body.Bytes())
	if bindToken, _ := createPayload["bind_token"].(string); strings.TrimSpace(bindToken) == "" {
		t.Fatalf("bind_token missing")
	}
	if got, _ := createPayload["org_id"].(string); got != orgID {
		t.Fatalf("expected org_id %q, got %q", orgID, got)
	}
	if _, ok := createPayload["connect_prompt"]; ok {
		t.Fatalf("expected connect_prompt to be omitted, payload=%v", createPayload)
	}
}

func TestMyAgentBindTokenCreateRetriesTransientStoreError(t *testing.T) {
	mem := &flakyBindTokenStore{
		MemoryStore:        store.NewMemoryStore(),
		failCreateBindLeft: 1,
	}
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	router := NewRouter(h)
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/bind-tokens", map[string]any{}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected transient create bind failure to retry and succeed, got %d %s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp.Body.Bytes())
	if token, _ := createPayload["bind_token"].(string); strings.TrimSpace(token) == "" {
		t.Fatalf("expected bind_token after retry success, payload=%v", createPayload)
	}
}

func TestMyAgentBindTokenCreateReturns503AfterRepeatedTransientStoreError(t *testing.T) {
	mem := &flakyBindTokenStore{
		MemoryStore:        store.NewMemoryStore(),
		failCreateBindLeft: 2,
	}
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	router := NewRouter(h)
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/bind-tokens", map[string]any{}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected repeated transient failure to return 503, got %d %s", createResp.Code, createResp.Body.String())
	}
	payload := decodeJSONMap(t, createResp.Body.Bytes())
	if payload["error"] != "store_error" {
		t.Fatalf("expected store_error for repeated transient failure, got %v payload=%v", payload["error"], payload)
	}
}

func TestMyAgentBindTokenCreateUsesForwardedHostInConnectPrompt(t *testing.T) {
	router := newTestRouter()
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	req := httptest.NewRequest(http.MethodPost, "/v1/me/agents/bind-tokens", bytes.NewReader([]byte(`{}`)))
	req.Host = "127.0.0.1:8081"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Human-Id", "alice")
	req.Header.Set("X-Human-Email", "alice@a.test")
	req.Header.Set("X-Forwarded-Host", "hub.qa.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create my bind token failed: %d %s", resp.Code, resp.Body.String())
	}

	connectPrompt, _ := decodeJSONMap(t, resp.Body.Bytes())["connect_prompt"].(string)
	if !strings.Contains(connectPrompt, "https://hub.qa.example.com/v1/agents/bind") {
		t.Fatalf("expected forwarded bind api url in connect prompt, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "https://hub.qa.example.com/v1/agents/me/skill") {
		t.Fatalf("expected forwarded skill url in connect prompt, got %q", connectPrompt)
	}
	if strings.Contains(connectPrompt, "127.0.0.1:8081") {
		t.Fatalf("expected forwarded host to replace loopback host, got %q", connectPrompt)
	}
}

func TestMyAgentBindTokenCreateFallsBackToCanonicalBaseWhenHostIsLoopback(t *testing.T) {
	router := newTestRouter()
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	req := httptest.NewRequest(http.MethodPost, "/v1/me/agents/bind-tokens", bytes.NewReader([]byte(`{}`)))
	req.Host = "127.0.0.1:8081"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Human-Id", "alice")
	req.Header.Set("X-Human-Email", "alice@a.test")

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create my bind token failed: %d %s", resp.Code, resp.Body.String())
	}

	connectPrompt, _ := decodeJSONMap(t, resp.Body.Bytes())["connect_prompt"].(string)
	if !strings.Contains(connectPrompt, "https://hub.example.com/v1/agents/bind") {
		t.Fatalf("expected canonical bind api url in connect prompt, got %q", connectPrompt)
	}
	if strings.Contains(connectPrompt, "127.0.0.1:8081") {
		t.Fatalf("expected canonical base to replace loopback host, got %q", connectPrompt)
	}
}

func TestRedeemBindTokenUsesForwardedHostInAPIBase(t *testing.T) {
	router := newTestRouter()
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/bind-tokens", map[string]any{}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create my bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	bindToken, _ := decodeJSONMap(t, createResp.Body.Bytes())["bind_token"].(string)
	if bindToken == "" {
		t.Fatalf("bind_token missing")
	}

	reqBody := bytes.NewReader([]byte(fmt.Sprintf(`{"bind_token":%q}`, bindToken)))
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/bind", reqBody)
	req.Host = "127.0.0.1:8081"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "hub.qa.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("redeem bind token failed: %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	apiBase, _ := payload["api_base"].(string)
	if apiBase != "https://hub.qa.example.com/v1" {
		t.Fatalf("expected forwarded api_base, got %q", apiBase)
	}
	endpoints, _ := payload["endpoints"].(map[string]any)
	if got, _ := endpoints["skill"].(string); got != "https://hub.qa.example.com/v1/agents/me/skill" {
		t.Fatalf("expected forwarded skill endpoint, got %q", got)
	}
}

func TestTrustLifecycleAndBlockPrecedence(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, orgTrustID, _, _, agentUUIDB := setupTrustedAgents(t, router)

	resp := publish(t, router, tokenA, agentUUIDB, "hello-before-block")
	if resp.Code != http.StatusAccepted {
		t.Fatalf("publish should be accepted queued before block: %d %s", resp.Code, resp.Body.String())
	}
	before := decodeJSONMap(t, resp.Body.Bytes())
	if before["status"] != "queued" {
		t.Fatalf("expected queued, got %v", before["status"])
	}

	block := doJSONRequest(t, router, http.MethodPost, "/v1/org-trusts/"+orgTrustID+"/block", nil, humanHeaders("bob", "bob@b.test"))
	if block.Code != http.StatusOK {
		t.Fatalf("block failed: %d %s", block.Code, block.Body.String())
	}

	resp = publish(t, router, tokenA, agentUUIDB, "hello-after-block")
	after := decodeJSONMap(t, resp.Body.Bytes())
	if after["status"] != "dropped" {
		t.Fatalf("expected dropped after block, got %v", after["status"])
	}
}

func TestPublishDropAndQueuedPaths(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Org A")
	orgB := createOrg(t, router, "bob", "bob@b.test", "Org B")
	tokenA, agentUUIDA := registerAgentWithUUID(t, router, "alice", "alice@a.test", orgA, "agent-a", aliceHumanID)
	tokenB, agentUUIDB := registerAgentWithUUID(t, router, "bob", "bob@b.test", orgB, "agent-b", bobHumanID)

	resp := publish(t, router, tokenA, agentUUIDB, "no-trust")
	if resp.Code != http.StatusAccepted {
		t.Fatalf("publish without trust should still be 202: %d", resp.Code)
	}
	noTrust := decodeJSONMap(t, resp.Body.Bytes())
	if noTrust["status"] != "dropped" {
		t.Fatalf("expected dropped, got %v", noTrust["status"])
	}

	orgTrustResp := doJSONRequest(t, router, http.MethodPost, "/v1/org-trusts", map[string]string{
		"org_id":      orgA,
		"peer_org_id": orgB,
	}, humanHeaders("alice", "alice@a.test"))
	orgTrust := decodeJSONMap(t, orgTrustResp.Body.Bytes())
	orgTrustID := orgTrust["trust"].(map[string]any)["edge_id"].(string)
	doJSONRequest(t, router, http.MethodPost, "/v1/org-trusts/"+orgTrustID+"/approve", nil, humanHeaders("bob", "bob@b.test"))

	agentTrustResp := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts", map[string]string{
		"org_id":          orgA,
		"agent_uuid":      agentUUIDA,
		"peer_agent_uuid": agentUUIDB,
	}, humanHeaders("alice", "alice@a.test"))
	agentTrust := decodeJSONMap(t, agentTrustResp.Body.Bytes())
	agentTrustID := agentTrust["trust"].(map[string]any)["edge_id"].(string)
	doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts/"+agentTrustID+"/approve", nil, humanHeaders("bob", "bob@b.test"))

	resp = publish(t, router, tokenA, agentUUIDB, "has-trust")
	withTrust := decodeJSONMap(t, resp.Body.Bytes())
	if withTrust["status"] != "queued" {
		t.Fatalf("expected queued, got %v", withTrust["status"])
	}

	pullResp := pull(t, router, tokenB, 50)
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected pull 200, got %d", pullResp.Code)
	}
}

func TestRevokedAgentCannotPublishOrPullAndQueuedMessagesArePurged(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, agentUUIDA, agentUUIDB := setupTrustedAgents(t, router)

	queuedBeforeRevoke := publish(t, router, tokenA, agentUUIDB, "queued-before-revoke")
	if queuedBeforeRevoke.Code != http.StatusAccepted {
		t.Fatalf("expected publish before revoke to be accepted, got %d %s", queuedBeforeRevoke.Code, queuedBeforeRevoke.Body.String())
	}
	queuedPayload := decodeJSONMap(t, queuedBeforeRevoke.Body.Bytes())
	if queuedPayload["status"] != "queued" {
		t.Fatalf("expected queued status before revoke, got %v", queuedPayload["status"])
	}

	revokeResp := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+agentUUIDA, nil, humanHeaders("alice", "alice@a.test"))
	if revokeResp.Code != http.StatusOK {
		t.Fatalf("revoke agent failed: %d %s", revokeResp.Code, revokeResp.Body.String())
	}

	publishAfterRevoke := publish(t, router, tokenA, agentUUIDB, "publish-after-revoke")
	if publishAfterRevoke.Code != http.StatusUnauthorized {
		t.Fatalf("expected revoked token publish to return 401, got %d %s", publishAfterRevoke.Code, publishAfterRevoke.Body.String())
	}

	pullAfterRevoke := pull(t, router, tokenA, 0)
	if pullAfterRevoke.Code != http.StatusUnauthorized {
		t.Fatalf("expected revoked token pull to return 401, got %d %s", pullAfterRevoke.Code, pullAfterRevoke.Body.String())
	}

	publishToRevoked := publish(t, router, tokenB, agentUUIDA, "to-revoked")
	if publishToRevoked.Code != http.StatusNotFound {
		t.Fatalf("expected publish to revoked receiver to return 404, got %d %s", publishToRevoked.Code, publishToRevoked.Body.String())
	}

	receiverPull := pull(t, router, tokenB, 0)
	if receiverPull.Code != http.StatusNoContent {
		t.Fatalf("expected revoked sender messages to be purged from receiver queue, got %d %s", receiverPull.Code, receiverPull.Body.String())
	}
}

func TestDeleteAgentRecordAuthorizationMatrix(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org Agent Delete")
	inviteBob := createInvite(t, router, "alice", "alice@a.test", orgID, "bob@b.test", "member")
	acceptInvite(t, router, "bob", "bob@b.test", inviteBob)
	inviteCharlie := createInvite(t, router, "alice", "alice@a.test", orgID, "charlie@c.test", "admin")
	acceptInvite(t, router, "charlie", "charlie@c.test", inviteCharlie)
	inviteDana := createInvite(t, router, "alice", "alice@a.test", orgID, "dana@d.test", "member")
	acceptInvite(t, router, "dana", "dana@d.test", inviteDana)

	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")
	_, bobOwnedAgentUUID := registerAgentWithUUID(t, router, "bob", "bob@b.test", orgID, "bob-agent-self", bobHumanID)
	_, bobOrgManagedAgentUUID := registerAgentWithUUID(t, router, "bob", "bob@b.test", orgID, "bob-agent-org-owner", bobHumanID)
	_, orgOwnedAgentUUID := registerAgentWithUUID(t, router, "alice", "alice@a.test", orgID, "org-agent", "")

	adminDeleteOwned := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+bobOwnedAgentUUID+"/record", nil, humanHeaders("charlie", "charlie@c.test"))
	if adminDeleteOwned.Code != http.StatusForbidden {
		t.Fatalf("expected org admin delete of human-owned agent to be forbidden, got %d %s", adminDeleteOwned.Code, adminDeleteOwned.Body.String())
	}

	memberDeleteOwned := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+bobOwnedAgentUUID+"/record", nil, humanHeaders("dana", "dana@d.test"))
	if memberDeleteOwned.Code != http.StatusForbidden {
		t.Fatalf("expected org member delete of human-owned agent to be forbidden, got %d %s", memberDeleteOwned.Code, memberDeleteOwned.Body.String())
	}

	agentOwnerDelete := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+bobOwnedAgentUUID+"/record", nil, humanHeaders("bob", "bob@b.test"))
	if agentOwnerDelete.Code != http.StatusForbidden {
		t.Fatalf("expected member-owned delete to be forbidden for non-owner member, got %d %s", agentOwnerDelete.Code, agentOwnerDelete.Body.String())
	}

	_, personalOrgID, danaPersonalAgentUUID := registerMyAgent(t, router, "dana", "dana@d.test", "", "dana-personal-delete")
	if strings.TrimSpace(personalOrgID) != "" {
		t.Fatalf("expected personal agent org_id empty, got %q", personalOrgID)
	}
	nonOwnerDeletePersonal := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+danaPersonalAgentUUID+"/record", nil, humanHeaders("bob", "bob@b.test"))
	if nonOwnerDeletePersonal.Code != http.StatusForbidden {
		t.Fatalf("expected non-owner delete of personal agent to be forbidden, got %d %s", nonOwnerDeletePersonal.Code, nonOwnerDeletePersonal.Body.String())
	}
	ownerDeletePersonal := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+danaPersonalAgentUUID+"/record", nil, humanHeaders("dana", "dana@d.test"))
	if ownerDeletePersonal.Code != http.StatusOK {
		t.Fatalf("expected personal owner delete to succeed, got %d %s", ownerDeletePersonal.Code, ownerDeletePersonal.Body.String())
	}
	if decodeJSONMap(t, ownerDeletePersonal.Body.Bytes())["result"] != "deleted" {
		t.Fatalf("expected personal owner delete response to report deleted")
	}

	orgOwnerDeleteHumanOwned := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+bobOwnedAgentUUID+"/record", nil, humanHeaders("alice", "alice@a.test"))
	if orgOwnerDeleteHumanOwned.Code != http.StatusOK {
		t.Fatalf("expected org owner delete of member-owned agent to succeed, got %d %s", orgOwnerDeleteHumanOwned.Code, orgOwnerDeleteHumanOwned.Body.String())
	}
	if decodeJSONMap(t, orgOwnerDeleteHumanOwned.Body.Bytes())["result"] != "deleted" {
		t.Fatalf("expected org owner delete response to report deleted")
	}

	orgOwnerDeleteSecondHumanOwned := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+bobOrgManagedAgentUUID+"/record", nil, humanHeaders("alice", "alice@a.test"))
	if orgOwnerDeleteSecondHumanOwned.Code != http.StatusOK {
		t.Fatalf("expected org owner delete of second human-owned org agent to succeed, got %d %s", orgOwnerDeleteSecondHumanOwned.Code, orgOwnerDeleteSecondHumanOwned.Body.String())
	}

	adminDeleteOrgOwned := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+orgOwnedAgentUUID+"/record", nil, humanHeaders("charlie", "charlie@c.test"))
	if adminDeleteOrgOwned.Code != http.StatusForbidden {
		t.Fatalf("expected org admin delete of org-owned agent to be forbidden, got %d %s", adminDeleteOrgOwned.Code, adminDeleteOrgOwned.Body.String())
	}

	memberDeleteOrgOwned := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+orgOwnedAgentUUID+"/record", nil, humanHeaders("bob", "bob@b.test"))
	if memberDeleteOrgOwned.Code != http.StatusForbidden {
		t.Fatalf("expected non-owner delete of org-owned agent to be forbidden, got %d %s", memberDeleteOrgOwned.Code, memberDeleteOrgOwned.Body.String())
	}

	orgOwnerDeleteOrgOwned := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+orgOwnedAgentUUID+"/record", nil, humanHeaders("alice", "alice@a.test"))
	if orgOwnerDeleteOrgOwned.Code != http.StatusOK {
		t.Fatalf("expected org owner delete of org-owned agent to succeed, got %d %s", orgOwnerDeleteOrgOwned.Code, orgOwnerDeleteOrgOwned.Body.String())
	}
	if decodeJSONMap(t, orgOwnerDeleteOrgOwned.Body.Bytes())["result"] != "deleted" {
		t.Fatalf("expected org-owned delete response to report deleted")
	}

	listResp := doJSONRequest(t, router, http.MethodGet, "/v1/me/agents", nil, humanHeaders("bob", "bob@b.test"))
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected /v1/me/agents 200 after delete, got %d %s", listResp.Code, listResp.Body.String())
	}
	listPayload := decodeJSONMap(t, listResp.Body.Bytes())
	rawAgents, _ := listPayload["agents"].([]any)
	for _, raw := range rawAgents {
		agent, _ := raw.(map[string]any)
		if agent["agent_uuid"] == bobOwnedAgentUUID || agent["agent_uuid"] == bobOrgManagedAgentUUID || agent["agent_uuid"] == orgOwnedAgentUUID {
			t.Fatalf("expected deleted agent to be absent from bob list, got %v", agent)
		}
	}

	danaListResp := doJSONRequest(t, router, http.MethodGet, "/v1/me/agents", nil, humanHeaders("dana", "dana@d.test"))
	if danaListResp.Code != http.StatusOK {
		t.Fatalf("expected dana /v1/me/agents 200 after personal delete, got %d %s", danaListResp.Code, danaListResp.Body.String())
	}
	danaListPayload := decodeJSONMap(t, danaListResp.Body.Bytes())
	danaAgents, _ := danaListPayload["agents"].([]any)
	for _, raw := range danaAgents {
		agent, _ := raw.(map[string]any)
		if agent["agent_uuid"] == danaPersonalAgentUUID {
			t.Fatalf("expected deleted personal agent to be absent from dana list, got %v", agent)
		}
	}

	orgListResp := doJSONRequest(t, router, http.MethodGet, "/v1/orgs/"+orgID+"/agents", nil, humanHeaders("alice", "alice@a.test"))
	if orgListResp.Code != http.StatusOK {
		t.Fatalf("expected /v1/orgs/{org_id}/agents 200 after deletes, got %d %s", orgListResp.Code, orgListResp.Body.String())
	}
	orgListPayload := decodeJSONMap(t, orgListResp.Body.Bytes())
	orgAgents, _ := orgListPayload["agents"].([]any)
	for _, raw := range orgAgents {
		agent, _ := raw.(map[string]any)
		if agent["agent_uuid"] == bobOwnedAgentUUID || agent["agent_uuid"] == bobOrgManagedAgentUUID || agent["agent_uuid"] == orgOwnedAgentUUID {
			t.Fatalf("expected deleted agent to be absent from org list, got %v", agent)
		}
	}
}

func TestDeleteAgentRecordInvalidatesDeletedAgentAccessAndPurgesQueuedMessages(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, agentUUIDA, agentUUIDB := setupTrustedAgents(t, router)

	queuedBeforeDelete := publish(t, router, tokenA, agentUUIDB, "queued-before-delete")
	if queuedBeforeDelete.Code != http.StatusAccepted {
		t.Fatalf("expected publish before delete to be accepted, got %d %s", queuedBeforeDelete.Code, queuedBeforeDelete.Body.String())
	}

	deleteResp := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+agentUUIDA+"/record", nil, humanHeaders("alice", "alice@a.test"))
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("expected delete record to succeed, got %d %s", deleteResp.Code, deleteResp.Body.String())
	}

	deletedAgentAuth := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if deletedAgentAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected deleted agent token auth to fail with 401, got %d %s", deletedAgentAuth.Code, deletedAgentAuth.Body.String())
	}

	publishAfterDelete := publish(t, router, tokenA, agentUUIDB, "publish-after-delete")
	if publishAfterDelete.Code != http.StatusUnauthorized {
		t.Fatalf("expected deleted token publish to return 401, got %d %s", publishAfterDelete.Code, publishAfterDelete.Body.String())
	}

	pullAfterDelete := pull(t, router, tokenA, 0)
	if pullAfterDelete.Code != http.StatusUnauthorized {
		t.Fatalf("expected deleted token pull to return 401, got %d %s", pullAfterDelete.Code, pullAfterDelete.Body.String())
	}

	publishToDeleted := publish(t, router, tokenB, agentUUIDA, "to-deleted")
	if publishToDeleted.Code != http.StatusNotFound {
		t.Fatalf("expected publish to deleted receiver to return 404, got %d %s", publishToDeleted.Code, publishToDeleted.Body.String())
	}

	receiverPull := pull(t, router, tokenB, 0)
	if receiverPull.Code != http.StatusNoContent {
		t.Fatalf("expected deleted sender messages to be purged from receiver queue, got %d %s", receiverPull.Code, receiverPull.Body.String())
	}
}

func TestLongPollTimeout(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org A")
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	token := registerAgent(t, router, "alice", "alice@a.test", orgID, "agent-a", aliceHumanID)
	start := time.Now()
	resp := pull(t, router, token, 25)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.Code)
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatalf("expected pull wait")
	}
}

func TestConcurrentPublishPull(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)
	const total = 30

	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := publish(t, router, tokenA, agentUUIDB, fmt.Sprintf("msg-%d", i))
			if resp.Code != http.StatusAccepted {
				t.Errorf("publish status=%d body=%s", resp.Code, resp.Body.String())
			}
		}(i)
	}
	wg.Wait()

	seen := 0
	deadline := time.Now().Add(4 * time.Second)
	for seen < total && time.Now().Before(deadline) {
		resp := pull(t, router, tokenB, 50)
		if resp.Code == http.StatusNoContent {
			continue
		}
		if resp.Code != http.StatusOK {
			t.Fatalf("pull failed: %d %s", resp.Code, resp.Body.String())
		}
		seen++
	}
	if seen != total {
		t.Fatalf("expected %d messages, got %d", total, seen)
	}
}

func TestPublishClientMsgIDIsIdempotent(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	first := publishWithClientMsgID(t, router, tokenA, agentUUIDB, "hello-once", "client-1")
	if first.Code != http.StatusAccepted {
		t.Fatalf("expected first publish 202, got %d %s", first.Code, first.Body.String())
	}
	firstPayload := decodeJSONMap(t, first.Body.Bytes())
	if firstPayload["idempotent_replay"] != false {
		t.Fatalf("expected first publish to not be replay")
	}

	second := publishWithClientMsgID(t, router, tokenA, agentUUIDB, "hello-once", "client-1")
	if second.Code != http.StatusAccepted {
		t.Fatalf("expected second publish 202, got %d %s", second.Code, second.Body.String())
	}
	secondPayload := decodeJSONMap(t, second.Body.Bytes())
	if secondPayload["idempotent_replay"] != true {
		t.Fatalf("expected second publish to be an idempotent replay")
	}
	if secondPayload["message_id"] != firstPayload["message_id"] {
		t.Fatalf("expected replay to reuse message_id, got first=%v second=%v", firstPayload["message_id"], secondPayload["message_id"])
	}

	pullResp := pull(t, router, tokenB, 0)
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	messageObj, _ := pullPayload["message"].(map[string]any)
	if got := messageObj["message_id"]; got != firstPayload["message_id"] {
		t.Fatalf("expected first queued message only once, got %v want %v", got, firstPayload["message_id"])
	}

	secondPull := pull(t, router, tokenB, 0)
	if secondPull.Code != http.StatusNoContent {
		t.Fatalf("expected duplicate publish to avoid a second queue entry, got %d %s", secondPull.Code, secondPull.Body.String())
	}
}

func TestPullAckAndStatusLifecycle(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	pubResp := publishWithClientMsgID(t, router, tokenA, agentUUIDB, "hello-ack", "client-ack")
	if pubResp.Code != http.StatusAccepted {
		t.Fatalf("expected publish 202, got %d %s", pubResp.Code, pubResp.Body.String())
	}
	pubPayload := decodeJSONMap(t, pubResp.Body.Bytes())
	messageID, _ := pubPayload["message_id"].(string)

	pullResp := pull(t, router, tokenB, 0)
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	deliveryObj, _ := pullPayload["delivery"].(map[string]any)
	if deliveryObj["delivery_id"] == nil {
		t.Fatalf("expected delivery_id in pull response, got %v", pullPayload)
	}
	if deliveryObj["attempt"] != float64(1) {
		t.Fatalf("expected first delivery attempt to be 1, got %v", deliveryObj["attempt"])
	}

	statusResp := messageStatus(t, router, tokenA, messageID)
	if statusResp.Code != http.StatusOK {
		t.Fatalf("expected sender message status 200, got %d %s", statusResp.Code, statusResp.Body.String())
	}
	statusPayload := decodeJSONMap(t, statusResp.Body.Bytes())
	if statusPayload["status"] != model.MessageDeliveryLeased {
		t.Fatalf("expected leased status after pull, got %v", statusPayload["status"])
	}

	ackResp := ackDelivery(t, router, tokenB, deliveryObj["delivery_id"].(string))
	if ackResp.Code != http.StatusOK {
		t.Fatalf("expected ack 200, got %d %s", ackResp.Code, ackResp.Body.String())
	}
	ackPayload := decodeJSONMap(t, ackResp.Body.Bytes())
	if ackPayload["status"] != model.MessageDeliveryAcked {
		t.Fatalf("expected acked status, got %v", ackPayload["status"])
	}
	if ackPayload["acked_at"] == nil {
		t.Fatalf("expected acked_at in ack payload")
	}

	finalStatus := messageStatus(t, router, tokenA, messageID)
	if finalStatus.Code != http.StatusOK {
		t.Fatalf("expected final sender status 200, got %d %s", finalStatus.Code, finalStatus.Body.String())
	}
	finalPayload := decodeJSONMap(t, finalStatus.Body.Bytes())
	if finalPayload["status"] != model.MessageDeliveryAcked {
		t.Fatalf("expected acked final status, got %v", finalPayload["status"])
	}
}

func TestNackRequeuesMessageWithNewAttempt(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	pubResp := publish(t, router, tokenA, agentUUIDB, "hello-nack")
	if pubResp.Code != http.StatusAccepted {
		t.Fatalf("expected publish 202, got %d %s", pubResp.Code, pubResp.Body.String())
	}
	pubPayload := decodeJSONMap(t, pubResp.Body.Bytes())

	firstPull := pull(t, router, tokenB, 0)
	if firstPull.Code != http.StatusOK {
		t.Fatalf("expected first pull 200, got %d %s", firstPull.Code, firstPull.Body.String())
	}
	firstPayload := decodeJSONMap(t, firstPull.Body.Bytes())
	deliveryObj, _ := firstPayload["delivery"].(map[string]any)

	nackResp := nackDelivery(t, router, tokenB, deliveryObj["delivery_id"].(string))
	if nackResp.Code != http.StatusOK {
		t.Fatalf("expected nack 200, got %d %s", nackResp.Code, nackResp.Body.String())
	}
	nackPayload := decodeJSONMap(t, nackResp.Body.Bytes())
	if nackPayload["status"] != model.MessageDeliveryQueued {
		t.Fatalf("expected queued status after nack, got %v", nackPayload["status"])
	}

	secondPull := pull(t, router, tokenB, 0)
	if secondPull.Code != http.StatusOK {
		t.Fatalf("expected second pull 200, got %d %s", secondPull.Code, secondPull.Body.String())
	}
	secondPayload := decodeJSONMap(t, secondPull.Body.Bytes())
	secondMessageObj, _ := secondPayload["message"].(map[string]any)
	secondDeliveryObj, _ := secondPayload["delivery"].(map[string]any)
	if secondMessageObj["message_id"] != pubPayload["message_id"] {
		t.Fatalf("expected same message_id after nack requeue, got %v want %v", secondMessageObj["message_id"], pubPayload["message_id"])
	}
	if secondDeliveryObj["attempt"] != float64(2) {
		t.Fatalf("expected second attempt after nack, got %v", secondDeliveryObj["attempt"])
	}
}

func TestExpiredLeaseRequeuesOnNextPull(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	router := NewRouter(h)

	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	pubResp := publish(t, router, tokenA, agentUUIDB, "hello-expire")
	if pubResp.Code != http.StatusAccepted {
		t.Fatalf("expected publish 202, got %d %s", pubResp.Code, pubResp.Body.String())
	}
	pubPayload := decodeJSONMap(t, pubResp.Body.Bytes())

	firstPull := pull(t, router, tokenB, 0)
	if firstPull.Code != http.StatusOK {
		t.Fatalf("expected first pull 200, got %d %s", firstPull.Code, firstPull.Body.String())
	}

	now = now.Add(defaultMessageLease + time.Second)

	secondPull := pull(t, router, tokenB, 0)
	if secondPull.Code != http.StatusOK {
		t.Fatalf("expected expired lease to requeue on next pull, got %d %s", secondPull.Code, secondPull.Body.String())
	}
	secondPayload := decodeJSONMap(t, secondPull.Body.Bytes())
	secondMessageObj, _ := secondPayload["message"].(map[string]any)
	secondDeliveryObj, _ := secondPayload["delivery"].(map[string]any)
	if secondMessageObj["message_id"] != pubPayload["message_id"] {
		t.Fatalf("expected same message after expiry requeue, got %v want %v", secondMessageObj["message_id"], pubPayload["message_id"])
	}
	if secondDeliveryObj["attempt"] != float64(2) {
		t.Fatalf("expected attempt 2 after lease expiry, got %v", secondDeliveryObj["attempt"])
	}
}

func TestBindTokenRedeemSingleUse(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org A")
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")

	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind-tokens", map[string]any{
		"org_id":         orgID,
		"owner_human_id": aliceHumanID,
	}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp.Body.Bytes())
	bindToken, _ := createPayload["bind_token"].(string)
	if bindToken == "" {
		t.Fatalf("bind_token missing in create response")
	}
	if !strings.HasPrefix(bindToken, "b_") {
		t.Fatalf("expected bind_token to start with b_, got %q", bindToken)
	}

	redeemResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]string{
		"hub_url":    "https://hub.qa.example.com",
		"bind_token": bindToken,
	}, nil)
	if redeemResp.Code != http.StatusCreated {
		t.Fatalf("redeem bind token failed: %d %s", redeemResp.Code, redeemResp.Body.String())
	}
	redeemPayload := decodeJSONMap(t, redeemResp.Body.Bytes())
	token, _ := redeemPayload["token"].(string)
	if token == "" {
		t.Fatalf("expected token in bind response")
	}
	if !strings.HasPrefix(token, "t_") {
		t.Fatalf("expected agent token to start with t_, got %q", token)
	}
	apiBase, _ := redeemPayload["api_base"].(string)
	if apiBase != "http://example.com/v1" {
		t.Fatalf("expected bind response api_base, got %q", apiBase)
	}
	endpoints, _ := redeemPayload["endpoints"].(map[string]any)
	if got, _ := endpoints["capabilities"].(string); got != "http://example.com/v1/agents/me/capabilities" {
		t.Fatalf("expected bind response capabilities endpoint, got %q", got)
	}

	capsResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/capabilities", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if capsResp.Code != http.StatusOK {
		t.Fatalf("expected capabilities call with issued token to succeed: %d %s", capsResp.Code, capsResp.Body.String())
	}

	redeemAgain := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]string{
		"hub_url":    "https://hub.qa.example.com",
		"bind_token": bindToken,
	}, nil)
	if redeemAgain.Code != http.StatusConflict {
		t.Fatalf("expected second redeem to fail with 409, got %d %s", redeemAgain.Code, redeemAgain.Body.String())
	}
}

func TestSuperAdminReadOnly(t *testing.T) {
	router := newTestRouter()
	_ = createOrg(t, router, "alice", "alice@a.test", "Org A")
	ensureHandleConfirmed(t, router, "root", "root@example.com")

	readonlyCreate := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{
		"handle":       "root-org",
		"display_name": "Root Org",
	}, humanHeaders("root", "root@example.com"))
	if readonlyCreate.Code != http.StatusCreated {
		t.Fatalf("expected super admin write allow 201, got %d %s", readonlyCreate.Code, readonlyCreate.Body.String())
	}

	snap := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@example.com"))
	if snap.Code != http.StatusOK {
		t.Fatalf("expected super admin snapshot 200, got %d %s", snap.Code, snap.Body.String())
	}
}

func TestOrganizationNameUniqueCaseInsensitive(t *testing.T) {
	router := newTestRouter()
	_ = createOrg(t, router, "alice", "alice@a.test", "Acme")
	ensureHandleConfirmed(t, router, "bob", "bob@b.test")

	dup := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{
		"handle":       "  acME  ",
		"display_name": "ACME Duplicate",
	}, humanHeaders("bob", "bob@b.test"))
	if dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate org handle, got %d %s", dup.Code, dup.Body.String())
	}
	body := decodeJSONMap(t, dup.Body.Bytes())
	if body["error"] != "org_handle_exists" {
		t.Fatalf("expected org_handle_exists error, got %v", body["error"])
	}
	ensureHandleConfirmed(t, router, "carol", "carol@c.test")

	dupSpacing := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{
		"handle":       "acme   ",
		"display_name": "Acme Duplicate 2",
	}, humanHeaders("carol", "carol@c.test"))
	if dupSpacing.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate org handle with spacing variance, got %d %s", dupSpacing.Code, dupSpacing.Body.String())
	}

	_ = createOrg(t, router, "dan", "dan@d.test", "Acme Labs")
	ensureHandleConfirmed(t, router, "erin", "erin@e.test")
	dupInternalSpacing := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{
		"handle":       "  acme    labs  ",
		"display_name": "Acme Labs Duplicate",
	}, humanHeaders("erin", "erin@e.test"))
	if dupInternalSpacing.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate org handle with internal spacing variance, got %d %s", dupInternalSpacing.Code, dupInternalSpacing.Body.String())
	}
}

func TestHumanCanSetHandleAndMetadata(t *testing.T) {
	router := newTestRouter()

	_ = currentHumanID(t, router, "alice", "alice@a.test")
	update := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
		"handle": "Alice Main",
	}, humanHeaders("alice", "alice@a.test"))
	if update.Code != http.StatusOK {
		t.Fatalf("expected profile patch 200, got %d %s", update.Code, update.Body.String())
	}
	payload := decodeJSONMap(t, update.Body.Bytes())
	human, _ := payload["human"].(map[string]any)
	if human["handle"] != "alice-main" {
		t.Fatalf("expected normalized handle alice-main, got %v", human["handle"])
	}

	metadataResp := doJSONRequest(t, router, http.MethodPatch, "/v1/me/metadata", map[string]any{
		"metadata": map[string]any{"public": false},
	}, humanHeaders("alice", "alice@a.test"))
	if metadataResp.Code != http.StatusOK {
		t.Fatalf("expected metadata patch 200, got %d %s", metadataResp.Code, metadataResp.Body.String())
	}
	metadataPayload := decodeJSONMap(t, metadataResp.Body.Bytes())
	updatedHuman, _ := metadataPayload["human"].(map[string]any)
	metadata, _ := updatedHuman["metadata"].(map[string]any)
	if metadata["public"] != false {
		t.Fatalf("expected metadata.public false, got %v", metadata["public"])
	}
}

func TestHumanHandleMustBeUnique(t *testing.T) {
	router := newTestRouter()

	_ = currentHumanID(t, router, "alice", "alice@a.test")
	_ = currentHumanID(t, router, "bob", "bob@b.test")
	first := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
		"handle": "shared",
	}, humanHeaders("alice", "alice@a.test"))
	if first.Code != http.StatusOK {
		t.Fatalf("expected first handle update 200, got %d %s", first.Code, first.Body.String())
	}

	dup := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
		"handle": "SHARED",
	}, humanHeaders("bob", "bob@b.test"))
	if dup.Code != http.StatusConflict {
		t.Fatalf("expected duplicate handle conflict 409, got %d %s", dup.Code, dup.Body.String())
	}
	body := decodeJSONMap(t, dup.Body.Bytes())
	if body["error"] != "human_handle_exists" {
		t.Fatalf("expected human_handle_exists, got %v", body["error"])
	}
}

func TestHumanBoundAgentNameUniqueAcrossHumans(t *testing.T) {
	router := newTestRouter()

	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	_ = currentHumanID(t, router, "bob", "bob@b.test")

	orgA := createOrg(t, router, "alice", "alice@a.test", "Org Human Agent A")
	orgB := createOrg(t, router, "bob", "bob@b.test", "Org Human Agent B")

	registerAgent(t, router, "alice", "alice@a.test", orgA, "alpha-agent", aliceHumanID)
	tokenBob, _ := registerAgentWithUUID(t, router, "bob", "bob@b.test", orgB, "ALPHA-AGENT", currentHumanID(t, router, "bob", "bob@b.test"))
	bobMe := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
		"Authorization": "Bearer " + tokenBob,
	})
	if bobMe.Code != http.StatusOK {
		t.Fatalf("expected bob agent /v1/agents/me to succeed, got %d %s", bobMe.Code, bobMe.Body.String())
	}
	bobPayload := decodeJSONMap(t, bobMe.Body.Bytes())
	agent, _ := bobPayload["agent"].(map[string]any)
	if agent["handle"] != "alpha-agent" {
		t.Fatalf("expected normalized handle alpha-agent, got %v", agent["handle"])
	}
}

func TestOrgBoundAgentNameUniqueWithinOrg(t *testing.T) {
	router := newTestRouter()

	orgA := createOrg(t, router, "alice", "alice@a.test", "Org Agents Unique")
	bindCreateA := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind-tokens", map[string]any{
		"org_id": orgA,
	}, humanHeaders("alice", "alice@a.test"))
	if bindCreateA.Code != http.StatusCreated {
		t.Fatalf("expected bind token creation for duplicate org-bound test, got %d %s", bindCreateA.Code, bindCreateA.Body.String())
	}
	bindTokenA, _ := decodeJSONMap(t, bindCreateA.Body.Bytes())["bind_token"].(string)
	if strings.TrimSpace(bindTokenA) == "" {
		t.Fatalf("expected first bind token")
	}
	if !strings.HasPrefix(bindTokenA, "b_") {
		t.Fatalf("expected first bind token to start with b_, got %q", bindTokenA)
	}
	redeemA := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]any{
		"bind_token": bindTokenA,
	}, nil)
	if redeemA.Code != http.StatusCreated {
		t.Fatalf("expected first bind redeem success, got %d %s", redeemA.Code, redeemA.Body.String())
	}
	tokenA, _ := decodeJSONMap(t, redeemA.Body.Bytes())["token"].(string)
	if strings.TrimSpace(tokenA) == "" {
		t.Fatalf("expected first agent token from bind redeem")
	}
	if !strings.HasPrefix(tokenA, "t_") {
		t.Fatalf("expected first agent token to start with t_, got %q", tokenA)
	}
	finalizeA := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me", map[string]any{
		"handle": "org-agent",
	}, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if finalizeA.Code != http.StatusOK {
		t.Fatalf("expected first org-bound finalize success, got %d %s", finalizeA.Code, finalizeA.Body.String())
	}

	bindCreateB := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind-tokens", map[string]any{
		"org_id": orgA,
	}, humanHeaders("alice", "alice@a.test"))
	if bindCreateB.Code != http.StatusCreated {
		t.Fatalf("expected second bind token creation success, got %d %s", bindCreateB.Code, bindCreateB.Body.String())
	}
	bindTokenB, _ := decodeJSONMap(t, bindCreateB.Body.Bytes())["bind_token"].(string)
	if strings.TrimSpace(bindTokenB) == "" {
		t.Fatalf("expected second bind token")
	}
	if !strings.HasPrefix(bindTokenB, "b_") {
		t.Fatalf("expected second bind token to start with b_, got %q", bindTokenB)
	}
	redeemB := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]any{
		"bind_token": bindTokenB,
	}, nil)
	if redeemB.Code != http.StatusCreated {
		t.Fatalf("expected second bind redeem success, got %d %s", redeemB.Code, redeemB.Body.String())
	}
	tokenB, _ := decodeJSONMap(t, redeemB.Body.Bytes())["token"].(string)
	if strings.TrimSpace(tokenB) == "" {
		t.Fatalf("expected second agent token from bind redeem")
	}
	if !strings.HasPrefix(tokenB, "t_") {
		t.Fatalf("expected second agent token to start with t_, got %q", tokenB)
	}
	dup := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me", map[string]any{
		"handle": "ORG-AGENT",
	}, map[string]string{
		"Authorization": "Bearer " + tokenB,
	})
	if dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate org-bound finalize handle in same org, got %d %s", dup.Code, dup.Body.String())
	}
	body := decodeJSONMap(t, dup.Body.Bytes())
	if body["error"] != "agent_exists" {
		t.Fatalf("expected agent_exists for duplicate org-bound name, got %v", body["error"])
	}
}

func TestBindTokenExpires(t *testing.T) {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	now := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	router := NewRouter(h)

	orgID := createOrg(t, router, "alice", "alice@a.test", "Org A")
	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind-tokens", map[string]any{
		"org_id": orgID,
	}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp.Body.Bytes())
	bindToken, _ := createPayload["bind_token"].(string)
	if bindToken == "" {
		t.Fatalf("bind token missing")
	}

	now = now.Add(16 * time.Minute)
	redeemResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]string{
		"hub_url":    "https://hub.qa.example.com",
		"bind_token": bindToken,
	}, nil)
	if redeemResp.Code != http.StatusBadRequest {
		t.Fatalf("expected expired bind token 400, got %d %s", redeemResp.Code, redeemResp.Body.String())
	}
	redeemPayload := decodeJSONMap(t, redeemResp.Body.Bytes())
	if redeemPayload["error"] != "bind_expired" {
		t.Fatalf("expected bind_expired, got %v", redeemPayload["error"])
	}
}

func TestAgentCapabilitiesAndSkillEndpoints(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, agentUUIDA, agentUUIDB := setupTrustedAgents(t, router)

	skillPatchA := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"skills": []map[string]any{
				{"name": "weather_lookup", "description": "Get current weather for a location."},
			},
		},
	}, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if skillPatchA.Code != http.StatusOK {
		t.Fatalf("expected agent A skill metadata patch 200, got %d %s", skillPatchA.Code, skillPatchA.Body.String())
	}
	skillPatchB := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"display_name": "Math Bot",
			"emoji":        "🧮",
			"skills": []map[string]any{
				{"name": "math.add", "description": "Add two numbers."},
			},
		},
	}, map[string]string{
		"Authorization": "Bearer " + tokenB,
	})
	if skillPatchB.Code != http.StatusOK {
		t.Fatalf("expected agent B skill metadata patch 200, got %d %s", skillPatchB.Code, skillPatchB.Body.String())
	}

	offlinePeer := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/offline", map[string]any{
		"session_key": "peer-main",
		"reason":      "peer_capability_test",
	}, map[string]string{
		"Authorization": "Bearer " + tokenB,
	})
	if offlinePeer.Code != http.StatusOK {
		t.Fatalf("expected agent B offline marker 200, got %d %s", offlinePeer.Code, offlinePeer.Body.String())
	}

	manifestResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/manifest", nil, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if manifestResp.Code != http.StatusOK {
		t.Fatalf("agent manifest failed: %d %s", manifestResp.Code, manifestResp.Body.String())
	}
	manifestPayload := decodeJSONMap(t, manifestResp.Body.Bytes())
	manifestObj, ok := manifestPayload["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("missing manifest object: %v", manifestPayload)
	}
	if manifestObj["schema_version"] != "2" {
		t.Fatalf("expected manifest schema_version=2, got %v", manifestObj["schema_version"])
	}
	manifestCommunication, ok := manifestObj["communication"].(map[string]any)
	if !ok {
		t.Fatalf("expected communication in manifest payload, got %v", manifestObj)
	}
	manifestTalkablePeers, ok := manifestCommunication["talkable_peers"].([]any)
	if !ok || len(manifestTalkablePeers) == 0 {
		t.Fatalf("expected communication.talkable_peers in manifest payload, got %v", manifestCommunication["talkable_peers"])
	}
	foundManifestPeerB := false
	for _, raw := range manifestTalkablePeers {
		peer, _ := raw.(map[string]any)
		if peer == nil {
			continue
		}
		if gotUUID, _ := peer["agent_uuid"].(string); gotUUID != agentUUIDB {
			continue
		}
		foundManifestPeerB = true
		presence, _ := peer["presence"].(map[string]any)
		if got, _ := presence["status"].(string); got != "offline" {
			t.Fatalf("expected manifest talkable peer B presence.status offline, got %q peer=%v", got, peer)
		}
		if ready, ok := presence["ready"].(bool); !ok || ready {
			t.Fatalf("expected manifest talkable peer B presence.ready=false, got %v peer=%v", presence["ready"], peer)
		}
	}
	if !foundManifestPeerB {
		t.Fatalf("expected manifest talkable_peers to include peer B %q, got %v", agentUUIDB, manifestTalkablePeers)
	}

	capsResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/capabilities", nil, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if capsResp.Code != http.StatusOK {
		t.Fatalf("agent capabilities failed: %d %s", capsResp.Code, capsResp.Body.String())
	}
	capsPayload := decodeJSONMap(t, capsResp.Body.Bytes())
	controlPlane, ok := capsPayload["control_plane"].(map[string]any)
	if !ok {
		t.Fatalf("missing control_plane: %v", capsPayload)
	}
	peers, ok := controlPlane["can_talk_to"].([]any)
	if !ok {
		t.Fatalf("missing can_talk_to array: %v", controlPlane["can_talk_to"])
	}
	if len(peers) == 0 {
		t.Fatalf("expected at least one talkable peer")
	}
	talkablePeers, ok := controlPlane["talkable_peers"].([]any)
	if !ok || len(talkablePeers) == 0 {
		t.Fatalf("expected talkable_peers in control_plane payload, got %v", controlPlane["talkable_peers"])
	}
	foundPeerB := false
	for _, raw := range talkablePeers {
		peer, _ := raw.(map[string]any)
		if peer == nil {
			continue
		}
		if gotUUID, _ := peer["agent_uuid"].(string); gotUUID != agentUUIDB {
			continue
		}
		foundPeerB = true
		if gotName, _ := peer["display_name"].(string); gotName != "Math Bot" {
			t.Fatalf("expected talkable peer B display_name Math Bot, got %q peer=%v", gotName, peer)
		}
		if gotEmoji, _ := peer["emoji"].(string); gotEmoji != "🧮" {
			t.Fatalf("expected talkable peer B emoji 🧮, got %q peer=%v", gotEmoji, peer)
		}
		presence, _ := peer["presence"].(map[string]any)
		if got, _ := presence["status"].(string); got != "offline" {
			t.Fatalf("expected talkable peer B presence.status offline, got %q peer=%v", got, peer)
		}
		if ready, ok := presence["ready"].(bool); !ok || ready {
			t.Fatalf("expected talkable peer B presence.ready=false, got %v peer=%v", presence["ready"], peer)
		}
		if gotSession, _ := presence["session_key"].(string); gotSession != "peer-main" {
			t.Fatalf("expected talkable peer B presence.session_key peer-main, got %q peer=%v", gotSession, peer)
		}
	}
	if !foundPeerB {
		t.Fatalf("expected talkable_peers to include peer B %q, got %v", agentUUIDB, talkablePeers)
	}
	communication, ok := capsPayload["communication"].(map[string]any)
	if !ok {
		t.Fatalf("expected communication in capabilities payload, got %v", capsPayload)
	}
	communicationTalkablePeers, ok := communication["talkable_peers"].([]any)
	if !ok || len(communicationTalkablePeers) == 0 {
		t.Fatalf("expected communication.talkable_peers in capabilities payload, got %v", communication["talkable_peers"])
	}

	capsRespB := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/capabilities", nil, map[string]string{
		"Authorization": "Bearer " + tokenB,
	})
	if capsRespB.Code != http.StatusOK {
		t.Fatalf("agent B capabilities failed: %d %s", capsRespB.Code, capsRespB.Body.String())
	}
	capsPayloadB := decodeJSONMap(t, capsRespB.Body.Bytes())
	controlPlaneB, ok := capsPayloadB["control_plane"].(map[string]any)
	if !ok {
		t.Fatalf("missing control_plane for agent B: %v", capsPayloadB)
	}
	talkablePeersB, ok := controlPlaneB["talkable_peers"].([]any)
	if !ok || len(talkablePeersB) == 0 {
		t.Fatalf("expected talkable_peers for agent B, got %v", controlPlaneB["talkable_peers"])
	}
	foundPeerA := false
	for _, raw := range talkablePeersB {
		peer, _ := raw.(map[string]any)
		if peer == nil {
			continue
		}
		if gotUUID, _ := peer["agent_uuid"].(string); gotUUID != agentUUIDA {
			continue
		}
		foundPeerA = true
		if gotName, _ := peer["display_name"].(string); gotName != "agent-a" {
			t.Fatalf("expected talkable peer A fallback display_name agent-a, got %q peer=%v", gotName, peer)
		}
	}
	if !foundPeerA {
		t.Fatalf("expected talkable_peers to include peer A %q, got %v", agentUUIDA, talkablePeersB)
	}
	if _, ok := capsPayload["manifest_url"].(string); !ok {
		t.Fatalf("expected manifest_url in capabilities payload, got %v", capsPayload)
	}
	if _, ok := capsPayload["capabilities"].([]any); !ok {
		t.Fatalf("expected capabilities array in capabilities payload, got %v", capsPayload)
	}
	if _, ok := capsPayload["routes"].([]any); !ok {
		t.Fatalf("expected routes array in capabilities payload, got %v", capsPayload)
	}
	if _, ok := capsPayload["advertised_skills"].([]any); !ok {
		t.Fatalf("expected advertised_skills in capabilities payload, got %v", capsPayload)
	}
	peerSkillCatalog, ok := capsPayload["peer_skill_catalog"].([]any)
	if !ok || len(peerSkillCatalog) == 0 {
		t.Fatalf("expected peer_skill_catalog in capabilities payload, got %v", capsPayload)
	}
	if _, ok := capsPayload["skill_call_contract"].(map[string]any); !ok {
		t.Fatalf("expected skill_call_contract in capabilities payload, got %v", capsPayload)
	}
	if _, ok := capsPayload["protocol_adapters"].(map[string]any); !ok {
		t.Fatalf("expected protocol_adapters in capabilities payload, got %v", capsPayload)
	}
	controlPlaneAdapters, ok := controlPlane["protocol_adapters"].(map[string]any)
	if !ok {
		t.Fatalf("expected control_plane.protocol_adapters, got %v", controlPlane)
	}
	openClawAdapter, ok := controlPlaneAdapters["openclaw_http_v1"].(map[string]any)
	if !ok {
		t.Fatalf("expected openclaw_http_v1 adapter in control_plane.protocol_adapters, got %v", controlPlaneAdapters)
	}
	if got, _ := openClawAdapter["protocol"].(string); got != "openclaw.http.v1" {
		t.Fatalf("expected openclaw adapter protocol, got %q", got)
	}

	skillJSONResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/skill", nil, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if skillJSONResp.Code != http.StatusOK {
		t.Fatalf("agent skill json failed: %d %s", skillJSONResp.Code, skillJSONResp.Body.String())
	}
	skillJSON := decodeJSONMap(t, skillJSONResp.Body.Bytes())
	skillObj, ok := skillJSON["skill"].(map[string]any)
	if !ok {
		t.Fatalf("missing skill object: %v", skillJSON)
	}
	skillContent, _ := skillObj["content"].(string)
	if !strings.Contains(skillContent, "SKILL: MoltenHub Agent Control Plane") {
		t.Fatalf("expected skill header, got %q", skillContent)
	}
	if !strings.Contains(skillContent, "Onboarding Checklist") {
		t.Fatalf("expected onboarding checklist in skill, got %q", skillContent)
	}
	if !strings.Contains(skillContent, "Operating Rules") {
		t.Fatalf("expected operating rules in skill, got %q", skillContent)
	}
	if !strings.Contains(skillContent, "Advertised Skills") || !strings.Contains(skillContent, "weather_lookup") {
		t.Fatalf("expected advertised skills in skill, got %q", skillContent)
	}
	if !strings.Contains(skillContent, "Talkable Peer Skills") || !strings.Contains(skillContent, "math.add") {
		t.Fatalf("expected peer skills in skill, got %q", skillContent)
	}
	if !strings.Contains(skillContent, "Skill Call Contract") || !strings.Contains(skillContent, "skill_request") {
		t.Fatalf("expected skill call contract in skill, got %q", skillContent)
	}
	if !strings.Contains(skillContent, "\"display_name\":\"<human-friendly-name>\"") || !strings.Contains(skillContent, "\"emoji\":\"<single-emoji>\"") || !strings.Contains(skillContent, "\"agent_type\":\"<assistant-type>\"") || !strings.Contains(skillContent, "\"llm\":\"<provider>/<model>@<version>\"") || !strings.Contains(skillContent, "\"harness\":\"<runtime-or-framework>@<version>\"") {
		t.Fatalf("expected onboarding skill to include profile metadata setup guidance, got %q", skillContent)
	}
	if !strings.Contains(skillContent, "metadata.display_name") || !strings.Contains(skillContent, "metadata.emoji") {
		t.Fatalf("expected onboarding skill to mention display_name and emoji guidance, got %q", skillContent)
	}
	if !strings.Contains(skillContent, "status=online") || !strings.Contains(skillContent, "status=offline") {
		t.Fatalf("expected onboarding skill to describe online/offline presence semantics, got %q", skillContent)
	}
	if strings.Contains(skillContent, "## OpenClaw Node + Agent HTTP Path") {
		t.Fatalf("did not expect OpenClaw-only section for non-OpenClaw profile, got %q", skillContent)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/agents/me/skill?format=markdown", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	req.Header.Set("Accept", "text/markdown")
	mdResp := httptest.NewRecorder()
	router.ServeHTTP(mdResp, req)
	if mdResp.Code != http.StatusOK {
		t.Fatalf("agent skill markdown failed: %d %s", mdResp.Code, mdResp.Body.String())
	}
	if !strings.HasPrefix(mdResp.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("expected markdown content type, got %q", mdResp.Header().Get("Content-Type"))
	}
	if !strings.Contains(mdResp.Body.String(), "You can currently talk to") {
		t.Fatalf("expected communication section in markdown skill, got %q", mdResp.Body.String())
	}
	if !strings.Contains(mdResp.Body.String(), "PATCH http://example.com/v1/agents/me") {
		t.Fatalf("expected handle finalize guidance in markdown skill, got %q", mdResp.Body.String())
	}
	if !strings.Contains(mdResp.Body.String(), "http://example.com/v1/messages/publish") {
		t.Fatalf("expected publish guidance in markdown skill, got %q", mdResp.Body.String())
	}
	if !strings.Contains(mdResp.Body.String(), "http://example.com/v1/messages/pull?timeout_ms=5000") {
		t.Fatalf("expected pull guidance in markdown skill, got %q", mdResp.Body.String())
	}

	manifestMDReq := httptest.NewRequest(http.MethodGet, "/v1/agents/me/manifest?format=markdown", nil)
	manifestMDReq.Header.Set("Authorization", "Bearer "+tokenA)
	manifestMDReq.Header.Set("Accept", "text/markdown")
	manifestMDResp := httptest.NewRecorder()
	router.ServeHTTP(manifestMDResp, manifestMDReq)
	if manifestMDResp.Code != http.StatusOK {
		t.Fatalf("agent manifest markdown failed: %d %s", manifestMDResp.Code, manifestMDResp.Body.String())
	}
	if !strings.HasPrefix(manifestMDResp.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("expected markdown content type for manifest, got %q", manifestMDResp.Header().Get("Content-Type"))
	}
	if !strings.Contains(manifestMDResp.Body.String(), "MoltenHub Agent Manifest") {
		t.Fatalf("expected manifest markdown heading, got %q", manifestMDResp.Body.String())
	}
	if !strings.Contains(manifestMDResp.Body.String(), "Retry Guidance") {
		t.Fatalf("expected retry guidance section in manifest markdown, got %q", manifestMDResp.Body.String())
	}
	if !strings.Contains(manifestMDResp.Body.String(), "GET /v1/agents/me/manifest") {
		t.Fatalf("expected manifest route contract in markdown, got %q", manifestMDResp.Body.String())
	}

	notAcceptableReq := httptest.NewRequest(http.MethodGet, "/v1/agents/me/manifest", nil)
	notAcceptableReq.Header.Set("Authorization", "Bearer "+tokenA)
	notAcceptableReq.Header.Set("Accept", "application/xml")
	notAcceptableResp := httptest.NewRecorder()
	router.ServeHTTP(notAcceptableResp, notAcceptableReq)
	if notAcceptableResp.Code != http.StatusNotAcceptable {
		t.Fatalf("expected 406 for unsupported manifest accept header, got %d %s", notAcceptableResp.Code, notAcceptableResp.Body.String())
	}
	notAcceptablePayload := decodeJSONMap(t, notAcceptableResp.Body.Bytes())
	if notAcceptablePayload["error"] != "not_acceptable" {
		t.Fatalf("expected not_acceptable error code, got %v", notAcceptablePayload["error"])
	}
}

func TestAgentCapabilitiesTalkablePeersIncludesRemoteURIOnlyEntry(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, agentUUIDA, _ := setupTrustedAgents(t, router)

	const peerID = "peer-remote"
	createPeer(t, router, peerID, "https://remote.example", "https://remote.example", "peer-secret")

	const remoteURI = "https://remote.example/human/remote/agent/assistant"
	createRemoteAgentTrustAdmin(t, router, agentUUIDA, peerID, remoteURI)

	capsResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/capabilities", nil, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if capsResp.Code != http.StatusOK {
		t.Fatalf("agent capabilities failed: %d %s", capsResp.Code, capsResp.Body.String())
	}
	capsPayload := decodeJSONMap(t, capsResp.Body.Bytes())
	controlPlane, ok := capsPayload["control_plane"].(map[string]any)
	if !ok {
		t.Fatalf("missing control_plane: %v", capsPayload)
	}
	talkablePeers, ok := controlPlane["talkable_peers"].([]any)
	if !ok || len(talkablePeers) == 0 {
		t.Fatalf("expected talkable_peers in control_plane payload, got %v", controlPlane["talkable_peers"])
	}

	foundRemote := false
	for _, raw := range talkablePeers {
		peer, _ := raw.(map[string]any)
		if peer == nil {
			continue
		}
		if gotURI, _ := peer["agent_uri"].(string); gotURI != remoteURI {
			continue
		}
		foundRemote = true
		if gotName, _ := peer["display_name"].(string); gotName != remoteURI {
			t.Fatalf("expected remote talkable peer display_name fallback %q, got %q peer=%v", remoteURI, gotName, peer)
		}
		if _, exists := peer["agent_uuid"]; exists {
			t.Fatalf("expected remote URI-only peer to omit agent_uuid, got peer=%v", peer)
		}
		if _, exists := peer["agent_id"]; exists {
			t.Fatalf("expected remote URI-only peer to omit agent_id, got peer=%v", peer)
		}
		if _, exists := peer["presence"]; exists {
			t.Fatalf("expected remote URI-only peer to omit presence, got peer=%v", peer)
		}
	}
	if !foundRemote {
		t.Fatalf("expected talkable_peers to include remote URI %q, got %v", remoteURI, talkablePeers)
	}
}

func TestAgentSkillOpenClawProfileIncludesAdapterSection(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)
	headers := map[string]string{"Authorization": "Bearer " + tokenA}

	patchResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"agent_type": "openclaw",
		},
	}, headers)
	if patchResp.Code != http.StatusOK {
		t.Fatalf("expected metadata patch 200, got %d %s", patchResp.Code, patchResp.Body.String())
	}

	skillResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/skill", nil, headers)
	if skillResp.Code != http.StatusOK {
		t.Fatalf("agent skill json failed: %d %s", skillResp.Code, skillResp.Body.String())
	}
	skillPayload := decodeJSONMap(t, skillResp.Body.Bytes())
	skillObj, _ := skillPayload["skill"].(map[string]any)
	skillContent, _ := skillObj["content"].(string)
	if !strings.Contains(skillContent, "## OpenClaw Node + Agent HTTP Path") {
		t.Fatalf("expected OpenClaw skill section, got %q", skillContent)
	}
	if !strings.Contains(skillContent, "POST http://example.com/v1/openclaw/messages/publish") {
		t.Fatalf("expected OpenClaw publish endpoint in skill content, got %q", skillContent)
	}

	capsResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/capabilities", nil, headers)
	if capsResp.Code != http.StatusOK {
		t.Fatalf("agent capabilities failed: %d %s", capsResp.Code, capsResp.Body.String())
	}
	capsPayload := decodeJSONMap(t, capsResp.Body.Bytes())
	adapters, _ := capsPayload["protocol_adapters"].(map[string]any)
	openClawAdapter, _ := adapters["openclaw_http_v1"].(map[string]any)
	if got, _ := openClawAdapter["protocol"].(string); got != "openclaw.http.v1" {
		t.Fatalf("expected openclaw adapter protocol, got %q", got)
	}
}

func TestAgentMeMetadataUpdateEndpoint(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, agentUUIDA, agentUUIDB := setupTrustedAgents(t, router)
	headers := map[string]string{"Authorization": "Bearer " + tokenA}

	patchResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{"public": false},
	}, headers)
	if patchResp.Code != http.StatusOK {
		t.Fatalf("expected PATCH /v1/agents/me/metadata 200, got %d %s", patchResp.Code, patchResp.Body.String())
	}
	patchPayload := decodeJSONMap(t, patchResp.Body.Bytes())
	patchAgent, _ := patchPayload["agent"].(map[string]any)
	if patchAgent["agent_uuid"] != agentUUIDA {
		t.Fatalf("expected PATCH /v1/agents/me/metadata to update authenticated agent %q, got %v", agentUUIDA, patchAgent["agent_uuid"])
	}
	metadata, _ := patchAgent["metadata"].(map[string]any)
	if metadata["public"] != false {
		t.Fatalf("expected PATCH /v1/agents/me/metadata to set metadata.public=false, got %v payload=%v", metadata["public"], patchPayload)
	}
	if metadata["agent_type"] != "unknown" {
		t.Fatalf("expected PATCH /v1/agents/me/metadata to default metadata.agent_type=unknown, got %v payload=%v", metadata["agent_type"], patchPayload)
	}

	humanRouteResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/"+agentUUIDB+"/metadata", map[string]any{
		"metadata": map[string]any{"public": false},
	}, headers)
	if humanRouteResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected agent token on human control-plane route to return 401, got %d %s", humanRouteResp.Code, humanRouteResp.Body.String())
	}
}

func TestAgentMeMetadataPatchMergesAndNullDeletes(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)
	headers := map[string]string{"Authorization": "Bearer " + tokenA}

	firstPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"public":           false,
			"profile_markdown": "# About\nFirst version",
			"llm":              "openai/gpt-5.4@2026-03-01",
		},
	}, headers)
	if firstPatch.Code != http.StatusOK {
		t.Fatalf("expected first metadata patch 200, got %d %s", firstPatch.Code, firstPatch.Body.String())
	}

	secondPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"profile_markdown": "# About\nSecond version",
		},
	}, headers)
	if secondPatch.Code != http.StatusOK {
		t.Fatalf("expected second metadata patch 200, got %d %s", secondPatch.Code, secondPatch.Body.String())
	}
	secondPayload := decodeJSONMap(t, secondPatch.Body.Bytes())
	secondResult := requireAgentRuntimeSuccessEnvelope(t, secondPayload)
	secondAgent, _ := secondResult["agent"].(map[string]any)
	secondMetadata, _ := secondAgent["metadata"].(map[string]any)
	if got, _ := secondMetadata["profile_markdown"].(string); got != "# About\nSecond version" {
		t.Fatalf("expected profile_markdown merge update, got %q metadata=%v", got, secondMetadata)
	}
	if got, _ := secondMetadata["llm"].(string); got != "openai/gpt-5.4@2026-03-01" {
		t.Fatalf("expected llm to be preserved by merge patch, got %q metadata=%v", got, secondMetadata)
	}
	if got, ok := secondMetadata["public"].(bool); !ok || got {
		t.Fatalf("expected public=false preserved by merge patch, got %v metadata=%v", secondMetadata["public"], secondMetadata)
	}

	thirdPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"llm": nil,
		},
	}, headers)
	if thirdPatch.Code != http.StatusOK {
		t.Fatalf("expected null-delete metadata patch 200, got %d %s", thirdPatch.Code, thirdPatch.Body.String())
	}
	thirdPayload := decodeJSONMap(t, thirdPatch.Body.Bytes())
	thirdResult := requireAgentRuntimeSuccessEnvelope(t, thirdPayload)
	thirdAgent, _ := thirdResult["agent"].(map[string]any)
	thirdMetadata, _ := thirdAgent["metadata"].(map[string]any)
	if _, exists := thirdMetadata["llm"]; exists {
		t.Fatalf("expected metadata.llm removed by null delete, got %v", thirdMetadata["llm"])
	}
	if got, _ := thirdMetadata["profile_markdown"].(string); got != "# About\nSecond version" {
		t.Fatalf("expected profile_markdown to remain after llm delete, got %q metadata=%v", got, thirdMetadata)
	}
}

func TestHumanManagedAgentProfilePatchUpdatesMetadataAndHandle(t *testing.T) {
	router := newTestRouter()
	_, _, agentUUID := registerMyAgent(t, router, "alice", "alice@a.test", "", "alice-agent-editable")

	resp := doJSONRequest(t, router, http.MethodPatch, "/v1/me/agents/"+agentUUID, map[string]any{
		"handle": "alice-agent-updated",
		"metadata": map[string]any{
			"display_name":     "Alice Agent",
			"emoji":            "😄",
			"profile_markdown": "Builds release pipelines.",
		},
	}, humanHeaders("alice", "alice@a.test"))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected owner profile patch 200, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	agent, _ := payload["agent"].(map[string]any)
	if got, _ := agent["agent_uuid"].(string); got != agentUUID {
		t.Fatalf("expected agent_uuid=%q, got %q payload=%v", agentUUID, got, payload)
	}
	if got, _ := agent["handle"].(string); got != "alice-agent-updated" {
		t.Fatalf("expected updated handle, got %q payload=%v", got, payload)
	}

	metadata, _ := agent["metadata"].(map[string]any)
	if got, _ := metadata["display_name"].(string); got != "Alice Agent" {
		t.Fatalf("expected display_name metadata, got %q payload=%v", got, payload)
	}
	if got, _ := metadata["emoji"].(string); got != "😄" {
		t.Fatalf("expected emoji metadata, got %q payload=%v", got, payload)
	}
	if got, _ := metadata["profile_markdown"].(string); got != "Builds release pipelines." {
		t.Fatalf("expected profile_markdown metadata, got %q payload=%v", got, payload)
	}
}

func TestHumanManagedAgentDisconnectMarksPresenceOffline(t *testing.T) {
	router := newTestRouter()
	_, _, agentUUID := registerMyAgent(t, router, "alice", "alice@a.test", "", "alice-agent-disconnect")

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/"+agentUUID+"/disconnect", map[string]any{
		"session_key": "main",
		"reason":      "owner_disconnect",
	}, humanHeaders("alice", "alice@a.test"))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected owner disconnect 200, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	agent, _ := payload["agent"].(map[string]any)
	metadata, _ := agent["metadata"].(map[string]any)
	presence, _ := metadata["presence"].(map[string]any)
	if got, _ := presence["status"].(string); got != "offline" {
		t.Fatalf("expected presence.status=offline, got %q payload=%v", got, payload)
	}
	if ready, ok := presence["ready"].(bool); !ok || ready {
		t.Fatalf("expected presence.ready=false, got %v payload=%v", presence["ready"], payload)
	}
	if got, _ := presence["session_key"].(string); got != "main" {
		t.Fatalf("expected presence.session_key=main, got %q payload=%v", got, payload)
	}
}

func TestHumanManagedAgentRoutesRejectUnmanageableAgent(t *testing.T) {
	router := newTestRouter()
	_, _, agentUUID := registerMyAgent(t, router, "alice", "alice@a.test", "", "alice-agent-owned")
	ensureHandleConfirmed(t, router, "bob", "bob@b.test")

	resp := doJSONRequest(t, router, http.MethodPatch, "/v1/me/agents/"+agentUUID, map[string]any{
		"metadata": map[string]any{
			"profile_markdown": "nope",
		},
	}, humanHeaders("bob", "bob@b.test"))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected non-manager profile patch 403, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	if got, _ := payload["error"].(string); got != "forbidden" {
		t.Fatalf("expected forbidden error, got %q payload=%v", got, payload)
	}
	detail, _ := payload["error_detail"].(map[string]any)
	if got, _ := detail["code"].(string); got != "forbidden" {
		t.Fatalf("expected forbidden error_detail.code, got %q payload=%v", got, payload)
	}
}

func hasSystemActivity(log []any, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, raw := range log {
		row, _ := raw.(map[string]any)
		if row == nil {
			continue
		}
		source, _ := row["source"].(string)
		if strings.TrimSpace(source) != "system" {
			continue
		}
		activity, _ := row["activity"].(string)
		if strings.TrimSpace(activity) == target {
			return true
		}
	}
	return false
}

func hasSystemActivityContaining(log []any, fragment string) bool {
	fragment = strings.TrimSpace(strings.ToLower(fragment))
	if fragment == "" {
		return false
	}
	for _, raw := range log {
		row, _ := raw.(map[string]any)
		if row == nil {
			continue
		}
		source, _ := row["source"].(string)
		if strings.TrimSpace(source) != "system" {
			continue
		}
		activity, _ := row["activity"].(string)
		if strings.Contains(strings.ToLower(strings.TrimSpace(activity)), fragment) {
			return true
		}
	}
	return false
}

func TestAgentSystemActivityLogIsAppendOnlyAndReadOnly(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)
	headersA := map[string]string{"Authorization": "Bearer " + tokenA}
	headersB := map[string]string{"Authorization": "Bearer " + tokenB}

	forgedPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"_system_activity_log": []map[string]any{
				{"activity": "forged activity should not persist", "source": "system"},
			},
			"activities": []any{"agent-authored note"},
		},
	}, headersA)
	if forgedPatch.Code != http.StatusOK {
		t.Fatalf("expected metadata patch with forged system activity to succeed safely, got %d %s", forgedPatch.Code, forgedPatch.Body.String())
	}
	forgedPayload := decodeJSONMap(t, forgedPatch.Body.Bytes())
	forgedResult := requireAgentRuntimeSuccessEnvelope(t, forgedPayload)
	forgedAgent, _ := forgedResult["agent"].(map[string]any)
	forgedMetadata, _ := forgedAgent["metadata"].(map[string]any)
	if _, exists := forgedMetadata[model.AgentMetadataKeySystemActivityLog]; exists {
		t.Fatalf("expected internal system activity key to be hidden from metadata response, got %v", forgedMetadata[model.AgentMetadataKeySystemActivityLog])
	}

	readA := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, headersA)
	if readA.Code != http.StatusOK {
		t.Fatalf("expected initial GET /v1/agents/me 200, got %d %s", readA.Code, readA.Body.String())
	}
	readAPayload := decodeJSONMap(t, readA.Body.Bytes())
	readAResult := requireAgentRuntimeSuccessEnvelope(t, readAPayload)
	readAAgent, _ := readAResult["agent"].(map[string]any)
	readALog, _ := readAAgent["activity_log"].([]any)
	if hasActivityText(readALog, "forged activity should not persist") {
		t.Fatalf("expected forged system activity to be ignored, got activity_log=%v", readALog)
	}

	pub := publish(t, router, tokenA, agentUUIDB, "activity-log-message")
	if pub.Code != http.StatusAccepted {
		t.Fatalf("publish failed: %d %s", pub.Code, pub.Body.String())
	}

	pullResp := pull(t, router, tokenB, 0)
	if pullResp.Code != http.StatusOK {
		t.Fatalf("pull failed: %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	pullResult := requireAgentRuntimeSuccessEnvelope(t, pullPayload)
	delivery, _ := pullResult["delivery"].(map[string]any)
	deliveryID, _ := delivery["delivery_id"].(string)
	if strings.TrimSpace(deliveryID) == "" {
		t.Fatalf("expected pull delivery_id, got %v", pullResult)
	}

	ackResp := ackDelivery(t, router, tokenB, deliveryID)
	if ackResp.Code != http.StatusOK {
		t.Fatalf("ack failed: %d %s", ackResp.Code, ackResp.Body.String())
	}

	readAAfter := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, headersA)
	if readAAfter.Code != http.StatusOK {
		t.Fatalf("expected GET /v1/agents/me for sender 200, got %d %s", readAAfter.Code, readAAfter.Body.String())
	}
	readAAfterPayload := decodeJSONMap(t, readAAfter.Body.Bytes())
	readAAfterResult := requireAgentRuntimeSuccessEnvelope(t, readAAfterPayload)
	readAAfterAgent, _ := readAAfterResult["agent"].(map[string]any)
	readAAfterLog, _ := readAAfterAgent["activity_log"].([]any)
	if !hasSystemActivity(readAAfterLog, "sent first message") && !hasSystemActivity(readAAfterLog, "sent message") {
		t.Fatalf("expected sender activity_log to include system send entry, got %v", readAAfterLog)
	}

	readBAfter := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, headersB)
	if readBAfter.Code != http.StatusOK {
		t.Fatalf("expected GET /v1/agents/me for receiver 200, got %d %s", readBAfter.Code, readBAfter.Body.String())
	}
	readBAfterPayload := decodeJSONMap(t, readBAfter.Body.Bytes())
	readBAfterResult := requireAgentRuntimeSuccessEnvelope(t, readBAfterPayload)
	readBAfterAgent, _ := readBAfterResult["agent"].(map[string]any)
	readBAfterLog, _ := readBAfterAgent["activity_log"].([]any)
	if !hasSystemActivityContaining(readBAfterLog, "received message") {
		t.Fatalf("expected receiver activity_log to include system receive entry, got %v", readBAfterLog)
	}
}

func TestAgentSystemActivityLogMetadataWriteLabels(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)
	headersA := map[string]string{"Authorization": "Bearer " + tokenA}

	emojiPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"emoji": "🤖",
		},
	}, headersA)
	if emojiPatch.Code != http.StatusOK {
		t.Fatalf("emoji patch failed: %d %s", emojiPatch.Code, emojiPatch.Body.String())
	}

	bioPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"profile_markdown": "## Bio\nBuilds systems.",
		},
	}, headersA)
	if bioPatch.Code != http.StatusOK {
		t.Fatalf("bio patch failed: %d %s", bioPatch.Code, bioPatch.Body.String())
	}

	skillsPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"skills": []map[string]any{
				{"name": "emoji.update", "description": "Update emoji metadata safely."},
			},
		},
	}, headersA)
	if skillsPatch.Code != http.StatusOK {
		t.Fatalf("skills patch failed: %d %s", skillsPatch.Code, skillsPatch.Body.String())
	}

	readResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, headersA)
	if readResp.Code != http.StatusOK {
		t.Fatalf("read agent failed: %d %s", readResp.Code, readResp.Body.String())
	}
	payload := decodeJSONMap(t, readResp.Body.Bytes())
	result := requireAgentRuntimeSuccessEnvelope(t, payload)
	agent, _ := result["agent"].(map[string]any)
	log, _ := agent["activity_log"].([]any)
	if !hasSystemActivity(log, "updated emoji") {
		t.Fatalf("expected activity_log to include 'updated emoji', got %v", log)
	}
	if !hasSystemActivity(log, "updated bio") {
		t.Fatalf("expected activity_log to include 'updated bio', got %v", log)
	}
	if !hasSystemActivity(log, "added new skills") {
		t.Fatalf("expected activity_log to include 'added new skills', got %v", log)
	}
}

func TestAgentSystemActivityLogIncludesPublicReceiverHandle(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)
	headersA := map[string]string{"Authorization": "Bearer " + tokenA}
	headersB := map[string]string{"Authorization": "Bearer " + tokenB}

	makePublic := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"public": true,
		},
	}, headersB)
	if makePublic.Code != http.StatusOK {
		t.Fatalf("expected receiver public metadata patch 200, got %d %s", makePublic.Code, makePublic.Body.String())
	}

	pub := publish(t, router, tokenA, agentUUIDB, "public-handle-message")
	if pub.Code != http.StatusAccepted {
		t.Fatalf("publish failed: %d %s", pub.Code, pub.Body.String())
	}

	readResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, headersA)
	if readResp.Code != http.StatusOK {
		t.Fatalf("sender read failed: %d %s", readResp.Code, readResp.Body.String())
	}
	payload := decodeJSONMap(t, readResp.Body.Bytes())
	result := requireAgentRuntimeSuccessEnvelope(t, payload)
	agent, _ := result["agent"].(map[string]any)
	log, _ := agent["activity_log"].([]any)
	if !hasSystemActivityContaining(log, "to agent-b") {
		t.Fatalf("expected sender activity to include public receiver handle, got %v", log)
	}
}

func TestAgentMeMetadataRejectsInvalidAgentType(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)
	headers := map[string]string{"Authorization": "Bearer " + tokenA}

	resp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{"agent_type": "bad type!"},
	}, headers)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid agent_type to return 400, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	if payload["error"] != "invalid_agent_type" {
		t.Fatalf("expected invalid_agent_type error, got %v payload=%v", payload["error"], payload)
	}
}

func TestAgentMeMetadataRejectsSecretLikeSkillDescriptions(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)
	headers := map[string]string{"Authorization": "Bearer " + tokenA}

	resp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"skills": []map[string]any{
				{"name": "weather_lookup", "description": "Use API key abc123 to query weather provider"},
			},
		},
	}, headers)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected secret-like skill description to return 400, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	if payload["error"] != "invalid_skill_description" {
		t.Fatalf("expected invalid_skill_description error, got %v payload=%v", payload["error"], payload)
	}
}

func TestPublishSkillActivationValidatesJSONParameters(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	metadataPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"skills": []map[string]any{
				{
					"name":        "weather_lookup",
					"description": "Get current weather for a location.",
					"parameters": map[string]any{
						"required": []map[string]any{
							{"name": "location", "description": "City or postal code."},
						},
						"optional": []map[string]any{
							{"name": "units", "description": "metric or imperial."},
						},
						"secret_policy": "forbidden",
					},
				},
			},
		},
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if metadataPatch.Code != http.StatusOK {
		t.Fatalf("metadata patch failed: %d %s", metadataPatch.Code, metadataPatch.Body.String())
	}

	payloadBytes, err := json.Marshal(map[string]any{
		"type":           "skill_request",
		"request_id":     "req-json-skill",
		"skill_name":     "weather_lookup",
		"reply_required": true,
		"payload": map[string]any{
			"units": "metric",
		},
	})
	if err != nil {
		t.Fatalf("marshal skill payload failed: %v", err)
	}

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
		"content_type":  "application/json",
		"payload":       string(payloadBytes),
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected publish validation failure 400, got %d %s", resp.Code, resp.Body.String())
	}
	body := decodeJSONMap(t, resp.Body.Bytes())
	if got, _ := body["error"].(string); got != "invalid_skill_request" {
		t.Fatalf("expected invalid_skill_request, got %q payload=%v", got, body)
	}
	if failure, _ := body["failure"].(bool); !failure {
		t.Fatalf("expected failure=true, got payload=%v", body)
	}
	validationErrors, _ := body["validation_errors"].([]any)
	if len(validationErrors) == 0 || !strings.Contains(fmt.Sprint(validationErrors[0]), "missing required parameter") {
		t.Fatalf("expected missing required parameter detail, got %v", validationErrors)
	}
}

func TestPublishSkillActivationValidatesMarkdownParameters(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	metadataPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"skills": []map[string]any{
				{
					"name":        "weather_lookup",
					"description": "Get current weather for a location.",
					"parameters": strings.Join([]string{
						"Required Parameters:",
						"- `location`: City or postal code.",
						"Optional Parameters:",
						"- `units`: metric or imperial.",
						"Never pass secrets.",
					}, "\n"),
				},
			},
		},
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if metadataPatch.Code != http.StatusOK {
		t.Fatalf("metadata patch failed: %d %s", metadataPatch.Code, metadataPatch.Body.String())
	}

	payloadBytes, err := json.Marshal(map[string]any{
		"type":           "skill_request",
		"request_id":     "req-markdown-skill",
		"skill_name":     "weather_lookup",
		"reply_required": true,
		"payload":        "units: metric",
		"payload_format": "markdown",
	})
	if err != nil {
		t.Fatalf("marshal skill payload failed: %v", err)
	}

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
		"content_type":  "application/json",
		"payload":       string(payloadBytes),
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected publish validation failure 400, got %d %s", resp.Code, resp.Body.String())
	}
	body := decodeJSONMap(t, resp.Body.Bytes())
	if got, _ := body["error"].(string); got != "invalid_skill_request" {
		t.Fatalf("expected invalid_skill_request, got %q payload=%v", got, body)
	}
	validationErrors, _ := body["validation_errors"].([]any)
	if len(validationErrors) == 0 || !strings.Contains(fmt.Sprint(validationErrors[0]), "missing required parameter") {
		t.Fatalf("expected missing required parameter detail, got %v", validationErrors)
	}
}

func TestAgentMeReadIncludesBoundHumanAndOrganization(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Runtime Context Org")
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")

	humanOwnedToken, humanOwnedUUID := registerAgentWithUUID(
		t,
		router,
		"alice",
		"alice@a.test",
		orgID,
		"human-owned-runtime",
		aliceHumanID,
	)
	humanOwnedResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
		"Authorization": "Bearer " + humanOwnedToken,
	})
	if humanOwnedResp.Code != http.StatusOK {
		t.Fatalf("expected GET /v1/agents/me 200 for human-owned agent, got %d %s", humanOwnedResp.Code, humanOwnedResp.Body.String())
	}
	humanOwnedPayload := decodeJSONMap(t, humanOwnedResp.Body.Bytes())
	humanOwnedAgent, _ := humanOwnedPayload["agent"].(map[string]any)
	if gotAgentUUID, _ := humanOwnedAgent["agent_uuid"].(string); gotAgentUUID != humanOwnedUUID {
		t.Fatalf("expected authenticated agent_uuid %q, got %q payload=%v", humanOwnedUUID, gotAgentUUID, humanOwnedPayload)
	}
	humanOwnedOrg, _ := humanOwnedPayload["organization"].(map[string]any)
	if gotOrgID, _ := humanOwnedOrg["org_id"].(string); gotOrgID != orgID {
		t.Fatalf("expected bound organization org_id %q, got %q payload=%v", orgID, gotOrgID, humanOwnedPayload)
	}
	ownerHuman, _ := humanOwnedPayload["human"].(map[string]any)
	if gotHumanID, _ := ownerHuman["human_id"].(string); gotHumanID != aliceHumanID {
		t.Fatalf("expected bound human.human_id %q, got %q payload=%v", aliceHumanID, gotHumanID, humanOwnedPayload)
	}

	orgOwnedToken, orgOwnedUUID := registerAgentWithUUID(
		t,
		router,
		"alice",
		"alice@a.test",
		orgID,
		"org-owned-runtime",
		"",
	)
	orgOwnedResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
		"Authorization": "Bearer " + orgOwnedToken,
	})
	if orgOwnedResp.Code != http.StatusOK {
		t.Fatalf("expected GET /v1/agents/me 200 for org-owned agent, got %d %s", orgOwnedResp.Code, orgOwnedResp.Body.String())
	}
	orgOwnedPayload := decodeJSONMap(t, orgOwnedResp.Body.Bytes())
	orgOwnedAgent, _ := orgOwnedPayload["agent"].(map[string]any)
	if gotAgentUUID, _ := orgOwnedAgent["agent_uuid"].(string); gotAgentUUID != orgOwnedUUID {
		t.Fatalf("expected authenticated org-owned agent_uuid %q, got %q payload=%v", orgOwnedUUID, gotAgentUUID, orgOwnedPayload)
	}
	if _, ok := orgOwnedPayload["human"]; ok {
		t.Fatalf("expected human object omitted for org-owned agent, got payload=%v", orgOwnedPayload)
	}
	ownerObj, _ := orgOwnedAgent["owner"].(map[string]any)
	if gotOrgID, _ := ownerObj["org_id"].(string); gotOrgID != orgID {
		t.Fatalf("expected org-owned agent.owner.org_id=%q, got %q payload=%v", orgID, gotOrgID, orgOwnedPayload)
	}
}

func TestAgentMetadataPatchAliasesSupportSelfOnly(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, agentUUIDA, agentUUIDB := setupTrustedAgents(t, router)
	headers := map[string]string{"Authorization": "Bearer " + tokenA}

	meAliasResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me", map[string]any{
		"metadata": map[string]any{"alias": "me"},
	}, headers)
	if meAliasResp.Code != http.StatusOK {
		t.Fatalf("expected PATCH /v1/agents/me 200, got %d %s", meAliasResp.Code, meAliasResp.Body.String())
	}
	meAliasPayload := decodeJSONMap(t, meAliasResp.Body.Bytes())
	meAliasAgent, _ := meAliasPayload["agent"].(map[string]any)
	if meAliasAgent["agent_uuid"] != agentUUIDA {
		t.Fatalf("expected /v1/agents/me alias to update authenticated agent %q, got %v", agentUUIDA, meAliasAgent["agent_uuid"])
	}

	uuidAliasResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/"+agentUUIDA, map[string]any{
		"metadata": map[string]any{"alias": "uuid"},
	}, headers)
	if uuidAliasResp.Code != http.StatusOK {
		t.Fatalf("expected PATCH /v1/agents/{uuid} alias 200 for self, got %d %s", uuidAliasResp.Code, uuidAliasResp.Body.String())
	}
	uuidAliasPayload := decodeJSONMap(t, uuidAliasResp.Body.Bytes())
	uuidAliasAgent, _ := uuidAliasPayload["agent"].(map[string]any)
	if uuidAliasAgent["agent_uuid"] != agentUUIDA {
		t.Fatalf("expected /v1/agents/{uuid} alias to update authenticated agent %q, got %v", agentUUIDA, uuidAliasAgent["agent_uuid"])
	}

	otherUUIDResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/"+agentUUIDB, map[string]any{
		"metadata": map[string]any{"alias": "forbidden"},
	}, headers)
	if otherUUIDResp.Code != http.StatusForbidden {
		t.Fatalf("expected PATCH /v1/agents/{other-uuid} with agent token to return 403, got %d %s", otherUUIDResp.Code, otherUUIDResp.Body.String())
	}
}

func TestOpenAPIYAMLHeaders(t *testing.T) {
	router := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("openapi failed: %d %s", resp.Code, resp.Body.String())
	}
	contentType := resp.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/yaml") {
		t.Fatalf("expected text/yaml content type, got %q", contentType)
	}
	if link := resp.Header().Get("Link"); !strings.Contains(link, "/openapi.md") {
		t.Fatalf("expected alternate markdown link header, got %q", link)
	}
}

func TestOpenAPIMarkdownHeaders(t *testing.T) {
	router := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/openapi.md", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("openapi markdown failed: %d %s", resp.Code, resp.Body.String())
	}
	contentType := resp.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/markdown") {
		t.Fatalf("expected text/markdown content type, got %q", contentType)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "MoltenHub OpenAPI Companion") {
		t.Fatalf("expected companion heading in markdown output, got %q", body)
	}
	if !strings.Contains(body, "```yaml") || !strings.Contains(body, "/v1/agents/me") {
		t.Fatalf("expected embedded yaml spec content in openapi markdown, got %q", body)
	}
	if !strings.Contains(body, "metadata.display_name") || !strings.Contains(body, "metadata.emoji") || !strings.Contains(body, "metadata.profile_markdown") || !strings.Contains(body, "metadata.activities") || !strings.Contains(body, "metadata.hire_me") {
		t.Fatalf("expected metadata directory fields in openapi markdown, got %q", body)
	}
	if !strings.Contains(body, "metadata.llm") || !strings.Contains(body, "metadata.harness") || !strings.Contains(body, "metadata.presence") {
		t.Fatalf("expected llm/harness/presence metadata fields in openapi markdown, got %q", body)
	}
	if !strings.Contains(body, "status=online") || !strings.Contains(body, "status=offline") {
		t.Fatalf("expected presence status guidance in openapi markdown, got %q", body)
	}
	if !strings.Contains(body, "copy-ready self-signup prompt") {
		t.Fatalf("expected self-signup prompt contract text in openapi markdown, got %q", body)
	}
	if link := resp.Header().Get("Link"); !strings.Contains(link, "/openapi.yaml") {
		t.Fatalf("expected alternate yaml link header, got %q", link)
	}
}

func TestDocsPageIncludesMarkdownAlternateLinks(t *testing.T) {
	router := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("docs page failed: %d %s", resp.Code, resp.Body.String())
	}
	contentType := resp.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("expected html docs content type, got %q", contentType)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "/openapi.md") || !strings.Contains(body, "Agent-readable references") {
		t.Fatalf("expected agent-readable docs content with openapi.md link, got %q", body)
	}
	if link := resp.Header().Get("Link"); !strings.Contains(link, "/openapi.md") {
		t.Fatalf("expected docs alternate link header, got %q", link)
	}
}

func TestAdminSnapshotDoesNotLeakMessagePayloads(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, agentUUIDB := setupTrustedAgents(t, router)
	secretPayload := "top-secret-payload-should-not-appear"

	pub := publish(t, router, tokenA, agentUUIDB, secretPayload)
	if pub.Code != http.StatusAccepted {
		t.Fatalf("publish failed: %d %s", pub.Code, pub.Body.String())
	}

	snap := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@example.com"))
	if snap.Code != http.StatusOK {
		t.Fatalf("snapshot failed: %d %s", snap.Code, snap.Body.String())
	}
	bodyText := snap.Body.String()
	if strings.Contains(bodyText, secretPayload) || strings.Contains(bodyText, "\"payload\"") {
		t.Fatalf("snapshot should not include message payload data: %s", bodyText)
	}
}

func TestAdminSnapshotDoesNotLeakHumanEmails(t *testing.T) {
	router := newTestRouter()
	_ = currentHumanID(t, router, "alice", "alice+private@a.test")

	snap := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@example.com"))
	if snap.Code != http.StatusOK {
		t.Fatalf("snapshot failed: %d %s", snap.Code, snap.Body.String())
	}
	bodyText := snap.Body.String()
	if strings.Contains(bodyText, "alice+private@a.test") || strings.Contains(bodyText, "root@example.com") {
		t.Fatalf("snapshot should not include human email addresses: %s", bodyText)
	}

	payload := decodeJSONMap(t, snap.Body.Bytes())
	snapshot, _ := payload["snapshot"].(map[string]any)
	humans, _ := snapshot["humans"].([]any)
	if len(humans) == 0 {
		t.Fatalf("expected snapshot.humans to be non-empty")
	}
	for _, raw := range humans {
		human, _ := raw.(map[string]any)
		if _, ok := human["email"]; ok {
			t.Fatalf("snapshot human row should not include email: %v", human)
		}
		if _, ok := human["auth_subject"]; ok {
			t.Fatalf("snapshot human row should not include auth_subject: %v", human)
		}
	}
}

func TestAdminSnapshotIncludesMessageRollups(t *testing.T) {
	router := newTestRouter()
	orgA, orgB, tokenA, tokenB, _, _, agentUUIDA, agentUUIDB := setupTrustedAgents(t, router)
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")

	typePatchA := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{"agent_type": "CoDeX"},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if typePatchA.Code != http.StatusOK {
		t.Fatalf("expected agent A metadata patch 200, got %d %s", typePatchA.Code, typePatchA.Body.String())
	}
	typePatchB := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{"agent_type": "Claude"},
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if typePatchB.Code != http.StatusOK {
		t.Fatalf("expected agent B metadata patch 200, got %d %s", typePatchB.Code, typePatchB.Body.String())
	}

	pub := publish(t, router, tokenA, agentUUIDB, "hello-rollup")
	if pub.Code != http.StatusAccepted {
		t.Fatalf("publish failed: %d %s", pub.Code, pub.Body.String())
	}
	pubPayload := decodeJSONMap(t, pub.Body.Bytes())
	messageID, _ := pubPayload["message_id"].(string)
	if messageID == "" {
		t.Fatalf("expected publish response to include message_id")
	}

	pullResp := pull(t, router, tokenB, 0)
	if pullResp.Code != http.StatusOK {
		t.Fatalf("pull failed: %d %s", pullResp.Code, pullResp.Body.String())
	}

	snap := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@example.com"))
	if snap.Code != http.StatusOK {
		t.Fatalf("snapshot failed: %d %s", snap.Code, snap.Body.String())
	}
	payload := decodeJSONMap(t, snap.Body.Bytes())
	snapshot, _ := payload["snapshot"].(map[string]any)
	metrics, _ := snapshot["message_metrics"].(map[string]any)
	if metrics == nil {
		t.Fatalf("expected snapshot.message_metrics in admin snapshot payload=%v", payload)
	}

	agents, _ := metrics["agents"].([]any)
	if len(agents) == 0 {
		t.Fatalf("expected message_metrics.agents to be non-empty")
	}
	var metricA map[string]any
	var metricB map[string]any
	for _, raw := range agents {
		row, _ := raw.(map[string]any)
		if row["agent_uuid"] == agentUUIDA {
			metricA = row
		}
		if row["agent_uuid"] == agentUUIDB {
			metricB = row
		}
	}
	if metricA == nil || metricB == nil {
		t.Fatalf("expected agent metrics for both trusted agents, got metrics=%v", agents)
	}
	if metricA["outbox_messages"] != float64(1) || metricA["inbox_messages"] != float64(0) {
		t.Fatalf("expected agent A outbox=1 inbox=0, got %v", metricA)
	}
	if metricB["outbox_messages"] != float64(0) || metricB["inbox_messages"] != float64(1) {
		t.Fatalf("expected agent B outbox=0 inbox=1, got %v", metricB)
	}
	archiveA, _ := metricA["archive"].(map[string]any)
	fromA, _ := archiveA["from"].([]any)
	if len(fromA) != 1 {
		t.Fatalf("expected agent A archive.from len=1, got %d archive=%v", len(fromA), archiveA)
	}
	fromAEntry, _ := fromA[0].(map[string]any)
	if fromAEntry["message_id"] != messageID {
		t.Fatalf("expected agent A archive.from message_id=%q, got %v", messageID, fromAEntry["message_id"])
	}
	if _, hasPayload := fromAEntry["payload"]; hasPayload {
		t.Fatalf("agent archive entry should not expose payload: %v", fromAEntry)
	}
	if metricA["agent_type"] != "codex" {
		t.Fatalf("expected normalized agent_type codex for agent A, got %v", metricA["agent_type"])
	}
	if metricB["agent_type"] != "claude" {
		t.Fatalf("expected normalized agent_type claude for agent B, got %v", metricB["agent_type"])
	}

	humans, _ := metrics["humans"].([]any)
	if len(humans) == 0 {
		t.Fatalf("expected message_metrics.humans to be non-empty")
	}
	var aliceMetrics map[string]any
	var bobMetrics map[string]any
	for _, raw := range humans {
		row, _ := raw.(map[string]any)
		if row["human_id"] == aliceHumanID {
			aliceMetrics = row
		}
		if row["human_id"] == bobHumanID {
			bobMetrics = row
		}
	}
	if aliceMetrics == nil || bobMetrics == nil {
		t.Fatalf("expected human rollups for alice and bob, got %v", humans)
	}
	if aliceMetrics["linked_agents"] != float64(1) || aliceMetrics["outbox_messages"] != float64(1) {
		t.Fatalf("unexpected alice human rollup: %v", aliceMetrics)
	}
	if bobMetrics["linked_agents"] != float64(1) || bobMetrics["inbox_messages"] != float64(1) {
		t.Fatalf("unexpected bob human rollup: %v", bobMetrics)
	}

	orgs, _ := metrics["organizations"].([]any)
	if len(orgs) == 0 {
		t.Fatalf("expected message_metrics.organizations to be non-empty")
	}
	var orgAMetrics map[string]any
	var orgBMetrics map[string]any
	for _, raw := range orgs {
		row, _ := raw.(map[string]any)
		if row["org_id"] == orgA {
			orgAMetrics = row
		}
		if row["org_id"] == orgB {
			orgBMetrics = row
		}
	}
	if orgAMetrics == nil || orgBMetrics == nil {
		t.Fatalf("expected org rollups for orgA/orgB, got %v", orgs)
	}
	if orgAMetrics["outbox_messages"] != float64(1) || orgBMetrics["inbox_messages"] != float64(1) {
		t.Fatalf("unexpected org rollups: orgA=%v orgB=%v", orgAMetrics, orgBMetrics)
	}
}

func TestAgentMetadataLLMHarnessOptionalAndIncludedInAdminSnapshot(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, agentUUIDA, agentUUIDB := setupTrustedAgents(t, router)

	withoutFingerprint := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"profile_markdown": "# Agent A",
			"activities":       []string{"bound to hub"},
			"hire_me":          false,
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if withoutFingerprint.Code != http.StatusOK {
		t.Fatalf("expected metadata patch without llm/harness 200, got %d %s", withoutFingerprint.Code, withoutFingerprint.Body.String())
	}
	withoutFingerprintPayload := decodeJSONMap(t, withoutFingerprint.Body.Bytes())
	withoutFingerprintResult := requireAgentRuntimeSuccessEnvelope(t, withoutFingerprintPayload)
	withoutFingerprintAgent, _ := withoutFingerprintResult["agent"].(map[string]any)
	withoutFingerprintMetadata, _ := withoutFingerprintAgent["metadata"].(map[string]any)
	if _, ok := withoutFingerprintMetadata["llm"]; ok {
		t.Fatalf("expected llm to remain optional/absent when not provided, got %v", withoutFingerprintMetadata["llm"])
	}
	if _, ok := withoutFingerprintMetadata["harness"]; ok {
		t.Fatalf("expected harness to remain optional/absent when not provided, got %v", withoutFingerprintMetadata["harness"])
	}

	expectedLLM := "openai/gpt-5.4@2026-03-01"
	expectedHarness := "openai-codex@v1"
	withFingerprint := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"agent_type": "assistant",
			"llm":        expectedLLM,
			"harness":    expectedHarness,
		},
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if withFingerprint.Code != http.StatusOK {
		t.Fatalf("expected metadata patch with llm/harness 200, got %d %s", withFingerprint.Code, withFingerprint.Body.String())
	}

	snap := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@example.com"))
	if snap.Code != http.StatusOK {
		t.Fatalf("snapshot failed: %d %s", snap.Code, snap.Body.String())
	}
	snapPayload := decodeJSONMap(t, snap.Body.Bytes())
	snapshot, _ := snapPayload["snapshot"].(map[string]any)
	agents, _ := snapshot["agents"].([]any)
	if len(agents) == 0 {
		t.Fatalf("expected snapshot.agents to be non-empty")
	}

	var agentARow map[string]any
	var agentBRow map[string]any
	for _, raw := range agents {
		row, _ := raw.(map[string]any)
		switch row["agent_uuid"] {
		case agentUUIDA:
			agentARow = row
		case agentUUIDB:
			agentBRow = row
		}
	}
	if agentARow == nil || agentBRow == nil {
		t.Fatalf("expected both agent rows in snapshot, got agents=%v", agents)
	}

	agentAMetadata, _ := agentARow["metadata"].(map[string]any)
	if _, ok := agentAMetadata["llm"]; ok {
		t.Fatalf("expected snapshot metadata.llm absent for agent without fingerprint, got %v", agentAMetadata["llm"])
	}
	if _, ok := agentAMetadata["harness"]; ok {
		t.Fatalf("expected snapshot metadata.harness absent for agent without fingerprint, got %v", agentAMetadata["harness"])
	}

	agentBMetadata, _ := agentBRow["metadata"].(map[string]any)
	if got, _ := agentBMetadata["llm"].(string); got != expectedLLM {
		t.Fatalf("expected snapshot metadata.llm=%q, got %q metadata=%v", expectedLLM, got, agentBMetadata)
	}
	if got, _ := agentBMetadata["harness"].(string); got != expectedHarness {
		t.Fatalf("expected snapshot metadata.harness=%q, got %q metadata=%v", expectedHarness, got, agentBMetadata)
	}
}

func TestAdminSnapshotIncludesActivityFeedForMessageLifecycle(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	pub := publish(t, router, tokenA, agentUUIDB, "hello-activity-feed")
	if pub.Code != http.StatusAccepted {
		t.Fatalf("publish failed: %d %s", pub.Code, pub.Body.String())
	}
	pubPayload := decodeJSONMap(t, pub.Body.Bytes())
	_ = requireAgentRuntimeSuccessEnvelope(t, pubPayload)

	firstPull := pull(t, router, tokenB, 0)
	if firstPull.Code != http.StatusOK {
		t.Fatalf("first pull failed: %d %s", firstPull.Code, firstPull.Body.String())
	}
	firstPullPayload := decodeJSONMap(t, firstPull.Body.Bytes())
	firstPullResult := requireAgentRuntimeSuccessEnvelope(t, firstPullPayload)
	firstDelivery, _ := firstPullResult["delivery"].(map[string]any)
	firstDeliveryID, _ := firstDelivery["delivery_id"].(string)
	if strings.TrimSpace(firstDeliveryID) == "" {
		t.Fatalf("expected delivery_id from first pull, got %v", firstPullResult)
	}

	nackResp := nackDelivery(t, router, tokenB, firstDeliveryID)
	if nackResp.Code != http.StatusOK {
		t.Fatalf("nack failed: %d %s", nackResp.Code, nackResp.Body.String())
	}

	secondPull := pull(t, router, tokenB, 0)
	if secondPull.Code != http.StatusOK {
		t.Fatalf("second pull failed: %d %s", secondPull.Code, secondPull.Body.String())
	}
	secondPullPayload := decodeJSONMap(t, secondPull.Body.Bytes())
	secondPullResult := requireAgentRuntimeSuccessEnvelope(t, secondPullPayload)
	secondDelivery, _ := secondPullResult["delivery"].(map[string]any)
	secondDeliveryID, _ := secondDelivery["delivery_id"].(string)
	if strings.TrimSpace(secondDeliveryID) == "" {
		t.Fatalf("expected delivery_id from second pull, got %v", secondPullResult)
	}

	ackResp := ackDelivery(t, router, tokenB, secondDeliveryID)
	if ackResp.Code != http.StatusOK {
		t.Fatalf("ack failed: %d %s", ackResp.Code, ackResp.Body.String())
	}

	snap := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@example.com"))
	if snap.Code != http.StatusOK {
		t.Fatalf("snapshot failed: %d %s", snap.Code, snap.Body.String())
	}
	payload := decodeJSONMap(t, snap.Body.Bytes())
	snapshot, _ := payload["snapshot"].(map[string]any)
	feed, _ := snapshot["activity_feed"].([]any)
	if len(feed) == 0 {
		t.Fatalf("expected non-empty snapshot.activity_feed in payload=%v", payload)
	}

	messageActions := map[string]bool{}
	for _, raw := range feed {
		row, _ := raw.(map[string]any)
		if row == nil {
			continue
		}
		category, _ := row["category"].(string)
		if category != "message" {
			continue
		}
		createdAt, _ := row["created_at"].(string)
		if strings.TrimSpace(createdAt) == "" {
			t.Fatalf("expected created_at on message activity row: %v", row)
		}
		action, _ := row["action"].(string)
		if strings.TrimSpace(action) != "" {
			messageActions[action] = true
		}
	}
	for _, required := range []string{"publish", "lease", "nack", "ack"} {
		if !messageActions[required] {
			t.Fatalf("expected message action %q in activity_feed, got actions=%v feed=%v", required, messageActions, feed)
		}
	}
}

func TestAdminSnapshotIncludesAgentCreationActivity(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org A")
	_, agentUUID := registerAgentWithUUID(t, router, "alice", "alice@a.test", orgID, "agent-a", aliceHumanID)

	snap := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@example.com"))
	if snap.Code != http.StatusOK {
		t.Fatalf("snapshot failed: %d %s", snap.Code, snap.Body.String())
	}
	payload := decodeJSONMap(t, snap.Body.Bytes())
	snapshot, _ := payload["snapshot"].(map[string]any)

	feed, _ := snapshot["activity_feed"].([]any)
	foundCreate := false
	foundRedeem := false
	for _, raw := range feed {
		row, _ := raw.(map[string]any)
		if row == nil {
			continue
		}
		category, _ := row["category"].(string)
		action, _ := row["action"].(string)
		subjectID, _ := row["subject_id"].(string)
		details, _ := row["details"].(map[string]any)
		if category == "agent" && action == "create" && subjectID == agentUUID {
			foundCreate = true
			if got, _ := details["creation_flow"].(string); got != "bind" {
				t.Fatalf("expected agent/create creation_flow=bind, got %q row=%v", got, row)
			}
			if got, _ := details["agent_uuid"].(string); got != agentUUID {
				t.Fatalf("expected agent_uuid detail %q, got %q row=%v", agentUUID, got, row)
			}
		}
		if category == "agent_bind" && action == "redeem" {
			if got, _ := details["agent_uuid"].(string); got == agentUUID {
				foundRedeem = true
			}
		}
	}
	if !foundCreate {
		t.Fatalf("expected snapshot.activity_feed to include agent/create for %q, got feed=%v", agentUUID, feed)
	}
	if !foundRedeem {
		t.Fatalf("expected existing agent_bind/redeem event for %q to remain, got feed=%v", agentUUID, feed)
	}

	agents, _ := snapshot["agents"].([]any)
	var createdAgent map[string]any
	for _, raw := range agents {
		agent, _ := raw.(map[string]any)
		if agent == nil {
			continue
		}
		if got, _ := agent["agent_uuid"].(string); got == agentUUID {
			createdAgent = agent
			break
		}
	}
	if createdAgent == nil {
		t.Fatalf("expected created agent %q in snapshot agents=%v", agentUUID, agents)
	}
	log, _ := createdAgent["activity_log"].([]any)
	for _, raw := range log {
		row, _ := raw.(map[string]any)
		if row == nil {
			continue
		}
		activity, _ := row["activity"].(string)
		source, _ := row["source"].(string)
		category, _ := row["category"].(string)
		action, _ := row["action"].(string)
		eventID, _ := row["event_id"].(string)
		subjectID, _ := row["subject_id"].(string)
		if activity == "created agent" && source == "system" && category == "agent" && action == "create" && eventID != "" && subjectID == agentUUID {
			return
		}
	}
	t.Fatalf("expected full created agent activity_log entry for %q, got log=%v", agentUUID, log)
}

func TestAdminSnapshotHeaderKeyAccess(t *testing.T) {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "snapshot-secret", "", "example.com", true, 15*time.Minute, false)
	router := NewRouter(h)

	unauth := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, nil)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected admin snapshot without auth to return 401, got %d %s", unauth.Code, unauth.Body.String())
	}

	wrongKey := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, map[string]string{
		"X-Admin-Snapshot-Key": "wrong",
	})
	if wrongKey.Code != http.StatusUnauthorized {
		t.Fatalf("expected admin snapshot with wrong key to return 401, got %d %s", wrongKey.Code, wrongKey.Body.String())
	}

	withKey := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, map[string]string{
		"X-Admin-Snapshot-Key": "snapshot-secret",
	})
	if withKey.Code != http.StatusOK {
		t.Fatalf("expected admin snapshot with key to return 200, got %d %s", withKey.Code, withKey.Body.String())
	}

	superAdmin := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@example.com"))
	if superAdmin.Code != http.StatusOK {
		t.Fatalf("expected super-admin snapshot without key to return 200, got %d %s", superAdmin.Code, superAdmin.Body.String())
	}
}

func TestPublicSnapshotFiltersPrivateEntities(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")

	publicOrgID := createOrg(t, router, "alice", "alice@a.test", "Public Org")
	privateOrgID := createOrg(t, router, "bob", "bob@b.test", "Private Org")

	privateOrgResp := doJSONRequest(t, router, http.MethodPatch, "/v1/orgs/"+privateOrgID+"/metadata", map[string]any{
		"metadata": map[string]any{"public": false},
	}, humanHeaders("bob", "bob@b.test"))
	if privateOrgResp.Code != http.StatusOK {
		t.Fatalf("expected private org metadata patch 200, got %d %s", privateOrgResp.Code, privateOrgResp.Body.String())
	}

	inviteID := createInvite(t, router, "alice", "alice@a.test", publicOrgID, "bob@b.test", model.RoleMember)
	acceptInvite(t, router, "bob", "bob@b.test", inviteID)

	_, _ = registerAgentWithUUID(t, router, "alice", "alice@a.test", publicOrgID, "alice-agent", aliceHumanID)
	_, _ = registerAgentWithUUID(t, router, "bob", "bob@b.test", publicOrgID, "bob-agent", bobHumanID)

	privateAlice := doJSONRequest(t, router, http.MethodPatch, "/v1/me/metadata", map[string]any{
		"metadata": map[string]any{"public": false},
	}, humanHeaders("alice", "alice@a.test"))
	if privateAlice.Code != http.StatusOK {
		t.Fatalf("expected alice metadata patch 200, got %d %s", privateAlice.Code, privateAlice.Body.String())
	}

	const bobImageURL = "https://example.com/humans/bob.png"
	bobMetadata := doJSONRequest(t, router, http.MethodPatch, "/v1/me/metadata", map[string]any{
		"metadata": map[string]any{
			"public":    true,
			"image_url": bobImageURL,
		},
	}, humanHeaders("bob", "bob@b.test"))
	if bobMetadata.Code != http.StatusOK {
		t.Fatalf("expected bob metadata patch 200, got %d %s", bobMetadata.Code, bobMetadata.Body.String())
	}

	publicSnap := doJSONRequest(t, router, http.MethodGet, "/v1/public/snapshot", nil, nil)
	if publicSnap.Code != http.StatusOK {
		t.Fatalf("expected public snapshot 200, got %d %s", publicSnap.Code, publicSnap.Body.String())
	}
	payload := decodeJSONMap(t, publicSnap.Body.Bytes())
	snap, _ := payload["snapshot"].(map[string]any)
	if scope, _ := snap["snapshot_scope"].(string); scope != "public" {
		t.Fatalf("expected snapshot_scope=public, got %q payload=%v", scope, payload)
	}

	orgs, _ := snap["organizations"].([]any)
	if len(orgs) != 1 {
		t.Fatalf("expected 1 public org, got %d payload=%v", len(orgs), payload)
	}
	org, _ := orgs[0].(map[string]any)
	if gotOrgID, _ := org["org_id"].(string); gotOrgID != publicOrgID {
		t.Fatalf("expected public org_id %q, got %q payload=%v", publicOrgID, gotOrgID, payload)
	}
	if gotURI, _ := org["uri"].(string); gotURI == "" {
		t.Fatalf("expected public org uri, payload=%v", org)
	}

	humans, _ := snap["humans"].([]any)
	if len(humans) != 1 {
		t.Fatalf("expected 1 public human, got %d payload=%v", len(humans), payload)
	}
	human, _ := humans[0].(map[string]any)
	if gotHumanID, _ := human["human_id"].(string); gotHumanID != bobHumanID {
		t.Fatalf("expected public human_id %q, got %q payload=%v", bobHumanID, gotHumanID, payload)
	}
	if gotURI, _ := human["uri"].(string); gotURI == "" {
		t.Fatalf("expected public human uri, payload=%v", human)
	}
	humanMetadata, _ := human["metadata"].(map[string]any)
	if gotImageURL, _ := humanMetadata["image_url"].(string); gotImageURL != bobImageURL {
		t.Fatalf("expected public human metadata.image_url %q, got %q payload=%v", bobImageURL, gotImageURL, human)
	}
	if gotImage, _ := humanMetadata["image"].(string); gotImage != bobImageURL {
		t.Fatalf("expected public human metadata.image %q, got %q payload=%v", bobImageURL, gotImage, human)
	}

	memberships, _ := snap["memberships"].([]any)
	if len(memberships) != 1 {
		t.Fatalf("expected 1 public active membership, got %d payload=%v", len(memberships), payload)
	}
	membership, _ := memberships[0].(map[string]any)
	if gotOrgID, _ := membership["org_id"].(string); gotOrgID != publicOrgID {
		t.Fatalf("expected membership org_id %q, got %q payload=%v", publicOrgID, gotOrgID, payload)
	}
	if gotHumanID, _ := membership["human_id"].(string); gotHumanID != bobHumanID {
		t.Fatalf("expected membership human_id %q, got %q payload=%v", bobHumanID, gotHumanID, payload)
	}

	agents, _ := snap["agents"].([]any)
	if len(agents) != 1 {
		t.Fatalf("expected 1 public agent after owner filtering, got %d payload=%v", len(agents), payload)
	}
	agent, _ := agents[0].(map[string]any)
	if gotURI, _ := agent["uri"].(string); gotURI == "" {
		t.Fatalf("expected public agent uri, payload=%v", agent)
	}
	owner, _ := agent["owner"].(map[string]any)
	if gotOwnerID, _ := owner["human_id"].(string); gotOwnerID != bobHumanID {
		t.Fatalf("expected public agent owner.human_id %q, got %q payload=%v", bobHumanID, gotOwnerID, payload)
	}
}

func TestPublicSnapshotIncludesSanitizedAgentActivityLog(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	makeReceiverPublic := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"public": true,
		},
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if makeReceiverPublic.Code != http.StatusOK {
		t.Fatalf("expected receiver public patch 200, got %d %s", makeReceiverPublic.Code, makeReceiverPublic.Body.String())
	}

	pub := publish(t, router, tokenA, agentUUIDB, "public-snapshot-activity")
	if pub.Code != http.StatusAccepted {
		t.Fatalf("publish failed: %d %s", pub.Code, pub.Body.String())
	}

	publicSnap := doJSONRequest(t, router, http.MethodGet, "/v1/public/snapshot", nil, nil)
	if publicSnap.Code != http.StatusOK {
		t.Fatalf("expected public snapshot 200, got %d %s", publicSnap.Code, publicSnap.Body.String())
	}
	payload := decodeJSONMap(t, publicSnap.Body.Bytes())
	snap, _ := payload["snapshot"].(map[string]any)
	agents, _ := snap["agents"].([]any)

	var senderAgent map[string]any
	for _, raw := range agents {
		agent, _ := raw.(map[string]any)
		if agent == nil {
			continue
		}
		handle, _ := agent["handle"].(string)
		if strings.TrimSpace(handle) == "agent-a" {
			senderAgent = agent
			break
		}
	}
	if senderAgent == nil {
		t.Fatalf("expected sender agent-a in public snapshot, got agents=%v", agents)
	}

	log, _ := senderAgent["activity_log"].([]any)
	if len(log) == 0 {
		t.Fatalf("expected public snapshot sender activity_log entries, got sender=%v", senderAgent)
	}
	if !hasActivityText(log, "sent first message to agent-b") && !hasActivityText(log, "sent message to agent-b") {
		t.Fatalf("expected sender public activity_log to include receiver handle when public, got %v", log)
	}
	for _, raw := range log {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		if _, exists := entry["event_id"]; exists {
			t.Fatalf("expected sanitized public activity_log without event_id, got %v", entry)
		}
		if _, exists := entry["subject_id"]; exists {
			t.Fatalf("expected sanitized public activity_log without subject_id, got %v", entry)
		}
		if _, exists := entry["category"]; exists {
			t.Fatalf("expected sanitized public activity_log without category, got %v", entry)
		}
		if _, exists := entry["action"]; exists {
			t.Fatalf("expected sanitized public activity_log without action, got %v", entry)
		}
	}
}

func TestMetadataValidationRejectsNonObjectAndOversizedPayload(t *testing.T) {
	router := newTestRouter()
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	nonObject := doJSONRequest(t, router, http.MethodPatch, "/v1/me/metadata", map[string]any{
		"metadata": []string{"not", "an", "object"},
	}, humanHeaders("alice", "alice@a.test"))
	if nonObject.Code != http.StatusBadRequest {
		t.Fatalf("expected non-object metadata to return 400, got %d %s", nonObject.Code, nonObject.Body.String())
	}

	tooLargeValue := strings.Repeat("x", maxMetadataBytes+1)
	tooLarge := doJSONRequest(t, router, http.MethodPatch, "/v1/me/metadata", map[string]any{
		"metadata": map[string]any{
			"description": tooLargeValue,
		},
	}, humanHeaders("alice", "alice@a.test"))
	if tooLarge.Code != http.StatusBadRequest {
		t.Fatalf("expected oversized metadata to return 400, got %d %s", tooLarge.Code, tooLarge.Body.String())
	}
}

func TestMetadataValidationHonorsConfiguredMaxBytes(t *testing.T) {
	t.Setenv("MOLTENHUB_MAX_METADATA_BYTES", "1024")

	router := newTestRouter()
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	tooLargeValue := strings.Repeat("x", 1025)
	tooLarge := doJSONRequest(t, router, http.MethodPatch, "/v1/me/metadata", map[string]any{
		"metadata": map[string]any{
			"description": tooLargeValue,
		},
	}, humanHeaders("alice", "alice@a.test"))
	if tooLarge.Code != http.StatusBadRequest {
		t.Fatalf("expected configured oversized metadata to return 400, got %d %s", tooLarge.Code, tooLarge.Body.String())
	}
	if !strings.Contains(tooLarge.Body.String(), "metadata exceeds 1024 bytes") {
		t.Fatalf("expected configured limit in error, got %s", tooLarge.Body.String())
	}
}

func TestPruneEmptyJSONObjectFieldsRemovesEmptyStringArrayAndObject(t *testing.T) {
	input := map[string]any{
		"empty_string": "",
		"empty_array":  []any{},
		"empty_obj":    map[string]any{},
		"kept":         "value",
		"nested": map[string]any{
			"drop_me": "",
			"keep_me": "ok",
		},
		"list": []any{
			"",
			map[string]any{},
			[]any{},
			"kept",
			map[string]any{"drop_me": "", "keep_me": "v"},
		},
	}

	prunedAny, err := pruneEmptyJSONObjectFields(input)
	if err != nil {
		t.Fatalf("prune failed: %v", err)
	}
	pruned, ok := prunedAny.(map[string]any)
	if !ok {
		t.Fatalf("expected map after prune, got %T", prunedAny)
	}

	if _, exists := pruned["empty_string"]; exists {
		t.Fatalf("expected empty string field removed, got %v", pruned)
	}
	if _, exists := pruned["empty_array"]; exists {
		t.Fatalf("expected empty array field removed, got %v", pruned)
	}
	if _, exists := pruned["empty_obj"]; exists {
		t.Fatalf("expected empty object field removed, got %v", pruned)
	}
	if got, _ := pruned["kept"].(string); got != "value" {
		t.Fatalf("expected non-empty value preserved, got %v", pruned["kept"])
	}
	nested, _ := pruned["nested"].(map[string]any)
	if _, exists := nested["drop_me"]; exists {
		t.Fatalf("expected nested empty string removed, got %v", nested)
	}
	if got, _ := nested["keep_me"].(string); got != "ok" {
		t.Fatalf("expected nested non-empty value preserved, got %v", nested["keep_me"])
	}
	list, _ := pruned["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("expected list to retain only non-empty entries, got %v", list)
	}
	if got, _ := list[0].(string); got != "kept" {
		t.Fatalf("expected first list entry kept, got %v", list[0])
	}
	obj, _ := list[1].(map[string]any)
	if got, _ := obj["keep_me"].(string); got != "v" {
		t.Fatalf("expected second list object kept with keep_me, got %v", obj)
	}
	if _, exists := obj["drop_me"]; exists {
		t.Fatalf("expected empty value in list object removed, got %v", obj)
	}
}

func TestHumanMetadataPassthrough(t *testing.T) {
	router := newTestRouter()
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	valid := doJSONRequest(t, router, http.MethodPatch, "/v1/me/metadata", map[string]any{
		"metadata": map[string]any{
			"title":   "Builder",
			"status":  "available",
			"emoji":   "🤖",
			"website": "https://example.com/h/alice",
		},
	}, humanHeaders("alice", "alice@a.test"))
	if valid.Code != http.StatusOK {
		t.Fatalf("expected valid human metadata update 200, got %d %s", valid.Code, valid.Body.String())
	}

	passthrough := doJSONRequest(t, router, http.MethodPatch, "/v1/me/metadata", map[string]any{
		"metadata": map[string]any{
			"title":  strings.Repeat("a", 128),
			"status": 7,
			"emoji":  "🙂🙂",
		},
	}, humanHeaders("alice", "alice@a.test"))
	if passthrough.Code != http.StatusOK {
		t.Fatalf("expected moltenhub metadata passthrough 200, got %d %s", passthrough.Code, passthrough.Body.String())
	}

	payload := decodeJSONMap(t, passthrough.Body.Bytes())
	human, _ := payload["human"].(map[string]any)
	metadata, _ := human["metadata"].(map[string]any)
	if metadata["status"] != float64(7) {
		t.Fatalf("expected numeric passthrough status=7, got %v payload=%v", metadata["status"], payload)
	}
}

func TestOrganizationMetadataPassthrough(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Profile Metadata Org")

	valid := doJSONRequest(t, router, http.MethodPatch, "/v1/orgs/"+orgID+"/metadata", map[string]any{
		"metadata": map[string]any{
			"imageurl": "https://example.com/logo.png",
			"location": "Vancouver",
			"website":  "https://example.com/org",
		},
	}, humanHeaders("alice", "alice@a.test"))
	if valid.Code != http.StatusOK {
		t.Fatalf("expected valid org metadata update 200, got %d %s", valid.Code, valid.Body.String())
	}

	passthrough := doJSONRequest(t, router, http.MethodPatch, "/v1/orgs/"+orgID+"/metadata", map[string]any{
		"metadata": map[string]any{
			"location": strings.Repeat("x", 256),
			"imageurl": true,
		},
	}, humanHeaders("alice", "alice@a.test"))
	if passthrough.Code != http.StatusOK {
		t.Fatalf("expected moltenhub metadata passthrough 200, got %d %s", passthrough.Code, passthrough.Body.String())
	}
}

func TestUIRoutes_MainPages(t *testing.T) {
	router := newTestRouter()

	testCases := []struct {
		path        string
		contentHint string
	}{
		{path: "/", contentHint: "Welcome Human"},
		{path: "/index.html", contentHint: "Welcome Human"},
		{path: "/profile", contentHint: "/profile"},
		{path: "/profile/", contentHint: "/profile"},
		{path: "/organization", contentHint: "/organization"},
		{path: "/agents", contentHint: "/agents"},
		{path: "/domains", contentHint: "Domains UI"},
		{path: "/domains/", contentHint: "Domains UI"},
	}

	for _, tc := range testCases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", tc.path, resp.Code, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), tc.contentHint) {
			t.Fatalf("%s response missing %q", tc.path, tc.contentHint)
		}
	}
}

func TestUIRoutes_AgentsPageIncludesSelfSignupMetadataControls(t *testing.T) {
	router := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("/agents expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, "Generate Self-Signup Prompt") || !strings.Contains(body, "Copy Agent Prompt") {
		t.Fatalf("expected self-signup prompt controls in /agents page, got %q", body)
	}
	if !strings.Contains(body, "display_name") || !strings.Contains(body, "emoji") || !strings.Contains(body, "profile_markdown") || !strings.Contains(body, "activities") || !strings.Contains(body, "hire_me") {
		t.Fatalf("expected metadata field hints in /agents page, got %q", body)
	}
	if !strings.Contains(body, "Agent Profile") || !strings.Contains(body, "Disconnect") || !strings.Contains(body, "emoji-picker-element") {
		t.Fatalf("expected agent profile modal controls and emoji picker library in /agents page, got %q", body)
	}
}

func TestUIRoutes_JavascriptAssets(t *testing.T) {
	router := newTestRouter()

	testCases := []string{
		"/login.js",
		"/common.js",
		"/profile.js",
		"/organization.js",
		"/agents.js",
		"/app.js",
		"/domains/app.js",
	}
	for _, path := range testCases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", path, resp.Code, resp.Body.String())
		}
		contentType := resp.Header().Get("Content-Type")
		if !strings.Contains(contentType, "application/javascript") {
			t.Fatalf("%s expected javascript content type, got %q", path, contentType)
		}
	}
}

func TestHeadlessModeDisablesUIRoutes(t *testing.T) {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, true)
	router := NewRouter(h)

	me := doJSONRequest(t, router, http.MethodGet, "/v1/me", nil, humanHeaders("alice", "alice@a.test"))
	if me.Code != http.StatusOK {
		t.Fatalf("expected /v1/me to work in headless mode, got %d %s", me.Code, me.Body.String())
	}

	profile := doJSONRequest(t, router, http.MethodGet, "/profile", nil, nil)
	if profile.Code != http.StatusNotFound {
		t.Fatalf("expected /profile to be 404 in headless mode, got %d %s", profile.Code, profile.Body.String())
	}

	live := doJSONRequest(t, router, http.MethodGet, "/live", nil, nil)
	if live.Code != http.StatusNotFound {
		t.Fatalf("expected /live to be 404 in headless mode, got %d %s", live.Code, live.Body.String())
	}
}

func TestHeadlessModeRedirectsUIRoutesWhenConfigured(t *testing.T) {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, true)
	h.SetHeadlessModeRedirectURL("https://example.com/headless")
	router := NewRouter(h)

	me := doJSONRequest(t, router, http.MethodGet, "/v1/me", nil, humanHeaders("alice", "alice@a.test"))
	if me.Code != http.StatusOK {
		t.Fatalf("expected /v1/me to work in headless mode, got %d %s", me.Code, me.Body.String())
	}

	for _, path := range []string{"/", "/profile", "/live"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusFound {
			t.Fatalf("%s expected 302 in headless mode redirect, got %d body=%s", path, resp.Code, resp.Body.String())
		}
		if location := resp.Header().Get("Location"); location != "https://example.com/headless" {
			t.Fatalf("%s expected redirect location https://example.com/headless, got %q", path, location)
		}
	}

	api404 := doJSONRequest(t, router, http.MethodGet, "/v1/unknown", nil, nil)
	if api404.Code != http.StatusNotFound {
		t.Fatalf("expected /v1/unknown to remain 404, got %d %s", api404.Code, api404.Body.String())
	}
}

func TestHeadlessModeKeepsPingAvailableWhenRedirectConfigured(t *testing.T) {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, true)
	h.SetHeadlessModeRedirectURL("https://example.com/headless")
	router := NewRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("expected /ping to remain 204 in headless redirect mode, got %d body=%s", resp.Code, resp.Body.String())
	}
	if location := resp.Header().Get("Location"); location != "" {
		t.Fatalf("expected /ping to avoid redirects, got location %q", location)
	}
}

func TestHeadlessModeServesRobotsAndHumansWhenRedirectConfigured(t *testing.T) {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, true)
	h.SetHeadlessModeRedirectURL("https://example.com/headless")
	router := NewRouter(h)

	tests := []struct {
		path string
		body string
	}{
		{path: "/robots.txt", body: "User-agent: *\nDisallow: /\n"},
		{path: "/humans.txt", body: "https://github.com/jefking\n"},
	}

	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", tc.path, resp.Code, resp.Body.String())
		}
		if location := resp.Header().Get("Location"); location != "" {
			t.Fatalf("%s expected no redirect, got location %q", tc.path, location)
		}
		if resp.Body.String() != tc.body {
			t.Fatalf("%s expected body %q, got %q", tc.path, tc.body, resp.Body.String())
		}
	}
}

func TestOnboardingBlocksWritesUntilHandleConfirmed(t *testing.T) {
	router := newTestRouter()

	createBefore := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{
		"handle":       "alpha",
		"display_name": "Alpha",
	}, humanHeaders("alice", "alice@a.test"))
	if createBefore.Code != http.StatusConflict {
		t.Fatalf("expected onboarding block 409 before handle confirmation, got %d %s", createBefore.Code, createBefore.Body.String())
	}
	beforePayload := decodeJSONMap(t, createBefore.Body.Bytes())
	if beforePayload["error"] != "onboarding_required" {
		t.Fatalf("expected onboarding_required error, got %v", beforePayload["error"])
	}

	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	createAfter := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{
		"handle":       "alpha",
		"display_name": "Alpha",
	}, humanHeaders("alice", "alice@a.test"))
	if createAfter.Code != http.StatusCreated {
		t.Fatalf("expected org create success after handle confirmation, got %d %s", createAfter.Code, createAfter.Body.String())
	}
}

func TestAgentLimitAndSuperAdminBypass(t *testing.T) {
	router := newTestRouter()
	ensureHandleConfirmed(t, router, "alice", "alice@a.test")
	ensureHandleConfirmed(t, router, "root", "root@example.com")

	_, _, _ = registerMyAgent(t, router, "alice", "alice@a.test", "", "alice-agent-1")
	_, _, _ = registerMyAgent(t, router, "alice", "alice@a.test", "", "alice-agent-2")
	third := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/bind-tokens", map[string]any{
		"org_id": "",
	}, humanHeaders("alice", "alice@a.test"))
	if third.Code != http.StatusConflict {
		t.Fatalf("expected non-super-admin third agent to fail with 409, got %d %s", third.Code, third.Body.String())
	}
	thirdPayload := decodeJSONMap(t, third.Body.Bytes())
	if thirdPayload["error"] != "agent_limit_reached" {
		t.Fatalf("expected agent_limit_reached error, got %v", thirdPayload["error"])
	}

	rootOrg := createOrg(t, router, "root", "root@example.com", "Root Ops")
	_, _, _ = registerMyAgent(t, router, "root", "root@example.com", rootOrg, "root-agent-1")
	_, _, _ = registerMyAgent(t, router, "root", "root@example.com", rootOrg, "root-agent-2")
	_, _, _ = registerMyAgent(t, router, "root", "root@example.com", rootOrg, "root-agent-3")
}

func TestErrorPayloadIncludesRequestCorrelationMetadata(t *testing.T) {
	router := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/agents/me", nil)
	req.Header.Set("X-Request-ID", "agent-runtime-test-id")
	resp := httptest.NewRecorder()

	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized response, got %d %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("X-Request-ID"); got != "agent-runtime-test-id" {
		t.Fatalf("expected response request id echo, got %q", got)
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	if payload["request_id"] != "agent-runtime-test-id" {
		t.Fatalf("expected request_id in error payload, got %v", payload["request_id"])
	}
	detail, ok := payload["error_detail"].(map[string]any)
	if !ok {
		t.Fatalf("expected error_detail object, got %v", payload["error_detail"])
	}
	if detail["code"] != "unauthorized" {
		t.Fatalf("expected detail.code=unauthorized, got %v", detail["code"])
	}
	if detail["request_id"] != "agent-runtime-test-id" {
		t.Fatalf("expected detail.request_id echo, got %v", detail["request_id"])
	}
}

func TestAgentMutatingRoutesRejectUnsupportedMediaType(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, agentUUIDB, _, _, _, _ := setupTrustedAgents(t, router)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		token  string
	}{
		{
			name:   "bind",
			method: http.MethodPost,
			path:   "/v1/agents/bind",
			body:   `{"bind_token":"x"}`,
		},
		{
			name:   "agent profile patch",
			method: http.MethodPatch,
			path:   "/v1/agents/me",
			body:   `{"metadata":{"public":true}}`,
			token:  tokenA,
		},
		{
			name:   "agent metadata patch",
			method: http.MethodPatch,
			path:   "/v1/agents/me/metadata",
			body:   `{"metadata":{"public":true}}`,
			token:  tokenA,
		},
		{
			name:   "publish",
			method: http.MethodPost,
			path:   "/v1/messages/publish",
			body:   fmt.Sprintf(`{"to_agent_uuid":"%s","content_type":"text/plain","payload":"hello"}`, agentUUIDB),
			token:  tokenA,
		},
		{
			name:   "ack",
			method: http.MethodPost,
			path:   "/v1/messages/ack",
			body:   `{"delivery_id":"delivery-1"}`,
		},
		{
			name:   "nack",
			method: http.MethodPost,
			path:   "/v1/messages/nack",
			body:   `{"delivery_id":"delivery-1"}`,
		},
	}

	for _, tc := range tests {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "text/plain")
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%s: expected 415 unsupported media type, got %d %s", tc.name, resp.Code, resp.Body.String())
		}
		payload := decodeJSONMap(t, resp.Body.Bytes())
		if payload["error"] != "unsupported_media_type" {
			t.Fatalf("%s: expected unsupported_media_type error, got %v", tc.name, payload["error"])
		}
	}
}

func TestAgentDiscoveryRoutesRejectUnsupportedAcceptHeaders(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)

	tests := []struct {
		name string
		path string
	}{
		{name: "manifest", path: "/v1/agents/me/manifest"},
		{name: "skill", path: "/v1/agents/me/skill"},
		{name: "capabilities", path: "/v1/agents/me/capabilities"},
	}

	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Accept", "application/xml")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusNotAcceptable {
			t.Fatalf("%s: expected 406 not acceptable, got %d %s", tc.name, resp.Code, resp.Body.String())
		}
		payload := decodeJSONMap(t, resp.Body.Bytes())
		if payload["error"] != "not_acceptable" {
			t.Fatalf("%s: expected not_acceptable error code, got %v", tc.name, payload["error"])
		}
	}
}

func TestAgentRuntimeUnauthorizedErrorEnvelopeContract(t *testing.T) {
	router := newTestRouter()

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{name: "me", method: http.MethodGet, path: "/v1/agents/me"},
		{name: "manifest", method: http.MethodGet, path: "/v1/agents/me/manifest"},
		{name: "capabilities", method: http.MethodGet, path: "/v1/agents/me/capabilities"},
		{name: "skill", method: http.MethodGet, path: "/v1/agents/me/skill"},
		{name: "profile-patch", method: http.MethodPatch, path: "/v1/agents/me", body: map[string]any{"metadata": map[string]any{"public": true}}},
		{name: "metadata-patch", method: http.MethodPatch, path: "/v1/agents/me/metadata", body: map[string]any{"metadata": map[string]any{"public": true}}},
		{name: "publish", method: http.MethodPost, path: "/v1/messages/publish", body: map[string]any{"to_agent_uuid": "11111111-1111-1111-1111-111111111111", "content_type": "text/plain", "payload": "hello"}},
		{name: "pull", method: http.MethodGet, path: "/v1/messages/pull?timeout_ms=0"},
		{name: "ack", method: http.MethodPost, path: "/v1/messages/ack", body: map[string]any{"delivery_id": "delivery-1"}},
		{name: "nack", method: http.MethodPost, path: "/v1/messages/nack", body: map[string]any{"delivery_id": "delivery-1"}},
		{name: "status", method: http.MethodGet, path: "/v1/messages/message-1"},
	}

	for _, tc := range tests {
		resp := doJSONRequest(t, router, tc.method, tc.path, tc.body, nil)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401 unauthorized, got %d %s", tc.name, resp.Code, resp.Body.String())
		}
		requestID := strings.TrimSpace(resp.Header().Get("X-Request-ID"))
		if requestID == "" {
			t.Fatalf("%s: expected X-Request-ID header", tc.name)
		}
		payload := decodeJSONMap(t, resp.Body.Bytes())
		if payload["error"] != "unauthorized" {
			t.Fatalf("%s: expected unauthorized error code, got %v", tc.name, payload["error"])
		}
		if payload["request_id"] != requestID {
			t.Fatalf("%s: expected request_id to match X-Request-ID header, got request_id=%v header=%s", tc.name, payload["request_id"], requestID)
		}
		if payload["retryable"] != false {
			t.Fatalf("%s: expected retryable=false for unauthorized, got %v", tc.name, payload["retryable"])
		}
		nextAction, _ := payload["next_action"].(string)
		if strings.TrimSpace(nextAction) == "" {
			t.Fatalf("%s: expected next_action guidance", tc.name)
		}

		detail, ok := payload["error_detail"].(map[string]any)
		if !ok {
			t.Fatalf("%s: expected error_detail object, got %v", tc.name, payload["error_detail"])
		}
		if detail["code"] != "unauthorized" {
			t.Fatalf("%s: expected error_detail.code unauthorized, got %v", tc.name, detail["code"])
		}
		if detail["request_id"] != requestID {
			t.Fatalf("%s: expected error_detail.request_id to match header, got %v", tc.name, detail["request_id"])
		}
		if detail["retryable"] != false {
			t.Fatalf("%s: expected error_detail.retryable=false, got %v", tc.name, detail["retryable"])
		}
	}
}

func TestAgentRuntimeSuccessEnvelopeContract(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	meResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if meResp.Code != http.StatusOK {
		t.Fatalf("expected /v1/agents/me 200, got %d %s", meResp.Code, meResp.Body.String())
	}
	meResult := requireAgentRuntimeSuccessEnvelope(t, decodeJSONMap(t, meResp.Body.Bytes()))
	if _, ok := meResult["agent"].(map[string]any); !ok {
		t.Fatalf("expected result.agent in /v1/agents/me response, got %v", meResult["agent"])
	}

	metadataResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{"public": true},
	}, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if metadataResp.Code != http.StatusOK {
		t.Fatalf("expected /v1/agents/me/metadata 200, got %d %s", metadataResp.Code, metadataResp.Body.String())
	}
	metadataResult := requireAgentRuntimeSuccessEnvelope(t, decodeJSONMap(t, metadataResp.Body.Bytes()))
	metadataAgent, _ := metadataResult["agent"].(map[string]any)
	agentMetadata, _ := metadataAgent["metadata"].(map[string]any)
	if got, ok := agentMetadata["public"].(bool); !ok || !got {
		t.Fatalf("expected metadata update reflected in result.agent.metadata.public, got %v", agentMetadata["public"])
	}

	for _, path := range []string{
		"/v1/agents/me/capabilities",
		"/v1/agents/me/manifest",
		"/v1/agents/me/skill",
	} {
		resp := doJSONRequest(t, router, http.MethodGet, path, nil, map[string]string{
			"Authorization": "Bearer " + tokenA,
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("expected %s 200, got %d %s", path, resp.Code, resp.Body.String())
		}
		requireAgentRuntimeSuccessEnvelope(t, decodeJSONMap(t, resp.Body.Bytes()))
	}

	publishResp := publish(t, router, tokenA, agentUUIDB, "envelope-test")
	if publishResp.Code != http.StatusAccepted {
		t.Fatalf("expected publish 202, got %d %s", publishResp.Code, publishResp.Body.String())
	}
	publishPayload := decodeJSONMap(t, publishResp.Body.Bytes())
	publishResult := requireAgentRuntimeSuccessEnvelope(t, publishPayload)
	messageID, _ := publishResult["message_id"].(string)
	if strings.TrimSpace(messageID) == "" {
		t.Fatalf("expected publish result.message_id, got %v", publishResult["message_id"])
	}
	if publishPayload["status"] != publishResult["status"] {
		t.Fatalf("expected compatibility top-level status mirror, got top=%v result=%v", publishPayload["status"], publishResult["status"])
	}

	pullResp := pull(t, router, tokenB, 0)
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullResult := requireAgentRuntimeSuccessEnvelope(t, decodeJSONMap(t, pullResp.Body.Bytes()))
	delivery, _ := pullResult["delivery"].(map[string]any)
	deliveryID, _ := delivery["delivery_id"].(string)
	if strings.TrimSpace(deliveryID) == "" {
		t.Fatalf("expected delivery_id in pull result, got %v", pullResult["delivery"])
	}

	ackResp := ackDelivery(t, router, tokenB, deliveryID)
	if ackResp.Code != http.StatusOK {
		t.Fatalf("expected ack 200, got %d %s", ackResp.Code, ackResp.Body.String())
	}
	ackResult := requireAgentRuntimeSuccessEnvelope(t, decodeJSONMap(t, ackResp.Body.Bytes()))
	if got, _ := ackResult["status"].(string); got != model.MessageDeliveryAcked {
		t.Fatalf("expected ack result.status=%s, got %q", model.MessageDeliveryAcked, got)
	}

	statusResp := messageStatus(t, router, tokenA, messageID)
	if statusResp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d %s", statusResp.Code, statusResp.Body.String())
	}
	statusResult := requireAgentRuntimeSuccessEnvelope(t, decodeJSONMap(t, statusResp.Body.Bytes()))
	messageObj, _ := statusResult["message"].(map[string]any)
	if got, _ := messageObj["message_id"].(string); got != messageID {
		t.Fatalf("expected status result message_id=%q, got %q", messageID, got)
	}
}

func TestAgentRuntimeRouteSpecificErrorHints(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)

	tests := []struct {
		name          string
		method        string
		path          string
		body          any
		wantStatus    int
		wantErrorCode string
	}{
		{
			name:          "invalid receiver uuid",
			method:        http.MethodPost,
			path:          "/v1/messages/publish",
			body:          map[string]any{"to_agent_uuid": "bad", "content_type": "text/plain", "payload": "hello"},
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: "invalid_to_agent_uuid",
		},
		{
			name:          "missing delivery id",
			method:        http.MethodPost,
			path:          "/v1/messages/ack",
			body:          map[string]any{"delivery_id": ""},
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: "invalid_delivery_id",
		},
		{
			name:          "unknown delivery",
			method:        http.MethodPost,
			path:          "/v1/messages/ack",
			body:          map[string]any{"delivery_id": "delivery-does-not-exist"},
			wantStatus:    http.StatusNotFound,
			wantErrorCode: "unknown_delivery",
		},
		{
			name:          "unknown message",
			method:        http.MethodGet,
			path:          "/v1/messages/does-not-exist",
			wantStatus:    http.StatusNotFound,
			wantErrorCode: "unknown_message",
		},
	}

	for _, tc := range tests {
		resp := doJSONRequest(t, router, tc.method, tc.path, tc.body, map[string]string{
			"Authorization": "Bearer " + tokenA,
		})
		if resp.Code != tc.wantStatus {
			t.Fatalf("%s: expected status %d, got %d %s", tc.name, tc.wantStatus, resp.Code, resp.Body.String())
		}
		payload := decodeJSONMap(t, resp.Body.Bytes())
		if payload["error"] != tc.wantErrorCode {
			t.Fatalf("%s: expected error code %q, got %v payload=%v", tc.name, tc.wantErrorCode, payload["error"], payload)
		}
		nextAction, _ := payload["next_action"].(string)
		if strings.TrimSpace(nextAction) == "" {
			t.Fatalf("%s: expected next_action hint, got %v", tc.name, payload["next_action"])
		}
		if _, ok := payload["retryable"].(bool); !ok {
			t.Fatalf("%s: expected retryable bool hint, got %v", tc.name, payload["retryable"])
		}
		detail, _ := payload["error_detail"].(map[string]any)
		if detail == nil {
			t.Fatalf("%s: expected error_detail object, got %v", tc.name, payload["error_detail"])
		}
		if detail["code"] != tc.wantErrorCode {
			t.Fatalf("%s: expected error_detail.code=%q, got %v", tc.name, tc.wantErrorCode, detail["code"])
		}
	}
}
