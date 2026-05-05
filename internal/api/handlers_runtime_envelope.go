package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"moltenhub/internal/model"
)

const (
	openClawHTTPProtocol    = "openclaw.http.v1"
	runtimeEnvelopeProtocol = "runtime.envelope.v1"
)

const (
	runtimeEnvelopeAdapterRuntime  = "runtime"
	runtimeEnvelopeAdapterOpenClaw = "openclaw"
)

const (
	openClawSkillPayloadFormatMarkdown = "markdown"
	openClawSkillPayloadFormatJSON     = "json"
)

type openClawPublishRequest struct {
	ToAgentUUID string         `json:"to_agent_uuid"`
	ToAgentID   string         `json:"to_agent_id,omitempty"`
	ToAgentURI  string         `json:"to_agent_uri,omitempty"`
	ClientMsgID *string        `json:"client_msg_id,omitempty"`
	Message     map[string]any `json:"message"`
}

func (h *Handler) handleOpenClawPublish(w http.ResponseWriter, r *http.Request) {
	h.handleRuntimeEnvelopePublish(w, r, openClawHTTPProtocol, runtimeEnvelopeAdapterOpenClaw)
}

func (h *Handler) handleRuntimePublish(w http.ResponseWriter, r *http.Request) {
	h.handleRuntimeEnvelopePublish(w, r, runtimeEnvelopeProtocol, runtimeEnvelopeAdapterRuntime)
}

func (h *Handler) handleRuntimeEnvelopePublish(w http.ResponseWriter, r *http.Request, defaultProtocol, adapterName string) {
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
	if heartbeatErr := h.touchAgentPresenceOnline(senderAgentUUID, "", ""); heartbeatErr != nil {
		writeRuntimeHandlerError(w, heartbeatErr)
		return
	}

	var raw map[string]any
	if err := decodeJSON(r, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}

	req, err := parseOpenClawPublishRequest(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.Message) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "message is required")
		return
	}

	envelope, err := normalizeRuntimeEnvelope(req.Message, h.now().UTC(), defaultProtocol)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "message must be a JSON object")
		return
	}

	result, handlerErr := h.publishFromAgent(r.Context(), senderAgentUUID, publishRequest{
		ToAgentUUID: req.ToAgentUUID,
		ToAgentID:   req.ToAgentID,
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
	out["transport"] = runtimeEnvelopeTransportMetadata(defaultProtocol, "http")
	out["envelope"] = envelope
	out["openclaw_message"] = envelope
	h.recordRuntimeEnvelopeAdapterUsage(senderAgentUUID, adapterName, "publish", map[string]any{
		"message_id": openClawMessageIDFromResult(out),
	})
	writeAgentRuntimeSuccess(w, http.StatusAccepted, out)
}

func parseOpenClawPublishRequest(raw map[string]any) (openClawPublishRequest, error) {
	var req openClawPublishRequest
	if raw == nil {
		return req, nil
	}

	req.ToAgentUUID = strings.TrimSpace(asStringAny(raw["to_agent_uuid"]))
	req.ToAgentID = strings.TrimSpace(asStringAny(raw["to_agent_id"]))
	req.ToAgentURI = strings.TrimSpace(asStringAny(raw["to_agent_uri"]))
	if rawClientMsgID, ok := raw["client_msg_id"]; ok {
		clientMsgID := strings.TrimSpace(asStringAny(rawClientMsgID))
		if clientMsgID == "" {
			return req, errors.New("client_msg_id must be a string")
		}
		req.ClientMsgID = &clientMsgID
	}

	if rawMessage, ok := raw["message"]; ok {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			return req, errors.New("message must be a JSON object")
		}
		req.Message = message
		return req, nil
	}

	message := make(map[string]any, len(raw))
	for key, value := range raw {
		switch key {
		case "to_agent_uuid", "to_agent_id", "to_agent_uri", "client_msg_id":
			continue
		default:
			message[key] = value
		}
	}
	req.Message = message
	return req, nil
}

