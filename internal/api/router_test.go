package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"statocyst/internal/auth"
	"statocyst/internal/longpoll"
	"statocyst/internal/model"
	"statocyst/internal/store"
)

func newTestRouter() http.Handler {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, false)
	return NewRouter(h)
}

func TestHandlerWiringWithInterfaceStores(t *testing.T) {
	mem := store.NewMemoryStore()
	var control store.ControlPlaneStore = mem
	var queue store.MessageQueueStore = mem
	waiters := longpoll.NewWaiters()
	h := NewHandler(control, queue, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, false)
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
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, false)
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

func TestParseCORSAllowedOrigins(t *testing.T) {
	origins, err := ParseCORSAllowedOrigins(" https://app.molten.bot,https://app.molten-qa.site/\nhttp://localhost:3000 ")
	if err != nil {
		t.Fatalf("ParseCORSAllowedOrigins returned error: %v", err)
	}

	for _, origin := range []string{
		"https://app.molten.bot",
		"https://app.molten-qa.site",
		"http://localhost:3000",
	} {
		if _, ok := origins[origin]; !ok {
			t.Fatalf("expected origin %q in parsed set, got %v", origin, origins)
		}
	}
}

func TestAPICORSAllowsExplicitOrigin(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, false)
	router := NewRouterWithOptions(h, RouterOptions{
		AllowedCORSOrigins: map[string]struct{}{
			"https://app.molten.bot": {},
		},
	})

	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "https://app.molten.bot")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("expected OPTIONS /health 204, got %d %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "https://app.molten.bot" {
		t.Fatalf("expected explicit CORS origin to be allowed, got %q", got)
	}
}

