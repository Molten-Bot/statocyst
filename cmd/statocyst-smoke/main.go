package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"
)

type runner struct {
	baseURL string
	client  *http.Client

	aliceOrgID  string
	agentsOrgID string

	tokenA     string
	tokenB     string
	agentUUIDA string
	agentUUIDB string
}

type step struct {
	name string
	run  func(*runner) error
}

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:18080", "Statocyst base URL")
	flag.Parse()

	r := &runner{
		baseURL: strings.TrimRight(*baseURL, "/"),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}

	steps := []step{
		{name: "Health endpoint responds and reports ok", run: (*runner).stepHealth},
		{name: "Alice creates handle", run: (*runner).stepAliceCreatesHandle},
		{name: "Bob tries to add the same handle and gets an error", run: (*runner).stepBobCannotTakeAliceHandle},
		{name: "Alice adds metadata to her profile", run: (*runner).stepAliceAddsProfileMetadata},
		{name: "Alice changes metadata from her profile", run: (*runner).stepAliceChangesProfileMetadata},
		{name: "Alice clears metadata from her profile", run: (*runner).stepAliceClearsProfileMetadata},
		{name: "Alice creates an organization", run: (*runner).stepAliceCreatesOrganization},
		{name: "Bob tries to add an organization with the same handle and gets an error", run: (*runner).stepBobCannotTakeOrgHandle},
		{name: "Alice adds metadata to an organization", run: (*runner).stepAliceAddsOrgMetadata},
		{name: "Alice changes metadata to an organization", run: (*runner).stepAliceChangesOrgMetadata},
		{name: "Alice clears metadata from an organization", run: (*runner).stepAliceClearsOrgMetadata},
		{name: "Alice creates an organization and deletes it", run: (*runner).stepAliceDeletesOrganization},
		{name: "Alice creates a bind token and an agent binds successfully", run: (*runner).stepAgentBinds},
		{name: "Alice creates a bind token and the agent updates its profile handle", run: (*runner).stepAgentFinalizesHandle},
		{name: "Alice creates a bind token and the agent tries to add an existing handle and gets an error", run: (*runner).stepAgentDuplicateHandleRejected},
		{name: "Alice creates a bind token and the agent adds profile metadata", run: (*runner).stepAgentAddsMetadata},
		{name: "Alice creates a bind token and the agent changes profile metadata", run: (*runner).stepAgentChangesMetadata},
		{name: "Alice creates a bind token and the agent clears profile metadata", run: (*runner).stepAgentClearsMetadata},
		{name: "Alice invites two agents by bind token, binds both agents, and sees both in her list", run: (*runner).stepAliceSeesBothAgents},
		{name: "Alice binds an agent and revokes it", run: (*runner).stepAliceRevokesFirstAgent},
		{name: "Alice binds two agents and revokes both agents", run: (*runner).stepAliceRevokesBothAgents},
	}

	for _, st := range steps {
		fmt.Printf("RUN  %s\n", st.name)
		if err := st.run(r); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", st.name, err)
			os.Exit(1)
		}
		fmt.Printf("PASS %s\n", st.name)
	}
}

func (r *runner) stepHealth() error {
	status, payload, err := r.requestJSON(http.MethodGet, "/health", nil, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200, got %d payload=%v", status, payload)
	}
	if payload["status"] != "ok" {
		return fmt.Errorf("expected status ok, got %v", payload["status"])
	}
	return nil
}

func (r *runner) stepAliceCreatesHandle() error {
	payload, err := r.setHumanHandle("alice", "alice@a.test", "alice")
	if err != nil {
		return err
	}
	human, err := requireEntity(payload, "human")
	if err != nil {
		return err
	}
	if human["handle"] != "alice" {
		return fmt.Errorf("expected handle alice, got %v", human["handle"])
	}
	return nil
}

