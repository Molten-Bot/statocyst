package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"moltenhub/internal/auth"
	"moltenhub/internal/longpoll"
	"moltenhub/internal/store"
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
	if got := readStringPath(publishResult, "envelope", "kind"); got != "node_event" {
		t.Fatalf("expected envelope.kind=node_event, got %q payload=%v", got, publishPayload)
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
	if got := readStringPath(pullResult, "envelope", "text"); got != "build completed" {
		t.Fatalf("expected pull envelope.text=build completed, got %q payload=%v", got, pullPayload)
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

func TestRuntimePublishPullAckFlowDefaultsRuntimeEnvelope(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	publishResp := doJSONRequest(t, router, http.MethodPost, "/v1/runtime/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
		"message": map[string]any{
			"kind": "task_result",
			"text": "runtime ready",
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if publishResp.Code != http.StatusAccepted {
		t.Fatalf("expected runtime publish 202, got %d %s", publishResp.Code, publishResp.Body.String())
	}
	publishPayload := decodeJSONMap(t, publishResp.Body.Bytes())
	publishResult := requireAgentRuntimeSuccessEnvelope(t, publishPayload)
	if got := readStringPath(publishResult, "transport", "protocol"); got != runtimeEnvelopeProtocol {
		t.Fatalf("expected runtime transport.protocol=%q, got %q payload=%v", runtimeEnvelopeProtocol, got, publishPayload)
	}
	if got := readStringPath(publishResult, "envelope", "protocol"); got != runtimeEnvelopeProtocol {
		t.Fatalf("expected runtime envelope.protocol=%q, got %q payload=%v", runtimeEnvelopeProtocol, got, publishPayload)
	}
	if got := readStringPath(publishResult, "openclaw_message", "text"); got != "runtime ready" {
		t.Fatalf("expected compatibility openclaw_message.text, got %q payload=%v", got, publishPayload)
	}
	messageID, _ := publishResult["message_id"].(string)
	if messageID == "" {
		t.Fatalf("expected message_id in publish response payload=%v", publishPayload)
	}

	pullResp := doJSONRequest(t, router, http.MethodGet, "/v1/runtime/messages/pull?timeout_ms=1000", nil, map[string]string{"Authorization": "Bearer " + tokenB})
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected runtime pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	pullResult := requireAgentRuntimeSuccessEnvelope(t, pullPayload)
	if got := readStringPath(pullResult, "envelope", "text"); got != "runtime ready" {
		t.Fatalf("expected pull envelope.text=runtime ready, got %q payload=%v", got, pullPayload)
	}
	if got := readStringPath(pullResult, "openclaw_message", "text"); got != "runtime ready" {
		t.Fatalf("expected pull compatibility openclaw_message.text=runtime ready, got %q payload=%v", got, pullPayload)
	}
	deliveryID := readStringPath(pullResult, "delivery", "delivery_id")
	if deliveryID == "" {
		t.Fatalf("expected delivery_id in pull response payload=%v", pullPayload)
	}

	ackResp := doJSONRequest(t, router, http.MethodPost, "/v1/runtime/messages/ack", map[string]any{
		"delivery_id": deliveryID,
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if ackResp.Code != http.StatusOK {
		t.Fatalf("expected runtime ack 200, got %d %s", ackResp.Code, ackResp.Body.String())
	}
	ackPayload := decodeJSONMap(t, ackResp.Body.Bytes())
	ackResult := requireAgentRuntimeSuccessEnvelope(t, ackPayload)
	if got := readStringPath(ackResult, "envelope", "protocol"); got != runtimeEnvelopeProtocol {
		t.Fatalf("expected ack envelope.protocol=%q, got %q payload=%v", runtimeEnvelopeProtocol, got, ackPayload)
	}

	statusResp := doJSONRequest(t, router, http.MethodGet, "/v1/runtime/messages/"+messageID, nil, map[string]string{"Authorization": "Bearer " + tokenA})
	if statusResp.Code != http.StatusOK {
		t.Fatalf("expected runtime status 200, got %d %s", statusResp.Code, statusResp.Body.String())
	}
	statusPayload := decodeJSONMap(t, statusResp.Body.Bytes())
	statusResult := requireAgentRuntimeSuccessEnvelope(t, statusPayload)
	if got := readStringPath(statusResult, "envelope", "text"); got != "runtime ready" {
		t.Fatalf("expected status envelope.text=runtime ready, got %q payload=%v", got, statusPayload)
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

func TestOpenClawPublishAcceptsTopLevelEnvelope(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	publishResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
		"kind":          "task_result",
		"text":          "Failure: clone failed\nError details: repository not found",
		"Failure:":      true,
		"Error details:": map[string]any{
			"code":    "clone_failed",
			"message": "repository not found",
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if publishResp.Code != http.StatusAccepted {
		t.Fatalf("expected openclaw top-level envelope publish 202, got %d %s", publishResp.Code, publishResp.Body.String())
	}

	pullResp := doJSONRequest(t, router, http.MethodGet, "/v1/openclaw/messages/pull?timeout_ms=1000", nil, map[string]string{"Authorization": "Bearer " + tokenB})
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected openclaw pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	pullResult := requireAgentRuntimeSuccessEnvelope(t, pullPayload)
	if got := readStringPath(pullResult, "openclaw_message", "kind"); got != "task_result" {
		t.Fatalf("expected top-level envelope kind to be preserved, got %q payload=%v", got, pullPayload)
	}
	openClawMessage, _ := pullResult["openclaw_message"].(map[string]any)
	if failureAlias, _ := openClawMessage["Failure:"].(bool); !failureAlias {
		t.Fatalf("expected Failure: alias in delivered OpenClaw message, got payload=%v", pullPayload)
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
	if failureAlias, _ := payload["Failure"].(bool); !failureAlias {
		t.Fatalf("expected Failure=true alias, got payload=%v", payload)
	}
	if failureAlias, _ := payload["Failure:"].(bool); !failureAlias {
		t.Fatalf("expected Failure:=true alias, got payload=%v", payload)
	}
	errorDetailsAlias, _ := payload["Error details"].(map[string]any)
	if got, _ := errorDetailsAlias["code"].(string); got != "invalid_request" {
		t.Fatalf("expected Error details.code=invalid_request, got %q payload=%v", got, payload)
	}
	errorDetailsColonAlias, _ := payload["Error details:"].(map[string]any)
	if got, _ := errorDetailsColonAlias["code"].(string); got != "invalid_request" {
		t.Fatalf("expected Error details:.code=invalid_request, got %q payload=%v", got, payload)
	}
}

func TestOpenClawPublishSkillActivationAllowsMissingPayload(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	metadataPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"skills": []map[string]any{
				{
					"name":        "weather_lookup",
					"description": "Get current weather for a location.",
				},
			},
		},
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if metadataPatch.Code != http.StatusOK {
		t.Fatalf("metadata patch failed: %d %s", metadataPatch.Code, metadataPatch.Body.String())
	}

	publishResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
		"message": map[string]any{
			"type":           "skill_request",
			"request_id":     "req-skill-no-payload",
			"skill_name":     "weather_lookup",
			"reply_required": false,
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if publishResp.Code != http.StatusAccepted {
		t.Fatalf("expected openclaw skill_request publish 202, got %d %s", publishResp.Code, publishResp.Body.String())
	}

	pullResp := doJSONRequest(t, router, http.MethodGet, "/v1/openclaw/messages/pull?timeout_ms=1000", nil, map[string]string{"Authorization": "Bearer " + tokenB})
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected openclaw pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	pullResult := requireAgentRuntimeSuccessEnvelope(t, pullPayload)
	if got := readStringPath(pullResult, "openclaw_message", "skill_name"); got != "weather_lookup" {
		t.Fatalf("expected pull openclaw_message.skill_name=weather_lookup, got %q payload=%v", got, pullPayload)
	}
	openClawMessage, _ := pullResult["openclaw_message"].(map[string]any)
	if _, ok := openClawMessage["payload"]; ok {
		t.Fatalf("expected payload to be omitted when not provided, got payload=%v", openClawMessage["payload"])
	}
}

func TestOpenClawPublishSkillActivationRejectsInvalidPayloadType(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
		"message": map[string]any{
			"type":       "skill_request",
			"skill_name": "weather_lookup",
			"payload":    123,
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected openclaw publish invalid payload type to return 400, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	if got, _ := payload["error"].(string); got != "invalid_request" {
		t.Fatalf("expected invalid_request, got %q payload=%v", got, payload)
	}
}

func TestOpenClawRegisterPluginUpdatesMetadataAndActivityLog(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/register-plugin", map[string]any{
		"plugin_id":    "moltenhub-openclaw",
		"package":      "@moltenbot/openclaw-plugin-moltenhub",
		"version":      "0.1.0-test",
		"transport":    "websocket",
		"session_key":  "dedicated-main",
		"session_mode": "dedicated",
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected register-plugin 200, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	result := requireAgentRuntimeSuccessEnvelope(t, payload)

	plugin, _ := result["plugin"].(map[string]any)
	if got := readStringPath(plugin, "id"); got != "moltenhub-openclaw" {
		t.Fatalf("expected plugin.id=moltenhub-openclaw, got %q payload=%v", got, payload)
	}
	if got := readStringPath(plugin, "transport"); got != "websocket" {
		t.Fatalf("expected plugin.transport=websocket, got %q payload=%v", got, payload)
	}

	agent, _ := result["agent"].(map[string]any)
	metadata, _ := agent["metadata"].(map[string]any)
	if got := readStringPath(metadata, "agent_type"); got != "openclaw" {
		t.Fatalf("expected metadata.agent_type=openclaw, got %q payload=%v", got, payload)
	}
	plugins, _ := metadata["plugins"].(map[string]any)
	moltenhubPlugin, _ := plugins["moltenhub-openclaw"].(map[string]any)
	if got := readStringPath(moltenhubPlugin, "session_mode"); got != "dedicated" {
		t.Fatalf("expected session_mode=dedicated, got %q payload=%v", got, payload)
	}

	activityLog, _ := agent["activity_log"].([]any)
	if !hasActivityText(activityLog, "registered OpenClaw plugin moltenhub-openclaw") {
		t.Fatalf("expected activity_log to include plugin registration, got %v", activityLog)
	}
}

func TestOpenClawWebSocketDeliveryAndAckFlow(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/openclaw/messages/ws?session_key=integration-main"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tokenB)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("expected websocket dial to succeed, got err=%v", err)
	}
	defer conn.Close()

	ready := readWSMessage(t, conn, 5*time.Second)
	if got := readStringPath(ready, "type"); got != "session_ready" {
		t.Fatalf("expected initial ws message type=session_ready, got %q payload=%v", got, ready)
	}
	if got := readStringPath(ready, "transport", "adapter"); got != "websocket" {
		t.Fatalf("expected ws transport.adapter=websocket, got %q payload=%v", got, ready)
	}

	publishResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
		"message": map[string]any{
			"kind": "skill_result",
			"text": "done",
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if publishResp.Code != http.StatusAccepted {
		t.Fatalf("expected publish 202, got %d %s", publishResp.Code, publishResp.Body.String())
	}

	delivery := waitForWSMessageType(t, conn, "delivery", 10*time.Second)
	deliveryID := readStringPath(delivery, "result", "delivery", "delivery_id")
	if deliveryID == "" {
		t.Fatalf("expected delivery message to include delivery_id, payload=%v", delivery)
	}
	if got := readStringPath(delivery, "result", "openclaw_message", "kind"); got != "skill_result" {
		t.Fatalf("expected delivery kind=skill_result, got %q payload=%v", got, delivery)
	}

	if err := conn.WriteJSON(map[string]any{
		"type":        "ack",
		"request_id":  "ack-1",
		"delivery_id": deliveryID,
	}); err != nil {
		t.Fatalf("expected websocket ack write to succeed, got err=%v", err)
	}

	ackResp := waitForWSResponseRequestID(t, conn, "ack-1", 5*time.Second)
	if ok, _ := ackResp["ok"].(bool); !ok {
		t.Fatalf("expected ws ack response ok=true, got payload=%v", ackResp)
	}
	if got := readStringPath(ackResp, "result", "transport", "adapter"); got != "websocket" {
		t.Fatalf("expected ws ack transport.adapter=websocket, got %q payload=%v", got, ackResp)
	}
}

func TestOpenClawWebSocketDeliversMessageQueuedWhileAgentOffline(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	offlineResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/offline", map[string]any{
		"session_key": "offline-queue",
		"reason":      "offline queue regression",
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if offlineResp.Code != http.StatusOK {
		t.Fatalf("expected openclaw offline 200, got %d %s", offlineResp.Code, offlineResp.Body.String())
	}

	publishResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/publish", map[string]any{
		"to_agent_uuid": agentUUIDB,
		"message": map[string]any{
			"kind": "offline_queue_probe",
			"text": "queued while offline",
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if publishResp.Code != http.StatusAccepted {
		t.Fatalf("expected publish 202 while receiver offline, got %d %s", publishResp.Code, publishResp.Body.String())
	}
	publishPayload := decodeJSONMap(t, publishResp.Body.Bytes())
	publishResult := requireAgentRuntimeSuccessEnvelope(t, publishPayload)
	if got, _ := publishResult["status"].(string); got != "queued" {
		t.Fatalf("expected offline receiver message to be queued, got %q payload=%v", got, publishPayload)
	}
	messageID, _ := publishResult["message_id"].(string)
	if messageID == "" {
		t.Fatalf("expected queued message_id, got payload=%v", publishPayload)
	}

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/openclaw/messages/ws?session_key=offline-queue"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tokenB)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("expected websocket dial to succeed, got err=%v", err)
	}
	defer conn.Close()

	ready := readWSMessage(t, conn, 5*time.Second)
	if got := readStringPath(ready, "type"); got != "session_ready" {
		t.Fatalf("expected initial ws message type=session_ready, got %q payload=%v", got, ready)
	}

	delivery := waitForWSMessageType(t, conn, "delivery", 10*time.Second)
	if got := readStringPath(delivery, "result", "message", "message_id"); got != messageID {
		t.Fatalf("expected queued offline message_id %q, got %q payload=%v", messageID, got, delivery)
	}
	if got := readStringPath(delivery, "result", "openclaw_message", "text"); got != "queued while offline" {
		t.Fatalf("expected queued offline payload text, got %q payload=%v", got, delivery)
	}
	deliveryID := readStringPath(delivery, "result", "delivery", "delivery_id")
	if deliveryID == "" {
		t.Fatalf("expected delivery_id in websocket delivery payload=%v", delivery)
	}

	if err := conn.WriteJSON(map[string]any{
		"type":        "ack",
		"request_id":  "offline-queue-ack",
		"delivery_id": deliveryID,
	}); err != nil {
		t.Fatalf("expected websocket ack write to succeed, got err=%v", err)
	}
	ackResp := waitForWSResponseRequestID(t, conn, "offline-queue-ack", 5*time.Second)
	if ok, _ := ackResp["ok"].(bool); !ok {
		t.Fatalf("expected ws ack response ok=true, got payload=%v", ackResp)
	}
}

func TestOpenClawWebSocketUpgradeWithGzipAcceptEncoding(t *testing.T) {
	router := newTestRouter()
	_, _, _, tokenB, _, _, _, _ := setupTrustedAgents(t, router)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/openclaw/messages/ws?session_key=gzip-header"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tokenB)
	headers.Set("Accept-Encoding", "gzip")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("expected websocket dial with Accept-Encoding=gzip to succeed, got err=%v", err)
	}
	defer conn.Close()

	ready := readWSMessage(t, conn, 5*time.Second)
	if got := readStringPath(ready, "type"); got != "session_ready" {
		t.Fatalf("expected initial ws message type=session_ready, got %q payload=%v", got, ready)
	}
}

func TestOpenClawWebSocketActivityCommand(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/openclaw/messages/ws?session_key=activity-main"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tokenA)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("expected websocket dial to succeed, got err=%v", err)
	}
	defer conn.Close()

	ready := readWSMessage(t, conn, 5*time.Second)
	if got := readStringPath(ready, "type"); got != "session_ready" {
		t.Fatalf("expected initial ws message type=session_ready, got %q payload=%v", got, ready)
	}

	if err := conn.WriteJSON(map[string]any{
		"type":       "activity",
		"request_id": "activity-1",
		"activity":   "Completed marketing brief",
		"category":   "marketing",
		"status":     "completed",
	}); err != nil {
		t.Fatalf("expected websocket activity write to succeed, got err=%v", err)
	}

	resp := waitForWSResponseRequestID(t, conn, "activity-1", 5*time.Second)
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected activity response ok=true, got payload=%v", resp)
	}
	if gotStatus, _ := resp["status"].(float64); int(gotStatus) != http.StatusCreated {
		t.Fatalf("expected activity response 201, got payload=%v", resp)
	}
	result, _ := resp["result"].(map[string]any)
	agent, _ := result["agent"].(map[string]any)
	log, _ := agent["activity_log"].([]any)
	if !hasAgentActivity(log, "Completed marketing brief", "marketing", "completed") {
		t.Fatalf("expected activity_log to include websocket agent activity, got %v", log)
	}
}

func TestOpenClawOfflineEndpointUpdatesPresenceAndActivityLog(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/offline", map[string]any{
		"session_key": "main",
		"reason":      "shutdown",
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected openclaw offline 200, got %d %s", resp.Code, resp.Body.String())
	}

	payload := decodeJSONMap(t, resp.Body.Bytes())
	result := requireAgentRuntimeSuccessEnvelope(t, payload)
	agent, _ := result["agent"].(map[string]any)
	metadata, _ := agent["metadata"].(map[string]any)
	presence, _ := metadata["presence"].(map[string]any)
	if got, _ := presence["status"].(string); got != "offline" {
		t.Fatalf("expected metadata.presence.status=offline, got %q payload=%v", got, payload)
	}
	if ready, ok := presence["ready"].(bool); !ok || ready {
		t.Fatalf("expected metadata.presence.ready=false, got %v payload=%v", presence["ready"], payload)
	}
	if got, _ := presence["transport"].(string); got != "websocket" {
		t.Fatalf("expected metadata.presence.transport=websocket, got %q payload=%v", got, payload)
	}
	if got, _ := presence["session_key"].(string); got != "main" {
		t.Fatalf("expected metadata.presence.session_key=main, got %q payload=%v", got, payload)
	}

	activityLog, _ := agent["activity_log"].([]any)
	if !hasActivityText(activityLog, "websocket transport offline") {
		t.Fatalf("expected activity_log to include websocket transport offline, got %v", activityLog)
	}
}

func TestOpenClawPullMarksPresenceOnline(t *testing.T) {
	router := newTestRouter()
	_, _, _, tokenB, _, _, _, _ := setupTrustedAgents(t, router)

	offlineResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/offline", map[string]any{
		"session_key": "main",
		"reason":      "pre-poll baseline",
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if offlineResp.Code != http.StatusOK {
		t.Fatalf("expected openclaw offline 200, got %d %s", offlineResp.Code, offlineResp.Body.String())
	}

	pullResp := doJSONRequest(t, router, http.MethodGet, "/v1/openclaw/messages/pull?timeout_ms=0", nil, map[string]string{"Authorization": "Bearer " + tokenB})
	if pullResp.Code != http.StatusNoContent {
		t.Fatalf("expected openclaw pull 204 while queue empty, got %d %s", pullResp.Code, pullResp.Body.String())
	}

	meResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{"Authorization": "Bearer " + tokenB})
	if meResp.Code != http.StatusOK {
		t.Fatalf("expected /v1/agents/me 200, got %d %s", meResp.Code, meResp.Body.String())
	}
	mePayload := decodeJSONMap(t, meResp.Body.Bytes())
	meResult := requireAgentRuntimeSuccessEnvelope(t, mePayload)
	agent, _ := meResult["agent"].(map[string]any)
	metadata, _ := agent["metadata"].(map[string]any)
	presence, _ := metadata["presence"].(map[string]any)
	if got, _ := presence["status"].(string); got != "online" {
		t.Fatalf("expected metadata.presence.status=online after pull heartbeat, got %q payload=%v", got, mePayload)
	}
	if ready, ok := presence["ready"].(bool); !ok || !ready {
		t.Fatalf("expected metadata.presence.ready=true after pull heartbeat, got %v payload=%v", presence["ready"], mePayload)
	}
}

func TestOpenClawWebSocketPresenceOnlineThenOffline(t *testing.T) {
	router := newTestRouter()
	_, _, _, tokenB, _, _, _, _ := setupTrustedAgents(t, router)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/openclaw/messages/ws?session_key=presence-main"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tokenB)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("expected websocket dial to succeed, got err=%v", err)
	}

	ready := readWSMessage(t, conn, 5*time.Second)
	if got := readStringPath(ready, "type"); got != "session_ready" {
		t.Fatalf("expected initial ws message type=session_ready, got %q payload=%v", got, ready)
	}

	onlineResult, onlineAgent := waitForAgentPresenceStatus(t, router, tokenB, "online", 2*time.Second)
	onlineMetadata, _ := onlineAgent["metadata"].(map[string]any)
	onlinePresence, _ := onlineMetadata["presence"].(map[string]any)
	if readyValue, ok := onlinePresence["ready"].(bool); !ok || !readyValue {
		t.Fatalf("expected metadata.presence.ready=true while connected, got %v payload=%v", onlinePresence["ready"], onlineResult)
	}
	if got, _ := onlinePresence["session_key"].(string); got != "presence-main" {
		t.Fatalf("expected metadata.presence.session_key=presence-main, got %q payload=%v", got, onlineResult)
	}
	onlineActivityLog, _ := onlineAgent["activity_log"].([]any)
	if !hasActivityText(onlineActivityLog, "websocket transport online") {
		t.Fatalf("expected activity_log to include websocket transport online, got %v", onlineActivityLog)
	}

	_ = conn.Close()

	offlineResult, offlineAgent := waitForAgentPresenceStatus(t, router, tokenB, "offline", 4*time.Second)
	offlineMetadata, _ := offlineAgent["metadata"].(map[string]any)
	offlinePresence, _ := offlineMetadata["presence"].(map[string]any)
	if readyValue, ok := offlinePresence["ready"].(bool); !ok || readyValue {
		t.Fatalf("expected metadata.presence.ready=false after disconnect, got %v payload=%v", offlinePresence["ready"], offlineResult)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		offlineActivityLog, _ := offlineAgent["activity_log"].([]any)
		if hasActivityText(offlineActivityLog, "websocket transport offline") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected activity_log to include websocket transport offline, got %v", offlineActivityLog)
		}
		time.Sleep(25 * time.Millisecond)
		resp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, map[string]string{"Authorization": "Bearer " + tokenB})
		if resp.Code != http.StatusOK {
			continue
		}
		payload := decodeJSONMap(t, resp.Body.Bytes())
		offlineResult = requireAgentRuntimeSuccessEnvelope(t, payload)
		offlineAgent, _ = offlineResult["agent"].(map[string]any)
	}
}

func TestOpenClawWebSocketPresenceWriteFailureReturnsErrorDetail(t *testing.T) {
	stateStore := &flakyStateWriteStore{MemoryStore: store.NewMemoryStore()}
	waiters := longpoll.NewWaiters()
	h := NewHandler(
		stateStore,
		stateStore,
		waiters,
		auth.NewDevHumanAuthProvider(),
		"https://hub.example.com",
		"",
		"",
		"",
		"",
		"example.com",
		true,
		15*time.Minute,
		false,
	)
	router := NewRouter(h)
	_, _, _, tokenB, _, _, _, _ := setupTrustedAgents(t, router)

	stateStore.mu.Lock()
	stateStore.failMetadataWriteLeft = 1
	stateStore.mu.Unlock()

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/openclaw/messages/ws?session_key=presence-fail"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tokenB)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("expected websocket dial to succeed, got err=%v", err)
	}
	defer conn.Close()

	resp := readWSMessage(t, conn, 5*time.Second)
	if got := readStringPath(resp, "type"); got != "response" {
		t.Fatalf("expected websocket failure response payload, got type=%q payload=%v", got, resp)
	}
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected websocket failure response ok=false, got payload=%v", resp)
	}
	if failure, _ := resp["failure"].(bool); !failure {
		t.Fatalf("expected websocket failure response failure=true, got payload=%v", resp)
	}
	if failureAlias, _ := resp["Failure"].(bool); !failureAlias {
		t.Fatalf("expected websocket failure response Failure=true alias, got payload=%v", resp)
	}
	if got := readStringPath(resp, "error", "code"); got != "store_error" {
		t.Fatalf("expected websocket error.code=store_error, got %q payload=%v", got, resp)
	}
	errorDetail, _ := resp["error_detail"].(map[string]any)
	if detail, _ := errorDetail["detail"].(string); strings.TrimSpace(detail) == "" {
		t.Fatalf("expected websocket failure error_detail.detail, got payload=%v", resp)
	}
	errorDetailsAlias, _ := resp["Error details"].(map[string]any)
	if detail, _ := errorDetailsAlias["detail"].(string); strings.TrimSpace(detail) == "" {
		t.Fatalf("expected websocket failure Error details.detail, got payload=%v", resp)
	}
}

func TestOpenClawWebSocketSkillActivationPublishAllowsMissingPayload(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	metadataPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"skills": []map[string]any{
				{
					"name":        "weather_lookup",
					"description": "Get current weather for a location.",
				},
			},
		},
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if metadataPatch.Code != http.StatusOK {
		t.Fatalf("metadata patch failed: %d %s", metadataPatch.Code, metadataPatch.Body.String())
	}

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/openclaw/messages/ws?session_key=skill-activation"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tokenA)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("expected websocket dial to succeed, got err=%v", err)
	}
	defer conn.Close()

	ready := readWSMessage(t, conn, 5*time.Second)
	if got := readStringPath(ready, "type"); got != "session_ready" {
		t.Fatalf("expected initial ws message type=session_ready, got %q payload=%v", got, ready)
	}

	if err := conn.WriteJSON(map[string]any{
		"type":          "publish",
		"request_id":    "skill-no-payload",
		"to_agent_uuid": agentUUIDB,
		"message": map[string]any{
			"type":       "skill_request",
			"skill_name": "weather_lookup",
		},
	}); err != nil {
		t.Fatalf("expected websocket publish write to succeed, got err=%v", err)
	}

	publishResp := waitForWSResponseRequestID(t, conn, "skill-no-payload", 5*time.Second)
	if ok, _ := publishResp["ok"].(bool); !ok {
		t.Fatalf("expected ws publish response ok=true, got payload=%v", publishResp)
	}
	if got, _ := publishResp["status"].(float64); got != float64(http.StatusAccepted) {
		t.Fatalf("expected ws publish response status=202, got %v payload=%v", got, publishResp)
	}

	pullResp := doJSONRequest(t, router, http.MethodGet, "/v1/openclaw/messages/pull?timeout_ms=1000", nil, map[string]string{"Authorization": "Bearer " + tokenB})
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected openclaw pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	pullResult := requireAgentRuntimeSuccessEnvelope(t, pullPayload)
	if got := readStringPath(pullResult, "openclaw_message", "skill_name"); got != "weather_lookup" {
		t.Fatalf("expected pull openclaw_message.skill_name=weather_lookup, got %q payload=%v", got, pullPayload)
	}
	openClawMessage, _ := pullResult["openclaw_message"].(map[string]any)
	if _, ok := openClawMessage["payload"]; ok {
		t.Fatalf("expected payload to be omitted when not provided, got payload=%v", openClawMessage["payload"])
	}
}

