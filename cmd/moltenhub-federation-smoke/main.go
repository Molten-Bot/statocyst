package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"moltenhub/internal/cmdutil"
)

const (
	peerID       = "alpha-beta"
	alphaPeerURL = "http://moltenhub-alpha:8080"
	betaPeerURL  = "http://moltenhub-beta:8080"
)

type runner struct {
	alphaBaseURL string
	betaBaseURL  string
	client       *http.Client

	alphaOrgID     string
	alphaOrgHandle string
	alphaToken     string
	alphaAgentUUID string
	alphaAgentURI  string

	betaOrgID     string
	betaOrgHandle string
	betaToken     string
	betaAgentUUID string
	betaAgentURI  string

	peerSecret string
}

type step struct {
	name string
	run  func(*runner) error
}

func main() {
	alphaBaseURL := flag.String("alpha-base-url", "http://127.0.0.1:18080", "Alpha MoltenHub base URL")
	betaBaseURL := flag.String("beta-base-url", "http://127.0.0.1:18081", "Beta MoltenHub base URL")
	flag.Parse()

	peerSecret, err := generateSharedSecret()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL generate federation peer secret: %v\n", err)
		os.Exit(1)
	}

	r := &runner{
		alphaBaseURL: strings.TrimRight(*alphaBaseURL, "/"),
		betaBaseURL:  strings.TrimRight(*betaBaseURL, "/"),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		peerSecret: peerSecret,
	}

	steps := []step{
		{name: "Both health endpoints respond", run: (*runner).stepHealth},
		{name: "Alpha and Beta create orgs and agents", run: (*runner).stepCreateOrgsAndAgents},
		{name: "Alpha and Beta pair as peers", run: (*runner).stepPairPeers},
		{name: "Alpha and Beta trust each other's orgs and agents", run: (*runner).stepCreateRemoteTrusts},
		{name: "Alpha agent sends a message to Beta over the bridge", run: (*runner).stepAlphaToBeta},
		{name: "Beta agent sends a message back to Alpha over the bridge", run: (*runner).stepBetaToAlpha},
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

func generateSharedSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (r *runner) stepHealth() error {
	for _, target := range []struct {
		name    string
		baseURL string
	}{
		{name: "alpha", baseURL: r.alphaBaseURL},
		{name: "beta", baseURL: r.betaBaseURL},
	} {
		status, payload, err := r.requestJSON(target.baseURL, http.MethodGet, "/health", nil, nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("%s health expected 200, got %d payload=%v", target.name, status, payload)
		}
		if payload["status"] != "ok" {
			return fmt.Errorf("%s health expected ok, got %v", target.name, payload["status"])
		}
	}
	return nil
}

func (r *runner) stepCreateOrgsAndAgents() error {
	alphaOrgID, alphaOrgHandle, err := r.createOrg(r.alphaBaseURL, "alice", "alice@a.test", "launch-alpha", "Launch Alpha")
	if err != nil {
		return err
	}
	r.alphaOrgID = alphaOrgID
	r.alphaOrgHandle = alphaOrgHandle
	alphaToken, alphaAgentUUID, alphaAgentURI, err := r.createAgent(r.alphaBaseURL, "alice", "alice@a.test", alphaOrgID, "launch-agent-a")
	if err != nil {
		return err
	}
	r.alphaToken = alphaToken
	r.alphaAgentUUID = alphaAgentUUID
	r.alphaAgentURI = alphaAgentURI

	betaOrgID, betaOrgHandle, err := r.createOrg(r.betaBaseURL, "bob", "bob@b.test", "launch-beta", "Launch Beta")
	if err != nil {
		return err
	}
	r.betaOrgID = betaOrgID
	r.betaOrgHandle = betaOrgHandle
	betaToken, betaAgentUUID, betaAgentURI, err := r.createAgent(r.betaBaseURL, "bob", "bob@b.test", betaOrgID, "launch-agent-b")
	if err != nil {
		return err
	}
	r.betaToken = betaToken
	r.betaAgentUUID = betaAgentUUID
	r.betaAgentURI = betaAgentURI
	return nil
}

func (r *runner) stepPairPeers() error {
	if err := r.createPeer(r.alphaBaseURL, betaPeerURL, betaPeerURL); err != nil {
		return err
	}
	if err := r.createPeer(r.betaBaseURL, alphaPeerURL, alphaPeerURL); err != nil {
		return err
	}
	return nil
}

func (r *runner) stepCreateRemoteTrusts() error {
	if err := r.createRemoteOrgTrust(r.alphaBaseURL, r.alphaOrgID, r.betaOrgHandle); err != nil {
		return err
	}
	if err := r.createRemoteOrgTrust(r.betaBaseURL, r.betaOrgID, r.alphaOrgHandle); err != nil {
		return err
	}
	if err := r.createRemoteAgentTrust(r.alphaBaseURL, r.alphaAgentUUID, r.betaAgentURI); err != nil {
		return err
	}
	if err := r.createRemoteAgentTrust(r.betaBaseURL, r.betaAgentUUID, r.alphaAgentURI); err != nil {
		return err
	}

	capStatus, capPayload, err := r.requestJSON(r.alphaBaseURL, http.MethodGet, "/v1/agents/me/capabilities", map[string]string{
		"Authorization": "Bearer " + r.alphaToken,
	}, nil)
	if err != nil {
		return err
	}
	if capStatus != http.StatusOK {
		return fmt.Errorf("alpha capabilities expected 200, got %d payload=%v", capStatus, capPayload)
	}
	if !containsString(capPayload, "control_plane", "can_talk_to_uris", r.betaAgentURI) {
		return fmt.Errorf("alpha capabilities missing beta agent URI: payload=%v", capPayload)
	}
	return nil
}

func (r *runner) stepAlphaToBeta() error {
	return r.publishPullAck(r.alphaBaseURL, r.alphaToken, r.betaBaseURL, r.betaToken, r.betaAgentURI, r.alphaAgentURI, "federation alpha to beta")
}

func (r *runner) stepBetaToAlpha() error {
	return r.publishPullAck(r.betaBaseURL, r.betaToken, r.alphaBaseURL, r.alphaToken, r.alphaAgentURI, r.betaAgentURI, "federation beta to alpha")
}

func (r *runner) publishPullAck(senderBaseURL, senderToken, receiverBaseURL, receiverToken, toAgentURI, fromAgentURI, wantPayload string) error {
	pubStatus, pubPayload, err := r.requestJSON(senderBaseURL, http.MethodPost, "/v1/messages/publish", map[string]string{
		"Authorization": "Bearer " + senderToken,
	}, map[string]any{
		"to_agent_uri": toAgentURI,
		"content_type": "text/plain",
		"payload":      wantPayload,
	})
	if err != nil {
		return err
	}
	if pubStatus != http.StatusAccepted {
		return fmt.Errorf("publish expected 202, got %d payload=%v", pubStatus, pubPayload)
	}

	pullStatus, pullPayload, err := r.requestJSON(receiverBaseURL, http.MethodGet, "/v1/messages/pull?timeout_ms=1000", map[string]string{
		"Authorization": "Bearer " + receiverToken,
	}, nil)
	if err != nil {
		return err
	}
	if pullStatus != http.StatusOK {
		return fmt.Errorf("pull expected 200, got %d payload=%v", pullStatus, pullPayload)
	}
	message, err := cmdutil.RequireObject(pullPayload, "message")
	if err != nil {
		return err
	}
	if cmdutil.AsString(message, "payload") != wantPayload {
		return fmt.Errorf("expected payload %q, got %q", wantPayload, cmdutil.AsString(message, "payload"))
	}
	if cmdutil.AsString(message, "from_agent_uri") != fromAgentURI {
		return fmt.Errorf("expected from_agent_uri %q, got %q", fromAgentURI, cmdutil.AsString(message, "from_agent_uri"))
	}
	if cmdutil.AsString(message, "to_agent_uri") != toAgentURI {
		return fmt.Errorf("expected to_agent_uri %q, got %q", toAgentURI, cmdutil.AsString(message, "to_agent_uri"))
	}

	delivery, err := cmdutil.RequireObject(pullPayload, "delivery")
	if err != nil {
		return err
	}
	deliveryID := cmdutil.AsString(delivery, "delivery_id")
	if deliveryID == "" {
		return fmt.Errorf("delivery_id missing from pull payload=%v", pullPayload)
	}
	ackStatus, ackPayload, err := r.requestJSON(receiverBaseURL, http.MethodPost, "/v1/messages/ack", map[string]string{
		"Authorization": "Bearer " + receiverToken,
	}, map[string]any{"delivery_id": deliveryID})
	if err != nil {
		return err
	}
	if ackStatus != http.StatusOK {
		return fmt.Errorf("ack expected 200, got %d payload=%v", ackStatus, ackPayload)
	}
	return nil
}

func (r *runner) createOrg(baseURL, humanID, email, handle, displayName string) (string, string, error) {
	if _, _, err := r.requestJSON(baseURL, http.MethodPatch, "/v1/me", cmdutil.HumanHeaders(humanID, email), map[string]any{
		"handle": humanID,
	}); err != nil {
		return "", "", err
	}
	status, payload, err := r.requestJSON(baseURL, http.MethodPost, "/v1/orgs", cmdutil.HumanHeaders(humanID, email), map[string]any{
		"handle":       handle,
		"display_name": displayName,
	})
	if err != nil {
		return "", "", err
	}
	if status != http.StatusCreated {
		return "", "", fmt.Errorf("create org expected 201, got %d payload=%v", status, payload)
	}
	org, err := cmdutil.RequireObject(payload, "organization")
	if err != nil {
		return "", "", err
	}
	return cmdutil.AsString(org, "org_id"), cmdutil.AsString(org, "handle"), nil
}

func (r *runner) createAgent(baseURL, humanID, email, orgID, handle string) (string, string, string, error) {
	status, payload, err := r.requestJSON(baseURL, http.MethodPost, "/v1/agents/bind-tokens", cmdutil.HumanHeaders(humanID, email), map[string]any{
		"org_id": orgID,
	})
	if err != nil {
		return "", "", "", err
	}
	if status != http.StatusCreated {
		return "", "", "", fmt.Errorf("create bind token expected 201, got %d payload=%v", status, payload)
	}
	bindToken := cmdutil.AsString(payload, "bind_token")
	if bindToken == "" {
		return "", "", "", fmt.Errorf("bind_token missing from payload=%v", payload)
	}
	status, payload, err = r.requestJSON(baseURL, http.MethodPost, "/v1/agents/bind", nil, map[string]any{
		"bind_token": bindToken,
		"agent_id":   "temporary-" + handle,
	})
	if err != nil {
		return "", "", "", err
	}
	if status != http.StatusCreated {
		return "", "", "", fmt.Errorf("bind expected 201, got %d payload=%v", status, payload)
	}
	token := cmdutil.AsString(payload, "token")
	if token == "" {
		return "", "", "", fmt.Errorf("token missing from payload=%v", payload)
	}
	status, payload, err = r.requestJSON(baseURL, http.MethodPatch, "/v1/agents/me", map[string]string{
		"Authorization": "Bearer " + token,
	}, map[string]any{
		"handle": handle,
	})
	if err != nil {
		return "", "", "", err
	}
	if status != http.StatusOK {
		return "", "", "", fmt.Errorf("finalize agent expected 200, got %d payload=%v", status, payload)
	}
	agent, err := cmdutil.RequireObject(payload, "agent")
	if err != nil {
		return "", "", "", err
	}
	return token, cmdutil.AsString(agent, "agent_uuid"), cmdutil.AsString(agent, "uri"), nil
}

func (r *runner) createPeer(baseURL, canonicalBaseURL, deliveryBaseURL string) error {
	status, payload, err := r.requestJSON(baseURL, http.MethodPost, "/v1/admin/peers", adminHeaders(), map[string]any{
		"peer_id":            peerID,
		"canonical_base_url": canonicalBaseURL,
		"delivery_base_url":  deliveryBaseURL,
		"shared_secret":      r.peerSecret,
	})
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return fmt.Errorf("create peer expected 201, got %d payload=%v", status, payload)
	}
	return nil
}

