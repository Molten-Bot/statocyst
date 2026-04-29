package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestLaunchSmoke(t *testing.T) {
	t.Run("Health endpoint responds and reports ok", func(t *testing.T) {
		router := newTestRouter()

		resp := doJSONRequest(t, router, http.MethodGet, "/health", nil, nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected /health 200, got %d %s", resp.Code, resp.Body.String())
		}

		payload := decodeJSONMap(t, resp.Body.Bytes())
		if payload["status"] != "ok" {
			t.Fatalf("expected health status ok, got %v payload=%v", payload["status"], payload)
		}
	})

	t.Run("Alice creates handle", func(t *testing.T) {
		router := newTestRouter()

		resp := setHumanHandle(t, router, "alice", "alice@a.test", "alice")
		human := requireEntity(t, decodeJSONMap(t, resp.Body.Bytes()), "human")
		if human["handle"] != "alice" {
			t.Fatalf("expected alice handle, got %v", human["handle"])
		}
	})

	t.Run("Bob tries to add the same handle and gets an error", func(t *testing.T) {
		router := newTestRouter()

		setHumanHandle(t, router, "alice", "alice@a.test", "alice")

		resp := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
			"handle": "alice",
		}, humanHeaders("bob", "bob@b.test"))
		if resp.Code != http.StatusConflict {
			t.Fatalf("expected duplicate handle 409, got %d %s", resp.Code, resp.Body.String())
		}
		requireErrorCode(t, decodeJSONMap(t, resp.Body.Bytes()), "human_handle_exists")
	})

	t.Run("Alice adds metadata to her profile", func(t *testing.T) {
		router := newTestRouter()
		ensureHandleConfirmed(t, router, "alice", "alice@a.test")

		resp := patchHumanMetadata(t, router, "alice", "alice@a.test", map[string]any{
			"public": true,
			"bio":    "Alice launch smoke profile",
		})
		requireEntityMetadata(t, decodeJSONMap(t, resp.Body.Bytes()), "human", map[string]any{
			"public": true,
			"bio":    "Alice launch smoke profile",
		})
	})

	t.Run("Alice changes metadata from her profile", func(t *testing.T) {
		router := newTestRouter()
		ensureHandleConfirmed(t, router, "alice", "alice@a.test")
		patchHumanMetadata(t, router, "alice", "alice@a.test", map[string]any{
			"public": true,
			"bio":    "Alice launch smoke profile",
		})

		resp := patchHumanMetadata(t, router, "alice", "alice@a.test", map[string]any{
			"public": true,
			"bio":    "Alice launch smoke profile updated",
			"stage":  "launch",
		})
		requireEntityMetadata(t, decodeJSONMap(t, resp.Body.Bytes()), "human", map[string]any{
			"public": true,
			"bio":    "Alice launch smoke profile updated",
			"stage":  "launch",
		})
	})

	t.Run("Alice clears metadata from her profile", func(t *testing.T) {
		router := newTestRouter()
		ensureHandleConfirmed(t, router, "alice", "alice@a.test")
		patchHumanMetadata(t, router, "alice", "alice@a.test", map[string]any{
			"public": true,
			"bio":    "Alice launch smoke profile",
		})

		resp := patchHumanMetadata(t, router, "alice", "alice@a.test", map[string]any{})
		requireEntityMetadata(t, decodeJSONMap(t, resp.Body.Bytes()), "human", map[string]any{})
	})

	t.Run("Alice creates an organization", func(t *testing.T) {
		router := newTestRouter()

		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Alpha")
		myOrgs := doJSONRequest(t, router, http.MethodGet, "/v1/me/orgs", nil, humanHeaders("alice", "alice@a.test"))
		if myOrgs.Code != http.StatusOK {
			t.Fatalf("expected /v1/me/orgs 200, got %d %s", myOrgs.Code, myOrgs.Body.String())
		}
		if !membershipHasOrg(t, decodeJSONMap(t, myOrgs.Body.Bytes()), orgID) {
			t.Fatalf("created org %q missing from /v1/me/orgs", orgID)
		}
	})

	t.Run("Bob tries to add an organization with the same handle and gets an error", func(t *testing.T) {
		router := newTestRouter()

		createOrg(t, router, "alice", "alice@a.test", "Launch Alpha")
		ensureHandleConfirmed(t, router, "bob", "bob@b.test")

		resp := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]any{
			"handle":       "launch-alpha",
			"display_name": "Launch Alpha Duplicate",
		}, humanHeaders("bob", "bob@b.test"))
		if resp.Code != http.StatusConflict {
			t.Fatalf("expected duplicate org handle 409, got %d %s", resp.Code, resp.Body.String())
		}
		requireErrorCode(t, decodeJSONMap(t, resp.Body.Bytes()), "org_handle_exists")
	})

	t.Run("Alice adds metadata to an organization", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Alpha")

		resp := patchOrgMetadata(t, router, "alice", "alice@a.test", orgID, map[string]any{
			"public":      true,
			"description": "Launch Alpha Organization",
		})
		requireEntityMetadata(t, decodeJSONMap(t, resp.Body.Bytes()), "organization", map[string]any{
			"public":      true,
			"description": "Launch Alpha Organization",
		})
	})

	t.Run("Alice changes metadata to an organization", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Alpha")
		patchOrgMetadata(t, router, "alice", "alice@a.test", orgID, map[string]any{
			"public":      true,
			"description": "Launch Alpha Organization",
		})

		resp := patchOrgMetadata(t, router, "alice", "alice@a.test", orgID, map[string]any{
			"public":      true,
			"description": "Launch Alpha Organization Updated",
			"stage":       "launch",
		})
		requireEntityMetadata(t, decodeJSONMap(t, resp.Body.Bytes()), "organization", map[string]any{
			"public":      true,
			"description": "Launch Alpha Organization Updated",
			"stage":       "launch",
		})
	})

	t.Run("Alice clears metadata from an organization", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Alpha")
		patchOrgMetadata(t, router, "alice", "alice@a.test", orgID, map[string]any{
			"public":      true,
			"description": "Launch Alpha Organization",
		})

		resp := patchOrgMetadata(t, router, "alice", "alice@a.test", orgID, map[string]any{})
		requireEntityMetadata(t, decodeJSONMap(t, resp.Body.Bytes()), "organization", map[string]any{})
	})

	t.Run("Alice creates an organization and deletes it", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Alpha")

		deleteResp := doJSONRequest(t, router, http.MethodDelete, "/v1/orgs/"+orgID, nil, humanHeaders("alice", "alice@a.test"))
		if deleteResp.Code != http.StatusOK {
			t.Fatalf("expected org delete 200, got %d %s", deleteResp.Code, deleteResp.Body.String())
		}
		deletePayload := decodeJSONMap(t, deleteResp.Body.Bytes())
		if deletePayload["result"] != "deleted" {
			t.Fatalf("expected org delete result deleted, got %v payload=%v", deletePayload["result"], deletePayload)
		}

		myOrgs := doJSONRequest(t, router, http.MethodGet, "/v1/me/orgs", nil, humanHeaders("alice", "alice@a.test"))
		if myOrgs.Code != http.StatusOK {
			t.Fatalf("expected /v1/me/orgs 200 after delete, got %d %s", myOrgs.Code, myOrgs.Body.String())
		}
		if membershipHasOrg(t, decodeJSONMap(t, myOrgs.Body.Bytes()), orgID) {
			t.Fatalf("deleted org %q still present in /v1/me/orgs", orgID)
		}
	})

	t.Run("Alice creates a bind token and an agent binds successfully", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Agents")

		bindToken := createMyBindToken(t, router, "alice", "alice@a.test", orgID)
		token := redeemBindToken(t, router, bindToken, "launch-agent-a")
		agent := currentAgent(t, router, token)
		if agent["status"] != "active" {
			t.Fatalf("expected bound agent active, got %v payload=%v", agent["status"], agent)
		}
		if agent["handle"] != "launch-agent-a" {
			t.Fatalf("expected bound agent handle launch-agent-a, got %v payload=%v", agent["handle"], agent)
		}
	})

	t.Run("Alice creates a bind token and the agent updates its profile handle", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Agents")

		bindToken := createMyBindToken(t, router, "alice", "alice@a.test", orgID)
		token := redeemBindToken(t, router, bindToken, "launch-agent-a")
		resp := patchAgentHandle(t, router, token, "launch-agent-a")
		agent := requireEntity(t, decodeJSONMap(t, resp.Body.Bytes()), "agent")
		if agent["handle"] != "launch-agent-a" {
			t.Fatalf("expected finalized handle launch-agent-a, got %v", agent["handle"])
		}
	})

	t.Run("Alice creates a bind token and the agent gets duplicate handle suggestions during bind", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Agents")
		aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
		registerAgentWithUUID(t, router, "alice", "alice@a.test", orgID, "launch-agent-a", aliceHumanID)

		bindToken := createMyBindToken(t, router, "alice", "alice@a.test", orgID)
		resp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]any{
			"bind_token": bindToken,
			"handle":     "launch-agent-a",
		}, nil)
		if resp.Code != http.StatusConflict {
			t.Fatalf("expected duplicate agent handle 409, got %d %s", resp.Code, resp.Body.String())
		}
		payload := decodeJSONMap(t, resp.Body.Bytes())
		requireErrorCode(t, payload, "agent_exists")
		suggested, _ := payload["suggested_handles"].([]any)
		if len(suggested) == 0 {
			t.Fatalf("expected duplicate bind suggestions, got %v", payload["suggested_handles"])
		}
	})

	t.Run("Alice creates a bind token and the agent adds profile metadata", func(t *testing.T) {
		router := newTestRouter()
		token := createBoundAgentForSmoke(t, router, "Launch Agents", "launch-agent-a")

		resp := patchAgentMetadata(t, router, token, map[string]any{
			"public": true,
			"role":   "primary",
		})
		requireEntityMetadata(t, decodeJSONMap(t, resp.Body.Bytes()), "agent", map[string]any{
			"public": true,
			"role":   "primary",
		})
	})

	t.Run("Alice creates a bind token and the agent changes profile metadata", func(t *testing.T) {
		router := newTestRouter()
		token := createBoundAgentForSmoke(t, router, "Launch Agents", "launch-agent-a")
		patchAgentMetadata(t, router, token, map[string]any{
			"public": true,
			"role":   "primary",
		})

		resp := patchAgentMetadata(t, router, token, map[string]any{
			"public": true,
			"role":   "primary-updated",
			"stage":  "launch",
		})
		requireEntityMetadata(t, decodeJSONMap(t, resp.Body.Bytes()), "agent", map[string]any{
			"public": true,
			"role":   "primary-updated",
			"stage":  "launch",
		})
	})

	t.Run("Alice creates a bind token and the agent clears selected profile metadata keys", func(t *testing.T) {
		router := newTestRouter()
		token := createBoundAgentForSmoke(t, router, "Launch Agents", "launch-agent-a")
		patchAgentMetadata(t, router, token, map[string]any{
			"public": true,
			"role":   "primary",
		})

		resp := patchAgentMetadata(t, router, token, map[string]any{
			"public": nil,
			"role":   nil,
		})
		requireEntityMetadata(t, decodeJSONMap(t, resp.Body.Bytes()), "agent", map[string]any{})
	})

	t.Run("Alice creates a bind token and the agent publishes activity", func(t *testing.T) {
		router := newTestRouter()
		token := createBoundAgentForSmoke(t, router, "Launch Agents", "launch-agent-a")

		resp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/me/activities", map[string]any{
			"activity": "Started coding launch smoke coverage",
			"category": "coding",
			"status":   "started",
		}, map[string]string{
			"Authorization": "Bearer " + token,
		})
		if resp.Code != http.StatusCreated {
			t.Fatalf("expected activity publish 201, got %d %s", resp.Code, resp.Body.String())
		}
		payload := decodeJSONMap(t, resp.Body.Bytes())
		result := requireAgentRuntimeSuccessEnvelope(t, payload)
		agent, _ := result["agent"].(map[string]any)
		metadata, _ := agent["metadata"].(map[string]any)
		activities, _ := metadata["activities"].([]any)
		if len(activities) == 0 {
			t.Fatalf("expected metadata.activities to include pushed activity, got metadata=%v", metadata)
		}
		log, _ := agent["activity_log"].([]any)
		if !hasAgentActivity(log, "Started coding launch smoke coverage", "coding", "started") {
			t.Fatalf("expected activity_log to include pushed agent activity, got %v", log)
		}
	})

	t.Run("Alice binds an agent and revokes it", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Agents")
		aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
		token, agentUUID := registerAgentWithUUID(t, router, "alice", "alice@a.test", orgID, "launch-agent-a", aliceHumanID)

		resp := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+agentUUID, nil, humanHeaders("alice", "alice@a.test"))
		if resp.Code != http.StatusOK {
			t.Fatalf("expected agent revoke 200, got %d %s", resp.Code, resp.Body.String())
		}

		revokedAuth := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
			"Authorization": "Bearer " + token,
		})
		if revokedAuth.Code != http.StatusUnauthorized {
			t.Fatalf("expected revoked agent token to fail with 401, got %d %s", revokedAuth.Code, revokedAuth.Body.String())
		}
	})

	t.Run("Alice invites two agents by bind token, binds both agents, and sees both in her list", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Agents")

		bindTokenA := createMyBindToken(t, router, "alice", "alice@a.test", orgID)
		tokenA := redeemBindToken(t, router, bindTokenA, "temporary-agent-a")
		agentA := finalizeAgentHandleAndGet(t, router, tokenA, "launch-agent-a")

		bindTokenB := createMyBindToken(t, router, "alice", "alice@a.test", orgID)
		tokenB := redeemBindToken(t, router, bindTokenB, "temporary-agent-b")
		agentB := finalizeAgentHandleAndGet(t, router, tokenB, "launch-agent-b")

		myAgents := doJSONRequest(t, router, http.MethodGet, "/v1/me/agents", nil, humanHeaders("alice", "alice@a.test"))
		if myAgents.Code != http.StatusOK {
			t.Fatalf("expected /v1/me/agents 200, got %d %s", myAgents.Code, myAgents.Body.String())
		}
		agents := requireAgentList(t, decodeJSONMap(t, myAgents.Body.Bytes()))
		requireAgentStatus(t, agents, asString(t, agentA, "agent_uuid"), "active")
		requireAgentStatus(t, agents, asString(t, agentB, "agent_uuid"), "active")
	})

	t.Run("Alice binds two agents and revokes both agents", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Agents")

		bindTokenA := createMyBindToken(t, router, "alice", "alice@a.test", orgID)
		tokenA := redeemBindToken(t, router, bindTokenA, "temporary-agent-a")
		agentA := finalizeAgentHandleAndGet(t, router, tokenA, "launch-agent-a")

		bindTokenB := createMyBindToken(t, router, "alice", "alice@a.test", orgID)
		tokenB := redeemBindToken(t, router, bindTokenB, "temporary-agent-b")
		agentB := finalizeAgentHandleAndGet(t, router, tokenB, "launch-agent-b")

		revokeAgentForSmoke(t, router, "alice", "alice@a.test", asString(t, agentA, "agent_uuid"))
		revokeAgentForSmoke(t, router, "alice", "alice@a.test", asString(t, agentB, "agent_uuid"))

		revokedAAuth := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
			"Authorization": "Bearer " + tokenA,
		})
		if revokedAAuth.Code != http.StatusUnauthorized {
			t.Fatalf("expected revoked first agent token to fail with 401, got %d %s", revokedAAuth.Code, revokedAAuth.Body.String())
		}

		revokedBAuth := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
			"Authorization": "Bearer " + tokenB,
		})
		if revokedBAuth.Code != http.StatusUnauthorized {
			t.Fatalf("expected revoked second agent token to fail with 401, got %d %s", revokedBAuth.Code, revokedBAuth.Body.String())
		}

		myAgents := doJSONRequest(t, router, http.MethodGet, "/v1/me/agents", nil, humanHeaders("alice", "alice@a.test"))
		if myAgents.Code != http.StatusOK {
			t.Fatalf("expected /v1/me/agents 200 after revoke, got %d %s", myAgents.Code, myAgents.Body.String())
		}
		agents := requireAgentList(t, decodeJSONMap(t, myAgents.Body.Bytes()))
		requireAgentStatus(t, agents, asString(t, agentA, "agent_uuid"), "revoked")
		requireAgentStatus(t, agents, asString(t, agentB, "agent_uuid"), "revoked")
	})

	t.Run("Alice deletes a revoked agent record and it cannot be deleted twice", func(t *testing.T) {
		router := newTestRouter()
		orgID := createOrg(t, router, "alice", "alice@a.test", "Launch Agents")
		aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
		_, agentUUID := registerAgentWithUUID(t, router, "alice", "alice@a.test", orgID, "launch-agent-delete", aliceHumanID)

		revokeAgentForSmoke(t, router, "alice", "alice@a.test", agentUUID)
		deleteAgentRecordForSmoke(t, router, "alice", "alice@a.test", agentUUID)

		deleteAgain := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+agentUUID+"/record", nil, humanHeaders("alice", "alice@a.test"))
		if deleteAgain.Code != http.StatusNotFound {
			t.Fatalf("expected second delete to return 404, got %d %s", deleteAgain.Code, deleteAgain.Body.String())
		}
	})

	t.Run("Super-admin pairs two moltenhub instances and agents exchange over canonical URIs", func(t *testing.T) {
		alpha := newFederatedTestServer(t, "https://alpha.example")
		beta := newFederatedTestServer(t, "https://beta.example")

		orgA := createOrg(t, alpha.router, "alice", "alice@a.test", "Org Alpha")
		aliceHumanID := currentHumanID(t, alpha.router, "alice", "alice@a.test")
		tokenA, agentUUIDA := registerFederatedAgentWithUUID(t, alpha.router, "alice", "alice@a.test", orgA, "agent-a", aliceHumanID)
		agentA := currentAgent(t, alpha.router, tokenA)
		agentURIA := asString(t, agentA, "uri")

		orgB := createOrg(t, beta.router, "bob", "bob@b.test", "Org Beta")
		bobHumanID := currentHumanID(t, beta.router, "bob", "bob@b.test")
		tokenB, agentUUIDB := registerFederatedAgentWithUUID(t, beta.router, "bob", "bob@b.test", orgB, "agent-b", bobHumanID)
		agentB := currentAgent(t, beta.router, tokenB)
		agentURIB := asString(t, agentB, "uri")

		const peerID = "alpha-beta"
		// Fake shared-secret fixture; required to pair in-memory federated test servers.
		const secret = "peer-shared-secret"
		createPeer(t, alpha.router, peerID, beta.canonicalBase, beta.server.URL, secret)
		createPeer(t, beta.router, peerID, alpha.canonicalBase, alpha.server.URL, secret)
		createRemoteOrgTrustAdmin(t, alpha.router, orgA, peerID, "org-beta")
		createRemoteOrgTrustAdmin(t, beta.router, orgB, peerID, "org-alpha")
		createRemoteAgentTrustAdmin(t, alpha.router, agentUUIDA, peerID, agentURIB)
		createRemoteAgentTrustAdmin(t, beta.router, agentUUIDB, peerID, agentURIA)

		pubResp := publishByURI(t, alpha.router, tokenA, agentURIB, "smoke-federation")
		if pubResp.Code != http.StatusAccepted {
			t.Fatalf("expected federated publish 202, got %d %s", pubResp.Code, pubResp.Body.String())
		}

		pullResp := pull(t, beta.router, tokenB, 10)
		if pullResp.Code != http.StatusOK {
			t.Fatalf("expected federated pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
		}
		message := decodeJSONMap(t, pullResp.Body.Bytes())["message"].(map[string]any)
		if got := asString(t, message, "from_agent_uri"); got != agentURIA {
			t.Fatalf("expected from_agent_uri %q, got %q", agentURIA, got)
		}
		if got := asString(t, message, "to_agent_uri"); got != agentURIB {
			t.Fatalf("expected to_agent_uri %q, got %q", agentURIB, got)
		}
		if got := asString(t, message, "payload"); got != "smoke-federation" {
			t.Fatalf("expected payload smoke-federation, got %q", got)
		}
	})
}

