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
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "", "", "", "molten.bot", true, 15*time.Minute, false)
	return NewRouter(h)
}

func TestHandlerWiringWithInterfaceStores(t *testing.T) {
	mem := store.NewMemoryStore()
	var control store.ControlPlaneStore = mem
	var queue store.MessageQueueStore = mem
	waiters := longpoll.NewWaiters()
	h := NewHandler(control, queue, waiters, auth.NewDevHumanAuthProvider(), "", "", "", "molten.bot", true, 15*time.Minute, false)
	router := NewRouter(h)

	health := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
	if health.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d %s", health.Code, health.Body.String())
	}

	me := doJSONRequest(t, router, http.MethodGet, "/v1/me", nil, humanHeaders("alice", "alice@a.test"))
	if me.Code != http.StatusOK {
		t.Fatalf("expected /v1/me 200, got %d %s", me.Code, me.Body.String())
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
	inviteCode, _ := payload["invite_code"].(string)
	if inviteCode == "" {
		t.Fatalf("missing invite_code")
	}
	return inviteID, inviteCode
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
	agentUUID, _ := payload["agent_uuid"].(string)
	if token == "" || agentUUID == "" {
		t.Fatalf("missing token or agent_uuid")
	}
	return token, agentUUID
}

func registerAgent(t *testing.T, router http.Handler, humanID, email, orgID, agentID, ownerHumanID string) string {
	t.Helper()
	token, _ := registerAgentWithUUID(t, router, humanID, email, orgID, agentID, ownerHumanID)
	return token
}

