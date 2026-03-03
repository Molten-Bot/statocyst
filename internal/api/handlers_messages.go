package api

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"statocyst/internal/model"
	"statocyst/internal/store"
)

var allowedContentTypes = map[string]struct{}{
	"text/plain":       {},
	"application/json": {},
}

type publishRequest struct {
	ToAgentID   string  `json:"to_agent_id"`
	ContentType string  `json:"content_type"`
	Payload     string  `json:"payload"`
	ClientMsgID *string `json:"client_msg_id,omitempty"`
}

func (h *Handler) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}

	senderAgentID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	var req publishRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}

	req.ToAgentID = strings.TrimSpace(req.ToAgentID)
	req.ContentType = strings.TrimSpace(req.ContentType)

	if !validateAgentID(req.ToAgentID) {
		writeError(w, http.StatusBadRequest, "invalid_to_agent_id", "to_agent_id must match [A-Za-z0-9._:-]{1,128}")
		return
	}
	if _, ok := allowedContentTypes[req.ContentType]; !ok {
		writeError(w, http.StatusBadRequest, "invalid_content_type", "content_type must be one of: text/plain, application/json")
		return
	}

	if err := h.store.CanPublish(senderAgentID, req.ToAgentID); err != nil {
		switch {
		case errors.Is(err, store.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "unknown_receiver", "to_agent_id is not registered")
		case errors.Is(err, store.ErrNoActiveBond):
			writeJSON(w, http.StatusAccepted, map[string]string{
				"status": "dropped",
				"reason": "no_active_bond",
			})
		case errors.Is(err, store.ErrSenderUnknown):
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to authorize publish")
		}
		return
	}

	messageID, err := newUUIDv7()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to create message_id")
		return
	}

	message := model.Message{
		MessageID:   messageID,
		FromAgentID: senderAgentID,
		ToAgentID:   req.ToAgentID,
		ContentType: req.ContentType,
		Payload:     req.Payload,
		ClientMsgID: req.ClientMsgID,
		CreatedAt:   h.now().UTC(),
	}

	if err := h.store.Enqueue(message); err != nil {
		if errors.Is(err, store.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "unknown_receiver", "to_agent_id is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to enqueue message")
		return
	}

	h.waiters.Notify(req.ToAgentID)
	writeJSON(w, http.StatusAccepted, map[string]string{
		"message_id": messageID,
		"status":     "queued",
	})
}

func (h *Handler) handlePull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	receiverAgentID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	timeout, err := parsePullTimeout(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_timeout", err.Error())
		return
	}

	deadline := h.now().Add(timeout)
	for {
		if message, ok := h.store.PopNext(receiverAgentID); ok {
			writeJSON(w, http.StatusOK, map[string]any{"message": message})
			return
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		notifyCh, cancel := h.waiters.Register(receiverAgentID)
		if message, ok := h.store.PopNext(receiverAgentID); ok {
			cancel()
			writeJSON(w, http.StatusOK, map[string]any{"message": message})
			return
		}

		timer := time.NewTimer(remaining)
		select {
		case <-r.Context().Done():
			timer.Stop()
			cancel()
			return
		case <-notifyCh:
			timer.Stop()
			cancel()
		case <-timer.C:
			cancel()
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
}

func newUUIDv7() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}

	ts := time.Now().UnixMilli()
	raw[0] = byte(ts >> 40)
	raw[1] = byte(ts >> 32)
	raw[2] = byte(ts >> 24)
	raw[3] = byte(ts >> 16)
	raw[4] = byte(ts >> 8)
	raw[5] = byte(ts)

	// RFC 9562 UUIDv7 version and RFC 4122 variant bits.
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80

	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		uint32(raw[0])<<24|uint32(raw[1])<<16|uint32(raw[2])<<8|uint32(raw[3]),
		uint16(raw[4])<<8|uint16(raw[5]),
		uint16(raw[6])<<8|uint16(raw[7]),
		uint16(raw[8])<<8|uint16(raw[9]),
		uint64(raw[10])<<40|uint64(raw[11])<<32|uint64(raw[12])<<24|uint64(raw[13])<<16|uint64(raw[14])<<8|uint64(raw[15]),
	), nil
}
