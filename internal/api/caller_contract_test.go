package api

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func TestCallerContract_AgentRuntimeEndpointsRejectHumanHeaders(t *testing.T) {
	router := newTestRouter()
	headers := humanHeaders("alice", "alice@a.test")

	profileResp := doJSONRequest(t, router, http.MethodPatch, "/v1/agents/me/metadata", map[string]any{
		"metadata": map[string]any{"public": false},
	}, headers)
	requireUnauthorized(t, profileResp)

	profileReadResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me", nil, headers)
	requireUnauthorized(t, profileReadResp)

	capsResp := doJSONRequest(t, router, http.MethodGet, "/v1/agents/me/capabilities", nil, headers)
	requireUnauthorized(t, capsResp)

	publishResp := doJSONRequest(t, router, http.MethodPost, "/v1/messages/publish", map[string]any{
		"to_agent_uuid": "11111111-1111-1111-1111-111111111111",
		"content_type":  "text/plain",
		"payload":       "hello",
	}, headers)
	requireUnauthorized(t, publishResp)

	pullResp := doJSONRequest(t, router, http.MethodGet, "/v1/messages/pull?timeout_ms=0", nil, headers)
	requireUnauthorized(t, pullResp)

	ackResp := doJSONRequest(t, router, http.MethodPost, "/v1/messages/ack", map[string]any{
		"delivery_id": "delivery-1",
	}, headers)
	requireUnauthorized(t, ackResp)

	openclawPublishResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/publish", map[string]any{
		"to_agent_uuid": "11111111-1111-1111-1111-111111111111",
		"message":       map[string]any{"kind": "agent_message", "text": "hello"},
	}, headers)
	requireUnauthorized(t, openclawPublishResp)

	openclawPullResp := doJSONRequest(t, router, http.MethodGet, "/v1/openclaw/messages/pull?timeout_ms=0", nil, headers)
	requireUnauthorized(t, openclawPullResp)

	openclawAckResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/ack", map[string]any{
		"delivery_id": "delivery-1",
	}, headers)
	requireUnauthorized(t, openclawAckResp)

	openclawOfflineResp := doJSONRequest(t, router, http.MethodPost, "/v1/openclaw/messages/offline", map[string]any{
		"session_key": "main",
	}, headers)
	requireUnauthorized(t, openclawOfflineResp)
}

func TestCallerContract_HumanControlPlaneEndpointsRejectAgentToken(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Caller Contract")
	agentToken, agentUUID := bindAgentWithUUID(t, router, "alice", "alice@a.test", orgID, "runtime-agent")
	authHeader := map[string]string{"Authorization": "Bearer " + agentToken}

	meResp := doJSONRequest(t, router, http.MethodGet, "/v1/me", nil, authHeader)
	requireUnauthorized(t, meResp)

	myAgentsResp := doJSONRequest(t, router, http.MethodGet, "/v1/me/agents", nil, authHeader)
	requireUnauthorized(t, myAgentsResp)

	rotateResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/"+agentUUID+"/rotate-token", nil, authHeader)
	requireUnauthorized(t, rotateResp)
}

func TestCallerContract_BindRedeemBootstrapWorksWithoutAuth(t *testing.T) {
	router := newTestRouter()
	orgID := createOrg(t, router, "alice", "alice@a.test", "Bind Bootstrap")

	createBindResp := doJSONRequest(t, router, http.MethodPost, "/v1/orgs/"+orgID+"/agents/bind-tokens", map[string]any{}, humanHeaders("alice", "alice@a.test"))
	if createBindResp.Code != http.StatusCreated {
		t.Fatalf("expected bind token creation to return 201, got %d %s", createBindResp.Code, createBindResp.Body.String())
	}

	createBindPayload := decodeJSONMap(t, createBindResp.Body.Bytes())
	bindToken, _ := createBindPayload["bind_token"].(string)
	if bindToken == "" {
		t.Fatalf("expected bind_token in response payload")
	}

	redeemResp := doJSONRequest(t, router, http.MethodPost, "/v1/agents/bind", map[string]any{
		"bind_token": bindToken,
		"agent_id":   "bootstrap-agent",
	}, nil)
	if redeemResp.Code != http.StatusCreated {
		t.Fatalf("expected anonymous bind redeem to return 201, got %d %s", redeemResp.Code, redeemResp.Body.String())
	}
	redeemPayload := decodeJSONMap(t, redeemResp.Body.Bytes())
	token, _ := redeemPayload["token"].(string)
	if token == "" {
		t.Fatalf("expected token in bind redeem response")
	}
}

