package api

import (
	"net/http"
	"strings"
	"testing"
)

func registerAgentWithDetails(t *testing.T, router http.Handler, humanID, email, orgID, agentID, ownerHumanID string) (string, string, string, string) {
	t.Helper()
	ensureHandleConfirmed(t, router, humanID, email)
	if ownerHumanID == "" {
		token, _ := bindAgentWithUUID(t, router, humanID, email, orgID, agentID)
		capsResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/capabilities", nil, map[string]string{
			"Authorization": "Bearer " + token,
		})
		if capsResp.Code != http.StatusOK {
			t.Fatalf("agent capabilities failed: %d %s", capsResp.Code, capsResp.Body.String())
		}
		capsPayload := decodeJSONMap(t, capsResp.Body.Bytes())
		agentObj, _ := capsPayload["agent"].(map[string]any)
		agentUUID, _ := agentObj["agent_uuid"].(string)
		canonicalID, _ := agentObj["agent_id"].(string)
		handle := canonicalID
		if idx := strings.LastIndex(canonicalID, "/"); idx >= 0 && idx < len(canonicalID)-1 {
			handle = canonicalID[idx+1:]
		}
		if token == "" || agentUUID == "" || canonicalID == "" || handle == "" {
			t.Fatalf("missing token/agent_uuid/agent_id/handle: %v", capsPayload)
		}
		return token, agentUUID, canonicalID, handle
	}

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents", map[string]any{
		"org_id":   orgID,
		"agent_id": agentID,
	}, humanHeaders(humanID, email))
	if resp.Code != http.StatusCreated {
		t.Fatalf("register my agent failed: %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	token, _ := payload["token"].(string)
	agentUUID, _ := payload["agent_uuid"].(string)
	canonicalID, _ := payload["agent_id"].(string)
	handle, _ := payload["handle"].(string)
	if token == "" || agentUUID == "" || canonicalID == "" || handle == "" {
		t.Fatalf("missing token/agent_uuid/agent_id/handle: %v", payload)
	}
	return token, agentUUID, canonicalID, handle
}

func TestHandleContractValidationRejectsShortAndBlocked(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")

	shortHuman := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
		"handle": "a",
	}, humanHeaders("alice", "alice@a.test"))
	if shortHuman.Code != http.StatusBadRequest {
		t.Fatalf("expected short human handle to fail with 400, got %d %s", shortHuman.Code, shortHuman.Body.String())
	}
	if decodeJSONMap(t, shortHuman.Body.Bytes())["error"] != "invalid_handle" {
		t.Fatalf("expected invalid_handle for short human handle")
	}

	blockedHuman := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
		"handle": "f.u.c.k",
	}, humanHeaders("alice", "alice@a.test"))
	if blockedHuman.Code != http.StatusBadRequest {
		t.Fatalf("expected blocked human handle to fail with 400, got %d %s", blockedHuman.Code, blockedHuman.Body.String())
	}
	if decodeJSONMap(t, blockedHuman.Body.Bytes())["error"] != "invalid_handle" {
		t.Fatalf("expected invalid_handle for blocked human handle")
	}

	ensureHandleConfirmed(t, router, "alice", "alice@a.test")

	shortOrg := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]any{
		"handle":       "a",
		"display_name": "Too Short",
	}, humanHeaders("alice", "alice@a.test"))
	if shortOrg.Code != http.StatusBadRequest {
		t.Fatalf("expected short org handle to fail with 400, got %d %s", shortOrg.Code, shortOrg.Body.String())
	}

	blockedOrg := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]any{
		"handle":       "s.h.i.t",
		"display_name": "Blocked",
	}, humanHeaders("alice", "alice@a.test"))
	if blockedOrg.Code != http.StatusBadRequest {
		t.Fatalf("expected blocked org handle to fail with 400, got %d %s", blockedOrg.Code, blockedOrg.Body.String())
	}

	orgID := createOrg(t, router, "alice", "alice@a.test", "Valid Org")

	shortAgent := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents", map[string]any{
		"org_id":   orgID,
		"agent_id": "a",
	}, humanHeaders("alice", "alice@a.test"))
	if shortAgent.Code != http.StatusBadRequest {
		t.Fatalf("expected short agent handle to fail with 400, got %d %s", shortAgent.Code, shortAgent.Body.String())
	}

	blockedAgent := doJSONRequest(t, router, http.MethodPost, "/v1/me/agents", map[string]any{
		"org_id":   orgID,
		"agent_id": "f.u.c.k",
	}, humanHeaders("alice", "alice@a.test"))
	if blockedAgent.Code != http.StatusBadRequest {
		t.Fatalf("expected blocked agent handle to fail with 400, got %d %s", blockedAgent.Code, blockedAgent.Body.String())
	}

	bindResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind-tokens", map[string]any{
		"org_id":         orgID,
		"owner_human_id": aliceHumanID,
	}, humanHeaders("alice", "alice@a.test"))
	if bindResp.Code != http.StatusCreated {
		t.Fatalf("expected bind token creation success, got %d %s", bindResp.Code, bindResp.Body.String())
	}
	bindToken, _ := decodeJSONMap(t, bindResp.Body.Bytes())["bind_token"].(string)
	if bindToken == "" {
		t.Fatalf("bind_token missing")
	}

	blockedRedeem := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]any{
		"bind_token": bindToken,
		"agent_id":   "f-u-c-k",
	}, nil)
	if blockedRedeem.Code != http.StatusCreated {
		t.Fatalf("expected bind redeem to ignore supplied agent_id and succeed, got %d %s", blockedRedeem.Code, blockedRedeem.Body.String())
	}
	if token, _ := decodeJSONMap(t, blockedRedeem.Body.Bytes())["token"].(string); strings.TrimSpace(token) == "" {
		t.Fatalf("expected bind redeem success payload to include token")
	}
}