func (r *runner) stepBobCannotTakeAliceHandle() error {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/me", humanHeaders("bob", "bob@b.test"), map[string]any{
		"handle": "alice",
	})
	if err != nil {
		return err
	}
	if status != http.StatusConflict {
		return fmt.Errorf("expected 409, got %d payload=%v", status, payload)
	}
	return requireErrorCode(payload, "human_handle_exists")
}

func (r *runner) stepAliceAddsProfileMetadata() error {
	payload, err := r.patchHumanMetadata("alice", "alice@a.test", map[string]any{
		"public": true,
		"bio":    "Alice launch smoke profile",
	})
	if err != nil {
		return err
	}
	return requireEntityMetadata(payload, "human", map[string]any{
		"public": true,
		"bio":    "Alice launch smoke profile",
	})
}

func (r *runner) stepAliceChangesProfileMetadata() error {
	payload, err := r.patchHumanMetadata("alice", "alice@a.test", map[string]any{
		"public": true,
		"bio":    "Alice launch smoke profile updated",
		"stage":  "launch",
	})
	if err != nil {
		return err
	}
	return requireEntityMetadata(payload, "human", map[string]any{
		"public": true,
		"bio":    "Alice launch smoke profile updated",
		"stage":  "launch",
	})
}

func (r *runner) stepAliceClearsProfileMetadata() error {
	payload, err := r.patchHumanMetadata("alice", "alice@a.test", map[string]any{})
	if err != nil {
		return err
	}
	return requireEntityMetadata(payload, "human", map[string]any{})
}

func (r *runner) stepAliceCreatesOrganization() error {
	orgID, err := r.createOrg("alice", "alice@a.test", "launch-alpha", "Launch Alpha")
	if err != nil {
		return err
	}
	r.aliceOrgID = orgID

	status, payload, err := r.requestJSON(http.MethodGet, "/v1/me/orgs", humanHeaders("alice", "alice@a.test"), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected /v1/me/orgs 200, got %d payload=%v", status, payload)
	}
	if !membershipHasOrg(payload, orgID) {
		return fmt.Errorf("created org %q missing from /v1/me/orgs", orgID)
	}
	return nil
}

func (r *runner) stepBobCannotTakeOrgHandle() error {
	if _, err := r.setHumanHandle("bob", "bob@b.test", "bob"); err != nil {
		return err
	}

	status, payload, err := r.requestJSON(http.MethodPost, "/v1/orgs", humanHeaders("bob", "bob@b.test"), map[string]any{
		"handle":       "launch-alpha",
		"display_name": "Launch Alpha Duplicate",
	})
	if err != nil {
		return err
	}
	if status != http.StatusConflict {
		return fmt.Errorf("expected 409, got %d payload=%v", status, payload)
	}
	return requireErrorCode(payload, "org_handle_exists")
}

func (r *runner) stepAliceAddsOrgMetadata() error {
	payload, err := r.patchOrgMetadata(r.aliceOrgID, map[string]any{
		"public":      true,
		"description": "Launch Alpha Organization",
	})
	if err != nil {
		return err
	}
	return requireEntityMetadata(payload, "organization", map[string]any{
		"public":      true,
		"description": "Launch Alpha Organization",
	})
}

func (r *runner) stepAliceChangesOrgMetadata() error {
	payload, err := r.patchOrgMetadata(r.aliceOrgID, map[string]any{
		"public":      true,
		"description": "Launch Alpha Organization Updated",
		"stage":       "launch",
	})
	if err != nil {
		return err
	}
	return requireEntityMetadata(payload, "organization", map[string]any{
		"public":      true,
		"description": "Launch Alpha Organization Updated",
		"stage":       "launch",
	})
}

func (r *runner) stepAliceClearsOrgMetadata() error {
	payload, err := r.patchOrgMetadata(r.aliceOrgID, map[string]any{})
	if err != nil {
		return err
	}
	return requireEntityMetadata(payload, "organization", map[string]any{})
}

