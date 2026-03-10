package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
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

const defaultMessageLease = 60 * time.Second

type publishRequest struct {
	ToAgentUUID string  `json:"to_agent_uuid"`
	ContentType string  `json:"content_type"`
	Payload     string  `json:"payload"`
	ClientMsgID *string `json:"client_msg_id,omitempty"`
}

type deliveryActionRequest struct {
	DeliveryID string `json:"delivery_id"`
}

func publishResponse(record model.MessageRecord, idempotent bool) map[string]any {
	return map[string]any{
		"message_id":        record.Message.MessageID,
		"status":            record.Status,
		"accepted_at":       record.AcceptedAt,
		"idempotent_replay": idempotent,
	}
}

func deliveryResponse(record model.MessageRecord, delivery model.MessageDelivery) map[string]any {
	return map[string]any{
		"message":  record.Message,
		"status":   record.Status,
		"delivery": delivery,
	}
}

func messageStatusResponse(record model.MessageRecord) map[string]any {
	return map[string]any{
		"message":             record.Message,
		"status":              record.Status,
		"accepted_at":         record.AcceptedAt,
		"updated_at":          record.UpdatedAt,
		"last_leased_at":      record.LastLeasedAt,
		"lease_expires_at":    record.LeaseExpiresAt,
		"acked_at":            record.AckedAt,
		"last_delivery_id":    record.LastDeliveryID,
		"delivery_attempts":   record.DeliveryAttempts,
		"requeue_count":       record.RequeueCount,
		"idempotent_replays":  record.IdempotentReplays,
		"last_failure_reason": record.LastFailureReason,
		"last_failure_at":     record.LastFailureAt,
	}
}

func (h *Handler) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}

	senderAgentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	var req publishRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}

	req.ToAgentUUID = normalizeUUID(req.ToAgentUUID)
	req.ContentType = strings.TrimSpace(req.ContentType)

	if !validateUUID(req.ToAgentUUID) {
		writeError(w, http.StatusBadRequest, "invalid_to_agent_uuid", "to_agent_uuid must be a valid UUID")
		return
	}
	if _, ok := allowedContentTypes[req.ContentType]; !ok {
		writeError(w, http.StatusBadRequest, "invalid_content_type", "content_type must be one of: text/plain, application/json")
		return
	}

	senderAgent, err := h.control.GetAgentByUUID(senderAgentUUID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	targetAgent, err := h.control.GetAgentByUUID(req.ToAgentUUID)
	if err != nil {
		if errors.Is(err, store.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "unknown_receiver", "to_agent_uuid is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to resolve receiver")
		return
	}

	senderOrgID, receiverOrgID, err := h.control.CanPublish(senderAgentUUID, targetAgent.AgentUUID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "unknown_receiver", "to_agent_uuid is not registered")
		case errors.Is(err, store.ErrNoTrustPath):
			h.control.RecordMessageDropped(senderOrgID)
			writeJSON(w, http.StatusAccepted, map[string]string{
				"status": "dropped",
				"reason": "no_trust_path",
			})
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
		MessageID:     messageID,
		FromAgentUUID: senderAgentUUID,
		ToAgentUUID:   targetAgent.AgentUUID,
		FromAgentID:   senderAgent.AgentID,
		ToAgentID:     targetAgent.AgentID,
		SenderOrgID:   senderOrgID,
		ReceiverOrgID: receiverOrgID,
		ContentType:   req.ContentType,
		Payload:       req.Payload,
		ClientMsgID:   req.ClientMsgID,
		CreatedAt:     h.now().UTC(),
	}

	record, replay, err := h.control.CreateOrGetMessageRecord(message, message.CreatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to register message")
		return
	}
	if replay {
		writeJSON(w, http.StatusAccepted, publishResponse(record, true))
		return
	}

	if err := h.queue.Enqueue(r.Context(), message); err != nil {
		_ = h.control.AbortMessageRecord(message.MessageID)
		h.setQueueRuntimeError(err)
		log.Printf(
			"publish enqueue failed: from_agent_uuid=%s to_agent_uuid=%s message_id=%s err=%v",
			senderAgentUUID,
			targetAgent.AgentUUID,
			messageID,
			err,
		)
		if errors.Is(err, store.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "unknown_receiver", "to_agent_uuid is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to enqueue message")
		return
	}

	h.clearQueueRuntimeError()
	h.control.RecordMessageQueued(senderOrgID)
	h.waiters.Notify(targetAgent.AgentUUID)
	writeJSON(w, http.StatusAccepted, publishResponse(record, false))
}