func registerMyAgent(t *testing.T, router http.Handler, humanID, email, agentID string) (string, string, string) {
	t.Helper()
	ensureHandleConfirmed(t, router, humanID, email)
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents", map[string]any{
		"agent_id": agentID,
	}, humanHeaders(humanID, email))
	if resp.Code != http.StatusCreated {
		t.Fatalf("register my agent failed: %d %s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode register my agent: %v", err)
	}
	token, _ := payload["token"].(string)
	orgID, _ := payload["org_id"].(string)
	agentUUID, _ := payload["agent_uuid"].(string)
	if token == "" || orgID == "" || agentUUID == "" {
		t.Fatalf("missing token or org_id or agent_uuid")
	}
	return token, orgID, agentUUID
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
	_, inviteCode := createInviteWithCode(t, router, "alice", "alice@a.test", orgID, "bob@b.test", "member")
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
		"invite_code": inviteCode,
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
	_, inviteCode := createInviteWithCode(t, router, "alice", "alice@a.test", orgID, "bob@b.test", "member")
	ensureHandleConfirmed(t, router, "charlie", "charlie@c.test")

	redeem := doJSONRequest(t, router, http.MethodPost, "/v1/org-invites/redeem", map[string]string{
		"invite_code": inviteCode,
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
	tokenA, orgA, agentUUIDA := registerMyAgent(t, router, "alice", "alice@a.test", "alice-agent-a")
	_, orgB, agentUUIDB := registerMyAgent(t, router, "alice", "alice@a.test", "alice-agent-b")
	if orgA != orgB {
		t.Fatalf("expected personal org reuse, got %q and %q", orgA, orgB)
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

func TestMyAgentBindTokenRedeemWithAgentChosenName(t *testing.T) {
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
		"agent_id":   "alice-agent-picked-name",
	}, nil)
	if redeemResp.Code != http.StatusCreated {
		t.Fatalf("redeem bind token failed: %d %s", redeemResp.Code, redeemResp.Body.String())
	}
	redeemPayload := decodeJSONMap(t, redeemResp.Body.Bytes())
	if redeemPayload["token"] == "" {
		t.Fatalf("expected bind response token")
	}

	listResp := doJSONRequest(t, router, http.MethodGet, "/v1/me/agents", nil, humanHeaders("alice", "alice@a.test"))
	if listResp.Code != http.StatusOK {
		t.Fatalf("list my agents failed: %d %s", listResp.Code, listResp.Body.String())
	}
	listPayload := decodeJSONMap(t, listResp.Body.Bytes())
	agents, ok := listPayload["agents"].([]any)
	if !ok || len(agents) == 0 {
		t.Fatalf("expected at least one agent in my list, got %v", listPayload["agents"])
	}
	found := false
	for _, item := range agents {
		agent, _ := item.(map[string]any)
		if agent["handle"] == "alice-agent-picked-name" {
			agentID, _ := agent["agent_id"].(string)
			if !strings.HasSuffix(agentID, "/alice-agent-picked-name") {
				t.Fatalf("expected canonical URI to end with /alice-agent-picked-name, got %q", agentID)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected agent created via bind redeem to appear in /v1/me/agents")
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

func TestHumanCanSetHandleAndVisibility(t *testing.T) {
	router := newTestRouter()

	_ = currentHumanID(t, router, "alice", "alice@a.test")
	update := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
		"handle":    "Alice Main",
		"is_public": false,
	}, humanHeaders("alice", "alice@a.test"))
	if update.Code != http.StatusOK {
		t.Fatalf("expected profile patch 200, got %d %s", update.Code, update.Body.String())
	}
	payload := decodeJSONMap(t, update.Body.Bytes())
	human, _ := payload["human"].(map[string]any)
	if human["handle"] != "alice-main" {
		t.Fatalf("expected normalized handle alice-main, got %v", human["handle"])
	}
	if human["is_public"] != false {
		t.Fatalf("expected is_public false, got %v", human["is_public"])
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
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")

	orgA := createOrg(t, router, "alice", "alice@a.test", "Org Human Agent A")
	orgB := createOrg(t, router, "bob", "bob@b.test", "Org Human Agent B")

	registerAgent(t, router, "alice", "alice@a.test", orgA, "alpha-agent", aliceHumanID)
	dup := doJSONRequest(t, router, http.MethodPost, "/v1/agents/register", map[string]any{
		"org_id":         orgB,
		"agent_id":       "ALPHA-AGENT",
		"owner_human_id": bobHumanID,
	}, humanHeaders("bob", "bob@b.test"))
	if dup.Code != http.StatusCreated {
		t.Fatalf("expected 201 for same handle in a different human scope, got %d %s", dup.Code, dup.Body.String())
	}
	body := decodeJSONMap(t, dup.Body.Bytes())
	if body["handle"] != "alpha-agent" {
		t.Fatalf("expected normalized handle alpha-agent, got %v", body["handle"])
	}
}

func TestOrgBoundAgentNameUniqueWithinOrg(t *testing.T) {
	router := newTestRouter()

	orgA := createOrg(t, router, "alice", "alice@a.test", "Org Agents Unique")
	registerAgent(t, router, "alice", "alice@a.test", orgA, "org-agent", "")

	dup := doJSONRequest(t, router, http.MethodPost, "/v1/agents/register", map[string]any{
		"org_id":   orgA,
		"agent_id": "ORG-AGENT",
	}, humanHeaders("alice", "alice@a.test"))
	if dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate org-bound agent name in same org, got %d %s", dup.Code, dup.Body.String())
	}
	body := decodeJSONMap(t, dup.Body.Bytes())
	if body["error"] != "agent_exists" {
		t.Fatalf("expected agent_exists for duplicate org-bound name, got %v", body["error"])
	}
}

func TestLiveShowsOnlyPublicEntities(t *testing.T) {
	router := newTestRouter()

	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	orgID := createOrg(t, router, "alice", "alice@a.test", "Live Org")
	_, liveAgentUUID := registerAgentWithUUID(t, router, "alice", "alice@a.test", orgID, "live-agent", aliceHumanID)

	live := doJSONRequest(t, router, http.MethodGet, "/live", nil, nil)
	if live.Code != http.StatusOK {
		t.Fatalf("expected /live 200, got %d %s", live.Code, live.Body.String())
	}
	liveBody := live.Body.String()
	if !strings.Contains(liveBody, "live-org") {
		t.Fatalf("expected live page to include org handle, got %q", liveBody)
	}
	if !strings.Contains(liveBody, "alice") {
		t.Fatalf("expected live page to include human handle, got %q", liveBody)
	}
	if !strings.Contains(liveBody, "live-agent") {
		t.Fatalf("expected live page to include agent handle, got %q", liveBody)
	}

	hideAgent := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/"+liveAgentUUID, map[string]any{
		"is_public": false,
	}, humanHeaders("alice", "alice@a.test"))
	if hideAgent.Code != http.StatusOK {
		t.Fatalf("expected agent hide 200, got %d %s", hideAgent.Code, hideAgent.Body.String())
	}
	liveAfterAgentHide := doJSONRequest(t, router, http.MethodGet, "/live", nil, nil)
	if strings.Contains(liveAfterAgentHide.Body.String(), "live-agent") {
		t.Fatalf("expected hidden agent to be absent from /live")
	}

	hideHuman := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
		"is_public": false,
	}, humanHeaders("alice", "alice@a.test"))
	if hideHuman.Code != http.StatusOK {
		t.Fatalf("expected human hide 200, got %d %s", hideHuman.Code, hideHuman.Body.String())
	}
	liveAfterHumanHide := doJSONRequest(t, router, http.MethodGet, "/live", nil, nil)
	if strings.Contains(liveAfterHumanHide.Body.String(), "alice") {
		t.Fatalf("expected hidden human to be absent from /live")
	}

	hideOrg := doJSONRequest(t, router, http.MethodPatch, "/v1/orgs/"+orgID, map[string]any{
		"is_public": false,
	}, humanHeaders("alice", "alice@a.test"))
	if hideOrg.Code != http.StatusOK {
		t.Fatalf("expected org hide 200, got %d %s", hideOrg.Code, hideOrg.Body.String())
	}
	liveAfterOrgHide := doJSONRequest(t, router, http.MethodGet, "/live", nil, nil)
	if strings.Contains(liveAfterOrgHide.Body.String(), "live-org") {
		t.Fatalf("expected hidden org to be absent from /live")
	}
}