func (r *runner) stepAliceDeletesOrganization() error {
	orgID, err := r.createOrg("alice", "alice@a.test", "launch-alpha-delete", "Launch Alpha Delete")
	if err != nil {
		return err
	}
	status, payload, err := r.requestJSON(http.MethodDelete, "/v1/orgs/"+orgID, humanHeaders("alice", "alice@a.test"), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected delete 200, got %d payload=%v", status, payload)
	}
	if payload["result"] != "deleted" {
		return fmt.Errorf("expected result deleted, got %v", payload["result"])
	}
	return nil
}

func (r *runner) stepAgentBinds() error {
	orgID, err := r.createOrg("alice", "alice@a.test", "launch-agents", "Launch Agents")
	if err != nil {
		return err
	}
	r.agentsOrgID = orgID

	bindToken, err := r.createBindToken(orgID)
	if err != nil {
		return err
	}
	token, err := r.redeemBindToken(bindToken, "temporary-agent-name")
	if err != nil {
		return err
	}
	r.tokenA = token
	agent, err := r.currentAgent(token)
	if err != nil {
		return err
	}
	if agent["status"] != "active" {
		return fmt.Errorf("expected bound agent active, got %v payload=%v", agent["status"], agent)
	}
	return nil
}

func (r *runner) stepAgentFinalizesHandle() error {
	payload, err := r.patchAgentHandle(r.tokenA, "launch-agent-a")
	if err != nil {
		return err
	}
	agent, err := requireEntity(payload, "agent")
	if err != nil {
		return err
	}
	r.agentUUIDA = asString(agent, "agent_uuid")
	if agent["handle"] != "launch-agent-a" {
		return fmt.Errorf("expected finalized handle launch-agent-a, got %v", agent["handle"])
	}
	return nil
}

func (r *runner) stepAgentDuplicateHandleRejected() error {
	bindToken, err := r.createBindToken(r.agentsOrgID)
	if err != nil {
		return err
	}
	token, err := r.redeemBindToken(bindToken, "temporary-agent-name-b")
	if err != nil {
		return err
	}
	r.tokenB = token

	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/agents/me", agentHeaders(token), map[string]any{
		"handle": "launch-agent-a",
	})
	if err != nil {
		return err
	}
	if status != http.StatusConflict {
		return fmt.Errorf("expected 409, got %d payload=%v", status, payload)
	}
	if err := requireErrorCode(payload, "agent_exists"); err != nil {
		return err
	}

	finalized, err := r.patchAgentHandle(token, "launch-agent-b")
	if err != nil {
		return err
	}
	agent, err := requireEntity(finalized, "agent")
	if err != nil {
		return err
	}
	r.agentUUIDB = asString(agent, "agent_uuid")
	return nil
}

func (r *runner) stepAgentAddsMetadata() error {
	payload, err := r.patchAgentMetadata(r.tokenA, map[string]any{
		"public": true,
		"role":   "primary",
	})
	if err != nil {
		return err
	}
	return requireEntityMetadata(payload, "agent", map[string]any{
		"public": true,
		"role":   "primary",
	})
}

func (r *runner) stepAgentChangesMetadata() error {
	payload, err := r.patchAgentMetadata(r.tokenA, map[string]any{
		"public": true,
		"role":   "primary-updated",
		"stage":  "launch",
	})
	if err != nil {
		return err
	}
	return requireEntityMetadata(payload, "agent", map[string]any{
		"public": true,
		"role":   "primary-updated",
		"stage":  "launch",
	})
}

func (r *runner) stepAgentClearsMetadata() error {
	payload, err := r.patchAgentMetadata(r.tokenA, map[string]any{})
	if err != nil {
		return err
	}
	return requireEntityMetadata(payload, "agent", map[string]any{})
}

func (r *runner) stepAliceRevokesFirstAgent() error {
	status, payload, err := r.requestJSON(http.MethodDelete, "/v1/agents/"+r.agentUUIDA, humanHeaders("alice", "alice@a.test"), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected revoke 200, got %d payload=%v", status, payload)
	}

	status, payload, err = r.requestJSON(http.MethodGet, "/v1/agents/me", agentHeaders(r.tokenA), nil)
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized {
		return fmt.Errorf("expected revoked token 401, got %d payload=%v", status, payload)
	}
	return nil
}

