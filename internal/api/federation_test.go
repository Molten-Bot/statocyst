package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"statocyst/internal/auth"
	"statocyst/internal/longpoll"
	"statocyst/internal/model"
	"statocyst/internal/store"
)

type federatedTestServer struct {
	router        http.Handler
	server        *httptest.Server
	canonicalBase string
}

func newFederatedTestServer(t *testing.T, canonicalBase string) *federatedTestServer {
	t.Helper()
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(mem, mem, waiters, auth.NewDevHumanAuthProvider(), canonicalBase, "", "", "", "", "molten.bot", true, 15*time.Minute, false)
	router := NewRouter(h)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &federatedTestServer{
		router:        router,
		server:        server,
		canonicalBase: canonicalBase,
	}
}

func adminHeaders() map[string]string {
	return humanHeaders("ops", "ops@molten.bot")
}

func createPeer(t *testing.T, router http.Handler, peerID, canonicalBaseURL, deliveryBaseURL, secret string) {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/admin/peers", map[string]any{
		"peer_id":            peerID,
		"canonical_base_url": canonicalBaseURL,
		"delivery_base_url":  deliveryBaseURL,
		"shared_secret":      secret,
	}, adminHeaders())
	if resp.Code != http.StatusCreated {
		t.Fatalf("create peer failed: %d %s", resp.Code, resp.Body.String())
	}
}

func createRemoteOrgTrustAdmin(t *testing.T, router http.Handler, localOrgID, peerID, remoteOrgHandle string) {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/admin/remote-org-trusts", map[string]any{
		"local_org_id":      localOrgID,
		"peer_id":           peerID,
		"remote_org_handle": remoteOrgHandle,
	}, adminHeaders())
	if resp.Code != http.StatusCreated {
		t.Fatalf("create remote org trust failed: %d %s", resp.Code, resp.Body.String())
	}
}

func createRemoteAgentTrustAdmin(t *testing.T, router http.Handler, localAgentUUID, peerID, remoteAgentURI string) {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/admin/remote-agent-trusts", map[string]any{
		"local_agent_uuid": localAgentUUID,
		"peer_id":          peerID,
		"remote_agent_uri": remoteAgentURI,
	}, adminHeaders())
	if resp.Code != http.StatusCreated {
		t.Fatalf("create remote agent trust failed: %d %s", resp.Code, resp.Body.String())
	}
}

func publishByURI(t *testing.T, router http.Handler, senderToken, toAgentURI, payload string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSONRequest(t, router, http.MethodPost, "/v1/messages/publish", map[string]string{
		"to_agent_uri":  toAgentURI,
		"content_type":  "text/plain",
		"payload":       payload,
		"client_msg_id": "federation-" + normalizeHandle(payload),
	}, map[string]string{"Authorization": "Bearer " + senderToken})
}

func registerFederatedAgentWithUUID(t *testing.T, router http.Handler, humanID, email, orgID, agentID, ownerHumanID string) (string, string) {
	t.Helper()
	ensureHandleConfirmed(t, router, humanID, email)
	bindReq := map[string]any{}
	bindPath := "/v1/me/agents/bind-tokens"
	if orgID != "" {
		bindPath = "/v1/agents/bind-tokens"
		bindReq["org_id"] = orgID
		if ownerHumanID != "" {
			bindReq["owner_human_id"] = ownerHumanID
		}
	}
	createResp := doJSONRequest(t, router, http.MethodPost, bindPath, bindReq, humanHeaders(humanID, email))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	bindToken := asString(t, decodeJSONMap(t, createResp.Body.Bytes()), "bind_token")
	token := redeemBindToken(t, router, bindToken, "temporary-"+agentID)
	agent := finalizeAgentHandleAndGet(t, router, token, agentID)
	return token, asString(t, agent, "agent_uuid")
}

