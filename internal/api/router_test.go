package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"statocyst/internal/auth"
	"statocyst/internal/longpoll"
	"statocyst/internal/store"
)

func newTestRouter() http.Handler {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, waiters, auth.NewDevHumanAuthProvider(), "", "", "", "molten.bot", true, 15*time.Minute)
	return NewRouter(h)
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

func createOrg(t *testing.T, router http.Handler, humanID, email, name string) string {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{"name": name}, humanHeaders(humanID, email))
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
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/orgs/"+orgID+"/invites", map[string]string{
		"email": inviteeEmail,
		"role":  role,
	}, humanHeaders(humanID, email))
	if resp.Code != http.StatusCreated {
		t.Fatalf("create invite failed: %d %s", resp.Code, resp.Body.String())
	}
	var payload map[string]map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode invite: %v", err)
	}
	inviteID, _ := payload["invite"]["invite_id"].(string)
	if inviteID == "" {
		t.Fatalf("missing invite_id")
	}
	return inviteID
}

func acceptInvite(t *testing.T, router http.Handler, humanID, email, inviteID string) {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/org-invites/"+inviteID+"/accept", nil, humanHeaders(humanID, email))
	if resp.Code != http.StatusOK {
		t.Fatalf("accept invite failed: %d %s", resp.Code, resp.Body.String())
	}
}

func registerAgent(t *testing.T, router http.Handler, humanID, email, orgID, agentID, ownerHumanID string) string {
	t.Helper()
	body := map[string]any{
		"org_id":   orgID,
		"agent_id": agentID,
	}
	if ownerHumanID != "" {
		body["owner_human_id"] = ownerHumanID
	}
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/register", body, humanHeaders(humanID, email))
	if resp.Code != http.StatusCreated {
		t.Fatalf("register agent failed: %d %s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode register agent: %v", err)
	}
	token, _ := payload["token"].(string)
	if token == "" {
		t.Fatalf("missing token")
	}
	return token
}

func publish(t *testing.T, router http.Handler, senderToken, toAgentID, payload string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSONRequest(t, router, http.MethodPost, "/v1/messages/publish", map[string]string{
		"to_agent_id":  toAgentID,
		"content_type": "text/plain",
		"payload":      payload,
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

func decodeJSONMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode json map: %v body=%s", err, string(body))
	}
	return m
}

func setupTrustedAgents(t *testing.T, router http.Handler) (string, string, string, string, string, string) {
	t.Helper()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Org A")
	orgB := createOrg(t, router, "bob", "bob@b.test", "Org B")

	tokenA := registerAgent(t, router, "alice", "alice@a.test", orgA, "agent-a", aliceHumanID)
	tokenB := registerAgent(t, router, "bob", "bob@b.test", orgB, "agent-b", bobHumanID)

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
		"org_id":        orgA,
		"agent_id":      "agent-a",
		"peer_agent_id": "agent-b",
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

	return orgA, orgB, tokenA, tokenB, orgTrustID, agentTrustID
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

func TestAgentRegisterHumanAndOrgOwned(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Org A")
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	registerAgent(t, router, "alice", "alice@a.test", orgID, "a-human", aliceHumanID)
	registerAgent(t, router, "alice", "alice@a.test", orgID, "a-org", "")
}

func TestTrustLifecycleAndBlockPrecedence(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, orgTrustID, _ := setupTrustedAgents(t, router)

	resp := publish(t, router, tokenA, "agent-b", "hello-before-block")
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

	resp = publish(t, router, tokenA, "agent-b", "hello-after-block")
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
	tokenA := registerAgent(t, router, "alice", "alice@a.test", orgA, "agent-a", aliceHumanID)
	tokenB := registerAgent(t, router, "bob", "bob@b.test", orgB, "agent-b", bobHumanID)

	resp := publish(t, router, tokenA, "agent-b", "no-trust")
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
		"org_id":        orgA,
		"agent_id":      "agent-a",
		"peer_agent_id": "agent-b",
	}, humanHeaders("alice", "alice@a.test"))
	agentTrust := decodeJSONMap(t, agentTrustResp.Body.Bytes())
	agentTrustID := agentTrust["trust"].(map[string]any)["edge_id"].(string)
	doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts/"+agentTrustID+"/approve", nil, humanHeaders("bob", "bob@b.test"))

	resp = publish(t, router, tokenA, "agent-b", "has-trust")
	withTrust := decodeJSONMap(t, resp.Body.Bytes())
	if withTrust["status"] != "queued" {
		t.Fatalf("expected queued, got %v", withTrust["status"])
	}

	pullResp := pull(t, router, tokenB, 50)
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected pull 200, got %d", pullResp.Code)
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
	_, _, tokenA, tokenB, _, _ := setupTrustedAgents(t, router)
	const total = 30

	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := publish(t, router, tokenA, "agent-b", fmt.Sprintf("msg-%d", i))
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

	redeemResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind/redeem", map[string]string{
		"bind_token": bindToken,
		"agent_id":   "bound-agent",
	}, nil)
	if redeemResp.Code != http.StatusCreated {
		t.Fatalf("redeem bind token failed: %d %s", redeemResp.Code, redeemResp.Body.String())
	}
	redeemPayload := decodeJSONMap(t, redeemResp.Body.Bytes())
	if redeemPayload["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", redeemPayload["status"])
	}

	redeemAgain := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind/redeem", map[string]string{
		"bind_token": bindToken,
		"agent_id":   "bound-agent-2",
	}, nil)
	if redeemAgain.Code != http.StatusConflict {
		t.Fatalf("expected second redeem to fail with 409, got %d %s", redeemAgain.Code, redeemAgain.Body.String())
	}
}