func (r *runner) stepAliceSeesBothAgents() error {
	status, payload, err := r.requestJSON(http.MethodGet, "/v1/me/agents", humanHeaders("alice", "alice@a.test"), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected /v1/me/agents 200, got %d payload=%v", status, payload)
	}
	agents, err := requireAgentList(payload)
	if err != nil {
		return err
	}
	if err := requireAgentStatus(agents, r.agentUUIDA, "active"); err != nil {
		return err
	}
	if err := requireAgentStatus(agents, r.agentUUIDB, "active"); err != nil {
		return err
	}
	return nil
}

func (r *runner) stepAliceRevokesBothAgents() error {
	status, payload, err := r.requestJSON(http.MethodDelete, "/v1/agents/"+r.agentUUIDB, humanHeaders("alice", "alice@a.test"), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected second revoke 200, got %d payload=%v", status, payload)
	}

	status, payload, err = r.requestJSON(http.MethodGet, "/v1/agents/me", agentHeaders(r.tokenB), nil)
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized {
		return fmt.Errorf("expected second revoked token 401, got %d payload=%v", status, payload)
	}

	status, payload, err = r.requestJSON(http.MethodGet, "/v1/me/agents", humanHeaders("alice", "alice@a.test"), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected /v1/me/agents 200 after revoke, got %d payload=%v", status, payload)
	}
	agents, err := requireAgentList(payload)
	if err != nil {
		return err
	}
	if err := requireAgentStatus(agents, r.agentUUIDA, "revoked"); err != nil {
		return err
	}
	return requireAgentStatus(agents, r.agentUUIDB, "revoked")
}

func (r *runner) setHumanHandle(humanID, email, handle string) (map[string]any, error) {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/me", humanHeaders(humanID, email), map[string]any{
		"handle": handle,
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("expected handle set 200, got %d payload=%v", status, payload)
	}
	return payload, nil
}

func (r *runner) patchHumanMetadata(humanID, email string, metadata map[string]any) (map[string]any, error) {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/me/metadata", humanHeaders(humanID, email), map[string]any{
		"metadata": metadata,
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("expected human metadata patch 200, got %d payload=%v", status, payload)
	}
	return payload, nil
}

func (r *runner) createOrg(humanID, email, handle, displayName string) (string, error) {
	status, payload, err := r.requestJSON(http.MethodPost, "/v1/orgs", humanHeaders(humanID, email), map[string]any{
		"handle":       handle,
		"display_name": displayName,
	})
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("expected org create 201, got %d payload=%v", status, payload)
	}
	org, err := requireEntity(payload, "organization")
	if err != nil {
		return "", err
	}
	return asString(org, "org_id"), nil
}

func (r *runner) patchOrgMetadata(orgID string, metadata map[string]any) (map[string]any, error) {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/orgs/"+orgID+"/metadata", humanHeaders("alice", "alice@a.test"), map[string]any{
		"metadata": metadata,
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("expected org metadata patch 200, got %d payload=%v", status, payload)
	}
	return payload, nil
}

func (r *runner) createBindToken(orgID string) (string, error) {
	status, payload, err := r.requestJSON(http.MethodPost, "/v1/me/agents/bind-tokens", humanHeaders("alice", "alice@a.test"), map[string]any{
		"org_id": orgID,
	})
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("expected bind token create 201, got %d payload=%v", status, payload)
	}
	return asString(payload, "bind_token"), nil
}

func (r *runner) redeemBindToken(bindToken, agentID string) (string, error) {
	status, payload, err := r.requestJSON(http.MethodPost, "/v1/agents/bind", nil, map[string]any{
		"bind_token": bindToken,
		"agent_id":   agentID,
	})
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("expected bind token redeem 201, got %d payload=%v", status, payload)
	}
	return asString(payload, "token"), nil
}

