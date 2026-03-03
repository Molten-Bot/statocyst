package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"statocyst/internal/longpoll"
	"statocyst/internal/store"
)

func newTestRouter() http.Handler {
	st := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	h := NewHandler(st, waiters)
	return NewRouter(h)
}

func registerAgent(t *testing.T, router http.Handler, agentID string) string {
	t.Helper()
	body := map[string]string{"agent_id": agentID}
	resp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/register", "", body)
	if resp.Code != http.StatusCreated {
		t.Fatalf("register %s failed: status=%d body=%s", agentID, resp.Code, resp.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	token := payload["token"]
	if token == "" {
		t.Fatalf("register %s returned empty token", agentID)
	}
	return token
}

func createBond(t *testing.T, router http.Handler, token, peerID string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]string{"peer_agent_id": peerID}
	return doJSONRequest(t, router, http.MethodPost, "/v1/bonds", token, body)
}

func deleteBond(t *testing.T, router http.Handler, token, bondID string) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/v1/bonds/%s", bondID)
	return doJSONRequest(t, router, http.MethodDelete, path, token, nil)
}

func publishMessage(t *testing.T, router http.Handler, senderToken, receiverID, payload string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]string{
		"to_agent_id":   receiverID,
		"content_type":  "text/plain",
		"payload":       payload,
		"client_msg_id": payload,
	}
	return doJSONRequest(t, router, http.MethodPost, "/v1/messages/publish", senderToken, body)
}

func pullMessage(t *testing.T, router http.Handler, token string, timeoutMS int) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/v1/messages/pull?timeout_ms=%d", timeoutMS)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func doJSONRequest(t *testing.T, router http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func decodePulledMessage(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var wrapper map[string]map[string]any
	if err := json.Unmarshal(body, &wrapper); err != nil {
		t.Fatalf("decode pull response: %v body=%s", err, string(body))
	}
	msg, ok := wrapper["message"]
	if !ok {
		t.Fatalf("pull response missing message: %s", string(body))
	}
	return msg
}

func decodeMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode JSON response: %v body=%s", err, string(body))
	}
	return payload
}

func activateBond(t *testing.T, router http.Handler, tokenA, tokenB string) string {
	t.Helper()
	resp := createBond(t, router, tokenA, "agent-b")
	if resp.Code != http.StatusCreated {
		t.Fatalf("bond create failed: %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeMap(t, resp.Body.Bytes())
	bond, _ := payload["bond"].(map[string]any)
	bondID, _ := bond["bond_id"].(string)
	if bondID == "" {
		t.Fatalf("missing bond_id in response: %s", resp.Body.String())
	}

	resp = createBond(t, router, tokenB, "agent-a")
	if resp.Code != http.StatusOK {
		t.Fatalf("bond join failed: %d %s", resp.Code, resp.Body.String())
	}
	return bondID
}

func TestRegisterAndDuplicate(t *testing.T) {
	router := newTestRouter()

	token := registerAgent(t, router, "agent-a")
	if token == "" {
		t.Fatal("expected token on first registration")
	}

	resp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/register", "", map[string]string{"agent_id": "agent-a"})
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate registration, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestCreateBondAndActivation(t *testing.T) {
	router := newTestRouter()
	tokenA := registerAgent(t, router, "agent-a")
	tokenB := registerAgent(t, router, "agent-b")

	resp := createBond(t, router, tokenA, "agent-b")
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201 for first bond join, got %d: %s", resp.Code, resp.Body.String())
	}
	payload := decodeMap(t, resp.Body.Bytes())
	bond, _ := payload["bond"].(map[string]any)
	bondID, _ := bond["bond_id"].(string)
	state, _ := bond["state"].(string)
	if state != "pending" {
		t.Fatalf("expected pending bond after first side joins, got %q", state)
	}
	if bondID == "" {
		t.Fatalf("missing bond_id in create response")
	}

	resp = publishMessage(t, router, tokenA, "agent-b", "hello-before-bond")
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for dropped message, got %d: %s", resp.Code, resp.Body.String())
	}
	publishBefore := decodeMap(t, resp.Body.Bytes())
	if publishBefore["status"] != "dropped" {
		t.Fatalf("expected dropped publish before active bond, got %v", publishBefore["status"])
	}

	resp = createBond(t, router, tokenB, "agent-a")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 when second side joins, got %d: %s", resp.Code, resp.Body.String())
	}
	payload = decodeMap(t, resp.Body.Bytes())
	bond, _ = payload["bond"].(map[string]any)
	state, _ = bond["state"].(string)
	if state != "active" {
		t.Fatalf("expected active bond after both sides join, got %q", state)
	}
	bondID2, _ := bond["bond_id"].(string)
	if bondID2 != bondID {
		t.Fatalf("expected same bond_id on second join, got %s vs %s", bondID2, bondID)
	}

	resp = publishMessage(t, router, tokenA, "agent-b", "hello-after-bond")
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 publish queued, got %d: %s", resp.Code, resp.Body.String())
	}
	publishAfter := decodeMap(t, resp.Body.Bytes())
	if publishAfter["status"] != "queued" {
		t.Fatalf("expected queued publish after active bond, got %v", publishAfter["status"])
	}
	if _, ok := publishAfter["message_id"]; !ok {
		t.Fatalf("expected message_id in queued response: %s", resp.Body.String())
	}

	pull := pullMessage(t, router, tokenB, 25)
	if pull.Code != http.StatusOK {
		t.Fatalf("pull after queued publish failed: %d %s", pull.Code, pull.Body.String())
	}
	msg := decodePulledMessage(t, pull.Body.Bytes())
	if got := msg["payload"]; got != "hello-after-bond" {
		t.Fatalf("expected payload hello-after-bond, got %v", got)
	}
}

