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

	"moltenhub/internal/model"
	"moltenhub/internal/store"
)

var allowedContentTypes = map[string]struct{}{
	"text/plain":       {},
	"application/json": {},
}

const defaultMessageLease = 60 * time.Second
const pullWaiterRecheckInterval = time.Second

type publishRequest struct {
	ToAgentUUID string  `json:"to_agent_uuid"`
	ToAgentURI  string  `json:"to_agent_uri,omitempty"`
	ContentType string  `json:"content_type"`
	Payload     string  `json:"payload"`
	ClientMsgID *string `json:"client_msg_id,omitempty"`
}

type deliveryActionRequest struct {
	DeliveryID string `json:"delivery_id"`
}

type runtimeHandlerError struct {
	status  int
	code    string
	message string
	extras  map[string]any
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
		"first_received_at":   record.FirstReceivedAt,
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

func queueRuntimeFailureSummary(operation string, err error) string {
	base := strings.TrimSpace("queue " + operation + " failed")
	detail := strings.TrimSpace(store.SanitizeErrorWithDetail(err))
	if detail == "" {
		return base
	}
	return base + ": " + detail
}

func writeRuntimeHandlerError(w http.ResponseWriter, err *runtimeHandlerError) {
	if err == nil {
		return
	}
	writeErrorWithHintAndExtras(w, err.status, err.code, err.message, nil, err.extras)
}

func (h *Handler) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !requireJSONRequestContentType(w, r) {
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

	result, handlerErr := h.publishFromAgent(r.Context(), senderAgentUUID, req)
	if handlerErr != nil {
		writeRuntimeHandlerError(w, handlerErr)
		return
	}
	writeAgentRuntimeSuccess(w, http.StatusAccepted, result)
}

func (h *Handler) publishFromAgent(ctx context.Context, senderAgentUUID string, req publishRequest) (map[string]any, *runtimeHandlerError) {
	req.ToAgentUUID = normalizeUUID(req.ToAgentUUID)
	req.ToAgentURI = strings.TrimSpace(req.ToAgentURI)
	req.ContentType = strings.TrimSpace(req.ContentType)

	if _, ok := allowedContentTypes[req.ContentType]; !ok {
		return nil, &runtimeHandlerError{
			status:  http.StatusBadRequest,
			code:    "invalid_content_type",
			message: "content_type must be one of: text/plain, application/json",
		}
	}

	senderAgent, err := h.control.GetAgentByUUID(senderAgentUUID)
	if err != nil {
		return nil, &runtimeHandlerError{
			status:  http.StatusUnauthorized,
			code:    "unauthorized",
			message: "missing or invalid bearer token",
		}
	}

	if req.ToAgentUUID == "" && req.ToAgentURI == "" {
		return nil, &runtimeHandlerError{
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: "to_agent_uuid or to_agent_uri is required",
		}
	}

	localBase := normalizeCanonicalBaseURL(h.canonicalBaseURL)
	if req.ToAgentURI != "" {
		targetBase, targetRef, err := splitCanonicalAgentURI(req.ToAgentURI)
		if err != nil {
			return nil, &runtimeHandlerError{
				status:  http.StatusBadRequest,
				code:    "invalid_to_agent_uri",
				message: "to_agent_uri must be a valid canonical agent URI",
			}
		}
		if targetBase != localBase {
			peer, err := h.control.ResolvePeerByCanonicalBase(targetBase)
			if err != nil {
				return nil, &runtimeHandlerError{
					status:  http.StatusNotFound,
					code:    "unknown_receiver",
					message: "to_agent_uri is not registered on a trusted peer",
				}
			}
			remoteScope := remoteOrgHandleFromAgentRef(targetRef)
			if !h.hasActiveFederatedTrustPath(senderAgent, senderAgentUUID, peer.PeerID, req.ToAgentURI, targetRef) {
				h.control.RecordMessageDropped(senderAgent.OrgID)
				return map[string]any{
					"status": "dropped",
					"reason": "no_trust_path",
				}, nil
			}
			messageID, err := newUUIDv7()
			if err != nil {
				return nil, &runtimeHandlerError{
					status:  http.StatusInternalServerError,
					code:    "id_generation_failed",
					message: "failed to create message_id",
				}
			}
			message := model.Message{
				MessageID:      messageID,
				FromAgentUUID:  senderAgentUUID,
				FromAgentID:    senderAgent.AgentID,
				FromAgentURI:   h.agentURI(senderAgent),
				ToAgentURI:     req.ToAgentURI,
				SenderOrgID:    senderAgent.OrgID,
				ReceiverOrgID:  remoteScope,
				ReceiverPeerID: peer.PeerID,
				ContentType:    req.ContentType,
				Payload:        req.Payload,
				ClientMsgID:    req.ClientMsgID,
				CreatedAt:      h.now().UTC(),
			}
			record, replay, err := h.control.CreateOrGetMessageRecord(message, message.CreatedAt)
			if err != nil {
				summary := stateRuntimeFailureSummary("message register", err)
				h.setStateRuntimeError(summary)
				log.Printf(
					"publish register message failed: from_agent_uuid=%s to_agent_uri=%s message_id=%s err=%v",
					senderAgentUUID,
					req.ToAgentURI,
					messageID,
					err,
				)
				return nil, &runtimeHandlerError{
					status:  http.StatusInternalServerError,
					code:    "store_error",
					message: "failed to register message",
				}
			}
			h.clearStateRuntimeError()
			if replay {
				return publishResponse(record, true), nil
			}
			outboundID, err := h.idFactory()
			if err != nil {
				_ = h.control.AbortMessageRecord(message.MessageID)
				return nil, &runtimeHandlerError{
					status:  http.StatusInternalServerError,
					code:    "id_generation_failed",
					message: "failed to create outbound_id",
				}
			}
			if _, err := h.control.EnqueuePeerOutbound(peer.PeerID, outboundID, message, message.CreatedAt); err != nil {
				_ = h.control.AbortMessageRecord(message.MessageID)
				return nil, &runtimeHandlerError{
					status:  http.StatusInternalServerError,
					code:    "store_error",
					message: "failed to enqueue peer delivery",
				}
			}
			h.processPeerOutboxes(ctx, 1)
			updatedRecord, err := h.control.GetMessageRecord(message.MessageID)
			if err == nil {
				record = updatedRecord
			}
			h.control.RecordMessageQueued(senderAgent.OrgID)
			return publishResponse(record, false), nil
		}
		if req.ToAgentUUID == "" {
			resolvedUUID, err := h.control.ResolveAgentUUID(targetRef)
			if err != nil {
				if errors.Is(err, store.ErrAgentNotFound) {
					return nil, &runtimeHandlerError{
						status:  http.StatusNotFound,
						code:    "unknown_receiver",
						message: "to_agent_uri is not registered",
					}
				}
				return nil, &runtimeHandlerError{
					status:  http.StatusInternalServerError,
					code:    "store_error",
					message: "failed to resolve receiver",
				}
			}
			req.ToAgentUUID = resolvedUUID
		}
	}
	if !validateUUID(req.ToAgentUUID) {
		return nil, &runtimeHandlerError{
			status:  http.StatusBadRequest,
			code:    "invalid_to_agent_uuid",
			message: "to_agent_uuid must be a valid UUID",
		}
	}

	targetAgent, err := h.control.GetAgentByUUID(req.ToAgentUUID)
	if err != nil {
		if errors.Is(err, store.ErrAgentNotFound) {
			return nil, &runtimeHandlerError{
				status:  http.StatusNotFound,
				code:    "unknown_receiver",
				message: "to_agent_uuid is not registered",
			}
		}
		return nil, &runtimeHandlerError{
			status:  http.StatusInternalServerError,
			code:    "store_error",
			message: "failed to resolve receiver",
		}
	}
	if req.ToAgentURI != "" && req.ToAgentURI != h.agentURI(targetAgent) {
		return nil, &runtimeHandlerError{
			status:  http.StatusBadRequest,
			code:    "agent_ref_mismatch",
			message: "to_agent_uuid and to_agent_uri refer to different agents",
		}
	}
	if validationErr := validateSkillActivationRequest(targetAgent, req.ContentType, req.Payload); validationErr != nil {
		return nil, validationErr
	}

	senderOrgID, receiverOrgID, err := h.control.CanPublish(senderAgentUUID, targetAgent.AgentUUID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAgentNotFound):
			return nil, &runtimeHandlerError{
				status:  http.StatusNotFound,
				code:    "unknown_receiver",
				message: "to_agent_uuid is not registered",
			}
		case errors.Is(err, store.ErrNoTrustPath):
			h.control.RecordMessageDropped(senderOrgID)
			return map[string]any{
				"status": "dropped",
				"reason": "no_trust_path",
			}, nil
		default:
			return nil, &runtimeHandlerError{
				status:  http.StatusInternalServerError,
				code:    "store_error",
				message: "failed to authorize publish",
			}
		}
	}

	messageID, err := newUUIDv7()
	if err != nil {
		return nil, &runtimeHandlerError{
			status:  http.StatusInternalServerError,
			code:    "id_generation_failed",
			message: "failed to create message_id",
		}
	}

	message := model.Message{
		MessageID:     messageID,
		FromAgentUUID: senderAgentUUID,
		ToAgentUUID:   targetAgent.AgentUUID,
		FromAgentID:   senderAgent.AgentID,
		ToAgentID:     targetAgent.AgentID,
		FromAgentURI:  h.agentURI(senderAgent),
		ToAgentURI:    h.agentURI(targetAgent),
		SenderOrgID:   senderOrgID,
		ReceiverOrgID: receiverOrgID,
		ContentType:   req.ContentType,
		Payload:       req.Payload,
		ClientMsgID:   req.ClientMsgID,
		CreatedAt:     h.now().UTC(),
	}

	record, replay, err := h.control.CreateOrGetMessageRecord(message, message.CreatedAt)
	if err != nil {
		summary := stateRuntimeFailureSummary("message register", err)
		h.setStateRuntimeError(summary)
		log.Printf(
			"publish register message failed: from_agent_uuid=%s to_agent_uuid=%s message_id=%s err=%v",
			senderAgentUUID,
			targetAgent.AgentUUID,
			messageID,
			err,
		)
		return nil, &runtimeHandlerError{
			status:  http.StatusInternalServerError,
			code:    "store_error",
			message: "failed to register message",
		}
	}
	h.clearStateRuntimeError()
	if replay {
		return publishResponse(record, true), nil
	}

	if err := h.queue.Enqueue(ctx, message); err != nil {
		_ = h.control.AbortMessageRecord(message.MessageID)
		summary := queueRuntimeFailureSummary("enqueue", err)
		h.setQueueRuntimeError(summary)
		log.Printf(
			"publish enqueue failed: from_agent_uuid=%s to_agent_uuid=%s message_id=%s err_summary=%q",
			senderAgentUUID,
			targetAgent.AgentUUID,
			messageID,
			summary,
		)
		if errors.Is(err, store.ErrAgentNotFound) {
			return nil, &runtimeHandlerError{
				status:  http.StatusNotFound,
				code:    "unknown_receiver",
				message: "to_agent_uuid is not registered",
			}
		}
		return nil, &runtimeHandlerError{
			status:  http.StatusInternalServerError,
			code:    "store_error",
			message: "failed to enqueue message",
		}
	}

	h.clearQueueRuntimeError()
	h.control.RecordMessageQueued(senderOrgID)
	h.waiters.Notify(targetAgent.AgentUUID)
	return publishResponse(record, false), nil
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

	status, result, handlerErr := h.pullForAgent(r.Context(), receiverAgentUUID, timeout)
	if handlerErr != nil {
		writeRuntimeHandlerError(w, handlerErr)
		return
	}
	if status == 0 {
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeAgentRuntimeSuccess(w, status, result)
}

