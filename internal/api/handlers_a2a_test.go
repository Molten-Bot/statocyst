package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	"moltenhub/internal/auth"
	"moltenhub/internal/longpoll"
	"moltenhub/internal/store"
)

func TestA2AAgentCardAdvertisesJSONRPCAndREST(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	resp := doJSONRequest(t, router, http.MethodGet, "/v1/a2a/agents/"+agentUUIDB+"/agent-card", nil, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected agent card 200, got %d %s", resp.Code, resp.Body.String())
	}
	card := decodeJSONMap(t, resp.Body.Bytes())
	if got := readStringPath(card, "capabilities", "extendedAgentCard"); got != "" {
		t.Fatalf("expected extendedAgentCard to decode as bool, got string %q", got)
	}
	if card["name"] == "" {
		t.Fatalf("expected agent card name, got %v", card)
	}
	interfaces, _ := card["supportedInterfaces"].([]any)
	if len(interfaces) != 2 {
		t.Fatalf("expected JSONRPC and HTTP+JSON interfaces, got %v", card["supportedInterfaces"])
	}
	bindings := map[string]bool{}
	for _, raw := range interfaces {
		item, _ := raw.(map[string]any)
		binding, _ := item["protocolBinding"].(string)
		bindings[binding] = true
		if item["protocolVersion"] != a2aProtocolVersion {
			t.Fatalf("expected protocolVersion %q, got %v", a2aProtocolVersion, item)
		}
	}
	if !bindings["JSONRPC"] || !bindings["HTTP+JSON"] {
		t.Fatalf("expected JSONRPC and HTTP+JSON bindings, got %v", bindings)
	}
	requirements, _ := card["securityRequirements"].([]any)
	if len(requirements) != 1 {
		t.Fatalf("expected securityRequirements to be preserved for SDK auth, got %v", card["securityRequirements"])
	}
	firstRequirement, _ := requirements[0].(map[string]any)
	schemes, _ := firstRequirement["schemes"].(map[string]any)
	if _, ok := schemes["moltenhubBearer"]; !ok {
		t.Fatalf("expected moltenhubBearer requirement, got %v", firstRequirement)
	}

	extended := doJSONRequest(t, router, http.MethodGet, "/v1/a2a/agents/"+agentUUIDB+"/extendedAgentCard", nil, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if extended.Code != http.StatusOK {
		t.Fatalf("expected extended card 200, got %d %s", extended.Code, extended.Body.String())
	}
	extendedPayload := decodeJSONMap(t, extended.Body.Bytes())
	metadata, _ := extendedPayload["metadata"].(map[string]any)
	if metadata["authenticated_agent_uuid"] == "" {
		t.Fatalf("expected authenticated metadata in extended card, got %v", extendedPayload)
	}
}

func TestA2AJSONRPCSendMessageDeliversToLegacyPull(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/a2a/agents/"+agentUUIDB, map[string]any{
		"jsonrpc": "2.0",
		"id":      "rpc-send-1",
		"method":  "SendMessage",
		"params": map[string]any{
			"message": map[string]any{
				"messageId": "a2a-client-msg-1",
				"role":      "ROLE_USER",
				"parts": []map[string]any{{
					"text": "hello via a2a",
				}},
			},
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected JSON-RPC HTTP 200, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	if payload["error"] != nil {
		t.Fatalf("expected JSON-RPC success, got %v", payload)
	}
	result, _ := payload["result"].(map[string]any)
	task, _ := result["task"].(map[string]any)
	messageID, _ := task["id"].(string)
	if messageID == "" {
		t.Fatalf("expected task id/message id, got %v", payload)
	}

	pullResp := pull(t, router, tokenB, 0)
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected legacy pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	message, _ := pullPayload["message"].(map[string]any)
	if message["message_id"] != messageID {
		t.Fatalf("expected pulled message_id %q, got %v", messageID, message)
	}
	if message["content_type"] != "text/plain" || message["payload"] != "hello via a2a" {
		t.Fatalf("expected legacy text/plain payload from A2A send, got %v", message)
	}
}

func TestA2AGoSDKJSONRPCClientDeliversToLegacyPull(t *testing.T) {
	router, server, handler := newA2ASDKTestServer(t)
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)
	handler.canonicalBaseURL = normalizeCanonicalBaseURL(server.URL)

	client := newA2ASDKClient(t, server, agentUUIDB, a2a.TransportProtocolJSONRPC)
	result, err := client.SendMessage(a2aSDKContext(tokenA), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello from go sdk jsonrpc")),
	})
	if err != nil {
		t.Fatalf("A2A Go SDK JSON-RPC SendMessage failed: %v", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("expected SDK SendMessage result *a2a.Task, got %T", result)
	}
	if task.ID == "" || task.Status.State != a2a.TaskStateSubmitted {
		t.Fatalf("expected submitted SDK task with id, got %#v", task)
	}

	pullResp := pull(t, router, tokenB, 0)
	if pullResp.Code != http.StatusOK {
		t.Fatalf("expected legacy pull 200, got %d %s", pullResp.Code, pullResp.Body.String())
	}
	pullPayload := decodeJSONMap(t, pullResp.Body.Bytes())
	message, _ := pullPayload["message"].(map[string]any)
	if message["message_id"] != string(task.ID) {
		t.Fatalf("expected pulled message_id %q, got %v", task.ID, message)
	}
	if message["content_type"] != "text/plain" || message["payload"] != "hello from go sdk jsonrpc" {
		t.Fatalf("expected legacy text/plain payload from SDK A2A send, got %v", message)
	}
}

func TestA2AGoSDKRESTClientReadsLegacyTask(t *testing.T) {
	router, server, handler := newA2ASDKTestServer(t)
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)
	handler.canonicalBaseURL = normalizeCanonicalBaseURL(server.URL)

	pubResp := publish(t, router, tokenA, agentUUIDB, "legacy visible to go sdk rest")
	if pubResp.Code != http.StatusAccepted {
		t.Fatalf("expected legacy publish 202, got %d %s", pubResp.Code, pubResp.Body.String())
	}
	pubPayload := decodeJSONMap(t, pubResp.Body.Bytes())
	messageID, _ := pubPayload["message_id"].(string)
	if messageID == "" {
		t.Fatalf("expected message_id, got %v", pubPayload)
	}

	client := newA2ASDKClient(t, server, agentUUIDB, a2a.TransportProtocolHTTPJSON)
	task, err := client.GetTask(a2aSDKContext(tokenB), &a2a.GetTaskRequest{ID: a2a.TaskID(messageID)})
	if err != nil {
		t.Fatalf("A2A Go SDK REST GetTask failed: %v", err)
	}
	if string(task.ID) != messageID {
		t.Fatalf("expected task id %q, got %q", messageID, task.ID)
	}
	if len(task.History) != 1 || len(task.History[0].Parts) != 1 {
		t.Fatalf("expected one SDK task history text part, got %#v", task.History)
	}
	if got := task.History[0].Parts[0].Text(); got != "legacy visible to go sdk rest" {
		t.Fatalf("expected SDK REST task history payload, got %q", got)
	}

	_, err = client.GetExtendedAgentCard(a2aSDKContext(tokenB), &a2a.GetExtendedAgentCardRequest{})
	if err != nil {
		t.Fatalf("A2A Go SDK REST GetExtendedAgentCard failed: %v", err)
	}
}