func TestOpenClawWebSocketSkillActivationRejectsInvalidPayloadType(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/openclaw/messages/ws?session_key=skill-invalid"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tokenA)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("expected websocket dial to succeed, got err=%v", err)
	}
	defer conn.Close()

	ready := readWSMessage(t, conn, 5*time.Second)
	if got := readStringPath(ready, "type"); got != "session_ready" {
		t.Fatalf("expected initial ws message type=session_ready, got %q payload=%v", got, ready)
	}

	if err := conn.WriteJSON(map[string]any{
		"type":          "publish",
		"request_id":    "skill-invalid-payload",
		"to_agent_uuid": agentUUIDB,
		"message": map[string]any{
			"type":       "skill_request",
			"skill_name": "weather_lookup",
			"payload":    99,
		},
	}); err != nil {
		t.Fatalf("expected websocket publish write to succeed, got err=%v", err)
	}

	resp := waitForWSResponseRequestID(t, conn, "skill-invalid-payload", 5*time.Second)
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected ws publish response ok=false, got payload=%v", resp)
	}
	if got, _ := resp["status"].(float64); got != float64(http.StatusBadRequest) {
		t.Fatalf("expected ws publish response status=400, got %v payload=%v", got, resp)
	}
}

func TestOpenClawWebSocketSkillActivationIncludesValidationErrors(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	metadataPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{
			"skills": []map[string]any{
				{
					"name":        "weather_lookup",
					"description": "Get current weather for a location.",
					"parameters": map[string]any{
						"required": []map[string]any{
							{"name": "location", "description": "City or postal code."},
						},
						"secret_policy": "forbidden",
					},
				},
			},
		},
	}, map[string]string{"Authorization": "Bearer " + tokenB})
	if metadataPatch.Code != http.StatusOK {
		t.Fatalf("metadata patch failed: %d %s", metadataPatch.Code, metadataPatch.Body.String())
	}

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/openclaw/messages/ws?session_key=skill-validation"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tokenA)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("expected websocket dial to succeed, got err=%v", err)
	}
	defer conn.Close()

	_ = readWSMessage(t, conn, 5*time.Second)

	if err := conn.WriteJSON(map[string]any{
		"type":          "publish",
		"request_id":    "skill-validation-errors",
		"to_agent_uuid": agentUUIDB,
		"message": map[string]any{
			"type":           "skill_request",
			"skill_name":     "weather_lookup",
			"reply_required": true,
			"payload": map[string]any{
				"units": "metric",
			},
		},
	}); err != nil {
		t.Fatalf("expected websocket publish write to succeed, got err=%v", err)
	}

	resp := waitForWSResponseRequestID(t, conn, "skill-validation-errors", 5*time.Second)
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected ws publish response ok=false, got payload=%v", resp)
	}
	if failure, _ := resp["failure"].(bool); !failure {
		t.Fatalf("expected ws publish response failure=true, got payload=%v", resp)
	}
	if failureAlias, _ := resp["Failure"].(bool); !failureAlias {
		t.Fatalf("expected ws publish response Failure=true alias, got payload=%v", resp)
	}
	if retryable, ok := resp["retryable"].(bool); !ok || retryable {
		t.Fatalf("expected ws publish response retryable=false, got payload=%v", resp)
	}
	nextAction, _ := resp["next_action"].(string)
	if !strings.Contains(nextAction, "read the receiver skill parameters") {
		t.Fatalf("expected ws publish response next_action guidance, got payload=%v", resp)
	}
	errorObj, _ := resp["error"].(map[string]any)
	validationErrors, _ := errorObj["validation_errors"].([]any)
	if len(validationErrors) == 0 || !strings.Contains(validationErrors[0].(string), "missing required parameter") {
		t.Fatalf("expected validation errors in websocket response, got %v", resp)
	}
	errorDetail, _ := resp["error_detail"].(map[string]any)
	if errorDetail["request_id"] != "skill-validation-errors" {
		t.Fatalf("expected error_detail.request_id echo, got %v payload=%v", errorDetail["request_id"], resp)
	}
	detailErrors, _ := errorDetail["validation_errors"].([]any)
	if len(detailErrors) == 0 {
		t.Fatalf("expected validation errors mirrored in error_detail, got %v", resp)
	}
	errorDetailsAlias, _ := resp["Error details"].(map[string]any)
	aliasErrors, _ := errorDetailsAlias["validation_errors"].([]any)
	if len(aliasErrors) == 0 {
		t.Fatalf("expected validation errors mirrored in Error details alias, got %v", resp)
	}
}