func TestBindTokenExpires(t *testing.T) {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "", "", "", "molten.bot", true, 15*time.Minute, false)
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
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "", "", "", "molten.bot", true, 15*time.Minute, true)
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

	_, _, _ = registerMyAgent(t, router, "alice", "alice@a.test", "alice-agent-1")
	_, _, _ = registerMyAgent(t, router, "alice", "alice@a.test", "alice-agent-2")
	third := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents", map[string]any{
		"agent_id": "alice-agent-3",
	}, humanHeaders("alice", "alice@a.test"))
	if third.Code != http.StatusConflict {
		t.Fatalf("expected non-super-admin third agent to fail with 409, got %d %s", third.Code, third.Body.String())
	}
	thirdPayload := decodeJSONMap(t, third.Body.Bytes())
	if thirdPayload["error"] != "agent_limit_reached" {
		t.Fatalf("expected agent_limit_reached error, got %v", thirdPayload["error"])
	}

	rootOrg := createOrg(t, router, "root", "root@molten.bot", "Root Ops")
	rootID := currentHumanID(t, router, "root", "root@molten.bot")
	createOne := doJSONRequest(t, router, http.MethodPost, "/v1/agents/register", map[string]any{
		"org_id":         rootOrg,
		"agent_id":       "root-agent-1",
		"owner_human_id": rootID,
	}, humanHeaders("root", "root@molten.bot"))
	if createOne.Code != http.StatusCreated {
		t.Fatalf("expected root first agent to be created, got %d %s", createOne.Code, createOne.Body.String())
	}
	createTwo := doJSONRequest(t, router, http.MethodPost, "/v1/agents/register", map[string]any{
		"org_id":         rootOrg,
		"agent_id":       "root-agent-2",
		"owner_human_id": rootID,
	}, humanHeaders("root", "root@molten.bot"))
	if createTwo.Code != http.StatusCreated {
		t.Fatalf("expected root second agent to be created, got %d %s", createTwo.Code, createTwo.Body.String())
	}
	createThree := doJSONRequest(t, router, http.MethodPost, "/v1/agents/register", map[string]any{
		"org_id":         rootOrg,
		"agent_id":       "root-agent-3",
		"owner_human_id": rootID,
	}, humanHeaders("root", "root@molten.bot"))
	if createThree.Code != http.StatusCreated {
		t.Fatalf("expected root third agent to bypass limit, got %d %s", createThree.Code, createThree.Body.String())
	}
}

func TestLiveSnapshotEndpoint(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Snapshot Org")
	aliceID := currentHumanID(t, router, "alice", "alice@a.test")
	_ = registerAgent(t, router, "alice", "alice@a.test", orgID, "snapshot-agent", aliceID)

	resp := doJSONRequest(t, router, http.MethodGet, "/v1/live/snapshot", nil, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected /v1/live/snapshot 200, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	orgs, ok := payload["organizations"].([]any)
	if !ok || len(orgs) == 0 {
		t.Fatalf("expected non-empty organizations in snapshot, got %v", payload["organizations"])
	}
	first, _ := orgs[0].(map[string]any)
	if first["handle"] == "" {
		t.Fatalf("expected organization handle in snapshot row: %v", first)
	}
}