func (h *Handler) handleOpenClawPull(w http.ResponseWriter, r *http.Request) {
	h.handleRuntimeEnvelopePull(w, r, openClawHTTPProtocol, runtimeEnvelopeAdapterOpenClaw)
}

func (h *Handler) handleRuntimePull(w http.ResponseWriter, r *http.Request) {
	h.handleRuntimeEnvelopePull(w, r, runtimeEnvelopeProtocol, runtimeEnvelopeAdapterRuntime)
}

func (h *Handler) handleRuntimeEnvelopePull(w http.ResponseWriter, r *http.Request, defaultProtocol, adapterName string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	receiverAgentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	if heartbeatErr := h.touchAgentPresenceOnline(receiverAgentUUID, "", ""); heartbeatErr != nil {
		writeRuntimeHandlerError(w, heartbeatErr)
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

	out := withRuntimeEnvelopeProjection(result, defaultProtocol)
	h.recordRuntimeEnvelopeAdapterUsage(receiverAgentUUID, adapterName, "pull", map[string]any{
		"message_id": openClawMessageIDFromResult(out),
	})
	writeAgentRuntimeSuccess(w, status, out)
}

func (h *Handler) handleOpenClawMessageSubroutes(w http.ResponseWriter, r *http.Request) {
	h.handleRuntimeEnvelopeMessageSubroutes(w, r, "/v1/openclaw/messages/", openClawHTTPProtocol, runtimeEnvelopeAdapterOpenClaw)
}

func (h *Handler) handleRuntimeMessageSubroutes(w http.ResponseWriter, r *http.Request) {
	h.handleRuntimeEnvelopeMessageSubroutes(w, r, "/v1/runtime/messages/", runtimeEnvelopeProtocol, runtimeEnvelopeAdapterRuntime)
}

func (h *Handler) handleRuntimeEnvelopeMessageSubroutes(w http.ResponseWriter, r *http.Request, prefix, defaultProtocol, adapterName string) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if !strings.HasPrefix(path, prefix) {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	tail := strings.TrimPrefix(path, prefix)
	switch tail {
	case "publish":
		h.handleRuntimeEnvelopePublish(w, r, defaultProtocol, adapterName)
		return
	case "pull":
		h.handleRuntimeEnvelopePull(w, r, defaultProtocol, adapterName)
		return
	case "ack":
		h.handleRuntimeEnvelopeAckDelivery(w, r, defaultProtocol, adapterName)
		return
	case "nack":
		h.handleRuntimeEnvelopeNackDelivery(w, r, defaultProtocol, adapterName)
		return
	case "ws":
		h.handleRuntimeEnvelopeWebSocket(w, r, defaultProtocol, adapterName)
		return
	case "offline":
		h.handleRuntimeEnvelopeOffline(w, r, adapterName)
		return
	case "register-plugin":
		if adapterName == runtimeEnvelopeAdapterOpenClaw {
			h.handleOpenClawRegisterPlugin(w, r)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if strings.TrimSpace(tail) == "" || strings.Contains(tail, "/") {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	h.handleRuntimeEnvelopeMessageStatus(w, r, tail, defaultProtocol, adapterName)
}

func (h *Handler) handleOpenClawAckDelivery(w http.ResponseWriter, r *http.Request) {
	h.handleRuntimeEnvelopeAckDelivery(w, r, openClawHTTPProtocol, runtimeEnvelopeAdapterOpenClaw)
}

func (h *Handler) handleRuntimeEnvelopeAckDelivery(w http.ResponseWriter, r *http.Request, defaultProtocol, adapterName string) {
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
	if heartbeatErr := h.touchAgentPresenceOnline(receiverAgentUUID, "", ""); heartbeatErr != nil {
		writeRuntimeHandlerError(w, heartbeatErr)
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

	result := withRuntimeEnvelopeProjection(messageStatusResponse(record), defaultProtocol)
	h.recordRuntimeEnvelopeAdapterUsage(receiverAgentUUID, adapterName, "ack", map[string]any{
		"message_id": openClawMessageIDFromResult(result),
	})
	writeAgentRuntimeSuccess(w, http.StatusOK, result)
}

func (h *Handler) handleOpenClawNackDelivery(w http.ResponseWriter, r *http.Request) {
	h.handleRuntimeEnvelopeNackDelivery(w, r, openClawHTTPProtocol, runtimeEnvelopeAdapterOpenClaw)
}

func (h *Handler) handleRuntimeEnvelopeNackDelivery(w http.ResponseWriter, r *http.Request, defaultProtocol, adapterName string) {
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
	if heartbeatErr := h.touchAgentPresenceOnline(receiverAgentUUID, "", ""); heartbeatErr != nil {
		writeRuntimeHandlerError(w, heartbeatErr)
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

	result := withRuntimeEnvelopeProjection(messageStatusResponse(record), defaultProtocol)
	h.recordRuntimeEnvelopeAdapterUsage(receiverAgentUUID, adapterName, "nack", map[string]any{
		"message_id": openClawMessageIDFromResult(result),
	})
	writeAgentRuntimeSuccess(w, http.StatusOK, result)
}

func (h *Handler) handleOpenClawMessageStatus(w http.ResponseWriter, r *http.Request, messageID string) {
	h.handleRuntimeEnvelopeMessageStatus(w, r, messageID, openClawHTTPProtocol, runtimeEnvelopeAdapterOpenClaw)
}

func (h *Handler) handleRuntimeEnvelopeMessageStatus(w http.ResponseWriter, r *http.Request, messageID, defaultProtocol, adapterName string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	agentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	if heartbeatErr := h.touchAgentPresenceOnline(agentUUID, "", ""); heartbeatErr != nil {
		writeRuntimeHandlerError(w, heartbeatErr)
		return
	}

	record, handlerErr := h.messageStatusForAgent(agentUUID, messageID)
	if handlerErr != nil {
		writeRuntimeHandlerError(w, handlerErr)
		return
	}

	result := withRuntimeEnvelopeProjection(messageStatusResponse(record), defaultProtocol)
	h.recordRuntimeEnvelopeAdapterUsage(agentUUID, adapterName, "status", map[string]any{
		"message_id": openClawMessageIDFromResult(result),
	})
	writeAgentRuntimeSuccess(w, http.StatusOK, result)
}

func normalizeOpenClawEnvelope(in map[string]any, now time.Time) (map[string]any, error) {
	return normalizeRuntimeEnvelope(in, now, openClawHTTPProtocol)
}

func normalizeRuntimeEnvelope(in map[string]any, now time.Time, defaultProtocol string) (map[string]any, error) {
	out := cloneStringAnyMap(in)
	if out == nil {
		out = map[string]any{}
	}
	if strings.TrimSpace(asStringAny(out["protocol"])) == "" {
		out["protocol"] = normalizeRuntimeEnvelopeProtocol(defaultProtocol)
	}
	if strings.TrimSpace(asStringAny(out["kind"])) == "" {
		out["kind"] = "agent_message"
	}
	if strings.TrimSpace(asStringAny(out["timestamp"])) == "" {
		out["timestamp"] = now.UTC().Format(time.RFC3339Nano)
	}
	if err := normalizeOpenClawSkillActivationEnvelope(out); err != nil {
		return nil, err
	}
	return out, nil
}

func withOpenClawProjection(result map[string]any) map[string]any {
	return withRuntimeEnvelopeProjection(result, openClawHTTPProtocol)
}

func withRuntimeEnvelopeProjection(result map[string]any, defaultProtocol string) map[string]any {
	out := cloneStringAnyMap(result)
	if out == nil {
		out = map[string]any{}
	}
	out["transport"] = runtimeEnvelopeTransportMetadata(defaultProtocol, "http")
	if message, ok := extractMessage(out["message"]); ok {
		envelope := parseRuntimeEnvelopeFromMessage(message, defaultProtocol)
		out["envelope"] = envelope
		out["openclaw_message"] = envelope
	}
	return out
}

func openClawTransportMetadata() map[string]any {
	return runtimeEnvelopeTransportMetadata(openClawHTTPProtocol, "http")
}

func openClawTransportMetadataForAdapter(adapter string) map[string]any {
	return runtimeEnvelopeTransportMetadata(openClawHTTPProtocol, adapter)
}

func runtimeEnvelopeTransportMetadata(defaultProtocol, adapter string) map[string]any {
	adapter = strings.TrimSpace(adapter)
	if adapter == "" {
		adapter = "http"
	}
	return map[string]any{
		"protocol": normalizeRuntimeEnvelopeProtocol(defaultProtocol),
		"adapter":  adapter,
	}
}

func parseOpenClawEnvelopeFromMessage(message model.Message) map[string]any {
	return parseRuntimeEnvelopeFromMessage(message, openClawHTTPProtocol)
}

func parseRuntimeEnvelopeFromMessage(message model.Message, defaultProtocol string) map[string]any {
	if strings.TrimSpace(message.ContentType) == "application/json" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(message.Payload), &payload); err == nil && payload != nil {
			out := cloneStringAnyMap(payload)
			if strings.TrimSpace(asStringAny(out["protocol"])) == "" {
				out["protocol"] = normalizeRuntimeEnvelopeProtocol(defaultProtocol)
			}
			if strings.TrimSpace(asStringAny(out["kind"])) == "" {
				out["kind"] = "agent_message"
			}
			_ = normalizeOpenClawSkillActivationEnvelope(out)
			return out
		}
		return map[string]any{
			"protocol": normalizeRuntimeEnvelopeProtocol(defaultProtocol),
			"kind":     "invalid_json_payload",
			"raw":      message.Payload,
		}
	}
	return map[string]any{
		"protocol": normalizeRuntimeEnvelopeProtocol(defaultProtocol),
		"kind":     "text_message",
		"text":     message.Payload,
	}
}

func normalizeRuntimeEnvelopeProtocol(raw string) string {
	protocol := strings.TrimSpace(raw)
	if protocol == "" {
		return runtimeEnvelopeProtocol
	}
	return protocol
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

func normalizeOpenClawSkillActivationEnvelope(envelope map[string]any) error {
	if envelope == nil {
		return nil
	}
	envelopeType := strings.ToLower(strings.TrimSpace(asStringAny(envelope["type"])))
	envelopeKind := strings.ToLower(strings.TrimSpace(asStringAny(envelope["kind"])))
	if envelopeType != "skill_request" && envelopeType != "skill_activation" &&
		envelopeKind != "skill_request" && envelopeKind != "skill_activation" {
		return nil
	}

	skillName := strings.TrimSpace(asStringAny(envelope["skill_name"]))
	if skillName == "" {
		return errors.New("skill_name is required when type/kind is skill_request or skill_activation")
	}

	payloadFormat := strings.ToLower(strings.TrimSpace(asStringAny(envelope["payload_format"])))
	if payloadFormat != "" && payloadFormat != openClawSkillPayloadFormatMarkdown && payloadFormat != openClawSkillPayloadFormatJSON {
		return errors.New("payload_format must be one of: markdown, json")
	}

	payload, hasPayload := envelope["payload"]
	if !hasPayload {
		payload, hasPayload = envelope["input"]
	}
	if !hasPayload {
		if payloadFormat != "" {
			return errors.New("payload_format requires payload")
		}
		envelope["skill_name"] = skillName
		return nil
	}

	switch payload.(type) {
	case string:
		if payloadFormat == "" {
			payloadFormat = openClawSkillPayloadFormatMarkdown
		}
		if payloadFormat != openClawSkillPayloadFormatMarkdown {
			return errors.New("payload must be a JSON object when payload_format is json")
		}
	case map[string]any:
		if payloadFormat == "" {
			payloadFormat = openClawSkillPayloadFormatJSON
		}
		if payloadFormat != openClawSkillPayloadFormatJSON {
			return errors.New("payload must be a markdown string when payload_format is markdown")
		}
	default:
		return errors.New("payload must be either a markdown string or a JSON object")
	}

	envelope["skill_name"] = skillName
	envelope["payload"] = payload
	envelope["payload_format"] = payloadFormat
	return nil
}