func TestPublicPeersListsCanonicalBasesWithoutAuth(t *testing.T) {
	alpha := newFederatedTestServer(t, "https://alpha.example")

	createPeer(t, alpha.router, "peer-zeta", "https://zeta.example", "https://zeta.internal", "secret-zeta")
	createPeer(t, alpha.router, "peer-beta", "https://beta.example", "https://beta.internal", "secret-beta")

	resp := doJSONRequest(t, alpha.router, http.MethodGet, "/v1/public/peers", nil, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected /v1/public/peers 200, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	peers, ok := payload["peers"].([]any)
	if !ok {
		t.Fatalf("expected peers array, got %T payload=%v", payload["peers"], payload)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d payload=%v", len(peers), payload)
	}

	first, ok := peers[0].(map[string]any)
	if !ok {
		t.Fatalf("expected peer object, got %T payload=%v", peers[0], payload)
	}
	second, ok := peers[1].(map[string]any)
	if !ok {
		t.Fatalf("expected peer object, got %T payload=%v", peers[1], payload)
	}
	if got, _ := first["canonical_base_url"].(string); got != "https://beta.example" {
		t.Fatalf("expected first canonical_base_url https://beta.example, got %q payload=%v", got, payload)
	}
	if got, _ := second["canonical_base_url"].(string); got != "https://zeta.example" {
		t.Fatalf("expected second canonical_base_url https://zeta.example, got %q payload=%v", got, payload)
	}

	for i, raw := range peers {
		peer, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("expected peer object at index %d, got %T", i, raw)
		}
		forbidden := []string{
			"peer_id",
			"updated_at",
			"last_successful_at",
			"last_failure_at",
			"delivery_base_url",
			"shared_secret",
			"created_by",
			"last_failure_reason",
		}
		for _, key := range forbidden {
			if _, exists := peer[key]; exists {
				t.Fatalf("expected public peer payload to omit %q, got peer=%v", key, peer)
			}
		}
		for _, key := range []string{"canonical_base_url", "status"} {
			if _, exists := peer[key]; !exists {
				t.Fatalf("expected public peer payload to include %q, got peer=%v", key, peer)
			}
		}
		if len(peer) != 2 {
			t.Fatalf("expected exactly 2 keys in public peer payload, got peer=%v", peer)
		}
	}
}

func TestPublicPeersRejectsNonGetMethods(t *testing.T) {
	alpha := newFederatedTestServer(t, "https://alpha.example")

	resp := doJSONRequest(t, alpha.router, http.MethodPost, "/v1/public/peers", map[string]any{}, nil)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected /v1/public/peers POST 405, got %d %s", resp.Code, resp.Body.String())
	}
}