func (r *runner) createRemoteOrgTrust(baseURL, localOrgID, remoteOrgHandle string) error {
	status, payload, err := r.requestJSON(baseURL, http.MethodPost, "/v1/admin/remote-org-trusts", adminHeaders(), map[string]any{
		"local_org_id":      localOrgID,
		"peer_id":           peerID,
		"remote_org_handle": remoteOrgHandle,
	})
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return fmt.Errorf("create remote org trust expected 201, got %d payload=%v", status, payload)
	}
	return nil
}

func (r *runner) createRemoteAgentTrust(baseURL, localAgentUUID, remoteAgentURI string) error {
	status, payload, err := r.requestJSON(baseURL, http.MethodPost, "/v1/admin/remote-agent-trusts", adminHeaders(), map[string]any{
		"local_agent_uuid": localAgentUUID,
		"peer_id":          peerID,
		"remote_agent_uri": remoteAgentURI,
	})
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return fmt.Errorf("create remote agent trust expected 201, got %d payload=%v", status, payload)
	}
	return nil
}

func (r *runner) requestJSON(baseURL, method, path string, headers map[string]string, body any) (int, map[string]any, error) {
	resp, err := cmdutil.RequestJSON(r.client, baseURL, method, path, headers, body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, resp.Payload, nil
}

func adminHeaders() map[string]string {
	return cmdutil.HumanHeaders("ops", "ops@example.com")
}

func containsString(payload map[string]any, topKey, nestedKey, want string) bool {
	top, ok := payload[topKey].(map[string]any)
	if !ok {
		return false
	}
	items, ok := top[nestedKey].([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if got, ok := item.(string); ok && got == want {
			return true
		}
	}
	return false
}
