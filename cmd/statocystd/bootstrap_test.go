package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected /v1/me 503 during startup, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After=1 for startup response, got %q", got)
	}
}

func TestBootstrapHandlerReturnsUnavailableForOpenAPIWhileStarting(t *testing.T) {
	handler := newBootstrapHandler(store.StorageStartupModeDegraded, "s3", "s3")

	req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected /openapi.yaml 503 during startup, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After=1 for openapi startup response, got %q", got)
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