func TestAPICORSRejectsUnknownOrigin(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, false)
	router := NewRouterWithOptions(h, RouterOptions{
		AllowedCORSOrigins: map[string]struct{}{
			"https://app.molten.bot": {},
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

type failOnceQueue struct {
	base            store.MessageQueueStore
	mu              sync.Mutex
	failNextEnqueue bool
	failNextDequeue bool
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

func TestHealthReportsRuntimeQueueFailureAndRecovery(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	queue := &failOnceQueue{
		base:            mem,
		failNextEnqueue: true,
	}
	h := NewHandler(mem, queue, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, false)
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
	if !strings.Contains(queueErr, "enqueue unavailable") {
		t.Fatalf("expected runtime queue error to include enqueue failure, got %q payload=%v", queueErr, payload)
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
}

func TestHealthReportsRuntimeDequeueFailureAndRecovery(t *testing.T) {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	queue := &failOnceQueue{
		base:            mem,
		failNextDequeue: true,
	}
	h := NewHandler(mem, queue, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, false)
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
	if !strings.Contains(queueErr, "dequeue unavailable") {
		t.Fatalf("expected runtime queue error to include dequeue failure, got %q payload=%v", queueErr, payload)
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
		"https://hub.molten.bot",
		"https://example.supabase.co",
		"should-not-leak",
		"",
		"admin1@molten.bot,admin2@molten.bot",
		"molten.bot",
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
	if !ok || len(domains) != 1 || domains[0] != "molten.bot" {
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
		"https://hub.molten.bot",
		"https://example.supabase.co",
		"should-leak-only-to-privileged-caller",
		"",
		"admin1@molten.bot,admin2@molten.bot",
		"molten.bot",
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
	if len(adminEmails) != 2 || adminEmails[0] != "admin1@molten.bot" || adminEmails[1] != "admin2@molten.bot" {
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
		"https://hub.molten.bot",
		"https://example.supabase.co",
		"should-not-leak",
		"",
		"admin1@molten.bot,admin2@molten.bot",
		"molten.bot",
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
	expectedURI := "https://hub.molten.bot/" + strings.ReplaceAll(url.PathEscape(gotAgentID), "%2F", "/")
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
		"hub_url":    "https://hub.molten-qa.site",
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
	if initialURI != "https://hub.molten.bot/human/alice/agent/alice-agent-picked-name" {
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
	suggestedHandles, _ := redeemPayload["suggested_handles"].([]any)
	if len(suggestedHandles) == 0 {
		t.Fatalf("expected suggested_handles in duplicate bind response, got %v", redeemPayload["suggested_handles"])
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
		t.Fatalf("expected connect prompt to explain duplicate retry permutations, got %q", connectPrompt)
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
	req.Header.Set("X-Forwarded-Host", "hub.molten-qa.site")
	req.Header.Set("X-Forwarded-Proto", "https")

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create my bind token failed: %d %s", resp.Code, resp.Body.String())
	}

	connectPrompt, _ := decodeJSONMap(t, resp.Body.Bytes())["connect_prompt"].(string)
	if !strings.Contains(connectPrompt, "https://hub.molten-qa.site/v1/agents/bind") {
		t.Fatalf("expected forwarded bind api url in connect prompt, got %q", connectPrompt)
	}
	if !strings.Contains(connectPrompt, "https://hub.molten-qa.site/v1/agents/me/skill") {
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
	if !strings.Contains(connectPrompt, "https://hub.molten.bot/v1/agents/bind") {
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
	req.Header.Set("X-Forwarded-Host", "hub.molten-qa.site")
	req.Header.Set("X-Forwarded-Proto", "https")

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("redeem bind token failed: %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	apiBase, _ := payload["api_base"].(string)
	if apiBase != "https://hub.molten-qa.site/v1" {
		t.Fatalf("expected forwarded api_base, got %q", apiBase)
	}
	endpoints, _ := payload["endpoints"].(map[string]any)
	if got, _ := endpoints["skill"].(string); got != "https://hub.molten-qa.site/v1/agents/me/skill" {
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
	if agentOwnerDelete.Code != http.StatusOK {
		t.Fatalf("expected agent owner delete to succeed, got %d %s", agentOwnerDelete.Code, agentOwnerDelete.Body.String())
	}
	if decodeJSONMap(t, agentOwnerDelete.Body.Bytes())["result"] != "deleted" {
		t.Fatalf("expected agent owner delete response to report deleted")
	}

	orgOwnerDeleteHumanOwned := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+bobOrgManagedAgentUUID+"/record", nil, humanHeaders("alice", "alice@a.test"))
	if orgOwnerDeleteHumanOwned.Code != http.StatusOK {
		t.Fatalf("expected org owner delete of human-owned org agent to succeed, got %d %s", orgOwnerDeleteHumanOwned.Code, orgOwnerDeleteHumanOwned.Body.String())
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
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, false)
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

	redeemResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]string{
		"hub_url":    "https://hub.molten-qa.site",
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
		"hub_url":    "https://hub.molten-qa.site",
		"bind_token": bindToken,
	}, nil)
	if redeemAgain.Code != http.StatusConflict {
		t.Fatalf("expected second redeem to fail with 409, got %d %s", redeemAgain.Code, redeemAgain.Body.String())
	}
}

func TestSuperAdminReadOnly(t *testing.T) {
	router := newTestRouter()
	_ = createOrg(t, router, "alice", "alice@a.test", "Org A")
	ensureHandleConfirmed(t, router, "root", "root@molten.bot")

	readonlyCreate := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{
		"handle":       "root-org",
		"display_name": "Root Org",
	}, humanHeaders("root", "root@molten.bot"))
	if readonlyCreate.Code != http.StatusCreated {
		t.Fatalf("expected super admin write allow 201, got %d %s", readonlyCreate.Code, readonlyCreate.Body.String())
	}

	snap := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@molten.bot"))
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
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, false)
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
		"hub_url":    "https://hub.molten-qa.site",
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
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)

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
	if !strings.Contains(skillContent, "SKILL: Statocyst Agent Control Plane") {
		t.Fatalf("expected skill header, got %q", skillContent)
	}
	if !strings.Contains(skillContent, "Onboarding Checklist") {
		t.Fatalf("expected onboarding checklist in skill, got %q", skillContent)
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

	humanRouteResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/"+agentUUIDB+"/metadata", map[string]any{
		"metadata": map[string]any{"public": false},
	}, headers)
	if humanRouteResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected agent token on human control-plane route to return 401, got %d %s", humanRouteResp.Code, humanRouteResp.Body.String())
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
}

func TestAdminSnapshotDoesNotLeakMessagePayloads(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, agentUUIDB := setupTrustedAgents(t, router)
	secretPayload := "top-secret-payload-should-not-appear"

	pub := publish(t, router, tokenA, agentUUIDB, secretPayload)
	if pub.Code != http.StatusAccepted {
		t.Fatalf("publish failed: %d %s", pub.Code, pub.Body.String())
	}

	snap := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@molten.bot"))
	if snap.Code != http.StatusOK {
		t.Fatalf("snapshot failed: %d %s", snap.Code, snap.Body.String())
	}
	bodyText := snap.Body.String()
	if strings.Contains(bodyText, secretPayload) || strings.Contains(bodyText, "\"payload\"") {
		t.Fatalf("snapshot should not include message payload data: %s", bodyText)
	}
}

func TestAdminSnapshotHeaderKeyAccess(t *testing.T) {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "snapshot-secret", "", "molten.bot", true, 15*time.Minute, false)
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

	superAdmin := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@molten.bot"))
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
	t.Setenv("STATOCYST_MAX_METADATA_BYTES", "1024")

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
		t.Fatalf("expected statocyst metadata passthrough 200, got %d %s", passthrough.Code, passthrough.Body.String())
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
		t.Fatalf("expected statocyst metadata passthrough 200, got %d %s", passthrough.Code, passthrough.Body.String())
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
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, true)
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
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.molten.bot", "", "", "", "", "molten.bot", true, 15*time.Minute, true)
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
	ensureHandleConfirmed(t, router, "root", "root@molten.bot")

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

	rootOrg := createOrg(t, router, "root", "root@molten.bot", "Root Ops")
	_, _, _ = registerMyAgent(t, router, "root", "root@molten.bot", rootOrg, "root-agent-1")
	_, _, _ = registerMyAgent(t, router, "root", "root@molten.bot", rootOrg, "root-agent-2")
	_, _, _ = registerMyAgent(t, router, "root", "root@molten.bot", rootOrg, "root-agent-3")
}
