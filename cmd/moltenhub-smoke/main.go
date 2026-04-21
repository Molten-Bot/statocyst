package main

import (
	"flag"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"moltenhub/internal/cmdutil"
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
	baseURL := flag.String("base-url", "http://127.0.0.1:18080", "MoltenHub base URL")
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
		{name: "Alice creates trust between both bound agents", run: (*runner).stepAliceCreatesAgentTrust},
		{name: "OpenClaw plugin registration succeeds for both agents", run: (*runner).stepOpenClawRegisterPlugin},
		{name: "OpenClaw HTTP publish/pull/ack succeeds between bound agents", run: (*runner).stepOpenClawHTTPDelivery},
		{name: "OpenClaw polling heartbeat marks runtime presence online", run: (*runner).stepOpenClawPresenceHeartbeat},
		{name: "OpenClaw websocket delivery and ack succeeds", run: (*runner).stepOpenClawWebSocketDelivery},
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
	human, err := cmdutil.RequireObject(payload, "human")
	if err != nil {
		return err
	}
	if human["handle"] != "alice" {
		return fmt.Errorf("expected handle alice, got %v", human["handle"])
	}
	return nil
}

func (r *runner) stepBobCannotTakeAliceHandle() error {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/me", cmdutil.HumanHeaders("bob", "bob@b.test"), map[string]any{
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

	status, payload, err := r.requestJSON(http.MethodGet, "/v1/me/orgs", cmdutil.HumanHeaders("alice", "alice@a.test"), nil)
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

	status, payload, err := r.requestJSON(http.MethodPost, "/v1/orgs", cmdutil.HumanHeaders("bob", "bob@b.test"), map[string]any{
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
	status, payload, err := r.requestJSON(http.MethodDelete, "/v1/orgs/"+orgID, cmdutil.HumanHeaders("alice", "alice@a.test"), nil)
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
	agent, err := cmdutil.RequireObject(payload, "agent")
	if err != nil {
		return err
	}
	r.agentUUIDA = cmdutil.AsString(agent, "agent_uuid")
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

	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/agents/me", cmdutil.AgentHeaders(token), map[string]any{
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
	agent, err := cmdutil.RequireObject(finalized, "agent")
	if err != nil {
		return err
	}
	r.agentUUIDB = cmdutil.AsString(agent, "agent_uuid")
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
	payload, err := r.patchAgentMetadata(r.tokenA, map[string]any{
		"public": nil,
		"role":   nil,
		"stage":  nil,
	})
	if err != nil {
		return err
	}
	return requireEntityMetadata(payload, "agent", map[string]any{})
}

func (r *runner) stepAliceRevokesFirstAgent() error {
	status, payload, err := r.requestJSON(http.MethodDelete, "/v1/agents/"+r.agentUUIDA, cmdutil.HumanHeaders("alice", "alice@a.test"), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected revoke 200, got %d payload=%v", status, payload)
	}

	status, payload, err = r.requestJSON(http.MethodGet, "/v1/agents/me", cmdutil.AgentHeaders(r.tokenA), nil)
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized {
		return fmt.Errorf("expected revoked token 401, got %d payload=%v", status, payload)
	}
	return nil
}

func (r *runner) stepAliceSeesBothAgents() error {
	status, payload, err := r.requestJSON(http.MethodGet, "/v1/me/agents", cmdutil.HumanHeaders("alice", "alice@a.test"), nil)
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

func (r *runner) stepAliceCreatesAgentTrust() error {
	status, payload, err := r.requestJSON(http.MethodPost, "/v1/agent-trusts", cmdutil.HumanHeaders("alice", "alice@a.test"), map[string]any{
		"org_id":          r.agentsOrgID,
		"agent_uuid":      r.agentUUIDA,
		"peer_agent_uuid": r.agentUUIDB,
	})
	if err != nil {
		return err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return fmt.Errorf("expected agent trust create 201/200, got %d payload=%v", status, payload)
	}
	trust, ok := payload["trust"].(map[string]any)
	if !ok {
		return fmt.Errorf("expected trust object payload=%v", payload)
	}
	edgeID := cmdutil.AsString(trust, "edge_id")
	if edgeID == "" {
		return fmt.Errorf("expected trust.edge_id payload=%v", payload)
	}
	if cmdutil.AsString(trust, "state") == "active" {
		return nil
	}

	status, payload, err = r.requestJSON(http.MethodPost, "/v1/agent-trusts/"+edgeID+"/approve", cmdutil.HumanHeaders("alice", "alice@a.test"), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected agent trust approve 200, got %d payload=%v", status, payload)
	}
	trust, ok = payload["trust"].(map[string]any)
	if !ok {
		return fmt.Errorf("expected trust object after approve payload=%v", payload)
	}
	if state := cmdutil.AsString(trust, "state"); state != "active" {
		return fmt.Errorf("expected trust state active after approve, got %q payload=%v", state, payload)
	}
	return nil
}

func (r *runner) stepAliceRevokesBothAgents() error {
	status, payload, err := r.requestJSON(http.MethodDelete, "/v1/agents/"+r.agentUUIDB, cmdutil.HumanHeaders("alice", "alice@a.test"), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected second revoke 200, got %d payload=%v", status, payload)
	}

	status, payload, err = r.requestJSON(http.MethodGet, "/v1/agents/me", cmdutil.AgentHeaders(r.tokenB), nil)
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized {
		return fmt.Errorf("expected second revoked token 401, got %d payload=%v", status, payload)
	}

	status, payload, err = r.requestJSON(http.MethodGet, "/v1/me/agents", cmdutil.HumanHeaders("alice", "alice@a.test"), nil)
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

func (r *runner) stepOpenClawRegisterPlugin() error {
	for _, target := range []struct {
		label string
		token string
	}{
		{label: "a", token: r.tokenA},
		{label: "b", token: r.tokenB},
	} {
		pluginID := "moltenhub-openclaw-smoke-" + target.label
		status, payload, err := r.requestJSON(http.MethodPost, "/v1/openclaw/messages/register-plugin", cmdutil.AgentHeaders(target.token), map[string]any{
			"plugin_id":    pluginID,
			"package":      "@moltenbot/openclaw-plugin-moltenhub",
			"transport":    "websocket",
			"session_mode": "dedicated",
			"session_key":  "smoke-main",
		})
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("expected register-plugin 200, got %d payload=%v", status, payload)
		}

		result := runtimeResult(payload)
		plugin, err := cmdutil.RequireObject(result, "plugin")
		if err != nil {
			return err
		}
		if got := cmdutil.AsString(plugin, "transport"); got != "websocket" {
			return fmt.Errorf("expected plugin transport websocket, got %q payload=%v", got, payload)
		}
		agent, err := cmdutil.RequireObject(result, "agent")
		if err != nil {
			return err
		}
		metadata, _ := agent["metadata"].(map[string]any)
		if got := readStringPath(metadata, "agent_type"); got != "openclaw" {
			return fmt.Errorf("expected metadata.agent_type openclaw, got %q payload=%v", got, payload)
		}
		plugins, _ := metadata["plugins"].(map[string]any)
		if _, ok := plugins[pluginID].(map[string]any); !ok {
			return fmt.Errorf("expected metadata.plugins.%s object, got %T payload=%v", pluginID, plugins[pluginID], payload)
		}
	}
	return nil
}

func (r *runner) stepOpenClawHTTPDelivery() error {
	if err := r.drainOpenClawQueue(r.tokenB); err != nil {
		return err
	}

	messageText := fmt.Sprintf("smoke-openclaw-http-%d", time.Now().UnixNano())
	messageID, err := r.publishOpenClawMessage(r.tokenA, r.agentUUIDB, messageText)
	if err != nil {
		return err
	}

	deliveryID, receivedText, err := r.pullOpenClawMessage(r.tokenB, messageID, 12*time.Second)
	if err != nil {
		return err
	}
	if receivedText != messageText {
		return fmt.Errorf("expected pull text %q, got %q", messageText, receivedText)
	}
	return r.ackOpenClawDeliveryHTTP(r.tokenB, deliveryID)
}

func (r *runner) stepOpenClawWebSocketDelivery() error {
	if err := r.drainOpenClawQueue(r.tokenB); err != nil {
		return err
	}

	conn, err := r.openOpenClawWebSocket(r.tokenB, fmt.Sprintf("smoke-session-%d", time.Now().UnixNano()))
	if err != nil {
		return err
	}
	defer conn.Close()

	messageText := fmt.Sprintf("smoke-openclaw-ws-%d", time.Now().UnixNano())
	messageID, err := r.publishOpenClawMessage(r.tokenA, r.agentUUIDB, messageText)
	if err != nil {
		return err
	}

	deliveryID, receivedText, err := r.waitForOpenClawWSDelivery(conn, messageID, 12*time.Second)
	if err != nil {
		return err
	}
	if receivedText != messageText {
		return fmt.Errorf("expected websocket delivery text %q, got %q", messageText, receivedText)
	}
	return r.ackOpenClawDeliveryWS(conn, deliveryID)
}

func (r *runner) stepOpenClawPresenceHeartbeat() error {
	if err := r.drainOpenClawQueue(r.tokenB); err != nil {
		return err
	}

	status, payload, err := r.requestJSON(http.MethodPost, "/v1/openclaw/messages/offline", cmdutil.AgentHeaders(r.tokenB), map[string]any{
		"session_key": "smoke-main",
		"reason":      "smoke presence heartbeat baseline",
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected openclaw offline 200, got %d payload=%v", status, payload)
	}

	status, payload, err = r.requestJSON(http.MethodGet, "/v1/openclaw/messages/pull?timeout_ms=0", cmdutil.AgentHeaders(r.tokenB), nil)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNoContent:
	case http.StatusOK:
		result := runtimeResult(payload)
		if deliveryID := readStringPath(result, "delivery", "delivery_id"); deliveryID != "" {
			if err := r.ackOpenClawDeliveryHTTP(r.tokenB, deliveryID); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("expected openclaw pull 200/204 for heartbeat, got %d payload=%v", status, payload)
	}

	agent, err := r.currentAgent(r.tokenB)
	if err != nil {
		return err
	}
	metadata, _ := agent["metadata"].(map[string]any)
	if got := readStringPath(metadata, "presence", "status"); got != "online" {
		return fmt.Errorf("expected presence status online after polling heartbeat, got %q payload=%v", got, agent)
	}
	if got := readStringPath(metadata, "presence", "ready"); got != "true" {
		return fmt.Errorf("expected presence ready true after polling heartbeat, got %q payload=%v", got, agent)
	}
	return nil
}

func (r *runner) publishOpenClawMessage(token, toAgentUUID, text string) (string, error) {
	status, payload, err := r.requestJSON(http.MethodPost, "/v1/openclaw/messages/publish", cmdutil.AgentHeaders(token), map[string]any{
		"to_agent_uuid": toAgentUUID,
		"message": map[string]any{
			"kind": "text_message",
			"text": text,
		},
	})
	if err != nil {
		return "", err
	}
	if status != http.StatusAccepted {
		return "", fmt.Errorf("expected openclaw publish 202, got %d payload=%v", status, payload)
	}
	result := runtimeResult(payload)
	messageID := readStringPath(result, "message_id")
	if messageID == "" {
		messageID = readStringPath(result, "message", "message_id")
	}
	if messageID == "" {
		return "", fmt.Errorf("expected openclaw publish response to include message_id payload=%v", payload)
	}
	return messageID, nil
}

func (r *runner) pullOpenClawMessage(token, expectedMessageID string, timeout time.Duration) (string, string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, payload, err := r.requestJSON(http.MethodGet, "/v1/openclaw/messages/pull?timeout_ms=1000", cmdutil.AgentHeaders(token), nil)
		if err != nil {
			return "", "", err
		}
		if status == http.StatusNoContent {
			continue
		}
		if status != http.StatusOK {
			return "", "", fmt.Errorf("expected openclaw pull 200/204, got %d payload=%v", status, payload)
		}

		result := runtimeResult(payload)
		messageID := readStringPath(result, "message", "message_id")
		if messageID == "" {
			messageID = readStringPath(result, "message_id")
		}
		deliveryID := readStringPath(result, "delivery", "delivery_id")
		if deliveryID == "" {
			return "", "", fmt.Errorf("expected openclaw pull to include delivery_id payload=%v", payload)
		}

		openClawMessage, err := cmdutil.RequireObject(result, "openclaw_message")
		if err != nil {
			return "", "", err
		}
		text := cmdutil.AsString(openClawMessage, "text")
		if messageID == expectedMessageID {
			return deliveryID, text, nil
		}
		if err := r.ackOpenClawDeliveryHTTP(token, deliveryID); err != nil {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("timed out waiting for openclaw pull message_id=%q", expectedMessageID)
}

func (r *runner) drainOpenClawQueue(token string) error {
	for i := 0; i < 64; i++ {
		status, payload, err := r.requestJSON(http.MethodGet, "/v1/openclaw/messages/pull?timeout_ms=0", cmdutil.AgentHeaders(token), nil)
		if err != nil {
			return err
		}
		if status == http.StatusNoContent {
			return nil
		}
		if status != http.StatusOK {
			return fmt.Errorf("expected openclaw drain pull 200/204, got %d payload=%v", status, payload)
		}

		result := runtimeResult(payload)
		deliveryID := readStringPath(result, "delivery", "delivery_id")
		if deliveryID == "" {
			continue
		}
		if err := r.ackOpenClawDeliveryHTTP(token, deliveryID); err != nil {
			return err
		}
	}
	return fmt.Errorf("openclaw queue drain exceeded maximum attempts")
}

func (r *runner) ackOpenClawDeliveryHTTP(token, deliveryID string) error {
	status, payload, err := r.requestJSON(http.MethodPost, "/v1/openclaw/messages/ack", cmdutil.AgentHeaders(token), map[string]any{
		"delivery_id": deliveryID,
	})
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status == http.StatusNotFound && cmdutil.AsString(payload, "error") == "unknown_delivery" {
		return nil
	}
	return fmt.Errorf("expected openclaw ack 200, got %d payload=%v", status, payload)
}

func (r *runner) openOpenClawWebSocket(token, sessionKey string) (*websocket.Conn, error) {
	base, err := url.Parse(r.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url %q: %w", r.baseURL, err)
	}
	switch strings.ToLower(strings.TrimSpace(base.Scheme)) {
	case "https":
		base.Scheme = "wss"
	case "http":
		base.Scheme = "ws"
	default:
		return nil, fmt.Errorf("unsupported base url scheme %q", base.Scheme)
	}
	base.Path = "/v1/openclaw/messages/ws"
	query := base.Query()
	query.Set("session_key", sessionKey)
	base.RawQuery = query.Encode()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	conn, resp, err := websocket.DefaultDialer.Dial(base.String(), headers)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("openclaw websocket dial failed status=%d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("openclaw websocket dial failed: %w", err)
	}
	first, err := readWSJSON(conn, 8*time.Second)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if got := readStringPath(first, "type"); got != "session_ready" {
		_ = conn.Close()
		return nil, fmt.Errorf("expected websocket session_ready, got %q payload=%v", got, first)
	}
	return conn, nil
}

func (r *runner) waitForOpenClawWSDelivery(conn *websocket.Conn, expectedMessageID string, timeout time.Duration) (string, string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evt, err := readWSJSON(conn, time.Until(deadline))
		if err != nil {
			return "", "", err
		}
		if readStringPath(evt, "type") != "delivery" {
			continue
		}
		result := runtimeResult(evt)
		messageID := readStringPath(result, "message", "message_id")
		if messageID == "" {
			messageID = readStringPath(result, "message_id")
		}
		deliveryID := readStringPath(result, "delivery", "delivery_id")
		if deliveryID == "" {
			return "", "", fmt.Errorf("expected websocket delivery_id payload=%v", evt)
		}

		openClawMessage, err := cmdutil.RequireObject(result, "openclaw_message")
		if err != nil {
			return "", "", err
		}
		text := cmdutil.AsString(openClawMessage, "text")
		if messageID == expectedMessageID {
			return deliveryID, text, nil
		}
		if err := r.ackOpenClawDeliveryHTTP(r.tokenB, deliveryID); err != nil {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("timed out waiting for websocket delivery message_id=%q", expectedMessageID)
}

func (r *runner) ackOpenClawDeliveryWS(conn *websocket.Conn, deliveryID string) error {
	requestID := fmt.Sprintf("smoke-ws-ack-%d", time.Now().UnixNano())
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("set websocket write deadline: %w", err)
	}
	if err := conn.WriteJSON(map[string]any{
		"type":        "ack",
		"request_id":  requestID,
		"delivery_id": deliveryID,
	}); err != nil {
		return fmt.Errorf("write websocket ack frame: %w", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		evt, err := readWSJSON(conn, time.Until(deadline))
		if err != nil {
			return err
		}
		if readStringPath(evt, "type") != "response" {
			if readStringPath(evt, "type") == "delivery" {
				strayDeliveryID := readStringPath(runtimeResult(evt), "delivery", "delivery_id")
				if strayDeliveryID != "" {
					_ = r.ackOpenClawDeliveryHTTP(r.tokenB, strayDeliveryID)
				}
			}
			continue
		}
		if readStringPath(evt, "request_id") != requestID {
			continue
		}
		if readStringPath(evt, "ok") != "true" {
			return fmt.Errorf("expected websocket ack response ok=true payload=%v", evt)
		}
		if got := readStringPath(evt, "status"); got != "200" {
			return fmt.Errorf("expected websocket ack response status=200, got %q payload=%v", got, evt)
		}
		return nil
	}
	return fmt.Errorf("timed out waiting for websocket ack response request_id=%q", requestID)
}

func (r *runner) setHumanHandle(humanID, email, handle string) (map[string]any, error) {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/me", cmdutil.HumanHeaders(humanID, email), map[string]any{
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
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/me/metadata", cmdutil.HumanHeaders(humanID, email), map[string]any{
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
	status, payload, err := r.requestJSON(http.MethodPost, "/v1/orgs", cmdutil.HumanHeaders(humanID, email), map[string]any{
		"handle":       handle,
		"display_name": displayName,
	})
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("expected org create 201, got %d payload=%v", status, payload)
	}
	org, err := cmdutil.RequireObject(payload, "organization")
	if err != nil {
		return "", err
	}
	return cmdutil.AsString(org, "org_id"), nil
}

func (r *runner) patchOrgMetadata(orgID string, metadata map[string]any) (map[string]any, error) {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/orgs/"+orgID+"/metadata", cmdutil.HumanHeaders("alice", "alice@a.test"), map[string]any{
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
	status, payload, err := r.requestJSON(http.MethodPost, "/v1/me/agents/bind-tokens", cmdutil.HumanHeaders("alice", "alice@a.test"), map[string]any{
		"org_id": orgID,
	})
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("expected bind token create 201, got %d payload=%v", status, payload)
	}
	return cmdutil.AsString(payload, "bind_token"), nil
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
	return cmdutil.AsString(payload, "token"), nil
}

func (r *runner) patchAgentHandle(token, handle string) (map[string]any, error) {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/agents/me", cmdutil.AgentHeaders(token), map[string]any{
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
	status, payload, err := r.requestJSON(http.MethodGet, "/v1/agents/me", cmdutil.AgentHeaders(token), nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("expected /v1/agents/me 200, got %d payload=%v", status, payload)
	}
	return cmdutil.RequireObject(payload, "agent")
}

func (r *runner) patchAgentMetadata(token string, metadata map[string]any) (map[string]any, error) {
	status, payload, err := r.requestJSON(http.MethodPatch, "/v1/agents/me/metadata", cmdutil.AgentHeaders(token), map[string]any{
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
	resp, err := cmdutil.RequestJSON(r.client, r.baseURL, method, path, headers, body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, resp.Payload, nil
}

func requireErrorCode(payload map[string]any, want string) error {
	if payload["error"] != want {
		return fmt.Errorf("expected error %q, got %v payload=%v", want, payload["error"], payload)
	}
	return nil
}

func requireEntityMetadata(payload map[string]any, entityKey string, want map[string]any) error {
	entity, err := cmdutil.RequireObject(payload, entityKey)
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
		if entityKey == "agent" && ok && len(gotMap) == 1 && gotMap["agent_type"] == "unknown" {
			return nil
		}
		return fmt.Errorf("expected %s.metadata empty or omitted, got %v payload=%v", entityKey, got, payload)
	}
	got, ok := entity["metadata"].(map[string]any)
	if !ok {
		return fmt.Errorf("expected %s.metadata object, got %T payload=%v", entityKey, entity["metadata"], payload)
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
			return fmt.Errorf("expected %s.metadata=%v, got %v", entityKey, normalizedWant, got)
		}
		return nil
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("expected %s.metadata=%v, got %v", entityKey, want, got)
	}
	return nil
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

func runtimeResult(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	result, ok := payload["result"].(map[string]any)
	if ok {
		return result
	}
	return payload
}

func readStringPath(root map[string]any, path ...string) string {
	current := any(root)
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		next, ok := object[segment]
		if !ok {
			return ""
		}
		current = next
	}
	switch value := current.(type) {
	case string:
		return strings.TrimSpace(value)
	case bool:
		if value {
			return "true"
		}
		return "false"
	case float64:
		if math.Mod(value, 1) == 0 {
			return fmt.Sprintf("%.0f", value)
		}
		return fmt.Sprintf("%f", value)
	default:
		return ""
	}
}

func readWSJSON(conn *websocket.Conn, timeout time.Duration) (map[string]any, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("set websocket read deadline: %w", err)
	}
	var payload map[string]any
	if err := conn.ReadJSON(&payload); err != nil {
		return nil, fmt.Errorf("read websocket json: %w", err)
	}
	return payload, nil
}