func setHumanHandle(t *testing.T, router http.Handler, humanID, email, handle string) *httptest.ResponseRecorder {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
		"handle": handle,
	}, humanHeaders(humanID, email))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected handle set 200, got %d %s", resp.Code, resp.Body.String())
	}
	return resp
}

func patchHumanMetadata(t *testing.T, router http.Handler, humanID, email string, metadata map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPatch, "/v1/me/metadata", map[string]any{
		"metadata": metadata,
	}, humanHeaders(humanID, email))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected human metadata patch 200, got %d %s", resp.Code, resp.Body.String())
	}
	return resp
}

func patchOrgMetadata(t *testing.T, router http.Handler, humanID, email, orgID string, metadata map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPatch, "/v1/orgs/"+orgID+"/metadata", map[string]any{
		"metadata": metadata,
	}, humanHeaders(humanID, email))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected org metadata patch 200, got %d %s", resp.Code, resp.Body.String())
	}
	return resp
}

func createMyBindToken(t *testing.T, router http.Handler, humanID, email, orgID string) string {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents/bind-tokens", map[string]any{
		"org_id": orgID,
	}, humanHeaders(humanID, email))
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected bind token create 201, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	return asString(t, payload, "bind_token")
}

func redeemBindToken(t *testing.T, router http.Handler, bindToken, agentID string) string {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]any{
		"bind_token": bindToken,
		"handle":     agentID,
	}, nil)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected bind token redeem 201, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	return asString(t, payload, "token")
}