func TestOpenClawWebSocketUpgradeWithWrappedWriter(t *testing.T) {
	router := newTestRouter()
	_, _, _, tokenB, _, _, _, _ := setupTrustedAgents(t, router)

	wrappedRouter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate middleware wrappers that hide Hijacker but still expose Unwrap.
		router.ServeHTTP(openClawUnwrapOnlyResponseWriter{ResponseWriter: w}, r)
	})

	server := httptest.NewServer(wrappedRouter)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/openclaw/messages/ws?session_key=wrapped-writer"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tokenB)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("expected websocket dial through wrapped writer to succeed, got err=%v", err)
	}
	defer conn.Close()

	ready := readWSMessage(t, conn, 5*time.Second)
	if got := readStringPath(ready, "type"); got != "session_ready" {
		t.Fatalf("expected initial ws message type=session_ready, got %q payload=%v", got, ready)
	}
}

type openClawUnwrapOnlyResponseWriter struct {
	http.ResponseWriter
}

func (w openClawUnwrapOnlyResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
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

func readWSMessage(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]any {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}
	var payload map[string]any
	if err := conn.ReadJSON(&payload); err != nil {
		t.Fatalf("failed to read websocket payload: %v", err)
	}
	return payload
}

func waitForWSMessageType(t *testing.T, conn *websocket.Conn, wantType string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		payload := readWSMessage(t, conn, time.Until(deadline))
		if readStringPath(payload, "type") == wantType {
			return payload
		}
	}
	t.Fatalf("timed out waiting for websocket message type=%q", wantType)
	return nil
}

