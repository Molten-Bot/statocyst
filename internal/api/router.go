package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"statocyst/internal/auth"
	"statocyst/internal/longpoll"
	"statocyst/internal/store"
)

const (
	maxPullTimeoutMS     = 30000
	defaultPullTimeoutMS = 5000
)

var agentIDRegex = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type Handler struct {
	store   *store.MemoryStore
	waiters *longpoll.Waiters
	now     func() time.Time
}

func NewHandler(st *store.MemoryStore, waiters *longpoll.Waiters) *Handler {
	return &Handler{
		store:   st,
		waiters: waiters,
		now:     time.Now,
	}
}

func NewRouter(handler *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handler.handleHealthz)
	mux.HandleFunc("/openapi.yaml", handler.handleOpenAPIYAML)
	mux.HandleFunc("/v1/agents/register", handler.handleRegister)
	mux.HandleFunc("/v1/bonds", handler.handleBonds)
	mux.HandleFunc("/v1/bonds/", handler.handleBondByID)
	mux.HandleFunc("/v1/messages/publish", handler.handlePublish)
	mux.HandleFunc("/v1/messages/pull", handler.handlePull)
	return mux
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]string{
		"error":   code,
		"message": message,
	})
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func decodeJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func validateAgentID(agentID string) bool {
	return agentIDRegex.MatchString(agentID)
}

func parseBondIDPath(path string) (string, bool) {
	const prefix = "/v1/bonds/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	trimmed := strings.TrimPrefix(path, prefix)
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return "", false
	}
	return trimmed, true
}

func (h *Handler) authenticateAgent(r *http.Request) (string, error) {
	token, err := auth.ExtractBearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return "", err
	}
	tokenHash := auth.HashToken(token)
	return h.store.AgentIDForTokenHash(tokenHash)
}

func parsePullTimeout(r *http.Request) (time.Duration, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("timeout_ms"))
	if raw == "" {
		return time.Duration(defaultPullTimeoutMS) * time.Millisecond, nil
	}
	ms, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("timeout_ms must be an integer")
	}
	if ms < 0 || ms > maxPullTimeoutMS {
		return 0, errors.New("timeout_ms must be in range 0..30000")
	}
	return time.Duration(ms) * time.Millisecond, nil
}
