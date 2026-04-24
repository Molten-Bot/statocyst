package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moltenhub/internal/api"
	"moltenhub/internal/auth"
	"moltenhub/internal/longpoll"
	"moltenhub/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newSmokeTestRunner(t *testing.T, handler http.HandlerFunc) (*runner, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	r := &runner{
		baseURL: server.URL,
		client: &http.Client{
			Timeout: 2 * time.Second,
		},
	}
	return r, server.Close
}

func newSmokeTestServer() *httptest.Server {
	mem := store.NewMemoryStore()
	waiters := longpoll.NewWaiters()
	handler := api.NewHandler(
		mem,
		mem,
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
	return httptest.NewServer(api.NewRouter(handler))
}

func TestRunnerLaunchSmokeFlow(t *testing.T) {
	server := newSmokeTestServer()
	defer server.Close()

	r := &runner{
		baseURL: server.URL,
		client:  server.Client(),
	}
	r.client.Timeout = 15 * time.Second

	steps := []struct {
		name string
		run  func(*runner) error
	}{
		{name: "Health endpoint responds and reports ok", run: (*runner).stepHealth},
		{name: "Alice creates handle", run: (*runner).stepAliceCreatesHandle},
		{name: "Bob tries to add the same handle and gets an error", run: (*runner).stepBobCannotTakeAliceHandle},
		{name: "Alice adds metadata to her profile", run: (*runner).stepAliceAddsProfileMetadata},
		{name: "Alice changes metadata from her profile", run: (*runner).stepAliceChangesProfileMetadata},
		{name: "Alice clears metadata from her profile", run: (*runner).stepAliceClearsProfileMetadata},
		{name: "Alice creates an organization", run: (*runner).stepAliceCreatesOrganization},
		{name: "Bob tries to add an organization with the same handle and gets an error", run: (*runner).stepBobCannotTakeOrgHandle},
		{name: "Alice adds metadata to an organization", run: (*runner).stepAliceAddsOrgMetadata},
		{name: "Alice changes metadata to an organization", run: (*runner).stepAliceChangesOrgMetadata},
		{name: "Alice clears metadata from an organization", run: (*runner).stepAliceClearsOrgMetadata},
		{name: "Alice creates an organization and deletes it", run: (*runner).stepAliceDeletesOrganization},
		{name: "Alice creates a bind token and an agent binds successfully", run: (*runner).stepAgentBinds},
		{name: "Alice creates a bind token and the agent updates its profile handle", run: (*runner).stepAgentFinalizesHandle},
		{name: "Alice creates a bind token and the agent tries to add an existing handle and gets an error", run: (*runner).stepAgentDuplicateHandleRejected},
		{name: "Alice creates a bind token and the agent adds profile metadata", run: (*runner).stepAgentAddsMetadata},
		{name: "Alice creates a bind token and the agent changes profile metadata", run: (*runner).stepAgentChangesMetadata},
		{name: "Alice creates a bind token and the agent clears profile metadata", run: (*runner).stepAgentClearsMetadata},
		{name: "Agent publishes activities over HTTP and OpenClaw websocket", run: (*runner).stepAgentPublishesActivities},
		{name: "Alice invites two agents by bind token, binds both agents, and sees both in her list", run: (*runner).stepAliceSeesBothAgents},
		{name: "Alice creates trust between both bound agents", run: (*runner).stepAliceCreatesAgentTrust},
		{name: "OpenClaw plugin registration succeeds for both agents", run: (*runner).stepOpenClawRegisterPlugin},
		{name: "OpenClaw HTTP publish/pull/ack succeeds between bound agents", run: (*runner).stepOpenClawHTTPDelivery},
		{name: "OpenClaw polling heartbeat marks runtime presence online", run: (*runner).stepOpenClawPresenceHeartbeat},
		{name: "OpenClaw websocket delivery and ack succeeds", run: (*runner).stepOpenClawWebSocketDelivery},
		{name: "Alice binds an agent and revokes it", run: (*runner).stepAliceRevokesFirstAgent},
		{name: "Alice binds two agents and revokes both agents", run: (*runner).stepAliceRevokesBothAgents},
	}

	for _, tc := range steps {
		if err := tc.run(r); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

func TestStepBobCannotTakeAliceHandle(t *testing.T) {
	r, cleanup := newSmokeTestRunner(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPatch || req.URL.Path != "/v1/me" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["handle"] != "alice" {
			t.Fatalf("expected handle=alice in request body, got %v", body)
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"human_handle_exists"}`))
	})
	defer cleanup()

	if err := r.stepBobCannotTakeAliceHandle(); err != nil {
		t.Fatalf("unexpected step error: %v", err)
	}
}

func TestStepBobCannotTakeAliceHandleUnexpectedStatus(t *testing.T) {
	r, cleanup := newSmokeTestRunner(t, func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":"human_handle_exists"}`))
	})
	defer cleanup()

	err := r.stepBobCannotTakeAliceHandle()
	if err == nil || !strings.Contains(err.Error(), "expected 409") {
		t.Fatalf("expected conflict-status error, got %v", err)
	}
}