func patchAgentHandle(t *testing.T, router http.Handler, token, handle string) *httptest.ResponseRecorder {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me", map[string]any{
		"handle": handle,
	}, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected agent handle patch 200, got %d %s", resp.Code, resp.Body.String())
	}
	return resp
}

func currentAgent(t *testing.T, router http.Handler, token string) map[string]any {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected /v1/agents/me 200, got %d %s", resp.Code, resp.Body.String())
	}
	return requireEntity(t, decodeJSONMap(t, resp.Body.Bytes()), "agent")
}

func patchAgentMetadata(t *testing.T, router http.Handler, token string, metadata map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": metadata,
	}, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected agent metadata patch 200, got %d %s", resp.Code, resp.Body.String())
	}
	return resp
}

func createBoundAgentForSmoke(t *testing.T, router http.Handler, orgName, handle string) string {
	t.Helper()
	orgID := createOrg(t, router, "alice", "alice@a.test", orgName)
	bindToken := createMyBindToken(t, router, "alice", "alice@a.test", orgID)
	token := redeemBindToken(t, router, bindToken, "temporary-agent-name")
	finalizeAgentHandleAndGet(t, router, token, handle)
	return token
}

func finalizeAgentHandleAndGet(t *testing.T, router http.Handler, token, handle string) map[string]any {
	t.Helper()
	resp := patchAgentHandle(t, router, token, handle)
	return requireEntity(t, decodeJSONMap(t, resp.Body.Bytes()), "agent")
}