func waitForWSResponseRequestID(t *testing.T, conn *websocket.Conn, requestID string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		payload := readWSMessage(t, conn, time.Until(deadline))
		if readStringPath(payload, "type") == "response" && readStringPath(payload, "request_id") == requestID {
			return payload
		}
	}
	t.Fatalf("timed out waiting for websocket response request_id=%q", requestID)
	return nil
}

func hasActivityText(log []any, target string) bool {
	target = strings.TrimSpace(target)
	for _, raw := range log {
		row, _ := raw.(map[string]any)
		if row == nil {
			continue
		}
		activity, _ := row["activity"].(string)
		if strings.TrimSpace(activity) == target {
			return true
		}
	}
	return false
}

func waitForAgentPresenceStatus(t *testing.T, router http.Handler, token, wantStatus string, timeout time.Duration) (map[string]any, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	headers := map[string]string{"Authorization": "Bearer " + token}

	for time.Now().Before(deadline) {
		resp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, headers)
		if resp.Code != http.StatusOK {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		payload := decodeJSONMap(t, resp.Body.Bytes())
		result := requireAgentRuntimeSuccessEnvelope(t, payload)
		agent, _ := result["agent"].(map[string]any)
		metadata, _ := agent["metadata"].(map[string]any)
		presence, _ := metadata["presence"].(map[string]any)
		status, _ := presence["status"].(string)
		if strings.EqualFold(strings.TrimSpace(status), strings.TrimSpace(wantStatus)) {
			return result, agent
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for agent presence status=%q", wantStatus)
	return nil, nil
}