func TestCanonicalAgentURIAndUUIDLifecycleRoutes(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	orgID := createOrg(t, router, "alice", "alice@a.test", "URI Org")

	_, agentUUID, canonicalAgentID, handle := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgID, "Alpha Bot", aliceHumanID)
	if handle != "alpha-bot" {
		t.Fatalf("expected normalized handle alpha-bot, got %q", handle)
	}
	if strings.Count(canonicalAgentID, "/") != 2 {
		t.Fatalf("expected human-owned canonical URI org/human/agent, got %q", canonicalAgentID)
	}

	metadata := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/"+agentUUID+"/metadata", map[string]any{
		"metadata": map[string]any{
			"public": false,
		},
	}, humanHeaders("alice", "alice@a.test"))
	if metadata.Code != http.StatusOK {
		t.Fatalf("expected metadata patch with agent_uuid to succeed, got %d %s", metadata.Code, metadata.Body.String())
	}
	metadataPayload := decodeJSONMap(t, metadata.Body.Bytes())
	agentObj, _ := metadataPayload["agent"].(map[string]any)
	if agentObj["agent_id"] != canonicalAgentID {
		t.Fatalf("expected visibility response agent_id=%q, got %v", canonicalAgentID, agentObj["agent_id"])
	}
	if agentObj["agent_uuid"] != agentUUID {
		t.Fatalf("expected visibility response agent_uuid=%q, got %v", agentUUID, agentObj["agent_uuid"])
	}

	rotate := doJSONRequest(t, router, http.MethodPost, "/v1/agents/"+agentUUID+"/rotate-token", nil, humanHeaders("alice", "alice@a.test"))
	if rotate.Code != http.StatusOK {
		t.Fatalf("expected rotate with agent_uuid to succeed, got %d %s", rotate.Code, rotate.Body.String())
	}
	if decodeJSONMap(t, rotate.Body.Bytes())["agent_uuid"] != agentUUID {
		t.Fatalf("expected rotate response to preserve agent_uuid")
	}

	revoke := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+agentUUID, nil, humanHeaders("alice", "alice@a.test"))
	if revoke.Code != http.StatusOK {
		t.Fatalf("expected revoke with agent_uuid to succeed, got %d %s", revoke.Code, revoke.Body.String())
	}
}

func TestPublishRequiresAgentUUIDReceiverRef(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Org A")

	senderToken, _, _, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "sender", aliceHumanID)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/messages/publish", map[string]string{
		"to_agent_uuid": "dup",
		"content_type":  "text/plain",
		"payload":       "hello",
	}, map[string]string{"Authorization": "Bearer " + senderToken})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid receiver uuid to return 400, got %d %s", resp.Code, resp.Body.String())
	}
	if decodeJSONMap(t, resp.Body.Bytes())["error"] != "invalid_to_agent_uuid" {
		t.Fatalf("expected invalid_to_agent_uuid error")
	}
}