func (h *Handler) handlePull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	receiverAgentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	timeout, err := parsePullTimeout(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_timeout", err.Error())
		return
	}

	h.requeueExpiredLeases(r.Context())

	deadline := h.now().Add(timeout)
	for {
		if message, ok, err := h.queue.Dequeue(r.Context(), receiverAgentUUID); err != nil {
			h.setQueueRuntimeError(err)
			log.Printf("pull dequeue failed: receiver_agent_uuid=%s err=%v", receiverAgentUUID, err)
			writeError(w, http.StatusInternalServerError, "store_error", "failed to dequeue message")
			return
		} else if ok {
			if h.writeClaimedMessage(w, r, receiverAgentUUID, message) {
				return
			}
			return
		} else {
			h.clearQueueRuntimeError()
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		notifyCh, cancel := h.waiters.Register(receiverAgentUUID)
		if message, ok, err := h.queue.Dequeue(r.Context(), receiverAgentUUID); err != nil {
			h.setQueueRuntimeError(err)
			log.Printf("pull dequeue failed after waiter register: receiver_agent_uuid=%s err=%v", receiverAgentUUID, err)
			cancel()
			writeError(w, http.StatusInternalServerError, "store_error", "failed to dequeue message")
			return
		} else if ok {
			cancel()
			if h.writeClaimedMessage(w, r, receiverAgentUUID, message) {
				return
			}
			return
		} else {
			h.clearQueueRuntimeError()
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

func (h *Handler) handleMessageSubroutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	const prefix = "/v1/messages/"
	if !strings.HasPrefix(path, prefix) {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	tail := strings.TrimPrefix(path, prefix)
	switch tail {
	case "publish":
		h.handlePublish(w, r)
		return
	case "pull":
		h.handlePull(w, r)
		return
	case "ack":
		h.handleAckDelivery(w, r)
		return
	case "nack":
		h.handleNackDelivery(w, r)
		return
	}
	if strings.TrimSpace(tail) == "" || strings.Contains(tail, "/") {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	h.handleMessageStatus(w, r, tail)
}

func (h *Handler) handleAckDelivery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	receiverAgentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	var req deliveryActionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	req.DeliveryID = strings.TrimSpace(req.DeliveryID)
	if req.DeliveryID == "" {
		writeError(w, http.StatusBadRequest, "invalid_delivery_id", "delivery_id is required")
		return
	}
	record, err := h.control.AckMessageDelivery(receiverAgentUUID, req.DeliveryID, h.now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrMessageDeliveryNotFound):
			writeError(w, http.StatusNotFound, "unknown_delivery", "delivery_id is not active")
		case errors.Is(err, store.ErrMessageDeliveryMismatch):
			writeError(w, http.StatusForbidden, "forbidden", "delivery_id does not belong to this agent")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to acknowledge delivery")
		}
		return
	}
	writeJSON(w, http.StatusOK, messageStatusResponse(record))
}

func (h *Handler) handleNackDelivery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	receiverAgentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	var req deliveryActionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	req.DeliveryID = strings.TrimSpace(req.DeliveryID)
	if req.DeliveryID == "" {
		writeError(w, http.StatusBadRequest, "invalid_delivery_id", "delivery_id is required")
		return
	}
	message, record, err := h.control.ReleaseMessageDelivery(receiverAgentUUID, req.DeliveryID, h.now().UTC(), "receiver_nack")
	if err != nil {
		switch {
		case errors.Is(err, store.ErrMessageDeliveryNotFound):
			writeError(w, http.StatusNotFound, "unknown_delivery", "delivery_id is not active")
		case errors.Is(err, store.ErrMessageDeliveryMismatch):
			writeError(w, http.StatusForbidden, "forbidden", "delivery_id does not belong to this agent")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to requeue delivery")
		}
		return
	}
	if err := h.queue.Enqueue(r.Context(), message); err != nil {
		h.setQueueRuntimeError(err)
		writeError(w, http.StatusInternalServerError, "store_error", "failed to requeue message")
		return
	}
	h.clearQueueRuntimeError()
	h.waiters.Notify(receiverAgentUUID)
	writeJSON(w, http.StatusOK, messageStatusResponse(record))
}

func (h *Handler) handleMessageStatus(w http.ResponseWriter, r *http.Request, messageID string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	agentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	record, err := h.control.GetMessageRecord(strings.TrimSpace(messageID))
	if err != nil {
		if errors.Is(err, store.ErrMessageNotFound) {
			writeError(w, http.StatusNotFound, "unknown_message", "message_id is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to load message status")
		return
	}
	if record.Message.FromAgentUUID != agentUUID && record.Message.ToAgentUUID != agentUUID {
		writeError(w, http.StatusForbidden, "forbidden", "message_id is not visible to this agent")
		return
	}
	writeJSON(w, http.StatusOK, messageStatusResponse(record))
}

func (h *Handler) requeueExpiredLeases(ctx context.Context) {
	messages, err := h.control.ExpireMessageLeases(h.now().UTC())
	if err != nil {
		h.setQueueRuntimeError(err)
		log.Printf("expire message leases failed: err=%v", err)
		return
	}
	for _, message := range messages {
		if err := h.queue.Enqueue(ctx, message); err != nil {
			h.setQueueRuntimeError(err)
			log.Printf("requeue expired lease failed: message_id=%s err=%v", message.MessageID, err)
			return
		}
		h.waiters.Notify(message.ToAgentUUID)
	}
	if len(messages) > 0 {
		h.clearQueueRuntimeError()
	}
}

func (h *Handler) writeClaimedMessage(w http.ResponseWriter, r *http.Request, receiverAgentUUID string, message model.Message) bool {
	h.clearQueueRuntimeError()
	deliveryID, err := h.idFactory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to create delivery_id")
		return false
	}
	leasedAt := h.now().UTC()
	leaseExpiresAt := leasedAt.Add(defaultMessageLease)
	delivery, record, err := h.control.LeaseMessage(message.MessageID, receiverAgentUUID, deliveryID, leasedAt, leaseExpiresAt)
	if err != nil {
		_ = h.queue.Enqueue(r.Context(), message)
		h.setQueueRuntimeError(err)
		writeError(w, http.StatusInternalServerError, "store_error", "failed to lease message")
		return false
	}
	writeJSON(w, http.StatusOK, deliveryResponse(record, delivery))
	return true
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