func revokeAgentForSmoke(t *testing.T, router http.Handler, humanID, email, agentUUID string) {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+agentUUID, nil, humanHeaders(humanID, email))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected revoke 200, got %d %s", resp.Code, resp.Body.String())
	}
}

func deleteAgentRecordForSmoke(t *testing.T, router http.Handler, humanID, email, agentUUID string) {
	t.Helper()
	resp := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+agentUUID+"/record", nil, humanHeaders(humanID, email))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected delete 200, got %d %s", resp.Code, resp.Body.String())
	}
}

func asString(t *testing.T, payload map[string]any, key string) string {
	t.Helper()
	value, _ := payload[key].(string)
	if value == "" {
		t.Fatalf("expected non-empty %q in payload=%v", key, payload)
	}
	return value
}

func requireErrorCode(t *testing.T, payload map[string]any, want string) {
	t.Helper()
	if payload["error"] != want {
		t.Fatalf("expected error %q, got %v payload=%v", want, payload["error"], payload)
	}
}

func requireEntityMetadata(t *testing.T, payload map[string]any, entityKey string, want map[string]any) {
	t.Helper()
	entity := requireEntity(t, payload, entityKey)
	if len(want) == 0 {
		got, exists := entity["metadata"]
		if !exists || got == nil {
			return
		}
		gotMap, ok := got.(map[string]any)
		if ok && len(gotMap) == 0 {
			return
		}
		if entityKey == "agent" && ok && len(gotMap) == 1 && gotMap["agent_type"] == "unknown" {
			return
		}
		t.Fatalf("expected %s.metadata to be empty or omitted, got %v payload=%v", entityKey, got, payload)
	}
	got, ok := entity["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected %s.metadata object, got %T payload=%v", entityKey, entity["metadata"], payload)
	}
	if entityKey == "agent" {
		normalizedWant := make(map[string]any, len(want)+1)
		for key, value := range want {
			normalizedWant[key] = value
		}
		if _, ok := normalizedWant["agent_type"]; !ok {
			normalizedWant["agent_type"] = "unknown"
		}
		if !reflect.DeepEqual(got, normalizedWant) {
			t.Fatalf("expected %s.metadata=%v, got %v", entityKey, normalizedWant, got)
		}
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %s.metadata=%v, got %v", entityKey, want, got)
	}
}