func TestAgentTrustRequiresAgentUUIDRefs(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Trust Org A")

	_, initiatorUUID, _, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "initiator", aliceHumanID)

	invalid := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts", map[string]any{
		"org_id":          orgA,
		"agent_uuid":      initiatorUUID,
		"peer_agent_uuid": "dup",
	}, humanHeaders("alice", "alice@a.test"))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid agent trust refs to return 400, got %d %s", invalid.Code, invalid.Body.String())
	}
	if decodeJSONMap(t, invalid.Body.Bytes())["error"] != "invalid_agent_uuid" {
		t.Fatalf("expected invalid_agent_uuid error")
	}
}

func TestAgentTrustAcceptsAgentIDRefs(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Trust IDs")

	_, _, initiatorID, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "initiator", aliceHumanID)
	_, _, peerID, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "peer", aliceHumanID)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts", map[string]any{
		"org_id":        orgA,
		"agent_id":      initiatorID,
		"peer_agent_id": peerID,
	}, humanHeaders("alice", "alice@a.test"))
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected agent-trust create with agent_id refs to return 201, got %d %s", resp.Code, resp.Body.String())
	}
	if decodeJSONMap(t, resp.Body.Bytes())["trust"] == nil {
		t.Fatalf("expected trust payload")
	}
}

func TestAgentBindPathSupportsAgentRef(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Trust Path")

	_, _, initiatorID, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "initiator", aliceHumanID)
	_, _, peerID, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "peer", aliceHumanID)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/"+initiatorID+"/bind", map[string]any{
		"org_id":        orgA,
		"peer_agent_id": peerID,
	}, humanHeaders("alice", "alice@a.test"))
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected /v1/agents/{agent_ref}/bind to return 201, got %d %s", resp.Code, resp.Body.String())
	}
	if decodeJSONMap(t, resp.Body.Bytes())["trust"] == nil {
		t.Fatalf("expected trust payload from bind route")
	}
}

func TestAgentTrustRejectsMismatchedAgentUUIDAndAgentID(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Trust Mismatch")

	_, initiatorUUID, initiatorID, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "initiator", aliceHumanID)
	_, peerUUID, peerID, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "peer", aliceHumanID)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts", map[string]any{
		"org_id":          orgA,
		"agent_uuid":      initiatorUUID,
		"agent_id":        peerID,
		"peer_agent_uuid": peerUUID,
		"peer_agent_id":   peerID,
	}, humanHeaders("alice", "alice@a.test"))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected mismatched uuid/id to return 400, got %d %s", resp.Code, resp.Body.String())
	}
	if decodeJSONMap(t, resp.Body.Bytes())["error"] != "agent_ref_mismatch" {
		t.Fatalf("expected agent_ref_mismatch error")
	}

	checkAligned := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts", map[string]any{
		"org_id":          orgA,
		"agent_uuid":      initiatorUUID,
		"agent_id":        initiatorID,
		"peer_agent_uuid": peerUUID,
		"peer_agent_id":   peerID,
	}, humanHeaders("alice", "alice@a.test"))
	if checkAligned.Code != http.StatusCreated {
		t.Fatalf("expected aligned uuid/id refs to succeed, got %d %s", checkAligned.Code, checkAligned.Body.String())
	}
}

func TestAgentTrustRejectsAmbiguousAgentID(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Trust Ambiguous A")
	orgB := createOrg(t, router, "bob", "bob@b.test", "Trust Ambiguous B")

	_, _, _, _ = registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "shared", aliceHumanID)
	_, _, peerID, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "peer", aliceHumanID)
	_, _, _, _ = registerAgentWithDetails(t, router, "bob", "bob@b.test", orgB, "shared", bobHumanID)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts", map[string]any{
		"org_id":        orgA,
		"agent_id":      "shared",
		"peer_agent_id": peerID,
	}, humanHeaders("alice", "alice@a.test"))
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected ambiguous agent_id to return 409, got %d %s", resp.Code, resp.Body.String())
	}
	if decodeJSONMap(t, resp.Body.Bytes())["error"] != "ambiguous_agent_id" {
		t.Fatalf("expected ambiguous_agent_id error")
	}
}