func (r *runner) patchAgentHandle(token, handle string) (map[string]any, error) {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/agents/me", agentHeaders(token), map[string]any{
		"handle": handle,
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("expected agent handle patch 200, got %d payload=%v", status, payload)
	}
	return payload, nil
}

func (r *runner) currentAgent(token string) (map[string]any, error) {
	status, payload, err := r.requestJSON(http.MethodGet, "/v1/agents/me", agentHeaders(token), nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("expected /v1/agents/me 200, got %d payload=%v", status, payload)
	}
	return requireEntity(payload, "agent")
}

func (r *runner) patchAgentMetadata(token string, metadata map[string]any) (map[string]any, error) {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/agents/me/metadata", agentHeaders(token), map[string]any{
		"metadata": metadata,
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("expected agent metadata patch 200, got %d payload=%v", status, payload)
	}
	return payload, nil
}

func (r *runner) requestJSON(method, path string, headers map[string]string, body any) (int, map[string]any, error) {
	var requestBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal request body: %w", err)
		}
		requestBody = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, r.baseURL+path, requestBody)
	if err != nil {
		return 0, nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("perform request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read response %s %s: %w", method, path, err)
	}
	if len(raw) == 0 {
		return resp.StatusCode, map[string]any{}, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, nil, fmt.Errorf("decode response %s %s: %w body=%s", method, path, err, string(raw))
	}
	return resp.StatusCode, payload, nil
}

func humanHeaders(humanID, email string) map[string]string {
	return map[string]string{
		"X-Human-Id":    humanID,
		"X-Human-Email": email,
	}
}

func agentHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
	}
}

func requireErrorCode(payload map[string]any, want string) error {
	if payload["error"] != want {
		return fmt.Errorf("expected error %q, got %v payload=%v", want, payload["error"], payload)
	}
	return nil
}

func requireEntityMetadata(payload map[string]any, entityKey string, want map[string]any) error {
	entity, err := requireEntity(payload, entityKey)
	if err != nil {
		return err
	}
	if len(want) == 0 {
		got, exists := entity["metadata"]
		if !exists || got == nil {
			return nil
		}
		gotMap, ok := got.(map[string]any)
		if ok && len(gotMap) == 0 {
			return nil
		}
		return fmt.Errorf("expected %s.metadata empty or omitted, got %v payload=%v", entityKey, got, payload)
	}
	got, ok := entity["metadata"].(map[string]any)
	if !ok {
		return fmt.Errorf("expected %s.metadata object, got %T payload=%v", entityKey, entity["metadata"], payload)
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("expected %s.metadata=%v, got %v", entityKey, want, got)
	}
	return nil
}

func requireEntity(payload map[string]any, entityKey string) (map[string]any, error) {
	entity, ok := payload[entityKey].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected %s object, got %T payload=%v", entityKey, payload[entityKey], payload)
	}
	return entity, nil
}

func membershipHasOrg(payload map[string]any, orgID string) bool {
	raw, exists := payload["memberships"]
	if !exists {
		return false
	}
	rows, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, entry := range rows {
		row, ok := entry.(map[string]any)
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

func requireAgentList(payload map[string]any) ([]map[string]any, error) {
	raw, ok := payload["agents"].([]any)
	if !ok {
		return nil, fmt.Errorf("expected agents array, got %T payload=%v", payload["agents"], payload)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		agent, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected agent row object, got %T payload=%v", entry, payload)
		}
		out = append(out, agent)
	}
	return out, nil
}

func requireAgentStatus(agents []map[string]any, agentUUID, wantStatus string) error {
	for _, agent := range agents {
		if agent["agent_uuid"] != agentUUID {
			continue
		}
		if agent["status"] != wantStatus {
			return fmt.Errorf("expected agent %q status %q, got %v payload=%v", agentUUID, wantStatus, agent["status"], agent)
		}
		return nil
	}
	return fmt.Errorf("agent %q not found in list %v", agentUUID, agents)
}

func asString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}
