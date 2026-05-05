package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"moltenhub/internal/model"
	"moltenhub/internal/store"
)

const (
	runtimeEnvelopeWebSocketPullTimeoutDefault = 20 * time.Second
	runtimePresenceStatusOnline                = "online"
	runtimePresenceStatusOffline               = "offline"
	runtimePresenceOfflineAfter                = 2 * time.Hour
)

var (
	runtimeEnvelopeWSUpgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(_ *http.Request) bool {
			return true
		},
	}
)

type runtimeEnvelopeOfflineRequest struct {
	SessionKey string `json:"session_key,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type runtimeEnvelopeWSRequest struct {
	Type        string          `json:"type"`
	RequestID   string          `json:"request_id,omitempty"`
	ToAgentUUID string          `json:"to_agent_uuid,omitempty"`
	ToAgentURI  string          `json:"to_agent_uri,omitempty"`
	ClientMsgID *string         `json:"client_msg_id,omitempty"`
	Message     map[string]any  `json:"message,omitempty"`
	Activity    string          `json:"activity,omitempty"`
	Activities  json.RawMessage `json:"activities,omitempty"`
	Category    string          `json:"category,omitempty"`
	Status      string          `json:"status,omitempty"`
	State       string          `json:"state,omitempty"`
	DeliveryID  string          `json:"delivery_id,omitempty"`
	MessageID   string          `json:"message_id,omitempty"`
	TimeoutMS   *int            `json:"timeout_ms,omitempty"`
}

// runtimeEnvelopeWSResponseWriter bridges websocket upgrades through wrappers that may
// not expose http.Hijacker directly, but still support hijacking via
// http.ResponseController.
type runtimeEnvelopeWSResponseWriter struct {
	http.ResponseWriter
}

func (w runtimeEnvelopeWSResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w runtimeEnvelopeWSResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (h *Handler) handleRuntimeEnvelopeOffline(w http.ResponseWriter, r *http.Request, adapterName string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if !requireJSONRequestContentType(w, r) {
		return
	}

	agentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	var req runtimeEnvelopeOfflineRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}

	sessionKey := normalizeRuntimeSessionKey(req.SessionKey)
	agent, err := h.setRuntimeWebSocketPresence(agentUUID, sessionKey, runtimePresenceStatusOffline, req.Reason)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
		case errors.Is(err, store.ErrInvalidAgentType):
			writeError(w, http.StatusBadRequest, "invalid_agent_type", "metadata.agent_type must be 2-64 chars: a-z, 0-9, ., _, -")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to update agent presence")
		}
		return
	}

	details := map[string]any{"session_key": sessionKey}
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		details["reason"] = reason
	}
	h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "ws_offline", details)

	out := map[string]any{
		"agent": h.agentResponsePayload(agent),
	}
	if presence := h.currentAgentPresence(agent.AgentUUID, agent.Metadata); presence != nil {
		out["presence"] = presence
	}
	writeAgentRuntimeSuccess(w, http.StatusOK, out)
}

func (h *Handler) handleRuntimeEnvelopeWebSocket(w http.ResponseWriter, r *http.Request, defaultProtocol, adapterName string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	agentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	conn, err := runtimeEnvelopeWSUpgrader.Upgrade(runtimeEnvelopeWSResponseWriter{ResponseWriter: w}, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sessionKey := normalizeRuntimeSessionKey(r.URL.Query().Get("session_key"))
	if _, err := h.setRuntimeWebSocketPresence(agentUUID, sessionKey, runtimePresenceStatusOnline, ""); err != nil {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_ = conn.WriteJSON(runtimeEnvelopeWSErrorFromRuntime("", runtimeHandlerErrorForPresenceUpdate(err)))
		return
	}
	defer func() {
		_, _ = h.setRuntimeWebSocketPresence(agentUUID, sessionKey, runtimePresenceStatusOffline, "")
		h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "ws_disconnect", map[string]any{"session_key": sessionKey})
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var writeMu sync.Mutex
	writeEvent := func(event map[string]any) bool {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteJSON(event); err != nil {
			return false
		}
		return true
	}

	h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "ws_connect", map[string]any{"session_key": sessionKey})

	if ok := writeEvent(map[string]any{
		"type":        "session_ready",
		"session_key": sessionKey,
		"transport":   runtimeEnvelopeTransportMetadata(defaultProtocol, "websocket"),
	}); !ok {
		return
	}

	deliveryDone := make(chan struct{})
	go func() {
		defer close(deliveryDone)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if heartbeatErr := h.touchAgentPresenceOnline(agentUUID, sessionKey, "websocket"); heartbeatErr != nil {
				if !writeEvent(runtimeEnvelopeWSErrorFromRuntime("", heartbeatErr)) {
					cancel()
					return
				}
				cancel()
				return
			}

			status, result, handlerErr := h.pullForAgent(ctx, agentUUID, runtimeEnvelopeWebSocketPullTimeoutDefault)
			if ctx.Err() != nil {
				return
			}
			if handlerErr != nil {
				if !writeEvent(runtimeEnvelopeWSErrorFromRuntime("", handlerErr)) {
					cancel()
					return
				}
				continue
			}
			switch status {
			case 0:
				return
			case http.StatusNoContent:
				continue
			case http.StatusOK:
				out := withRuntimeEnvelopeProjection(result, defaultProtocol)
				out["transport"] = runtimeEnvelopeTransportMetadata(defaultProtocol, "websocket")
				if !writeEvent(map[string]any{
					"type":        "delivery",
					"session_key": sessionKey,
					"result":      out,
				}) {
					cancel()
					return
				}
				h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "ws_delivery", map[string]any{
					"message_id":  runtimeEnvelopeMessageIDFromResult(out),
					"session_key": sessionKey,
				})
			default:
				if !writeEvent(runtimeEnvelopeWSError("", status, "unexpected_status", "unexpected pull status")) {
					cancel()
					return
				}
			}
		}
	}()

	for {
		var req runtimeEnvelopeWSRequest
		if err := conn.ReadJSON(&req); err != nil {
			break
		}
		if !h.handleRuntimeEnvelopeWSCommand(ctx, agentUUID, sessionKey, req, defaultProtocol, adapterName, writeEvent) {
			break
		}
	}

	cancel()
	<-deliveryDone
}

func (h *Handler) handleRuntimeEnvelopeWSCommand(
	ctx context.Context,
	agentUUID,
	sessionKey string,
	req runtimeEnvelopeWSRequest,
	defaultProtocol,
	adapterName string,
	writeEvent func(map[string]any) bool,
) bool {
	kind := strings.ToLower(strings.TrimSpace(req.Type))
	requestID := strings.TrimSpace(req.RequestID)
	switch kind {
	case "ping":
		return writeEvent(map[string]any{
			"type":       "pong",
			"request_id": requestID,
		})
	case "publish":
		if len(req.Message) == 0 {
			return writeEvent(runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_request", "message is required"))
		}
		envelope, err := normalizeRuntimeEnvelope(req.Message, h.now().UTC(), defaultProtocol)
		if err != nil {
			if errors.Is(err, errInvalidRuntimeEnvelopeProtocol) {
				return writeEvent(runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_protocol", err.Error()))
			}
			return writeEvent(runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_request", err.Error()))
		}
		payload, err := json.Marshal(envelope)
		if err != nil {
			return writeEvent(runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_request", "message must be a JSON object"))
		}
		result, handlerErr := h.publishFromAgent(ctx, agentUUID, publishRequest{
			ToAgentUUID: req.ToAgentUUID,
			ToAgentURI:  req.ToAgentURI,
			ContentType: "application/json",
			Payload:     string(payload),
			ClientMsgID: req.ClientMsgID,
		})
		if handlerErr != nil {
			return writeEvent(runtimeEnvelopeWSErrorFromRuntime(requestID, handlerErr))
		}
		out := cloneStringAnyMap(result)
		out["transport"] = runtimeEnvelopeTransportMetadata(defaultProtocol, "websocket")
		out["envelope"] = envelope
		h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "ws_publish", map[string]any{
			"message_id":  runtimeEnvelopeMessageIDFromResult(out),
			"session_key": sessionKey,
		})
		return writeEvent(runtimeEnvelopeWSResponse(requestID, http.StatusAccepted, out))
	case "activity", "publish_activity":
		agent, err := h.publishAgentActivity(agentUUID, publishAgentActivityRequest{
			Activity:   req.Activity,
			Activities: req.Activities,
			Category:   req.Category,
			Status:     req.Status,
			State:      req.State,
		})
		if err != nil {
			return writeEvent(runtimeEnvelopeWSErrorFromAgentActivity(requestID, err))
		}
		result := map[string]any{
			"agent":     h.agentResponsePayload(agent),
			"transport": runtimeEnvelopeTransportMetadata(defaultProtocol, "websocket"),
		}
		h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "ws_activity", map[string]any{
			"session_key": sessionKey,
		})
		return writeEvent(runtimeEnvelopeWSResponse(requestID, http.StatusCreated, result))
	case "ack":
		deliveryID := strings.TrimSpace(req.DeliveryID)
		if deliveryID == "" {
			return writeEvent(runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_delivery_id", "delivery_id is required"))
		}
		record, handlerErr := h.ackDeliveryForAgent(agentUUID, deliveryID)
		if handlerErr != nil {
			return writeEvent(runtimeEnvelopeWSErrorFromRuntime(requestID, handlerErr))
		}
		result := withRuntimeEnvelopeProjection(messageStatusResponse(record), defaultProtocol)
		result["transport"] = runtimeEnvelopeTransportMetadata(defaultProtocol, "websocket")
		h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "ws_ack", map[string]any{
			"message_id":  runtimeEnvelopeMessageIDFromResult(result),
			"session_key": sessionKey,
		})
		return writeEvent(runtimeEnvelopeWSResponse(requestID, http.StatusOK, result))
	case "nack":
		deliveryID := strings.TrimSpace(req.DeliveryID)
		if deliveryID == "" {
			return writeEvent(runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_delivery_id", "delivery_id is required"))
		}
		record, handlerErr := h.nackDeliveryForAgent(ctx, agentUUID, deliveryID)
		if handlerErr != nil {
			return writeEvent(runtimeEnvelopeWSErrorFromRuntime(requestID, handlerErr))
		}
		result := withRuntimeEnvelopeProjection(messageStatusResponse(record), defaultProtocol)
		result["transport"] = runtimeEnvelopeTransportMetadata(defaultProtocol, "websocket")
		h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "ws_nack", map[string]any{
			"message_id":  runtimeEnvelopeMessageIDFromResult(result),
			"session_key": sessionKey,
		})
		return writeEvent(runtimeEnvelopeWSResponse(requestID, http.StatusOK, result))
	case "status":
		messageID := strings.TrimSpace(req.MessageID)
		if messageID == "" {
			return writeEvent(runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_message_id", "message_id is required"))
		}
		record, handlerErr := h.messageStatusForAgent(agentUUID, messageID)
		if handlerErr != nil {
			return writeEvent(runtimeEnvelopeWSErrorFromRuntime(requestID, handlerErr))
		}
		result := withRuntimeEnvelopeProjection(messageStatusResponse(record), defaultProtocol)
		result["transport"] = runtimeEnvelopeTransportMetadata(defaultProtocol, "websocket")
		h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "ws_status", map[string]any{
			"message_id":  runtimeEnvelopeMessageIDFromResult(result),
			"session_key": sessionKey,
		})
		return writeEvent(runtimeEnvelopeWSResponse(requestID, http.StatusOK, result))
	case "pull":
		timeout, err := runtimeEnvelopeWSPullTimeout(req.TimeoutMS)
		if err != nil {
			return writeEvent(runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_timeout", err.Error()))
		}
		status, result, handlerErr := h.pullForAgent(ctx, agentUUID, timeout)
		if handlerErr != nil {
			return writeEvent(runtimeEnvelopeWSError(requestID, handlerErr.status, handlerErr.code, handlerErr.message))
		}
		if status == http.StatusNoContent {
			return writeEvent(runtimeEnvelopeWSResponse(requestID, http.StatusNoContent, map[string]any{"status": "empty"}))
		}
		if status == 0 {
			return false
		}
		out := withRuntimeEnvelopeProjection(result, defaultProtocol)
		out["transport"] = runtimeEnvelopeTransportMetadata(defaultProtocol, "websocket")
		h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "ws_pull", map[string]any{
			"message_id":  runtimeEnvelopeMessageIDFromResult(out),
			"session_key": sessionKey,
		})
		return writeEvent(runtimeEnvelopeWSResponse(requestID, http.StatusOK, out))
	default:
		return writeEvent(runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_request", "unsupported websocket command type"))
	}
}

func runtimeEnvelopeWSResponse(requestID string, status int, result map[string]any) map[string]any {
	payload := map[string]any{
		"type":   "response",
		"ok":     true,
		"status": status,
		"result": result,
	}
	if requestID != "" {
		payload["request_id"] = requestID
	}
	return payload
}

func runtimeEnvelopeWSError(requestID string, status int, code, message string) map[string]any {
	hint, hasHint := defaultErrorHint(code)
	errorDetail := map[string]any{
		"code":    strings.TrimSpace(code),
		"message": strings.TrimSpace(message),
	}
	payload := map[string]any{
		"type":           "response",
		"ok":             false,
		"failure":        true,
		"status":         status,
		"message":        strings.TrimSpace(message),
		"error_detail":   errorDetail,
		"Failure":        true,
		"Failure:":       true,
		"Error details":  errorDetail,
		"Error details:": errorDetail,
		"error": map[string]any{
			"code":    strings.TrimSpace(code),
			"message": strings.TrimSpace(message),
		},
	}
	if requestID != "" {
		payload["request_id"] = requestID
		errorDetail["request_id"] = requestID
	}
	if hasHint {
		payload["retryable"] = hint.Retryable
		errorDetail["retryable"] = hint.Retryable
		if hint.NextAction != "" {
			payload["next_action"] = hint.NextAction
			errorDetail["next_action"] = hint.NextAction
		}
	}
	return payload
}

func runtimeEnvelopeWSErrorFromAgentActivity(requestID string, err error) map[string]any {
	switch {
	case errors.Is(err, store.ErrAgentNotFound):
		return runtimeEnvelopeWSError(requestID, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
	case errors.Is(err, store.ErrInvalidAgentActivity):
		return runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_agent_activity", "activity text must be non-sensitive and must not include secrets, tokens, passwords, keys, or credentials")
	case errors.Is(err, errInvalidAgentActivityRequest):
		return runtimeEnvelopeWSError(requestID, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), errInvalidAgentActivityRequest.Error()+": "))
	default:
		return runtimeEnvelopeWSError(requestID, http.StatusInternalServerError, "store_error", "failed to publish agent activity")
	}
}

func runtimeEnvelopeWSErrorFromRuntime(requestID string, handlerErr *runtimeHandlerError) map[string]any {
	if handlerErr == nil {
		return runtimeEnvelopeWSError(requestID, http.StatusInternalServerError, "unknown_error", "unknown runtime error")
	}
	payload := runtimeEnvelopeWSError(requestID, handlerErr.status, handlerErr.code, handlerErr.message)
	errorPayload, _ := payload["error"].(map[string]any)
	if errorPayload == nil {
		errorPayload = map[string]any{}
		payload["error"] = errorPayload
	}
	errorDetail, _ := payload["error_detail"].(map[string]any)
	if errorDetail == nil {
		errorDetail = map[string]any{}
		payload["error_detail"] = errorDetail
	}
	for key, value := range handlerErr.extras {
		payload[key] = value
		errorPayload[key] = value
		errorDetail[key] = value
	}
	return payload
}

func runtimeEnvelopeWSPullTimeout(raw *int) (time.Duration, error) {
	if raw == nil {
		return runtimeEnvelopeWebSocketPullTimeoutDefault, nil
	}
	if *raw < 0 || *raw > maxPullTimeoutMS {
		return 0, errors.New("timeout_ms must be between 0 and 30000")
	}
	return time.Duration(*raw) * time.Millisecond, nil
}

func normalizeRuntimeSessionKey(raw string) string {
	candidate := strings.ToLower(strings.TrimSpace(raw))
	if candidate == "" {
		return "main"
	}
	if len(candidate) > 80 {
		candidate = candidate[:80]
	}
	for _, ch := range candidate {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '.':
		default:
			return "main"
		}
	}
	return candidate
}

func runtimeEnvelopeMessageIDFromResult(result map[string]any) string {
	if result == nil {
		return ""
	}
	if direct := strings.TrimSpace(asStringAny(result["message_id"])); direct != "" {
		return direct
	}
	message, ok := extractMessage(result["message"])
	if !ok {
		return ""
	}
	return strings.TrimSpace(message.MessageID)
}

func (h *Handler) recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, action string, details map[string]any) {
	agentUUID = strings.TrimSpace(agentUUID)
	adapterName = strings.ToLower(strings.TrimSpace(adapterName))
	action = strings.TrimSpace(action)
	if agentUUID == "" || action == "" {
		return
	}
	category := "runtime_adapter"
	activityPrefix := "runtime adapter "
	entry := map[string]any{
		"activity": activityPrefix + action,
		"category": category,
		"action":   action,
	}
	for k, v := range details {
		if k == "" || v == nil {
			continue
		}
		if asString, ok := v.(string); ok {
			asString = strings.TrimSpace(asString)
			if asString == "" {
				continue
			}
			entry[k] = asString
			continue
		}
		entry[k] = v
	}
	now := h.now().UTC()
	agent, err := h.control.RecordAgentSystemActivity(agentUUID, entry, now)
	if err != nil {
		return
	}
	h.publishCollectiveEvent(collectiveStreamEvent{
		At:        now,
		Category:  category,
		Action:    action,
		AgentUUID: agent.AgentUUID,
		OrgID:     agent.OrgID,
		Details:   details,
	})
}

func (h *Handler) touchAgentPresenceOnline(agentUUID, sessionKey, transport string) *runtimeHandlerError {
	now := h.now().UTC()
	agentUUID = strings.TrimSpace(agentUUID)
	if agentUUID == "" {
		return &runtimeHandlerError{
			status:  http.StatusUnauthorized,
			code:    "unauthorized",
			message: "missing or invalid bearer token",
		}
	}

	presence := map[string]any{
		"status":     runtimePresenceStatusOnline,
		"ready":      true,
		"updated_at": now.Format(time.RFC3339),
	}
	if sessionKey = strings.TrimSpace(sessionKey); sessionKey != "" {
		presence["session_key"] = normalizeRuntimeSessionKey(sessionKey)
	}
	if transport = strings.TrimSpace(transport); transport != "" {
		presence["transport"] = transport
	}
	agent, changedStatus, err := h.control.SetAgentPresence(agentUUID, presence, now)
	if err != nil {
		// Presence heartbeat is best-effort. Fail open for runtime traffic and only
		// surface auth-style errors when identity is missing.
		h.setStateRuntimeError(stateRuntimeFailureSummary("agent presence update", err))
		return nil
	}
	if changedStatus {
		entry := map[string]any{
			"activity":   "websocket transport online",
			"category":   "agent_presence",
			"action":     runtimePresenceStatusOnline,
			"subject_id": normalizeRuntimeSessionKey(sessionKey),
			"event_id":   "agent-presence:online:" + normalizeRuntimeSessionKey(sessionKey) + ":" + strconv.FormatInt(now.UnixNano(), 10),
		}
		if transport = strings.TrimSpace(transport); transport != "" {
			entry["transport"] = transport
		}
		if recorded, recordErr := h.control.RecordAgentSystemActivity(agent.AgentUUID, entry, now); recordErr == nil {
			agent = recorded
			h.publishCollectiveEvent(collectiveStreamEvent{
				At:        now,
				Category:  "agent_presence",
				Action:    runtimePresenceStatusOnline,
				AgentUUID: agent.AgentUUID,
				OrgID:     agent.OrgID,
				Details:   map[string]any{"session_key": normalizeRuntimeSessionKey(sessionKey), "transport": transport},
			})
		}
	}
	return nil
}

func runtimeHandlerErrorForPresenceUpdate(err error) *runtimeHandlerError {
	extras := map[string]any{}
	if detail := strings.TrimSpace(store.SanitizeErrorWithDetail(err)); detail != "" {
		extras["detail"] = detail
	}
	if len(extras) == 0 {
		extras = nil
	}

	switch {
	case errors.Is(err, store.ErrAgentNotFound):
		return &runtimeHandlerError{
			status:  http.StatusNotFound,
			code:    "unknown_agent",
			message: "agent_uuid is not registered",
			extras:  extras,
		}
	case errors.Is(err, store.ErrInvalidAgentType):
		return &runtimeHandlerError{
			status:  http.StatusBadRequest,
			code:    "invalid_agent_type",
			message: "metadata.agent_type must be 2-64 chars: a-z, 0-9, ., _, -",
			extras:  extras,
		}
	default:
		return &runtimeHandlerError{
			status:  http.StatusInternalServerError,
			code:    "store_error",
			message: "failed to update agent presence",
			extras:  extras,
		}
	}
}

func (h *Handler) setRuntimeWebSocketPresence(agentUUID, sessionKey, status, reason string) (model.Agent, error) {
	now := h.now().UTC()
	agentUUID = strings.TrimSpace(agentUUID)
	if agentUUID == "" {
		return model.Agent{}, store.ErrAgentNotFound
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if status != runtimePresenceStatusOnline && status != runtimePresenceStatusOffline {
		status = runtimePresenceStatusOffline
	}
	sessionKey = normalizeRuntimeSessionKey(sessionKey)

	patch := map[string]any{
		"status":      status,
		"ready":       status == runtimePresenceStatusOnline,
		"transport":   "websocket",
		"session_key": sessionKey,
		"updated_at":  now.Format(time.RFC3339),
	}
	agent, changedStatus, err := h.control.SetAgentPresence(agentUUID, patch, now)
	if err != nil {
		return model.Agent{}, err
	}
	if changedStatus {
		activityText := "websocket transport " + status
		entry := map[string]any{
			"activity":   activityText,
			"category":   "agent_presence",
			"action":     status,
			"subject_id": sessionKey,
			"event_id":   "agent-presence:" + status + ":" + sessionKey + ":" + strconv.FormatInt(now.UnixNano(), 10),
		}
		if trimmedReason := strings.TrimSpace(reason); trimmedReason != "" {
			entry["reason"] = trimmedReason
		}
		if recorded, recordErr := h.control.RecordAgentSystemActivity(agentUUID, entry, now); recordErr == nil {
			agent = recorded
			h.publishCollectiveEvent(collectiveStreamEvent{
				At:        now,
				Category:  "agent_presence",
				Action:    status,
				AgentUUID: agent.AgentUUID,
				OrgID:     agent.OrgID,
				Details:   map[string]any{"session_key": sessionKey, "reason": strings.TrimSpace(reason), "transport": "websocket"},
			})
		}
	}
	return agent, nil
}

func runtimePresenceFromMetadata(metadata map[string]any) map[string]any {
	return runtimePresenceFromMetadataAt(metadata, time.Now().UTC(), runtimePresenceOfflineAfter)
}

func runtimePresenceFromMetadataAt(metadata map[string]any, now time.Time, staleAfter time.Duration) map[string]any {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata[model.AgentMetadataKeyPresence]
	if !ok {
		return nil
	}
	presence, ok := raw.(map[string]any)
	if !ok || len(presence) == 0 {
		return nil
	}

	status := strings.ToLower(strings.TrimSpace(asStringAny(presence["status"])))
	if status != runtimePresenceStatusOnline && status != runtimePresenceStatusOffline {
		status = ""
	}
	transport := strings.TrimSpace(asStringAny(presence["transport"]))
	sessionKey := normalizeRuntimeSessionKey(asStringAny(presence["session_key"]))
	updatedAt := strings.TrimSpace(asStringAny(presence["updated_at"]))
	ready, readyOK := presence["ready"].(bool)
	if status == runtimePresenceStatusOnline && staleAfter > 0 {
		if seenAt, ok := parseRuntimePresenceTimestamp(updatedAt); ok && now.Sub(seenAt) >= staleAfter {
			status = runtimePresenceStatusOffline
			ready = false
			readyOK = true
		}
	}
	if status == "" && !readyOK && transport == "" && updatedAt == "" {
		return nil
	}

	out := map[string]any{}
	if status != "" {
		out["status"] = status
	}
	if readyOK {
		out["ready"] = ready
	}
	if transport != "" {
		out["transport"] = transport
	}
	if sessionKey != "" {
		out["session_key"] = sessionKey
	}
	if updatedAt != "" {
		out["updated_at"] = updatedAt
	}
	return out
}

func parseRuntimePresenceTimestamp(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}
