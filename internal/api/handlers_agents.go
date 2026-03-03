package api

import (
	"errors"
	"net/http"
	"strings"

	"statocyst/internal/auth"
	"statocyst/internal/store"
)

type registerRequest struct {
	AgentID string `json:"agent_id"`
}

func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}

	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}

	req.AgentID = strings.TrimSpace(req.AgentID)
	if !validateAgentID(req.AgentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "agent_id must match [A-Za-z0-9._:-]{1,128}")
		return
	}

	token, err := auth.GenerateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_generation_failed", "failed to generate token")
		return
	}

	tokenHash := auth.HashToken(token)
	agent, err := h.store.RegisterAgent(req.AgentID, tokenHash, h.now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrAgentExists) {
			writeError(w, http.StatusConflict, "agent_exists", "agent_id already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to register agent")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"agent_id": agent.AgentID,
		"token":    token,
	})
}
