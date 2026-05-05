package api

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestCollectiveStreamAcceptsQueryAccessToken(t *testing.T) {
	router := newTestRouter()
	_, _, _, tokenB, _, _, _, _ := setupTrustedAgents(t, router)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/collective/stream?access_token=" + url.QueryEscape(tokenB)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial collective stream with query token failed: status=%d err=%v", status, err)
	}
	defer conn.Close()

	var ready map[string]any
	if err := conn.ReadJSON(&ready); err != nil {
		t.Fatalf("read ready event: %v", err)
	}
	viewer, _ := ready["viewer"].(map[string]any)
	if got, _ := viewer["kind"].(string); got != "agent" {
		t.Fatalf("expected query token to authenticate agent stream, got ready=%v", ready)
	}
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

func TestCollectiveStreamAgentScopeRequiresOwnerOrOrgOwner(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Collective Agent Wall")
	charlieHumanID := currentHumanID(t, router, "charlie", "charlie@c.test")

	adminInviteID := createInvite(t, router, "alice", "alice@a.test", orgID, "bob@b.test", "admin")
	acceptInvite(t, router, "bob", "bob@b.test", adminInviteID)
	memberInviteID := createInvite(t, router, "alice", "alice@a.test", orgID, "charlie@c.test", "member")
	acceptInvite(t, router, "charlie", "charlie@c.test", memberInviteID)

	_, agentUUID := registerAgentWithUUID(t, router, "charlie", "charlie@c.test", orgID, "charlie-owned", charlieHumanID)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/collective/stream?agent_uuid=" + agentUUID
	for _, tc := range []struct {
		name    string
		headers http.Header
	}{
		{name: "direct owner", headers: http.Header{"X-Human-Id": []string{"charlie"}, "X-Human-Email": []string{"charlie@c.test"}}},
		{name: "org owner", headers: http.Header{"X-Human-Id": []string{"alice"}, "X-Human-Email": []string{"alice@a.test"}}},
	} {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, tc.headers)
		if err != nil {
			t.Fatalf("expected %s collective stream dial to succeed: %v", tc.name, err)
		}
		_ = conn.Close()
	}

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"X-Human-Id":    []string{"bob"},
		"X-Human-Email": []string{"bob@b.test"},
	})
	if err == nil {
		_ = conn.Close()
		t.Fatalf("expected org admin collective stream dial to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("expected org admin 403, got status=%d err=%v", status, err)
	}
}

func TestCollectiveStreamHumanDefaultScopeFiltersByOwnerPermissions(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Collective Default Wall")
	charlieHumanID := currentHumanID(t, router, "charlie", "charlie@c.test")

	adminInviteID := createInvite(t, router, "alice", "alice@a.test", orgID, "bob@b.test", "admin")
	acceptInvite(t, router, "bob", "bob@b.test", adminInviteID)
	memberInviteID := createInvite(t, router, "alice", "alice@a.test", orgID, "charlie@c.test", "member")
	acceptInvite(t, router, "charlie", "charlie@c.test", memberInviteID)

	agentToken, agentUUID := registerAgentWithUUID(t, router, "charlie", "charlie@c.test", orgID, "charlie-default-owned", charlieHumanID)
	server := httptest.NewServer(router)
	defer server.Close()

	ownerConn := dialCollectiveStreamForHuman(t, server.URL, "alice", "alice@a.test")
	defer ownerConn.Close()
	adminConn := dialCollectiveStreamForHuman(t, server.URL, "bob", "bob@b.test")
	defer adminConn.Close()

	publishActivityToServer(t, server, agentToken, "Started private default stream coverage")

	event := readCollectiveEventWithin(t, ownerConn, 2*time.Second, func(event map[string]any) bool {
		return event["category"] == "agent_activity" && event["action"] == "published" && event["agent_uuid"] == agentUUID
	})
	if event == nil {
		t.Fatalf("expected org owner to see agent activity for %q", agentUUID)
	}
	if leaked := readCollectiveEventWithin(t, adminConn, 500*time.Millisecond, func(event map[string]any) bool {
		return event["category"] == "agent_activity" && event["action"] == "published" && event["agent_uuid"] == agentUUID
	}); leaked != nil {
		t.Fatalf("expected org admin default stream to hide unowned agent %q, got event=%v", agentUUID, leaked)
	}
}

func dialCollectiveStreamForHuman(t *testing.T, serverURL, humanID, email string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/v1/collective/stream"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"X-Human-Id":    []string{humanID},
		"X-Human-Email": []string{email},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial collective stream for %s failed: status=%d err=%v", humanID, status, err)
	}
	var ready map[string]any
	if err := conn.ReadJSON(&ready); err != nil {
		_ = conn.Close()
		t.Fatalf("read ready event for %s: %v", humanID, err)
	}
	if got, _ := ready["type"].(string); got != "session_ready" {
		_ = conn.Close()
		t.Fatalf("expected session_ready for %s, got %v", humanID, ready)
	}
	return conn
}

func publishActivityToServer(t *testing.T, server *httptest.Server, agentToken, activity string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"activity": activity,
		"category": "coding",
		"status":   "started",
	})
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agents/me/activities", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build activity request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentToken)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("publish activity request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected activity publish 201, got %d", resp.StatusCode)
	}
}

func readCollectiveEventWithin(t *testing.T, conn *websocket.Conn, timeout time.Duration, match func(map[string]any) bool) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		var event map[string]any
		if err := conn.ReadJSON(&event); err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return nil
			}
			return nil
		}
		if event["type"] == collectiveStreamEventType && match(event) {
			return event
		}
	}
	return nil
}