func requireEntity(t *testing.T, payload map[string]any, entityKey string) map[string]any {
	t.Helper()
	entity, ok := payload[entityKey].(map[string]any)
	if !ok {
		t.Fatalf("expected %s object, got %T payload=%v", entityKey, payload[entityKey], payload)
	}
	return entity
}

func membershipHasOrg(t *testing.T, payload map[string]any, orgID string) bool {
	t.Helper()
	if _, exists := payload["memberships"]; !exists {
		return false
	}
	memberships, ok := payload["memberships"].([]any)
	if !ok {
		t.Fatalf("expected memberships array, got %T payload=%v", payload["memberships"], payload)
	}
	for _, raw := range memberships {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		org, ok := row["org"].(map[string]any)
		if !ok {
			continue
		}
		if org["org_id"] == orgID {
			return true
		}
	}
	return false
}

func requireAgentList(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	rawAgents, ok := payload["agents"].([]any)
	if !ok {
		t.Fatalf("expected agents array, got %T payload=%v", payload["agents"], payload)
	}
	out := make([]map[string]any, 0, len(rawAgents))
	for _, raw := range rawAgents {
		agent, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("expected agent row object, got %T payload=%v", raw, payload)
		}
		out = append(out, agent)
	}
	return out
}

func requireAgentStatus(t *testing.T, agents []map[string]any, agentUUID, wantStatus string) {
	t.Helper()
	for _, agent := range agents {
		if agent["agent_uuid"] != agentUUID {
			continue
		}
		if agent["status"] != wantStatus {
			t.Fatalf("expected agent %q status %q, got %v payload=%v", agentUUID, wantStatus, agent["status"], agent)
		}
		return
	}
	t.Fatalf("agent %q not found in list %v", agentUUID, agents)
}