func TestA2AGoSDKJSONRPCSubscribeUnsupportedReturnsA2AError(t *testing.T) {
	router, server, handler := newA2ASDKTestServer(t)
	_, _, _, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)
	handler.canonicalBaseURL = normalizeCanonicalBaseURL(server.URL)

	client := newA2ASDKClient(t, server, agentUUIDB, a2a.TransportProtocolJSONRPC)
	var gotErr error
	for _, err := range client.SubscribeToTask(a2aSDKContext(tokenB), &a2a.SubscribeToTaskRequest{ID: a2a.TaskID("missing-task")}) {
		gotErr = err
		break
	}
	if !errors.Is(gotErr, a2a.ErrUnsupportedOperation) {
		t.Fatalf("expected SDK SubscribeToTask unsupported operation error, got %v", gotErr)
	}
}

func TestA2ARESTSendMessageAndTaskStatus(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, _, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	sendResp := doJSONRequest(t, router, http.MethodPost, "/v1/a2a/agents/"+agentUUIDB+"/message:send", map[string]any{
		"message": map[string]any{
			"messageId": "a2a-rest-msg-1",
			"contextId": "ctx-rest-1",
			"role":      "ROLE_USER",
			"parts": []map[string]any{{
				"data": map[string]any{"intent": "ping"},
			}},
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if sendResp.Code != http.StatusOK {
		t.Fatalf("expected REST send 200, got %d %s", sendResp.Code, sendResp.Body.String())
	}
	sendPayload := decodeJSONMap(t, sendResp.Body.Bytes())
	task, _ := sendPayload["task"].(map[string]any)
	taskID, _ := task["id"].(string)
	if taskID == "" {
		t.Fatalf("expected task id, got %v", sendPayload)
	}
	if readStringPath(task, "status", "state") != "TASK_STATE_SUBMITTED" {
		t.Fatalf("expected submitted task, got %v", task)
	}

	statusResp := doJSONRequest(t, router, http.MethodGet, "/v1/a2a/agents/"+agentUUIDB+"/tasks/"+taskID, nil, map[string]string{
		"Authorization": "Bearer " + tokenA,
	})
	if statusResp.Code != http.StatusOK {
		t.Fatalf("expected get task 200, got %d %s", statusResp.Code, statusResp.Body.String())
	}
	statusTask := decodeJSONMap(t, statusResp.Body.Bytes())
	if statusTask["id"] != taskID {
		t.Fatalf("expected task id %q, got %v", taskID, statusTask)
	}
	history, _ := statusTask["history"].([]any)
	if len(history) != 1 {
		t.Fatalf("expected one history message, got %v", statusTask)
	}
}

func TestLegacyPublishVisibleAsA2ATask(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)

	pubResp := publish(t, router, tokenA, agentUUIDB, "legacy to a2a")
	if pubResp.Code != http.StatusAccepted {
		t.Fatalf("expected legacy publish 202, got %d %s", pubResp.Code, pubResp.Body.String())
	}
	pubPayload := decodeJSONMap(t, pubResp.Body.Bytes())
	messageID, _ := pubPayload["message_id"].(string)
	if messageID == "" {
		t.Fatalf("expected message_id, got %v", pubPayload)
	}

	taskResp := doJSONRequest(t, router, http.MethodGet, "/v1/a2a/agents/"+agentUUIDB+"/tasks/"+messageID, nil, map[string]string{
		"Authorization": "Bearer " + tokenB,
	})
	if taskResp.Code != http.StatusOK {
		t.Fatalf("expected A2A get task 200, got %d %s", taskResp.Code, taskResp.Body.String())
	}
	task := decodeJSONMap(t, taskResp.Body.Bytes())
	history, _ := task["history"].([]any)
	if len(history) != 1 {
		t.Fatalf("expected history for legacy message, got %v", task)
	}
	msg, _ := history[0].(map[string]any)
	parts, _ := msg["parts"].([]any)
	firstPart, _ := parts[0].(map[string]any)
	if firstPart["text"] != "legacy to a2a" {
		t.Fatalf("expected legacy payload as A2A text part, got %v", task)
	}

	listResp := doJSONRequest(t, router, http.MethodGet, "/v1/a2a/agents/"+agentUUIDB+"/tasks", nil, map[string]string{
		"Authorization": "Bearer " + tokenB,
	})
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected A2A list tasks 200, got %d %s", listResp.Code, listResp.Body.String())
	}
	listPayload := decodeJSONMap(t, listResp.Body.Bytes())
	tasks, _ := listPayload["tasks"].([]any)
	if len(tasks) == 0 {
		t.Fatalf("expected A2A list tasks to include legacy message, got %v", listPayload)
	}
}

func TestA2AJSONRPCNoTrustReturnsFailureDetails(t *testing.T) {
	router := newTestRouter()
	aliceHumanID := currentHumanID(t, router, "alice", "alice@a.test")
	bobHumanID := currentHumanID(t, router, "bob", "bob@b.test")
	orgA := createOrg(t, router, "alice", "alice@a.test", "A2A No Trust A")
	orgB := createOrg(t, router, "bob", "bob@b.test", "A2A No Trust B")
	tokenA, _ := registerAgentWithUUID(t, router, "alice", "alice@a.test", orgA, "a2a-no-trust-a", aliceHumanID)
	_, agentUUIDB := registerAgentWithUUID(t, router, "bob", "bob@b.test", orgB, "a2a-no-trust-b", bobHumanID)

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/a2a/agents/"+agentUUIDB, map[string]any{
		"jsonrpc": "2.0",
		"id":      "rpc-no-trust",
		"method":  "SendMessage",
		"params": map[string]any{
			"message": map[string]any{
				"messageId": "a2a-no-trust-msg",
				"role":      "ROLE_USER",
				"parts": []map[string]any{{
					"text": "no trust",
				}},
			},
		},
	}, map[string]string{"Authorization": "Bearer " + tokenA})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected JSON-RPC HTTP 200, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	errObj, _ := payload["error"].(map[string]any)
	if errObj["code"] != float64(a2aCodeUnauthorized) {
		t.Fatalf("expected unauthorized JSON-RPC error, got %v", payload)
	}
	data, _ := errObj["data"].(map[string]any)
	if data["Failure"] != true {
		t.Fatalf("expected Failure=true in A2A error data, got %v", errObj)
	}
	if data["Failure:"] != true {
		t.Fatalf("expected Failure:=true in A2A error data, got %v", errObj)
	}
	details, _ := data["Error details"].(map[string]any)
	if details["code"] != "no_trust_path" {
		t.Fatalf("expected no_trust_path details, got %v", errObj)
	}
	colonDetails, _ := data["Error details:"].(map[string]any)
	if colonDetails["code"] != "no_trust_path" {
		t.Fatalf("expected no_trust_path colon details, got %v", errObj)
	}
}