func TestOpenAPICallerContractSecuritySchemes(t *testing.T) {
	securityByOperation := parseOpenAPIOperationSecurity(t)
	expected := map[openAPIOperation][]string{
		{Method: http.MethodGet, Path: "/v1/me"}:                                      {"humanAuth"},
		{Method: http.MethodPost, Path: "/v1/agent-trusts"}:                           {"humanAuth"},
		{Method: http.MethodPost, Path: "/v1/agents/bind"}:                            nil,
		{Method: http.MethodPatch, Path: "/v1/agents/me/metadata"}:                    {"agentAuth"},
		{Method: http.MethodGet, Path: "/v1/agents/me/capabilities"}:                  {"agentAuth"},
		{Method: http.MethodGet, Path: "/v1/agents/me/skill"}:                         {"agentAuth"},
		{Method: http.MethodPost, Path: "/v1/messages/publish"}:                       {"agentAuth"},
		{Method: http.MethodGet, Path: "/v1/messages/pull"}:                           {"agentAuth"},
		{Method: http.MethodPost, Path: "/v1/messages/ack"}:                           {"agentAuth"},
		{Method: http.MethodPost, Path: "/v1/messages/nack"}:                          {"agentAuth"},
		{Method: http.MethodGet, Path: "/v1/messages/{message_id}"}:                   {"agentAuth"},
		{Method: http.MethodPost, Path: "/v1/openclaw/messages/publish"}:              {"agentAuth"},
		{Method: http.MethodGet, Path: "/v1/openclaw/messages/pull"}:                  {"agentAuth"},
		{Method: http.MethodPost, Path: "/v1/openclaw/messages/ack"}:                  {"agentAuth"},
		{Method: http.MethodPost, Path: "/v1/openclaw/messages/nack"}:                 {"agentAuth"},
		{Method: http.MethodGet, Path: "/v1/openclaw/messages/{message_id}"}:          {"agentAuth"},
		{Method: http.MethodPost, Path: "/v1/openclaw/messages/offline"}:              {"agentAuth"},
		{Method: http.MethodPost, Path: "/v1/scheduler/agents/{agent_uuid}/dispatch"}: {"schedulerAuth"},
	}

	for op, want := range expected {
		got, ok := securityByOperation[op]
		if !ok {
			t.Fatalf("missing operation in parsed OpenAPI security map: %s %s", op.Method, op.Path)
		}
		if !equalSecuritySchemes(got, want) {
			t.Fatalf("unexpected security schemes for %s %s: got=%v want=%v", op.Method, op.Path, got, want)
		}
	}

	for op, schemes := range securityByOperation {
		if strings.HasPrefix(op.Path, "/v1/messages/") || strings.HasPrefix(op.Path, "/v1/openclaw/messages/") || op.Path == "/v1/agents/me" || strings.HasPrefix(op.Path, "/v1/agents/me/") {
			if !equalSecuritySchemes(schemes, []string{"agentAuth"}) {
				t.Fatalf("runtime endpoint must require agentAuth: %s %s got=%v", op.Method, op.Path, schemes)
			}
		}
	}
}

func parseOpenAPIOperationSecurity(t *testing.T) map[openAPIOperation][]string {
	t.Helper()

	lines := strings.Split(string(openapiYAML), "\n")
	inPaths := false
	currentPath := ""
	currentMethod := ""
	ops := make(map[openAPIOperation][]string)

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if !inPaths {
			if trimmed == "paths:" {
				inPaths = true
			}
			continue
		}
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}

		if strings.HasPrefix(line, "  /") && strings.HasSuffix(trimmed, ":") {
			currentPath = strings.TrimSuffix(trimmed, ":")
			currentMethod = ""
			continue
		}
		if currentPath == "" {
			continue
		}

		if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      ") && strings.HasSuffix(trimmed, ":") {
			method := strings.TrimSuffix(trimmed, ":")
			switch method {
			case "get", "post", "patch", "delete", "put", "head", "options":
				currentMethod = strings.ToUpper(method)
				ops[openAPIOperation{Method: currentMethod, Path: currentPath}] = nil
			default:
				currentMethod = ""
			}
			continue
		}
		if currentMethod == "" || !strings.HasPrefix(line, "      security:") {
			continue
		}

		op := openAPIOperation{Method: currentMethod, Path: currentPath}
		schemes := make([]string, 0, 2)
		for j := i + 1; j < len(lines); j++ {
			next := lines[j]
			nextTrim := strings.TrimSpace(next)
			if nextTrim == "" {
				continue
			}
			if strings.HasPrefix(next, "        - ") {
				item := strings.TrimSpace(strings.TrimPrefix(nextTrim, "- "))
				parts := strings.SplitN(item, ":", 2)
				scheme := strings.TrimSpace(parts[0])
				if scheme != "" {
					schemes = append(schemes, scheme)
				}
				continue
			}
			if !strings.HasPrefix(next, "        ") {
				i = j - 1
				break
			}
		}
		ops[op] = schemes
	}

	return ops
}

func equalSecuritySchemes(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	sort.Strings(gotCopy)
	sort.Strings(wantCopy)
	for i := range gotCopy {
		if gotCopy[i] != wantCopy[i] {
			return false
		}
	}
	return true
}

func requireUnauthorized(t *testing.T, resp *httptest.ResponseRecorder) {
	t.Helper()
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 unauthorized, got %d %s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp.Body.Bytes())
	if payload["error"] != "unauthorized" {
		t.Fatalf("expected unauthorized error code, got %v payload=%v", payload["error"], payload)
	}
}