func TestFederatedPublishRoutesByCanonicalAgentURI(t *testing.T) {
	alpha := newFederatedTestServer(t, "https://alpha.example")
	beta := newFederatedTestServer(t, "https://beta.example")

	orgA := createOrg(t, alpha.router, "alice", "alice@a.test", "Org Alpha")
	aliceHumanID := currentHumanID(t, alpha.router, "alice", "alice@a.test")
	tokenA, agentUUIDA := registerFederatedAgentWithUUID(t, alpha.router, "alice", "alice@a.test", orgA, "agent-a", aliceHumanID)
	agentA := currentAgent(t, alpha.router, tokenA)
	agentURIA, _ := agentA["uri"].(string)

	orgB := createOrg(t, beta.router, "bob", "bob@b.test", "Org Beta")
	bobHumanID := currentHumanID(t, beta.router, "bob", "bob@b.test")
	tokenB, agentUUIDB := registerFederatedAgentWithUUID(t, beta.router, "bob", "bob@b.test", orgB, "agent-b", bobHumanID)
	agentB := currentAgent(t, beta.router, tokenB)
	agentURIB, _ := agentB["uri"].(string)

	const peerID = "alpha-beta"
	const secret = "peer-shared-secret"
	createPeer(t, alpha.router, peerID, beta.canonicalBase, beta.server.URL, secret)
	createPeer(t, beta.router, peerID, alpha.canonicalBase, alpha.server.URL, secret)
	createRemoteOrgTrustAdmin(t, alpha.router, orgA, peerID, "org-beta")
	createRemoteOrgTrustAdmin(t, beta.router, orgB, peerID, "org-alpha")
	createRemoteAgentTrustAdmin(t, alpha.router, agentUUIDA, peerID, agentURIB)
	createRemoteAgentTrustAdmin(t, beta.router, agentUUIDB, peerID, agentURIA)

	capsResp := doJSONRequest(t, alpha.router, http.MethodGet, "/v1/agents/me/capabilities", nil, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if capsResp.Code != http.StatusOK {
		t.Fatalf("capabilities failed: %d %s", capsResp.Code, capsResp.Body.String())
	}
	capsPayload := decodeJSONMap(t, capsResp.Body.Bytes())
	cp := capsPayload["control_plane"].(map[string]any)
	canTalkToURIs, _ := cp["can_talk_to_uris"].([]any)
	foundRemote := false
	for _, item := range canTalkToURIs {
		if item == agentURIB {
			foundRemote = true
			break
		}
	}
	if !foundRemote {
		t.Fatalf("expected remote agent URI in can_talk_to_uris, got %v", cp["can_talk_to_uris"])
	}

	pubResp := publishByURI(t, alpha.router, tokenA, agentURIB, "hello-beta")
	if pubResp.Code != http.StatusAccepted {
		t.Fatalf("expected remote publish 202, got %d %s", pubResp.Code, pubResp.Body.String())
	}
	pubPayload := decodeJSONMap(t, pubResp.Body.Bytes())
	messageID, _ := pubPayload["message_id"].(string)
	if messageID == "" {
		t.Fatalf("expected message_id, got payload=%v", pubPayload)
	}

	pullResp := pull(t, beta.router, tokenB, 10)
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected beta pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	message := pullPayload["message"].(map[string]any)
	if got, _ := message["payload"].(string); got != "hello-beta" {
		t.Fatalf("expected payload hello-beta, got %q payload=%v", got, pullPayload)
	}
	if got, _ := message["from_agent_uri"].(string); got != agentURIA {
		t.Fatalf("expected from_agent_uri %q, got %q", agentURIA, got)
	}
	if got, _ := message["to_agent_uri"].(string); got != agentURIB {
		t.Fatalf("expected to_agent_uri %q, got %q", agentURIB, got)
	}

	statusResp := messageStatus(t, alpha.router, tokenA, messageID)
	if statusResp.Code != http.StatusOK {
		t.Fatalf("expected sender message status 200, got %d %s", statusResp.Code, statusResp.Body.String())
	}
	statusPayload := decodeJSONMap(t, statusResp.Body.Bytes())
	if got, _ := statusPayload["status"].(string); got != model.MessageForwarded {
		t.Fatalf("expected sender status %q, got %q payload=%v", model.MessageForwarded, got, statusPayload)
	}
}

func TestFederatedPublishDropsWithoutRemoteTrust(t *testing.T) {
	alpha := newFederatedTestServer(t, "https://alpha.example")
	beta := newFederatedTestServer(t, "https://beta.example")

	orgA := createOrg(t, alpha.router, "alice", "alice@a.test", "Org Alpha")
	aliceHumanID := currentHumanID(t, alpha.router, "alice", "alice@a.test")
	tokenA, agentUUIDA := registerFederatedAgentWithUUID(t, alpha.router, "alice", "alice@a.test", orgA, "agent-a", aliceHumanID)
	_ = agentUUIDA

	orgB := createOrg(t, beta.router, "bob", "bob@b.test", "Org Beta")
	bobHumanID := currentHumanID(t, beta.router, "bob", "bob@b.test")
	tokenB, _ := registerFederatedAgentWithUUID(t, beta.router, "bob", "bob@b.test", orgB, "agent-b", bobHumanID)
	_ = tokenB
	agentURIB, _ := currentAgent(t, beta.router, tokenB)["uri"].(string)

	const peerID = "alpha-beta"
	const secret = "peer-shared-secret"
	createPeer(t, alpha.router, peerID, beta.canonicalBase, beta.server.URL, secret)
	createPeer(t, beta.router, peerID, alpha.canonicalBase, alpha.server.URL, secret)
	createRemoteOrgTrustAdmin(t, alpha.router, orgA, peerID, "org-beta")

	pubResp := publishByURI(t, alpha.router, tokenA, agentURIB, "missing-agent-trust")
	if pubResp.Code != http.StatusAccepted {
		t.Fatalf("expected dropped remote publish 202, got %d %s", pubResp.Code, pubResp.Body.String())
	}
	pubPayload := decodeJSONMap(t, pubResp.Body.Bytes())
	if got, _ := pubPayload["status"].(string); got != "dropped" {
		t.Fatalf("expected dropped status, got %q payload=%v", got, pubPayload)
	}
	if got, _ := pubPayload["reason"].(string); got != "no_trust_path" {
		t.Fatalf("expected no_trust_path, got %q payload=%v", got, pubPayload)
	}
}

func TestFederatedPublishRoutesForHumanOwnedAgentsWithMutualRemoteAgentTrust(t *testing.T) {
	alpha := newFederatedTestServer(t, "https://alpha.example")
	beta := newFederatedTestServer(t, "https://beta.example")

	aliceHumanID := currentHumanID(t, alpha.router, "alice", "alice@a.test")
	tokenA, agentUUIDA := registerFederatedAgentWithUUID(t, alpha.router, "alice", "alice@a.test", "", "agent-a", aliceHumanID)
	agentA := currentAgent(t, alpha.router, tokenA)
	agentURIA, _ := agentA["uri"].(string)

	bobHumanID := currentHumanID(t, beta.router, "bob", "bob@b.test")
	tokenB, agentUUIDB := registerFederatedAgentWithUUID(t, beta.router, "bob", "bob@b.test", "", "agent-b", bobHumanID)
	agentB := currentAgent(t, beta.router, tokenB)
	agentURIB, _ := agentB["uri"].(string)

	const peerID = "alpha-beta"
	const secret = "peer-shared-secret"
	createPeer(t, alpha.router, peerID, beta.canonicalBase, beta.server.URL, secret)
	createPeer(t, beta.router, peerID, alpha.canonicalBase, alpha.server.URL, secret)
	createRemoteAgentTrustAdmin(t, alpha.router, agentUUIDA, peerID, agentURIB)
	createRemoteAgentTrustAdmin(t, beta.router, agentUUIDB, peerID, agentURIA)

	pubAlpha := publishByURI(t, alpha.router, tokenA, agentURIB, "hello-human-beta")
	if pubAlpha.Code != http.StatusAccepted {
		t.Fatalf("expected remote publish 202, got %d %s", pubAlpha.Code, pubAlpha.Body.String())
	}
	pullBeta := pull(t, beta.router, tokenB, 10)
	if pullBeta.Code != http.StatusOK {
		t.Fatalf("expected beta pull 200, got %d %s", pullBeta.Code, pullBeta.Body.String())
	}
	pullBetaPayload := decodeJSONMap(t, pullBeta.Body.Bytes())
	msgBeta := pullBetaPayload["message"].(map[string]any)
	if got, _ := msgBeta["payload"].(string); got != "hello-human-beta" {
		t.Fatalf("expected payload hello-human-beta, got %q payload=%v", got, pullBetaPayload)
	}

	pubBeta := publishByURI(t, beta.router, tokenB, agentURIA, "hello-human-alpha")
	if pubBeta.Code != http.StatusAccepted {
		t.Fatalf("expected remote publish 202, got %d %s", pubBeta.Code, pubBeta.Body.String())
	}
	pullAlpha := pull(t, alpha.router, tokenA, 10)
	if pullAlpha.Code != http.StatusOK {
		t.Fatalf("expected alpha pull 200, got %d %s", pullAlpha.Code, pullAlpha.Body.String())
	}
	pullAlphaPayload := decodeJSONMap(t, pullAlpha.Body.Bytes())
	msgAlpha := pullAlphaPayload["message"].(map[string]any)
	if got, _ := msgAlpha["payload"].(string); got != "hello-human-alpha" {
		t.Fatalf("expected payload hello-human-alpha, got %q payload=%v", got, pullAlphaPayload)
	}
}

func TestFederatedPublishStillRequiresRemoteOrgTrustForOrgScopedAgents(t *testing.T) {
	alpha := newFederatedTestServer(t, "https://alpha.example")
	beta := newFederatedTestServer(t, "https://beta.example")

	orgA := createOrg(t, alpha.router, "alice", "alice@a.test", "Org Alpha")
	aliceHumanID := currentHumanID(t, alpha.router, "alice", "alice@a.test")
	tokenA, agentUUIDA := registerFederatedAgentWithUUID(t, alpha.router, "alice", "alice@a.test", orgA, "agent-a", aliceHumanID)

	orgB := createOrg(t, beta.router, "bob", "bob@b.test", "Org Beta")
	bobHumanID := currentHumanID(t, beta.router, "bob", "bob@b.test")
	tokenB, agentUUIDB := registerFederatedAgentWithUUID(t, beta.router, "bob", "bob@b.test", orgB, "agent-b", bobHumanID)
	agentURIB, _ := currentAgent(t, beta.router, tokenB)["uri"].(string)

	const peerID = "alpha-beta"
	const secret = "peer-shared-secret"
	createPeer(t, alpha.router, peerID, beta.canonicalBase, beta.server.URL, secret)
	createPeer(t, beta.router, peerID, alpha.canonicalBase, alpha.server.URL, secret)
	createRemoteAgentTrustAdmin(t, alpha.router, agentUUIDA, peerID, agentURIB)
	createRemoteAgentTrustAdmin(t, beta.router, agentUUIDB, peerID, currentAgent(t, alpha.router, tokenA)["uri"].(string))

	pubResp := publishByURI(t, alpha.router, tokenA, agentURIB, "missing-org-trust")
	if pubResp.Code != http.StatusAccepted {
		t.Fatalf("expected dropped remote publish 202, got %d %s", pubResp.Code, pubResp.Body.String())
	}
	pubPayload := decodeJSONMap(t, pubResp.Body.Bytes())
	if got, _ := pubPayload["status"].(string); got != "dropped" {
		t.Fatalf("expected dropped status, got %q payload=%v", got, pubPayload)
	}
	if got, _ := pubPayload["reason"].(string); got != "no_trust_path" {
		t.Fatalf("expected no_trust_path, got %q payload=%v", got, pubPayload)
	}
}

func TestFederatedPublishHumanOwnedRequiresMutualRemoteTrustBeforeDelivery(t *testing.T) {
	alpha := newFederatedTestServer(t, "https://alpha.example")
	beta := newFederatedTestServer(t, "https://beta.example")

	aliceHumanID := currentHumanID(t, alpha.router, "alice", "alice@a.test")
	tokenA, agentUUIDA := registerFederatedAgentWithUUID(t, alpha.router, "alice", "alice@a.test", "", "agent-a", aliceHumanID)
	agentA := currentAgent(t, alpha.router, tokenA)
	agentURIA, _ := agentA["uri"].(string)

	bobHumanID := currentHumanID(t, beta.router, "bob", "bob@b.test")
	tokenB, _ := registerFederatedAgentWithUUID(t, beta.router, "bob", "bob@b.test", "", "agent-b", bobHumanID)
	agentB := currentAgent(t, beta.router, tokenB)
	agentURIB, _ := agentB["uri"].(string)

	const peerID = "alpha-beta"
	const secret = "peer-shared-secret"
	createPeer(t, alpha.router, peerID, beta.canonicalBase, beta.server.URL, secret)
	createPeer(t, beta.router, peerID, alpha.canonicalBase, alpha.server.URL, secret)

	// A accepts B, but B has not accepted A yet.
	createRemoteAgentTrustAdmin(t, alpha.router, agentUUIDA, peerID, agentURIB)

	pubResp := publishByURI(t, alpha.router, tokenA, agentURIB, "one-sided-acceptance")
	if pubResp.Code != http.StatusAccepted {
		t.Fatalf("expected remote publish 202, got %d %s", pubResp.Code, pubResp.Body.String())
	}
	pubPayload := decodeJSONMap(t, pubResp.Body.Bytes())
	messageID, _ := pubPayload["message_id"].(string)
	if messageID == "" {
		t.Fatalf("expected message_id in publish payload=%v", pubPayload)
	}

	pullResp := pull(t, beta.router, tokenB, 10)
	if pullResp.Code != http.StatusNoContent {
		t.Fatalf("expected beta pull 204 without reciprocal trust, got %d %s", pullResp.Code, pullResp.Body.String())
	}

	statusResp := messageStatus(t, alpha.router, tokenA, messageID)
	if statusResp.Code != http.StatusOK {
		t.Fatalf("expected sender message status 200, got %d %s", statusResp.Code, statusResp.Body.String())
	}
	statusPayload := decodeJSONMap(t, statusResp.Body.Bytes())
	if got, _ := statusPayload["status"].(string); got == model.MessageForwarded {
		t.Fatalf("expected message to remain unforwarded without reciprocal trust, payload=%v", statusPayload)
	}
	msg, _ := statusPayload["message"].(map[string]any)
	if got, _ := msg["to_agent_uri"].(string); got != agentURIB {
		t.Fatalf("expected to_agent_uri %q, got %q payload=%v", agentURIB, got, statusPayload)
	}
	if got, _ := msg["from_agent_uri"].(string); got != agentURIA {
		t.Fatalf("expected from_agent_uri %q, got %q payload=%v", agentURIA, got, statusPayload)
	}
}

func TestPeerIngressRejectsInvalidSignature(t *testing.T) {
	beta := newFederatedTestServer(t, "https://beta.example")

	const peerID = "alpha-beta"
	createPeer(t, beta.router, peerID, "https://alpha.example", "http://alpha.invalid", "correct-secret")

	body, _ := json.Marshal(peerInboundEnvelope{
		Message: model.Message{
			MessageID:    "019cd9f1-e91f-7e03-9d21-5667f024b90b",
			FromAgentURI: "https://alpha.example/org-alpha/agent-a",
			ToAgentURI:   "https://beta.example/org-beta/agent-b",
			ContentType:  "text/plain",
			Payload:      "bad-signature",
			CreatedAt:    time.Now().UTC(),
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/peer/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	signPeerRequest(req, "wrong-secret", peerID, body, time.Now().UTC())
	resp := httptest.NewRecorder()
	beta.router.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalid signature 401, got %d %s", resp.Code, resp.Body.String())
	}
}

func TestPeerIngressRejectsTransitiveTarget(t *testing.T) {
	beta := newFederatedTestServer(t, "https://beta.example")
	createPeer(t, beta.router, "alpha-beta", "https://alpha.example", "http://alpha.invalid", "correct-secret")

	body, _ := json.Marshal(peerInboundEnvelope{
		Message: model.Message{
			MessageID:    "019cd9f1-e91f-7e03-9d21-5667f024b90c",
			FromAgentURI: "https://alpha.example/org-alpha/agent-a",
			ToAgentURI:   "https://gamma.example/org-gamma/agent-c",
			ContentType:  "text/plain",
			Payload:      "no-transit",
			CreatedAt:    time.Now().UTC(),
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/peer/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	signPeerRequest(req, "correct-secret", "alpha-beta", body, time.Now().UTC())
	resp := httptest.NewRecorder()
	beta.router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected transitive target 400, got %d %s", resp.Code, resp.Body.String())
	}
}

func TestRemoteAgentTrustOwnerCanCreateAndDeleteForHumanOwnedAgent(t *testing.T) {
	router := newTestRouter()
	const peerID = "alpha-beta"
	createPeer(t, router, peerID, "https://beta.example", "https://beta.example", "secret")

	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	_, agentUUID := registerFederatedAgentWithUUID(t, router, "alice", "alice@a.test", "", "agent-a", aliceHumanID)

	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/admin/remote-agent-trusts", map[string]any{
		"local_agent_uuid": agentUUID,
		"remote_agent_uri": "https://beta.example/human/bob/agent/agent-b",
	}, humanHeaders("alice", "alice@a.test"))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected owner create remote trust 201, got %d %s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp.Body.Bytes())
	trust, _ := createPayload["remote_agent_trust"].(map[string]any)
	trustID, _ := trust["trust_id"].(string)
	if trustID == "" {
		t.Fatalf("expected trust_id in create payload=%v", createPayload)
	}

	listResp := doJSONRequest(t, router, http.MethodGet, "/v1/admin/remote-agent-trusts", nil, humanHeaders("alice", "alice@a.test"))
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected owner list remote trusts 200, got %d %s", listResp.Code, listResp.Body.String())
	}
	listPayload := decodeJSONMap(t, listResp.Body.Bytes())
	trusts, _ := listPayload["remote_agent_trusts"].([]any)
	if len(trusts) != 1 {
		t.Fatalf("expected exactly one visible trust, got %d payload=%v", len(trusts), listPayload)
	}

	deleteResp := doJSONRequest(t, router, http.MethodDelete, "/v1/admin/remote-agent-trusts/"+trustID, nil, humanHeaders("alice", "alice@a.test"))
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("expected owner delete remote trust 200, got %d %s", deleteResp.Code, deleteResp.Body.String())
	}
}