func TestA2AJSONRPCContentTypeFailureIncludesDetails(t *testing.T) {
	router := newTestRouter()
	req := httptest.NewRequest(http.MethodPost, "/v1/a2a", strings.NewReader(`{"jsonrpc":"2.0","id":"bad-content-type","method":"ListTasks"}`))
	req.Header.Set("Content-Type", "text/plain")

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected JSON-RPC HTTP 200, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	errObj, _ := payload["error"].(map[string]any)
	if errObj["code"] != float64(a2aCodeContentType) {
		t.Fatalf("expected content type JSON-RPC error, got %v", payload)
	}
	data, _ := errObj["data"].(map[string]any)
	if data["Failure"] != true {
		t.Fatalf("expected Failure=true in A2A error data, got %v", errObj)
	}
	details, _ := data["Error details"].(map[string]any)
	if details["code"] != "unsupported_media_type" {
		t.Fatalf("expected unsupported_media_type details, got %v", errObj)
	}
}

func newA2ASDKTestServer(t *testing.T) (http.Handler, *httptest.Server, *Handler) {
	t.Helper()
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, st, waiters, auth.NewDevHumanAuthProvider(), "https://hub.example.com", "", "", "", "", "example.com", true, 15*time.Minute, false)
	router := NewRouter(h)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return router, server, h
}

func newA2ASDKClient(t *testing.T, server *httptest.Server, agentUUID string, transport a2a.TransportProtocol) *a2aclient.Client {
	t.Helper()
	card, err := agentcard.NewResolver(server.Client()).Resolve(
		context.Background(),
		server.URL+"/v1/a2a/agents/"+agentUUID,
	)
	if err != nil {
		t.Fatalf("A2A Go SDK resolve agent card failed: %v", err)
	}
	client, err := a2aclient.NewFromCard(
		context.Background(),
		card,
		a2aclient.WithConfig(a2aclient.Config{PreferredTransports: []a2a.TransportProtocol{transport}}),
		a2aclient.WithJSONRPCTransport(server.Client()),
		a2aclient.WithRESTTransport(server.Client()),
	)
	if err != nil {
		t.Fatalf("A2A Go SDK client creation failed: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Destroy(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("A2A Go SDK client destroy failed: %v", err)
		}
	})
	return client
}

func a2aSDKContext(token string) context.Context {
	return a2aclient.AttachServiceParams(context.Background(), a2aclient.ServiceParams{
		"Authorization": []string{"Bearer " + token},
	})
}