func TestCreateBondUnknownPeer(t *testing.T) {
	router := newTestRouter()
	tokenA := registerAgent(t, router, "agent-a")

	resp := createBond(t, router, tokenA, "ghost")
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown peer, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestDeleteBondAccessAndPublishDrop(t *testing.T) {
	router := newTestRouter()
	tokenA := registerAgent(t, router, "agent-a")
	tokenB := registerAgent(t, router, "agent-b")
	tokenC := registerAgent(t, router, "agent-c")

	bondID := activateBond(t, router, tokenA, tokenB)

	resp := deleteBond(t, router, tokenC, bondID)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-participant delete, got %d: %s", resp.Code, resp.Body.String())
	}

	resp = deleteBond(t, router, tokenA, bondID)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for participant delete, got %d: %s", resp.Code, resp.Body.String())
	}

	resp = publishMessage(t, router, tokenA, "agent-b", "after-delete")
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 publish response after delete, got %d: %s", resp.Code, resp.Body.String())
	}
	payload := decodeMap(t, resp.Body.Bytes())
	if payload["status"] != "dropped" {
		t.Fatalf("expected dropped publish after bond delete, got %v", payload["status"])
	}
}

func TestLongPollTimeout(t *testing.T) {
	router := newTestRouter()
	tokenA := registerAgent(t, router, "agent-a")

	start := time.Now()
	resp := pullMessage(t, router, tokenA, 25)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on timeout, got %d: %s", resp.Code, resp.Body.String())
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatalf("expected long-poll to wait before timeout")
	}
}

func TestFIFOOrdering(t *testing.T) {
	router := newTestRouter()
	tokenA := registerAgent(t, router, "agent-a")
	tokenB := registerAgent(t, router, "agent-b")
	_ = activateBond(t, router, tokenA, tokenB)

	resp := publishMessage(t, router, tokenA, "agent-b", "first")
	if resp.Code != http.StatusAccepted {
		t.Fatalf("publish first failed: %d %s", resp.Code, resp.Body.String())
	}
	resp = publishMessage(t, router, tokenA, "agent-b", "second")
	if resp.Code != http.StatusAccepted {
		t.Fatalf("publish second failed: %d %s", resp.Code, resp.Body.String())
	}

	pull1 := pullMessage(t, router, tokenB, 10)
	if pull1.Code != http.StatusOK {
		t.Fatalf("pull first failed: %d %s", pull1.Code, pull1.Body.String())
	}
	msg1 := decodePulledMessage(t, pull1.Body.Bytes())
	if got := msg1["payload"]; got != "first" {
		t.Fatalf("expected first payload, got %v", got)
	}

	pull2 := pullMessage(t, router, tokenB, 10)
	if pull2.Code != http.StatusOK {
		t.Fatalf("pull second failed: %d %s", pull2.Code, pull2.Body.String())
	}
	msg2 := decodePulledMessage(t, pull2.Body.Bytes())
	if got := msg2["payload"]; got != "second" {
		t.Fatalf("expected second payload, got %v", got)
	}
}

func TestConcurrentPublishPull(t *testing.T) {
	router := newTestRouter()
	tokenA := registerAgent(t, router, "agent-a")
	tokenB := registerAgent(t, router, "agent-b")
	_ = activateBond(t, router, tokenA, tokenB)

	const total = 40
	var pubWG sync.WaitGroup
	for i := 0; i < total; i++ {
		pubWG.Add(1)
		go func(i int) {
			defer pubWG.Done()
			payload := fmt.Sprintf("msg-%02d", i)
			resp := publishMessage(t, router, tokenA, "agent-b", payload)
			if resp.Code != http.StatusAccepted {
				t.Errorf("publish failed for %s: %d %s", payload, resp.Code, resp.Body.String())
				return
			}
			pub := decodeMap(t, resp.Body.Bytes())
			if pub["status"] != "queued" {
				t.Errorf("publish not queued for %s: %v", payload, pub["status"])
			}
		}(i)
	}
	pubWG.Wait()

	received := make(map[string]struct{})
	deadline := time.Now().Add(5 * time.Second)
	for len(received) < total && time.Now().Before(deadline) {
		resp := pullMessage(t, router, tokenB, 250)
		if resp.Code == http.StatusNoContent {
			continue
		}
		if resp.Code != http.StatusOK {
			t.Fatalf("pull failed: %d %s", resp.Code, resp.Body.String())
		}
		msg := decodePulledMessage(t, resp.Body.Bytes())
		payload, _ := msg["payload"].(string)
		received[payload] = struct{}{}
	}

	if len(received) != total {
		t.Fatalf("expected %d received messages, got %d", total, len(received))
	}
}
