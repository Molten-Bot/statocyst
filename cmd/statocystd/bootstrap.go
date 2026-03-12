package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"

	"statocyst/internal/store"
)

type bootstrapHandler struct {
	active        atomic.Value
	startupHealth map[string]any
}

func newBootstrapHandler(startupMode store.StorageStartupMode, stateBackend, queueBackend string) *bootstrapHandler {
	if strings.TrimSpace(stateBackend) == "" {
		stateBackend = "memory"
	}
	if strings.TrimSpace(queueBackend) == "" {
		queueBackend = "memory"
	}

	return &bootstrapHandler{
		startupHealth: map[string]any{
			"status":      "degraded",
			"boot_status": "starting",
			"storage": map[string]any{
				"startup_mode": startupMode,
				"state": map[string]any{
					"backend": stateBackend,
					"healthy": false,
					"error":   "startup in progress",
				},
				"queue": map[string]any{
					"backend":            queueBackend,
					"healthy":            false,
					"error":              "startup in progress",
					"available_messages": 0,
					"leased_messages":    0,
				},
			},
		},
	}
}

func (h *bootstrapHandler) SetReady(next http.Handler) {
	h.active.Store(next)
}

func (h *bootstrapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if next := h.active.Load(); next != nil {
		next.(http.Handler).ServeHTTP(w, r)
		return
	}

	switch r.URL.Path {
	case "/ping":
		handlePing(w, r)
	case "/health":
		h.handleStartupHealth(w, r)
	default:
		h.handleStarting(w, r)
	}
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *bootstrapHandler) handleStartupHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(h.startupHealth)
}

func (h *bootstrapHandler) handleStarting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "1")
	if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/openapi.yaml" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "starting",
			"message": "statocyst is starting",
		})
		return
	}

	http.Error(w, "statocyst is starting", http.StatusServiceUnavailable)
}

func configuredBackendFromEnv(raw, fallback string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return fallback
	}
	return value
}
