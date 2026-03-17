package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"statocyst/internal/auth"
	"statocyst/internal/store"
)

func TestBootstrapHandlerPingAndHealthBeforeReady(t *testing.T) {
	handler := newBootstrapHandler(store.StorageStartupModeDegraded, "s3", "s3")

	pingReq := httptest.NewRequest(http.MethodGet, "/ping", nil)
	pingResp := httptest.NewRecorder()
	handler.ServeHTTP(pingResp, pingReq)
	if pingResp.Code != http.StatusNoContent {
		t.Fatalf("expected /ping 204 before ready, got %d", pingResp.Code)
	}

	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthResp := httptest.NewRecorder()
	handler.ServeHTTP(healthResp, healthReq)
	if healthResp.Code != http.StatusOK {
		t.Fatalf("expected /health 200 before ready, got %d body=%s", healthResp.Code, healthResp.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(healthResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode startup health: %v", err)
	}
	if got, _ := payload["boot_status"].(string); got != "starting" {
		t.Fatalf("expected boot_status=starting, got %q payload=%v", got, payload)
	}
	if got, _ := payload["status"].(string); got != "degraded" {
		t.Fatalf("expected status=degraded during startup, got %q payload=%v", got, payload)
	}
}

func TestBootstrapHandlerDelegatesAfterReady(t *testing.T) {
	handler := newBootstrapHandler(store.StorageStartupModeStrict, "memory", "memory")
	handler.SetReady(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ready"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusTeapot {
		t.Fatalf("expected ready handler status, got %d", resp.Code)
	}
	if resp.Body.String() != "ready" {
		t.Fatalf("expected ready handler body, got %q", resp.Body.String())
	}
}

func TestBootstrapHandlerReturnsUnavailableForApplicationRoutes(t *testing.T) {
	handler := newBootstrapHandler(store.StorageStartupModeDegraded, "s3", "s3")

	req := httptest.NewRequest(http.MethodGet, "/v1/orgs", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected /v1/orgs 503 during startup, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After=1 for startup response, got %q", got)
	}
}

func TestBootstrapHandlerServesOpenAPIWhileStarting(t *testing.T) {
	handler := newBootstrapHandler(store.StorageStartupModeDegraded, "s3", "s3")

	req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected /openapi.yaml 200 during startup, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestBootstrapHandlerPingAllowsHeadBeforeReady(t *testing.T) {
	handler := newBootstrapHandler(store.StorageStartupModeDegraded, "s3", "s3")

	req := httptest.NewRequest(http.MethodHead, "/ping", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("expected HEAD /ping 204 before ready, got %d", resp.Code)
	}
}

func TestBootstrapHandlerAllowsOpenAPIAndUIConfigBeforeReady(t *testing.T) {
	handler := newBootstrapHandler(
		store.StorageStartupModeDegraded,
		"s3",
		"s3",
		bootstrapOptions{
			humanAuth:    auth.NewDevHumanAuthProvider(),
			bindTokenTTL: 10 * time.Minute,
		},
	)

	openAPIReq := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	openAPIResp := httptest.NewRecorder()
	handler.ServeHTTP(openAPIResp, openAPIReq)
	if openAPIResp.Code != http.StatusOK {
		t.Fatalf("expected /openapi.yaml 200 before ready, got %d", openAPIResp.Code)
	}

	uiReq := httptest.NewRequest(http.MethodGet, "/v1/ui/config", nil)
	uiResp := httptest.NewRecorder()
	handler.ServeHTTP(uiResp, uiReq)
	if uiResp.Code != http.StatusOK {
		t.Fatalf("expected /v1/ui/config 200 before ready, got %d body=%s", uiResp.Code, uiResp.Body.String())
	}
}

func TestBootstrapHandlerServesIdentityOnlyMeBeforeReady(t *testing.T) {
	handler := newBootstrapHandler(
		store.StorageStartupModeDegraded,
		"s3",
		"s3",
		bootstrapOptions{
			humanAuth: auth.NewDevHumanAuthProvider(),
		},
	)

	getReq := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	getReq.Header.Set("X-Human-Id", "alice")
	getReq.Header.Set("X-Human-Email", "alice@example.com")
	getResp := httptest.NewRecorder()
	handler.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("expected GET /v1/me 200 during startup, got %d body=%s", getResp.Code, getResp.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(getResp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode startup me payload: %v", err)
	}
	if got, _ := payload["boot_status"].(string); got != "starting" {
		t.Fatalf("expected startup me boot_status=starting, got %q payload=%v", got, payload)
	}

	patchReq := httptest.NewRequest(http.MethodPatch, "/v1/me", nil)
	patchResp := httptest.NewRecorder()
	handler.ServeHTTP(patchResp, patchReq)
	if patchResp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected PATCH /v1/me 503 during startup, got %d body=%s", patchResp.Code, patchResp.Body.String())
	}
}

func TestBootstrapHandlerServesStaticRoutesWhileStarting(t *testing.T) {
	handler := newBootstrapHandler(store.StorageStartupModeDegraded, "s3", "s3")

	tests := []struct {
		path string
		code int
	}{
		{path: "/", code: http.StatusOK},
		{path: "/login.js", code: http.StatusOK},
		{path: "/common.js", code: http.StatusOK},
		{path: "/robots.txt", code: http.StatusOK},
		{path: "/humans.txt", code: http.StatusOK},
	}

	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != tc.code {
			t.Fatalf("expected %s %d during startup, got %d body=%s", tc.path, tc.code, resp.Code, resp.Body.String())
		}
	}
}

func TestBootstrapHandlerHeadlessStillServesRobotsAndHumansOnly(t *testing.T) {
	handler := newBootstrapHandler(
		store.StorageStartupModeDegraded,
		"s3",
		"s3",
		bootstrapOptions{headlessMode: true},
	)

	robotsReq := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	robotsResp := httptest.NewRecorder()
	handler.ServeHTTP(robotsResp, robotsReq)
	if robotsResp.Code != http.StatusOK {
		t.Fatalf("expected /robots.txt 200 during headless startup, got %d body=%s", robotsResp.Code, robotsResp.Body.String())
	}

	humansReq := httptest.NewRequest(http.MethodGet, "/humans.txt", nil)
	humansResp := httptest.NewRecorder()
	handler.ServeHTTP(humansResp, humansReq)
	if humansResp.Code != http.StatusOK {
		t.Fatalf("expected /humans.txt 200 during headless startup, got %d body=%s", humansResp.Code, humansResp.Body.String())
	}

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootResp := httptest.NewRecorder()
	handler.ServeHTTP(rootResp, rootReq)
	if rootResp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected / to remain 503 during headless startup, got %d body=%s", rootResp.Code, rootResp.Body.String())
	}
}
