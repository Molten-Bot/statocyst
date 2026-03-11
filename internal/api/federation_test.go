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
	bindReq := map[string]any{"org_id": orgID}
	if ownerHumanID != "" {
		bindReq["owner_human_id"] = ownerHumanID
	}
	createResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind-tokens", bindReq, humanHeaders(humanID, email))
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create bind token failed: %d %s", createResp.Code, createResp.Body.String())
	}
	bindToken := asString(t, decodeJSONMap(t, createResp.Body.Bytes()), "bind_token")
	token := redeemBindToken(t, router, bindToken, "temporary-"+agentID)
	agent := finalizeAgentHandleAndGet(t, router, token, agentID)
	return token, asString(t, agent, "agent_uuid")
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
