package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"statocyst/internal/model"
)

const openClawHTTPProtocol = "openclaw.http.v1"

type openClawPublishRequest struct {
	ToAgentUUID string         `json:"to_agent_uuid"`
	ToAgentURI  string         `json:"to_agent_uri,omitempty"`
	ClientMsgID *string        `json:"client_msg_id,omitempty"`
	Message     map[string]any `json:"message"`
}

func (h *Handler) handleOpenClawPublish(w http.ResponseWriter, r *http.Request) {
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

	var req openClawPublishRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	if len(req.Message) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "message is required")
		return
	}

	envelope := normalizeOpenClawEnvelope(req.Message, h.now().UTC())
	payload, err := json.Marshal(envelope)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "message must be a JSON object")
		return
	}

	result, handlerErr := h.publishFromAgent(r.Context(), senderAgentUUID, publishRequest{
		ToAgentUUID: req.ToAgentUUID,
		ToAgentURI:  req.ToAgentURI,
		ContentType: "application/json",
		Payload:     string(payload),
		ClientMsgID: req.ClientMsgID,
	})
	if handlerErr != nil {
		writeRuntimeHandlerError(w, handlerErr)
		return
	}

	out := cloneStringAnyMap(result)
	out["transport"] = openClawTransportMetadata()
	out["openclaw_message"] = envelope
	writeAgentRuntimeSuccess(w, http.StatusAccepted, out)
}

func (h *Handler) handleOpenClawPull(w http.ResponseWriter, r *http.Request) {
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

	out := withOpenClawProjection(result)
	writeAgentRuntimeSuccess(w, status, out)
}

func (h *Handler) handleOpenClawMessageSubroutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	const prefix = "/v1/openclaw/messages/"
	if !strings.HasPrefix(path, prefix) {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	tail := strings.TrimPrefix(path, prefix)
	switch tail {
	case "publish":
		h.handleOpenClawPublish(w, r)
		return
	case "pull":
		h.handleOpenClawPull(w, r)
		return
	case "ack":
		h.handleOpenClawAckDelivery(w, r)
		return
	case "nack":
		h.handleOpenClawNackDelivery(w, r)
		return
	}
	if strings.TrimSpace(tail) == "" || strings.Contains(tail, "/") {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	h.handleOpenClawMessageStatus(w, r, tail)
}

func (h *Handler) handleOpenClawAckDelivery(w http.ResponseWriter, r *http.Request) {
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

	result := withOpenClawProjection(messageStatusResponse(record))
	writeAgentRuntimeSuccess(w, http.StatusOK, result)
}

func (h *Handler) handleOpenClawNackDelivery(w http.ResponseWriter, r *http.Request) {
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

	result := withOpenClawProjection(messageStatusResponse(record))
	writeAgentRuntimeSuccess(w, http.StatusOK, result)
}

func (h *Handler) handleOpenClawMessageStatus(w http.ResponseWriter, r *http.Request, messageID string) {
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

	result := withOpenClawProjection(messageStatusResponse(record))
	writeAgentRuntimeSuccess(w, http.StatusOK, result)
}

func normalizeOpenClawEnvelope(in map[string]any, now time.Time) map[string]any {
	out := cloneStringAnyMap(in)
	if out == nil {
		out = map[string]any{}
	}
	if strings.TrimSpace(asStringAny(out["protocol"])) == "" {
		out["protocol"] = openClawHTTPProtocol
	}
	if strings.TrimSpace(asStringAny(out["kind"])) == "" {
		out["kind"] = "agent_message"
	}
	if strings.TrimSpace(asStringAny(out["timestamp"])) == "" {
		out["timestamp"] = now.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func withOpenClawProjection(result map[string]any) map[string]any {
	out := cloneStringAnyMap(result)
	if out == nil {
		out = map[string]any{}
	}
	out["transport"] = openClawTransportMetadata()
	if message, ok := extractMessage(out["message"]); ok {
		out["openclaw_message"] = parseOpenClawEnvelopeFromMessage(message)
	}
	return out
}

func openClawTransportMetadata() map[string]any {
	return map[string]any{
		"protocol": openClawHTTPProtocol,
		"adapter":  "http",
	}
}

func parseOpenClawEnvelopeFromMessage(message model.Message) map[string]any {
	if strings.TrimSpace(message.ContentType) == "application/json" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(message.Payload), &payload); err == nil && payload != nil {
			out := cloneStringAnyMap(payload)
			if strings.TrimSpace(asStringAny(out["protocol"])) == "" {
				out["protocol"] = openClawHTTPProtocol
			}
			if strings.TrimSpace(asStringAny(out["kind"])) == "" {
				out["kind"] = "agent_message"
			}
			return out
		}
		return map[string]any{
			"protocol": openClawHTTPProtocol,
			"kind":     "invalid_json_payload",
			"raw":      message.Payload,
		}
	}
	return map[string]any{
		"protocol": openClawHTTPProtocol,
		"kind":     "text_message",
		"text":     message.Payload,
	}
}

func extractMessage(raw any) (model.Message, bool) {
	switch typed := raw.(type) {
	case model.Message:
		return typed, true
	case map[string]any:
		body, err := json.Marshal(typed)
		if err != nil {
			return model.Message{}, false
		}
		var message model.Message
		if err := json.Unmarshal(body, &message); err != nil {
			return model.Message{}, false
		}
		return message, true
	default:
		return model.Message{}, false
	}
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func asStringAny(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}
