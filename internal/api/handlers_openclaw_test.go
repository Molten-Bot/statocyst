package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

func TestOpenClawRegisterPluginUpdatesMetadataAndActivityLog(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, _ := setupTrustedAgents(t, router)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/register-plugin", map[string]any{
		"plugin_id":    "statocyst-openclaw",
		"package":      "@moltenbot/openclaw-plugin-statocyst",
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
	if got := readStringPath(plugin, "id"); got != "statocyst-openclaw" {
		t.Fatalf("expected plugin.id=statocyst-openclaw, got %q payload=%v", got, payload)
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
	statocystPlugin, _ := plugins["statocyst-openclaw"].(map[string]any)
	if got := readStringPath(statocystPlugin, "session_mode"); got != "dedicated" {
		t.Fatalf("expected session_mode=dedicated, got %q payload=%v", got, payload)
	}

	activityLog, _ := agent["activity_log"].([]any)
	if !hasActivityText(activityLog, "registered OpenClaw plugin statocyst-openclaw") {
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