func TestRemoteAgentTrustNonOwnerCannotCreateForHumanOwnedAgent(t *testing.T) {
	router := newTestRouter()
	const peerID = "alpha-beta"
	createPeer(t, router, peerID, "https://beta.example", "https://beta.example", "secret")

	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	_, agentUUID := registerFederatedAgentWithUUID(t, router, "alice", "alice@a.test", "", "agent-a", aliceHumanID)

	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/admin/remote-agent-trusts", map[string]any{
		"local_agent_uuid": agentUUID,
		"peer_id":          peerID,
		"remote_agent_uri": "https://beta.example/human/bob/agent/agent-b",
	}, humanHeaders("bob", "bob@b.test"))
	if createResp.Code != http.StatusForbidden {
		t.Fatalf("expected non-owner create remote trust 403, got %d %s", createResp.Code, createResp.Body.String())
	}
}

func TestAdminPeerEndpointsRequireSuperAdmin(t *testing.T) {
	router := newTestRouter()
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/admin/peers", map[string]any{
		"peer_id":            "peer-a",
		"canonical_base_url": "https://beta.example",
		"delivery_base_url":  "https://beta.example",
		"shared_secret":      "secret",
	}, humanHeaders("alice", "alice@a.test"))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected non-admin peer create 403, got %d %s", resp.Code, resp.Body.String())
	}
}