func TestStepBobCannotTakeAliceHandleRequestError(t *testing.T) {
	r := &runner{
		baseURL: "http://example.test",
		client: &http.Client{
			Timeout: 2 * time.Second,
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial failed")
			}),
		},
	}

	err := r.stepBobCannotTakeAliceHandle()
	if err == nil || !strings.Contains(err.Error(), "perform request") {
		t.Fatalf("expected request transport error, got %v", err)
	}
}

func TestStepBobCannotTakeOrgHandle(t *testing.T) {
	r, cleanup := newSmokeTestRunner(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPatch && req.URL.Path == "/v1/me":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"human":{"handle":"bob"}}`))
		case req.Method == http.MethodPost && req.URL.Path == "/v1/orgs":
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode org request body: %v", err)
			}
			if body["handle"] != "launch-alpha" {
				t.Fatalf("expected launch-alpha org handle, got %v", body)
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"org_handle_exists"}`))
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	})
	defer cleanup()

	if err := r.stepBobCannotTakeOrgHandle(); err != nil {
		t.Fatalf("unexpected step error: %v", err)
	}
}

func TestStepBobCannotTakeOrgHandleSetHandleFailure(t *testing.T) {
	r, cleanup := newSmokeTestRunner(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPatch && req.URL.Path == "/v1/me" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
	})
	defer cleanup()

	err := r.stepBobCannotTakeOrgHandle()
	if err == nil || !strings.Contains(err.Error(), "expected handle set 200") {
		t.Fatalf("expected set-handle failure, got %v", err)
	}
}

func TestStepBobCannotTakeOrgHandleUnexpectedStatus(t *testing.T) {
	r, cleanup := newSmokeTestRunner(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPatch && req.URL.Path == "/v1/me":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"human":{"handle":"bob"}}`))
		case req.Method == http.MethodPost && req.URL.Path == "/v1/orgs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"error":"org_handle_exists"}`))
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
	})
	defer cleanup()

	err := r.stepBobCannotTakeOrgHandle()
	if err == nil || !strings.Contains(err.Error(), "expected 409") {
		t.Fatalf("expected conflict-status error, got %v", err)
	}
}

func TestStepBobCannotTakeOrgHandleRequestError(t *testing.T) {
	r := &runner{
		baseURL: "http://example.test",
		client: &http.Client{
			Timeout: 2 * time.Second,
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/v1/me":
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"human":{"handle":"bob"}}`)),
						Header:     make(http.Header),
					}, nil
				case "/v1/orgs":
					return nil, errors.New("dial failed")
				default:
					return nil, errors.New("unexpected path")
				}
			}),
		},
	}

	err := r.stepBobCannotTakeOrgHandle()
	if err == nil || !strings.Contains(err.Error(), "perform request") {
		t.Fatalf("expected request transport error, got %v", err)
	}
}

func TestRequireErrorCode(t *testing.T) {
	if err := requireErrorCode(map[string]any{"error": "human_handle_exists"}, "human_handle_exists"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if err := requireErrorCode(map[string]any{"error": "wrong"}, "human_handle_exists"); err == nil {
		t.Fatal("expected error for mismatched code")
	}
}
