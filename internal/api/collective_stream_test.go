package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCollectiveStreamAgentSeesTrustedPeerPublish(t *testing.T) {
	router := newTestRouter()
	_, _, tokenA, tokenB, _, _, _, agentUUIDB := setupTrustedAgents(t, router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/collective/stream"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer " + tokenB},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial collective stream failed: status=%d err=%v", status, err)
	}
	defer conn.Close()

	var ready map[string]any
	if err := conn.ReadJSON(&ready); err != nil {
		t.Fatalf("read ready event: %v", err)
	}
	if got, _ := ready["type"].(string); got != "session_ready" {
		t.Fatalf("expected session_ready, got %v", ready)
	}

	body, _ := json.Marshal(map[string]any{
		"to_agent_uuid": agentUUIDB,
		"content_type":  "text/plain",
		"payload":       "hello",
	})
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages/publish", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build publish request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenA)
	publishResp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("publish request failed: %v", err)
	}
	defer publishResp.Body.Close()
	if publishResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected publish 202, got %d", publishResp.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		var event map[string]any
		if err := conn.ReadJSON(&event); err != nil {
			continue
		}
		if event["type"] == collectiveStreamEventType && event["category"] == "message" && event["action"] == "published" {
			if got, _ := event["peer_agent_uuid"].(string); got != agentUUIDB {
				t.Fatalf("expected peer_agent_uuid %q, got %v", agentUUIDB, event)
			}
			return
		}
	}
	t.Fatalf("timed out waiting for collective publish event")
}

func TestCollectiveStreamOrgScopeRequiresOwner(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Collective Org")
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/collective/stream?scope=org&org_id=" + orgID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"X-Human-Id":    []string{"alice"},
		"X-Human-Email": []string{"alice@a.test"},
	})
	if err != nil {
		t.Fatalf("expected owner collective stream dial to succeed: %v", err)
	}
	_ = conn.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"X-Human-Id":    []string{"bob"},
		"X-Human-Email": []string{"bob@b.test"},
	})
	if err == nil {
		_ = conn.Close()
		t.Fatalf("expected non-owner collective stream dial to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("expected non-owner 403, got status=%d err=%v", status, err)
	}
}
