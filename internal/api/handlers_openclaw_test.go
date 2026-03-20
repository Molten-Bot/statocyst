package api

import (
	"net/http"
	"testing"
)

func TestOpenClawPublishPullAckFlow(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	publishResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
		"message": map[string]any{
			"kind":        "node_event",
			"session_key": "main",
			"node": map[string]any{
				"id":   "node-123",
				"name": "Build Node",
			},
			"text": "build completed",
			"data": map[string]any{"exit_code": 0},
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if publishResp.Code != http.StatusAccepted {
		t.Fatalf("expected openclaw publish 202, got %d %s", publishResp.Code, publishResp.Body.String())
	}
	publishPayload := decodeJSONMap(t, publishResp.Body.Bytes())
	publishResult := requireAgentRuntimeSuccessEnvelope(t, publishPayload)
	if got := readStringPath(publishResult, "transport", "protocol"); got != openClawHTTPProtocol {
		t.Fatalf("expected transport.protocol=%q, got %q payload=%v", openClawHTTPProtocol, got, publishPayload)
	}
	if got := readStringPath(publishResult, "openclaw_message", "kind"); got != "node_event" {
		t.Fatalf("expected openclaw_message.kind=node_event, got %q payload=%v", got, publishPayload)
	}
	messageID, _ := publishResult["message_id"].(string)
	if messageID == "" {
		t.Fatalf("expected message_id in publish response payload=%v", publishPayload)
	}

	pullResp := doJSONRequest(t, router, http.MethodGet, "/v1/openclaw/messages/pull?timeout_ms=1000", nil, map[string]string{"Authorization": "Bearer " + tokenB})
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected openclaw pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	pullResult := requireAgentRuntimeSuccessEnvelope(t, pullPayload)
	if got := readStringPath(pullResult, "transport", "protocol"); got != openClawHTTPProtocol {
		t.Fatalf("expected pull transport.protocol=%q, got %q payload=%v", openClawHTTPProtocol, got, pullPayload)
	}
	if got := readStringPath(pullResult, "openclaw_message", "kind"); got != "node_event" {
		t.Fatalf("expected pull openclaw_message.kind=node_event, got %q payload=%v", got, pullPayload)
	}
	if got := readStringPath(pullResult, "openclaw_message", "text"); got != "build completed" {
		t.Fatalf("expected pull openclaw_message.text=build completed, got %q payload=%v", got, pullPayload)
	}
	deliveryID := readStringPath(pullResult, "delivery", "delivery_id")
	if deliveryID == "" {
		t.Fatalf("expected delivery_id in pull response payload=%v", pullPayload)
	}

	ackResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/ack", map[string]any{
		"delivery_id": deliveryID,
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if ackResp.Code != http.StatusOK {
		t.Fatalf("expected openclaw ack 200, got %d %s", ackResp.Code, ackResp.Body.String())
	}
	ackPayload := decodeJSONMap(t, ackResp.Body.Bytes())
	ackResult := requireAgentRuntimeSuccessEnvelope(t, ackPayload)
	if got := readStringPath(ackResult, "transport", "protocol"); got != openClawHTTPProtocol {
		t.Fatalf("expected ack transport.protocol=%q, got %q payload=%v", openClawHTTPProtocol, got, ackPayload)
	}
	if got := readStringPath(ackResult, "openclaw_message", "kind"); got != "node_event" {
		t.Fatalf("expected ack openclaw_message.kind=node_event, got %q payload=%v", got, ackPayload)
	}

	statusResp := doJSONRequest(t, router, http.MethodGet, "/v1/openclaw/messages/"+messageID, nil, map[string]string{"Authorization": "Bearer " + tokenA})
	if statusResp.Code != http.StatusOK {
		t.Fatalf("expected openclaw status 200, got %d %s", statusResp.Code, statusResp.Body.String())
	}
	statusPayload := decodeJSONMap(t, statusResp.Body.Bytes())
	statusResult := requireAgentRuntimeSuccessEnvelope(t, statusPayload)
	if got := readStringPath(statusResult, "transport", "protocol"); got != openClawHTTPProtocol {
		t.Fatalf("expected status transport.protocol=%q, got %q payload=%v", openClawHTTPProtocol, got, statusPayload)
	}
	if got := readStringPath(statusResult, "openclaw_message", "kind"); got != "node_event" {
		t.Fatalf("expected status openclaw_message.kind=node_event, got %q payload=%v", got, statusPayload)
	}
}

func TestOpenClawPullProjectsTextPayload(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	publishResp := doJSONRequest(t, router, http.MethodPost, "/v1/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
		"content_type":  "text/plain",
		"payload":       "hello from text/plain",
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if publishResp.Code != http.StatusAccepted {
		t.Fatalf("expected runtime publish 202, got %d %s", publishResp.Code, publishResp.Body.String())
	}

	pullResp := doJSONRequest(t, router, http.MethodGet, "/v1/openclaw/messages/pull?timeout_ms=1000", nil, map[string]string{"Authorization": "Bearer " + tokenB})
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected openclaw pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	pullResult := requireAgentRuntimeSuccessEnvelope(t, pullPayload)
	if got := readStringPath(pullResult, "openclaw_message", "kind"); got != "text_message" {
		t.Fatalf("expected text_message projection, got %q payload=%v", got, pullPayload)
	}
	if got := readStringPath(pullResult, "openclaw_message", "text"); got != "hello from text/plain" {
		t.Fatalf("expected projected text payload, got %q payload=%v", got, pullPayload)
	}
}

func TestOpenClawPublishRequiresMessageObject(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected openclaw publish missing message to return 400, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	if got, _ := payload["error"].(string); got != "invalid_request" {
		t.Fatalf("expected invalid_request, got %q payload=%v", got, payload)
	}
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
	value, _ := current.(string)
	return value
}