func (h *Handler) pullForAgent(ctx context.Context, receiverAgentUUID string, timeout time.Duration) (int, map[string]any, *runtimeHandlerError) {
	h.requeueExpiredLeases(ctx)

	deadline := h.now().Add(timeout)
	for {
		if message, ok, err := h.queue.Dequeue(ctx, receiverAgentUUID); err != nil {
			summary := queueRuntimeFailureSummary("dequeue", err)
			h.setQueueRuntimeError(summary)
			log.Printf("pull dequeue failed: receiver_agent_uuid=%s err_summary=%q", receiverAgentUUID, summary)
			return 0, nil, &runtimeHandlerError{
				status:  http.StatusInternalServerError,
				code:    "store_error",
				message: "failed to dequeue message",
			}
		} else if ok {
			result, handlerErr := h.claimMessageForAgent(ctx, receiverAgentUUID, message)
			if handlerErr != nil {
				return 0, nil, handlerErr
			}
			return http.StatusOK, result, nil
		} else {
			h.clearQueueRuntimeError()
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return http.StatusNoContent, nil, nil
		}

		notifyCh, cancel := h.waiters.Register(receiverAgentUUID)
		if message, ok, err := h.queue.Dequeue(ctx, receiverAgentUUID); err != nil {
			summary := queueRuntimeFailureSummary("dequeue", err)
			h.setQueueRuntimeError(summary)
			log.Printf("pull dequeue failed after waiter register: receiver_agent_uuid=%s err_summary=%q", receiverAgentUUID, summary)
			cancel()
			return 0, nil, &runtimeHandlerError{
				status:  http.StatusInternalServerError,
				code:    "store_error",
				message: "failed to dequeue message",
			}
		} else if ok {
			cancel()
			result, handlerErr := h.claimMessageForAgent(ctx, receiverAgentUUID, message)
			if handlerErr != nil {
				return 0, nil, handlerErr
			}
			return http.StatusOK, result, nil
		} else {
			h.clearQueueRuntimeError()
		}

		waitInterval := remaining
		if waitInterval > pullWaiterRecheckInterval {
			waitInterval = pullWaiterRecheckInterval
		}
		timer := time.NewTimer(waitInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancel()
			return 0, nil, nil
		case <-notifyCh:
			timer.Stop()
			cancel()
		case <-timer.C:
			cancel()
			if time.Until(deadline) <= 0 {
				return http.StatusNoContent, nil, nil
			}
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
	if !requireJSONRequestContentType(w, r) {
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
	record, handlerErr := h.ackDeliveryForAgent(receiverAgentUUID, req.DeliveryID)
	if handlerErr != nil {
		writeRuntimeHandlerError(w, handlerErr)
		return
	}
	writeAgentRuntimeSuccess(w, http.StatusOK, messageStatusResponse(record))
}

func (h *Handler) handleNackDelivery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !requireJSONRequestContentType(w, r) {
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
	record, handlerErr := h.nackDeliveryForAgent(r.Context(), receiverAgentUUID, req.DeliveryID)
	if handlerErr != nil {
		writeRuntimeHandlerError(w, handlerErr)
		return
	}
	writeAgentRuntimeSuccess(w, http.StatusOK, messageStatusResponse(record))
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
	record, handlerErr := h.messageStatusForAgent(agentUUID, messageID)
	if handlerErr != nil {
		writeRuntimeHandlerError(w, handlerErr)
		return
	}
	writeAgentRuntimeSuccess(w, http.StatusOK, messageStatusResponse(record))
}

func (h *Handler) ackDeliveryForAgent(receiverAgentUUID, deliveryID string) (model.MessageRecord, *runtimeHandlerError) {
	record, err := h.control.AckMessageDelivery(receiverAgentUUID, deliveryID, h.now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrMessageDeliveryNotFound):
			return model.MessageRecord{}, &runtimeHandlerError{
				status:  http.StatusNotFound,
				code:    "unknown_delivery",
				message: "delivery_id is not active",
			}
		case errors.Is(err, store.ErrMessageDeliveryMismatch):
			return model.MessageRecord{}, &runtimeHandlerError{
				status:  http.StatusForbidden,
				code:    "forbidden",
				message: "delivery_id does not belong to this agent",
			}
		default:
			return model.MessageRecord{}, &runtimeHandlerError{
				status:  http.StatusInternalServerError,
				code:    "store_error",
				message: "failed to acknowledge delivery",
			}
		}
	}
	return record, nil
}

func (h *Handler) nackDeliveryForAgent(ctx context.Context, receiverAgentUUID, deliveryID string) (model.MessageRecord, *runtimeHandlerError) {
	message, record, err := h.control.ReleaseMessageDelivery(receiverAgentUUID, deliveryID, h.now().UTC(), "receiver_nack")
	if err != nil {
		switch {
		case errors.Is(err, store.ErrMessageDeliveryNotFound):
			return model.MessageRecord{}, &runtimeHandlerError{
				status:  http.StatusNotFound,
				code:    "unknown_delivery",
				message: "delivery_id is not active",
			}
		case errors.Is(err, store.ErrMessageDeliveryMismatch):
			return model.MessageRecord{}, &runtimeHandlerError{
				status:  http.StatusForbidden,
				code:    "forbidden",
				message: "delivery_id does not belong to this agent",
			}
		default:
			return model.MessageRecord{}, &runtimeHandlerError{
				status:  http.StatusInternalServerError,
				code:    "store_error",
				message: "failed to requeue delivery",
			}
		}
	}
	if err := h.queue.Enqueue(ctx, message); err != nil {
		h.setQueueRuntimeError(queueRuntimeFailureSummary("requeue", err))
		return model.MessageRecord{}, &runtimeHandlerError{
			status:  http.StatusInternalServerError,
			code:    "store_error",
			message: "failed to requeue message",
		}
	}
	h.clearQueueRuntimeError()
	h.waiters.Notify(receiverAgentUUID)
	return record, nil
}

func (h *Handler) messageStatusForAgent(agentUUID, messageID string) (model.MessageRecord, *runtimeHandlerError) {
	record, err := h.control.GetMessageRecord(strings.TrimSpace(messageID))
	if err != nil {
		if errors.Is(err, store.ErrMessageNotFound) {
			return model.MessageRecord{}, &runtimeHandlerError{
				status:  http.StatusNotFound,
				code:    "unknown_message",
				message: "message_id is not registered",
			}
		}
		return model.MessageRecord{}, &runtimeHandlerError{
			status:  http.StatusInternalServerError,
			code:    "store_error",
			message: "failed to load message status",
		}
	}
	if record.Message.FromAgentUUID != agentUUID && record.Message.ToAgentUUID != agentUUID {
		return model.MessageRecord{}, &runtimeHandlerError{
			status:  http.StatusForbidden,
			code:    "forbidden",
			message: "message_id is not visible to this agent",
		}
	}
	return record, nil
}

func (h *Handler) requeueExpiredLeases(ctx context.Context) {
	messages, err := h.control.ExpireMessageLeases(h.now().UTC())
	if err != nil {
		summary := queueRuntimeFailureSummary("lease expiry", err)
		h.setQueueRuntimeError(summary)
		log.Printf("expire message leases failed: err_summary=%q", summary)
		return
	}
	for _, message := range messages {
		if err := h.queue.Enqueue(ctx, message); err != nil {
			summary := queueRuntimeFailureSummary("requeue", err)
			h.setQueueRuntimeError(summary)
			log.Printf("requeue expired lease failed: message_id=%s err_summary=%q", message.MessageID, summary)
			return
		}
		h.waiters.Notify(message.ToAgentUUID)
	}
	if len(messages) > 0 {
		h.clearQueueRuntimeError()
	}
}

func (h *Handler) writeClaimedMessage(w http.ResponseWriter, r *http.Request, receiverAgentUUID string, message model.Message) bool {
	result, handlerErr := h.claimMessageForAgent(r.Context(), receiverAgentUUID, message)
	if handlerErr != nil {
		writeRuntimeHandlerError(w, handlerErr)
		return false
	}
	writeAgentRuntimeSuccess(w, http.StatusOK, result)
	return true
}

func (h *Handler) claimMessageForAgent(ctx context.Context, receiverAgentUUID string, message model.Message) (map[string]any, *runtimeHandlerError) {
	h.clearQueueRuntimeError()
	deliveryID, err := h.idFactory()
	if err != nil {
		return nil, &runtimeHandlerError{
			status:  http.StatusInternalServerError,
			code:    "id_generation_failed",
			message: "failed to create delivery_id",
		}
	}
	leasedAt := h.now().UTC()
	leaseExpiresAt := leasedAt.Add(defaultMessageLease)
	delivery, record, err := h.control.LeaseMessage(message.MessageID, receiverAgentUUID, deliveryID, leasedAt, leaseExpiresAt)
	if err != nil {
		_ = h.queue.Enqueue(ctx, message)
		h.setQueueRuntimeError(queueRuntimeFailureSummary("lease", err))
		return nil, &runtimeHandlerError{
			status:  http.StatusInternalServerError,
			code:    "store_error",
			message: "failed to lease message",
		}
	}
	return deliveryResponse(record, delivery), nil
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
