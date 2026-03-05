package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func registerAgentWithDetails(t *testing.T, router http.Handler, humanID, email, orgID, agentID, ownerHumanID string) (string, string, string) {
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
	payload := decodeJSONMap(t, resp.Body.Bytes())
	token, _ := payload["token"].(string)
	canonicalID, _ := payload["agent_id"].(string)
	handle, _ := payload["handle"].(string)
	if token == "" || canonicalID == "" || handle == "" {
		t.Fatalf("missing token/agent_id/handle: %v", payload)
	}
	return token, canonicalID, handle
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

	shortAgent := doJSONRequest(t, router, http.MethodPost, "/v1/agents/register", map[string]any{
		"org_id":         orgID,
		"owner_human_id": aliceHumanID,
		"agent_id":       "a",
	}, humanHeaders("alice", "alice@a.test"))
	if shortAgent.Code != http.StatusBadRequest {
		t.Fatalf("expected short agent handle to fail with 400, got %d %s", shortAgent.Code, shortAgent.Body.String())
	}

	blockedAgent := doJSONRequest(t, router, http.MethodPost, "/v1/agents/register", map[string]any{
		"org_id":         orgID,
		"owner_human_id": aliceHumanID,
		"agent_id":       "f.u.c.k",
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

	blockedRedeem := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind/redeem", map[string]any{
		"bind_token": bindToken,
		"agent_id":   "f-u-c-k",
	}, nil)
	if blockedRedeem.Code != http.StatusBadRequest {
		t.Fatalf("expected blocked bind redeem handle to fail with 400, got %d %s", blockedRedeem.Code, blockedRedeem.Body.String())
	}
	if decodeJSONMap(t, blockedRedeem.Body.Bytes())["error"] != "invalid_agent_id" {
		t.Fatalf("expected invalid_agent_id for blocked bind redeem handle")
	}
}

func TestCanonicalAgentURISupportsLifecycleRoutes(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	orgID := createOrg(t, router, "alice", "alice@a.test", "URI Org")

	_, canonicalAgentID, handle := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgID, "Alpha Bot", aliceHumanID)
	if handle != "alpha-bot" {
		t.Fatalf("expected normalized handle alpha-bot, got %q", handle)
	}
	if strings.Count(canonicalAgentID, "/") != 2 {
		t.Fatalf("expected human-owned canonical URI org/human/agent, got %q", canonicalAgentID)
	}

	visibility := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/"+canonicalAgentID, map[string]any{
		"is_public": false,
	}, humanHeaders("alice", "alice@a.test"))
	if visibility.Code != http.StatusOK {
		t.Fatalf("expected visibility patch with canonical URI to succeed, got %d %s", visibility.Code, visibility.Body.String())
	}
	visPayload := decodeJSONMap(t, visibility.Body.Bytes())
	agentObj, _ := visPayload["agent"].(map[string]any)
	if agentObj["agent_id"] != canonicalAgentID {
		t.Fatalf("expected visibility response agent_id=%q, got %v", canonicalAgentID, agentObj["agent_id"])
	}

	rotate := doJSONRequest(t, router, http.MethodPost, "/v1/agents/"+canonicalAgentID+"/rotate-token", nil, humanHeaders("alice", "alice@a.test"))
	if rotate.Code != http.StatusOK {
		t.Fatalf("expected rotate with canonical URI to succeed, got %d %s", rotate.Code, rotate.Body.String())
	}
	if decodeJSONMap(t, rotate.Body.Bytes())["agent_id"] != canonicalAgentID {
		t.Fatalf("expected rotate response to preserve canonical agent_id")
	}

	revoke := doJSONRequest(t, router, http.MethodDelete, "/v1/agents/"+canonicalAgentID, nil, humanHeaders("alice", "alice@a.test"))
	if revoke.Code != http.StatusOK {
		t.Fatalf("expected revoke with canonical URI to succeed, got %d %s", revoke.Code, revoke.Body.String())
	}
}

func TestAmbiguousReceiverRequiresCanonicalURI(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Org A")
	orgB := createOrg(t, router, "bob", "bob@b.test", "Org B")

	senderToken, _, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "sender", aliceHumanID)
	_, _, _ = registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "dup", aliceHumanID)
	_, dupBID, _ := registerAgentWithDetails(t, router, "bob", "bob@b.test", orgB, "dup", bobHumanID)

	ambiguous := publish(t, router, senderToken, "dup", "hello")
	if ambiguous.Code != http.StatusConflict {
		t.Fatalf("expected ambiguous publish to return 409, got %d %s", ambiguous.Code, ambiguous.Body.String())
	}
	if decodeJSONMap(t, ambiguous.Body.Bytes())["error"] != "ambiguous_to_agent_id" {
		t.Fatalf("expected ambiguous_to_agent_id error")
	}

	canonical := publish(t, router, senderToken, dupBID, "hello")
	if canonical.Code != http.StatusAccepted {
		t.Fatalf("expected canonical publish to be accepted, got %d %s", canonical.Code, canonical.Body.String())
	}
	if decodeJSONMap(t, canonical.Body.Bytes())["status"] != "dropped" {
		t.Fatalf("expected dropped status without trust path")
	}
}

func TestAmbiguousAgentTrustRequiresCanonicalURI(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "Trust Org A")
	orgB := createOrg(t, router, "bob", "bob@b.test", "Trust Org B")

	_, initiatorID, _ := registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "initiator", aliceHumanID)
	_, _, _ = registerAgentWithDetails(t, router, "alice", "alice@a.test", orgA, "dup", aliceHumanID)
	_, dupBID, _ := registerAgentWithDetails(t, router, "bob", "bob@b.test", orgB, "dup", bobHumanID)

	ambiguous := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts", map[string]any{
		"org_id":        orgA,
		"agent_id":      initiatorID,
		"peer_agent_id": "dup",
	}, humanHeaders("alice", "alice@a.test"))
	if ambiguous.Code != http.StatusConflict {
		t.Fatalf("expected ambiguous agent trust to return 409, got %d %s", ambiguous.Code, ambiguous.Body.String())
	}
	if decodeJSONMap(t, ambiguous.Body.Bytes())["error"] != "ambiguous_agent_id" {
		t.Fatalf("expected ambiguous_agent_id error")
	}

	canonical := doJSONRequest(t, router, http.MethodPost, "/v1/agent-trusts", map[string]any{
		"org_id":        orgA,
		"agent_id":      initiatorID,
		"peer_agent_id": dupBID,
	}, humanHeaders("alice", "alice@a.test"))
	if canonical.Code != http.StatusCreated {
		t.Fatalf("expected canonical agent trust creation to succeed, got %d %s", canonical.Code, canonical.Body.String())
	}

	payload := decodeJSONMap(t, canonical.Body.Bytes())
	trust, _ := payload["trust"].(map[string]any)
	if trust == nil {
		t.Fatalf("expected trust object in response")
	}
	left, _ := trust["left_id"].(string)
	right, _ := trust["right_id"].(string)
	if left != initiatorID && right != initiatorID {
		raw, _ := json.Marshal(trust)
		t.Fatalf("expected canonical initiator id in trust edge, got %s", string(raw))
	}
}
