package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"moltenhub/internal/cmdutil"
)

type fakeFedMessage struct {
	deliveryID string
	payload    string
	fromURI    string
	toURI      string
}

type fakeFedHub struct {
	name string

	mu sync.Mutex

	server *httptest.Server
	peer   *fakeFedHub

	nextOrgID      int
	nextBindToken  int
	nextTokenID    int
	nextAgentID    int
	nextDeliveryID int

	tokenToAgentURI  map[string]string
	tokenToAgentUUID map[string]string
	allowedRemoteURI map[string]struct{}
	queue            []fakeFedMessage
}

func newFakeFedHub(name string) *fakeFedHub {
	h := &fakeFedHub{
		name:             name,
		tokenToAgentURI:  map[string]string{},
		tokenToAgentUUID: map[string]string{},
		allowedRemoteURI: map[string]struct{}{},
	}
	h.server = httptest.NewServer(http.HandlerFunc(h.handle))
	return h
}

func (h *fakeFedHub) close() {
	h.server.Close()
}

func (h *fakeFedHub) handle(w http.ResponseWriter, r *http.Request) {
	writeJSON := func(status int, payload map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}
	decodeBody := func() map[string]any {
		if r.Body == nil {
			return map[string]any{}
		}
		defer r.Body.Close()
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload == nil {
			return map[string]any{}
		}
		return payload
	}
	authToken := func() string {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(header, "Bearer ") {
			return ""
		}
		return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/health":
		writeJSON(http.StatusOK, map[string]any{"status": "ok"})
		return

	case r.Method == http.MethodPatch && r.URL.Path == "/v1/me":
		writeJSON(http.StatusOK, map[string]any{"status": "ok"})
		return

	case r.Method == http.MethodPost && r.URL.Path == "/v1/orgs":
		body := decodeBody()
		h.mu.Lock()
		h.nextOrgID++
		orgID := fmt.Sprintf("%s-org-%d", h.name, h.nextOrgID)
		h.mu.Unlock()
		writeJSON(http.StatusCreated, map[string]any{
			"organization": map[string]any{
				"org_id": orgID,
				"handle": cmdutil.AsString(body, "handle"),
			},
		})
		return

	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/orgs/") && strings.HasSuffix(r.URL.Path, "/agents/bind-tokens"):
		h.mu.Lock()
		h.nextBindToken++
		bindToken := fmt.Sprintf("%s-bind-%d", h.name, h.nextBindToken)
		h.mu.Unlock()
		writeJSON(http.StatusCreated, map[string]any{"bind_token": bindToken})
		return

	case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/bind":
		h.mu.Lock()
		h.nextTokenID++
		token := fmt.Sprintf("%s-token-%d", h.name, h.nextTokenID)
		h.mu.Unlock()
		writeJSON(http.StatusCreated, map[string]any{"token": token})
		return

	case r.Method == http.MethodPatch && r.URL.Path == "/v1/agents/me":
		body := decodeBody()
		handle := cmdutil.AsString(body, "handle")
		token := authToken()
		if handle == "" || token == "" {
			writeJSON(http.StatusBadRequest, map[string]any{"error": "invalid_request"})
			return
		}
		h.mu.Lock()
		h.nextAgentID++
		agentUUID := fmt.Sprintf("%s-agent-%d", h.name, h.nextAgentID)
		agentURI := fmt.Sprintf("agent://%s/%s", h.name, handle)
		h.tokenToAgentURI[token] = agentURI
		h.tokenToAgentUUID[token] = agentUUID
		h.mu.Unlock()
		writeJSON(http.StatusOK, map[string]any{
			"agent": map[string]any{
				"agent_uuid": agentUUID,
				"uri":        agentURI,
				"handle":     handle,
			},
		})
		return

	case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/peers":
		writeJSON(http.StatusCreated, map[string]any{"peer_id": peerID})
		return

	case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/remote-org-trusts":
		writeJSON(http.StatusCreated, map[string]any{"status": "ok"})
		return

	case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/remote-agent-trusts":
		body := decodeBody()
		remoteURI := cmdutil.AsString(body, "remote_agent_uri")
		h.mu.Lock()
		h.allowedRemoteURI[remoteURI] = struct{}{}
		h.mu.Unlock()
		writeJSON(http.StatusCreated, map[string]any{"status": "ok"})
		return

	case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/me/capabilities":
		h.mu.Lock()
		uris := make([]any, 0, len(h.allowedRemoteURI))
		for uri := range h.allowedRemoteURI {
			uris = append(uris, uri)
		}
		h.mu.Unlock()
		writeJSON(http.StatusOK, map[string]any{
			"control_plane": map[string]any{
				"can_talk_to_uris": uris,
			},
		})
		return

	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/publish":
		if h.peer == nil {
			writeJSON(http.StatusServiceUnavailable, map[string]any{"error": "peer_unavailable"})
			return
		}
		body := decodeBody()
		token := authToken()
		h.mu.Lock()
		fromURI := h.tokenToAgentURI[token]
		h.mu.Unlock()
		toURI := cmdutil.AsString(body, "to_agent_uri")
		payload := cmdutil.AsString(body, "payload")
		if fromURI == "" || toURI == "" {
			writeJSON(http.StatusBadRequest, map[string]any{"error": "invalid_request"})
			return
		}
		h.peer.mu.Lock()
		h.peer.nextDeliveryID++
		deliveryID := fmt.Sprintf("%s-delivery-%d", h.peer.name, h.peer.nextDeliveryID)
		h.peer.queue = append(h.peer.queue, fakeFedMessage{
			deliveryID: deliveryID,
			payload:    payload,
			fromURI:    fromURI,
			toURI:      toURI,
		})
		h.peer.mu.Unlock()
		writeJSON(http.StatusAccepted, map[string]any{"status": "accepted"})
		return

	case r.Method == http.MethodGet && r.URL.Path == "/v1/messages/pull":
		h.mu.Lock()
		if len(h.queue) == 0 {
			h.mu.Unlock()
			writeJSON(http.StatusNoContent, map[string]any{})
			return
		}
		msg := h.queue[0]
		h.queue = h.queue[1:]
		h.mu.Unlock()
		writeJSON(http.StatusOK, map[string]any{
			"message": map[string]any{
				"payload":        msg.payload,
				"from_agent_uri": msg.fromURI,
				"to_agent_uri":   msg.toURI,
			},
			"delivery": map[string]any{
				"delivery_id": msg.deliveryID,
			},
		})
		return

	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/ack":
		writeJSON(http.StatusOK, map[string]any{"status": "ok"})
		return

	default:
		writeJSON(http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
}

func TestRunnerFederationSmokeFlow(t *testing.T) {
	alpha := newFakeFedHub("alpha")
	defer alpha.close()
	beta := newFakeFedHub("beta")
	defer beta.close()
	alpha.peer = beta
	beta.peer = alpha

	r := &runner{
		alphaBaseURL: alpha.server.URL,
		betaBaseURL:  beta.server.URL,
		client:       &http.Client{Timeout: 5 * time.Second},
	}

	steps := []struct {
		name string
		run  func(*runner) error
	}{
		{name: "Both health endpoints respond", run: (*runner).stepHealth},
		{name: "Alpha and Beta create orgs and agents", run: (*runner).stepCreateOrgsAndAgents},
		{name: "Alpha and Beta pair as peers", run: (*runner).stepPairPeers},
		{name: "Alpha and Beta trust each other's orgs and agents", run: (*runner).stepCreateRemoteTrusts},
		{name: "Alpha agent sends a message to Beta over the bridge", run: (*runner).stepAlphaToBeta},
		{name: "Beta agent sends a message back to Alpha over the bridge", run: (*runner).stepBetaToAlpha},
	}
	for _, tc := range steps {
		if err := tc.run(r); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}