func TestSuperAdminReadOnly(t *testing.T) {
	router := newTestRouter()
	_ = createOrg(t, router, "alice", "alice@a.test", "Org A")

	readonlyCreate := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{"name": "ShouldFail"}, humanHeaders("root", "root@molten.bot"))
	if readonlyCreate.Code != http.StatusForbidden {
		t.Fatalf("expected super admin write deny 403, got %d %s", readonlyCreate.Code, readonlyCreate.Body.String())
	}

	snap := doJSONRequest(t, router, http.MethodGet, "/v1/admin/snapshot", nil, humanHeaders("root", "root@molten.bot"))
	if snap.Code != http.StatusOK {
		t.Fatalf("expected super admin snapshot 200, got %d %s", snap.Code, snap.Body.String())
	}
}

func TestOrganizationNameUniqueCaseInsensitive(t *testing.T) {
	router := newTestRouter()
	_ = createOrg(t, router, "alice", "alice@a.test", "Acme")

	dup := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]string{
		"name": "  acME  ",
	}, humanHeaders("bob", "bob@b.test"))
	if dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate org name, got %d %s", dup.Code, dup.Body.String())
	}
	body := decodeJSONMap(t, dup.Body.Bytes())
	if body["error"] != "org_name_exists" {
		t.Fatalf("expected org_name_exists error, got %v", body["error"])
	}
}

func TestBindTokenExpires(t *testing.T) {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, waiters, auth.NewDevHumanAuthProvider(), "", "", "", "molten.bot", true, 15*time.Minute)
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
	redeemResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind/redeem", map[string]string{
		"bind_token": bindToken,
		"agent_id":   "expired-agent",
	}, nil)
	if redeemResp.Code != http.StatusBadRequest {
		t.Fatalf("expected expired bind token 400, got %d %s", redeemResp.Code, redeemResp.Body.String())
	}
	redeemPayload := decodeJSONMap(t, redeemResp.Body.Bytes())
	if redeemPayload["error"] != "bind_expired" {
		t.Fatalf("expected bind_expired, got %v", redeemPayload["error"])
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
	if !strings.HasPrefix(contentType, "application/yaml") {
		t.Fatalf("expected application/yaml content type, got %q", contentType)
	}
	disposition := resp.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "inline") {
		t.Fatalf("expected inline content disposition, got %q", disposition)
	}
}

func TestAdminSnapshotDoesNotLeakMessagePayloads(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _ := setupTrustedAgents(t, router)
	secretPayload := "top-secret-payload-should-not-appear"

	pub := publish(t, router, tokenA, "agent-b", secretPayload)
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

func TestUIRoutes_MainPages(t *testing.T) {
	router := newTestRouter()

	testCases := []struct {
		path        string
		contentHint string
	}{
		{path: "/", contentHint: "Statocyst Human Login"},
		{path: "/index.html", contentHint: "Statocyst Human Login"},
		{path: "/profile", contentHint: "/profile"},
		{path: "/profile/", contentHint: "/profile"},
		{path: "/organization", contentHint: "/organization"},
		{path: "/agents", contentHint: "/agents"},
		{path: "/domains", contentHint: "Statocyst Domains UI"},
		{path: "/domains/", contentHint: "Statocyst Domains UI"},
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
