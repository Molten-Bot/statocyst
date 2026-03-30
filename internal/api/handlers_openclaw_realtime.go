package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"statocyst/internal/model"
	"statocyst/internal/store"
)

const (
	openClawWebSocketPullTimeoutDefault = 20 * time.Second
	openClawPluginDefaultID             = "statocyst-openclaw"
)

var (
	openClawWSUpgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(_ *http.Request) bool {
			return true
		},
	}
	openClawPluginMetadataKeyPattern = regexp.MustCompile(`[^a-z0-9_.-]+`)
)

type openClawPluginRegisterRequest struct {
	PluginID    string `json:"plugin_id,omitempty"`
	Package     string `json:"package,omitempty"`
	Version     string `json:"version,omitempty"`
	Transport   string `json:"transport,omitempty"`
	SessionKey  string `json:"session_key,omitempty"`
	SessionMode string `json:"session_mode,omitempty"`
}

type openClawWSRequest struct {
	Type        string         `json:"type"`
	RequestID   string         `json:"request_id,omitempty"`
	ToAgentUUID string         `json:"to_agent_uuid,omitempty"`
	ToAgentURI  string         `json:"to_agent_uri,omitempty"`
	ClientMsgID *string        `json:"client_msg_id,omitempty"`
	Message     map[string]any `json:"message,omitempty"`
	DeliveryID  string         `json:"delivery_id,omitempty"`
	MessageID   string         `json:"message_id,omitempty"`
	TimeoutMS   *int           `json:"timeout_ms,omitempty"`
}

// openClawWSResponseWriter bridges websocket upgrades through wrappers that may
// not expose http.Hijacker directly, but still support hijacking via
// http.ResponseController.
type openClawWSResponseWriter struct {
	http.ResponseWriter
}

func (w openClawWSResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w openClawWSResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (h *Handler) handleOpenClawRegisterPlugin(w http.ResponseWriter, r *http.Request) {
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

	var req openClawPluginRegisterRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}

	now := h.now().UTC()
	pluginID := normalizeOpenClawPluginID(req.PluginID)
	pluginKey := openClawPluginMetadataKey(pluginID)
	sessionKey := normalizeOpenClawSessionKey(req.SessionKey)
	transport := strings.ToLower(strings.TrimSpace(req.Transport))
	if transport == "" {
		transport = "websocket"
	}
	sessionMode := strings.ToLower(strings.TrimSpace(req.SessionMode))
	if sessionMode == "" {
		sessionMode = "dedicated"
	}

	marker := map[string]any{
		"id":            pluginID,
		"package":       strings.TrimSpace(req.Package),
		"version":       strings.TrimSpace(req.Version),
		"enabled":       true,
		"transport":     transport,
		"session_mode":  sessionMode,
		"session_key":   sessionKey,
		"registered_at": now.Format(time.RFC3339),
		"last_seen_at":  now.Format(time.RFC3339),
	}

	patch := map[string]any{
		model.AgentMetadataKeyType: "openclaw",
		"plugins": map[string]any{
			pluginKey: marker,
		},
	}
	agent, err := h.control.UpdateAgentMetadataSelf(agentUUID, patch, now)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAgentNotFound):
			writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
		case errors.Is(err, store.ErrInvalidAgentType):
			writeError(w, http.StatusBadRequest, "invalid_agent_type", "metadata.agent_type must be 2-64 chars: a-z, 0-9, ., _, -")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", "failed to register plugin metadata")
		}
		return
	}

	activityEntry := map[string]any{
		"activity":   "registered OpenClaw plugin " + pluginID,
		"category":   "openclaw_plugin",
		"action":     "register",
		"subject_id": pluginID,
		"event_id":   "openclaw-plugin-register:" + pluginID + ":" + strconv.FormatInt(now.UnixNano(), 10),
	}
	agent, err = h.control.RecordAgentSystemActivity(agentUUID, activityEntry, now)
	if err != nil {
		if errors.Is(err, store.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "unknown_agent", "agent_uuid is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to register plugin activity")
		return
	}

	writeAgentRuntimeSuccess(w, http.StatusOK, map[string]any{
		"agent":  h.agentResponsePayload(agent),
		"plugin": marker,
	})
}

func (h *Handler) handleOpenClawWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	agentUUID, err := h.authenticateAgent(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}

	conn, err := openClawWSUpgrader.Upgrade(openClawWSResponseWriter{ResponseWriter: w}, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sessionKey := normalizeOpenClawSessionKey(r.URL.Query().Get("session_key"))
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

	h.recordOpenClawAdapterUsage(agentUUID, "ws_connect", map[string]any{"session_key": sessionKey})

	if ok := writeEvent(map[string]any{
		"type":        "session_ready",
		"session_key": sessionKey,
		"transport":   openClawTransportMetadataForAdapter("websocket"),
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

			status, result, handlerErr := h.pullForAgent(ctx, agentUUID, openClawWebSocketPullTimeoutDefault)
			if ctx.Err() != nil {
				return
			}
			if handlerErr != nil {
				if !writeEvent(openClawWSError("", handlerErr.status, handlerErr.code, handlerErr.message)) {
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
				out := withOpenClawProjection(result)
				out["transport"] = openClawTransportMetadataForAdapter("websocket")
				if !writeEvent(map[string]any{
					"type":        "delivery",
					"session_key": sessionKey,
					"result":      out,
				}) {
					cancel()
					return
				}
				h.recordOpenClawAdapterUsage(agentUUID, "ws_delivery", map[string]any{
					"message_id":  openClawMessageIDFromResult(out),
					"session_key": sessionKey,
				})
			default:
				if !writeEvent(openClawWSError("", status, "unexpected_status", "unexpected pull status")) {
					cancel()
					return
				}
			}
		}
	}()

	for {
		var req openClawWSRequest
		if err := conn.ReadJSON(&req); err != nil {
			break
		}
		if !h.handleOpenClawWSCommand(ctx, agentUUID, sessionKey, req, writeEvent) {
			break
		}
	}

	cancel()
	<-deliveryDone
	h.recordOpenClawAdapterUsage(agentUUID, "ws_disconnect", map[string]any{"session_key": sessionKey})
}

func (h *Handler) handleOpenClawWSCommand(
	ctx context.Context,
	agentUUID,
	sessionKey string,
	req openClawWSRequest,
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
			return writeEvent(openClawWSError(requestID, http.StatusBadRequest, "invalid_request", "message is required"))
		}
		envelope := normalizeOpenClawEnvelope(req.Message, h.now().UTC())
		payload, err := json.Marshal(envelope)
		if err != nil {
			return writeEvent(openClawWSError(requestID, http.StatusBadRequest, "invalid_request", "message must be a JSON object"))
		}
		result, handlerErr := h.publishFromAgent(ctx, agentUUID, publishRequest{
			ToAgentUUID: req.ToAgentUUID,
			ToAgentURI:  req.ToAgentURI,
			ContentType: "application/json",
			Payload:     string(payload),
			ClientMsgID: req.ClientMsgID,
		})
		if handlerErr != nil {
			return writeEvent(openClawWSError(requestID, handlerErr.status, handlerErr.code, handlerErr.message))
		}
		out := cloneStringAnyMap(result)
		out["transport"] = openClawTransportMetadataForAdapter("websocket")
		out["openclaw_message"] = envelope
		h.recordOpenClawAdapterUsage(agentUUID, "ws_publish", map[string]any{
			"message_id":  openClawMessageIDFromResult(out),
			"session_key": sessionKey,
		})
		return writeEvent(openClawWSResponse(requestID, http.StatusAccepted, out))
	case "ack":
		deliveryID := strings.TrimSpace(req.DeliveryID)
		if deliveryID == "" {
			return writeEvent(openClawWSError(requestID, http.StatusBadRequest, "invalid_delivery_id", "delivery_id is required"))
		}
		record, handlerErr := h.ackDeliveryForAgent(agentUUID, deliveryID)
		if handlerErr != nil {
			return writeEvent(openClawWSError(requestID, handlerErr.status, handlerErr.code, handlerErr.message))
		}
		result := withOpenClawProjection(messageStatusResponse(record))
		result["transport"] = openClawTransportMetadataForAdapter("websocket")
		h.recordOpenClawAdapterUsage(agentUUID, "ws_ack", map[string]any{
			"message_id":  openClawMessageIDFromResult(result),
			"session_key": sessionKey,
		})
		return writeEvent(openClawWSResponse(requestID, http.StatusOK, result))
	case "nack":
		deliveryID := strings.TrimSpace(req.DeliveryID)
		if deliveryID == "" {
			return writeEvent(openClawWSError(requestID, http.StatusBadRequest, "invalid_delivery_id", "delivery_id is required"))
		}
		record, handlerErr := h.nackDeliveryForAgent(ctx, agentUUID, deliveryID)
		if handlerErr != nil {
			return writeEvent(openClawWSError(requestID, handlerErr.status, handlerErr.code, handlerErr.message))
		}
		result := withOpenClawProjection(messageStatusResponse(record))
		result["transport"] = openClawTransportMetadataForAdapter("websocket")
		h.recordOpenClawAdapterUsage(agentUUID, "ws_nack", map[string]any{
			"message_id":  openClawMessageIDFromResult(result),
			"session_key": sessionKey,
		})
		return writeEvent(openClawWSResponse(requestID, http.StatusOK, result))
	case "status":
		messageID := strings.TrimSpace(req.MessageID)
		if messageID == "" {
			return writeEvent(openClawWSError(requestID, http.StatusBadRequest, "invalid_message_id", "message_id is required"))
		}
		record, handlerErr := h.messageStatusForAgent(agentUUID, messageID)
		if handlerErr != nil {
			return writeEvent(openClawWSError(requestID, handlerErr.status, handlerErr.code, handlerErr.message))
		}
		result := withOpenClawProjection(messageStatusResponse(record))
		result["transport"] = openClawTransportMetadataForAdapter("websocket")
		h.recordOpenClawAdapterUsage(agentUUID, "ws_status", map[string]any{
			"message_id":  openClawMessageIDFromResult(result),
			"session_key": sessionKey,
		})
		return writeEvent(openClawWSResponse(requestID, http.StatusOK, result))
	case "pull":
		timeout, err := openClawWSPullTimeout(req.TimeoutMS)
		if err != nil {
			return writeEvent(openClawWSError(requestID, http.StatusBadRequest, "invalid_timeout", err.Error()))
		}
		status, result, handlerErr := h.pullForAgent(ctx, agentUUID, timeout)
		if handlerErr != nil {
			return writeEvent(openClawWSError(requestID, handlerErr.status, handlerErr.code, handlerErr.message))
		}
		if status == http.StatusNoContent {
			return writeEvent(openClawWSResponse(requestID, http.StatusNoContent, map[string]any{"status": "empty"}))
		}
		if status == 0 {
			return false
		}
		out := withOpenClawProjection(result)
		out["transport"] = openClawTransportMetadataForAdapter("websocket")
		h.recordOpenClawAdapterUsage(agentUUID, "ws_pull", map[string]any{
			"message_id":  openClawMessageIDFromResult(out),
			"session_key": sessionKey,
		})
		return writeEvent(openClawWSResponse(requestID, http.StatusOK, out))
	default:
		return writeEvent(openClawWSError(requestID, http.StatusBadRequest, "invalid_request", "unsupported websocket command type"))
	}
}

func openClawWSResponse(requestID string, status int, result map[string]any) map[string]any {
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

func openClawWSError(requestID string, status int, code, message string) map[string]any {
	payload := map[string]any{
		"type":   "response",
		"ok":     false,
		"status": status,
		"error": map[string]any{
			"code":    strings.TrimSpace(code),
			"message": strings.TrimSpace(message),
		},
	}
	if requestID != "" {
		payload["request_id"] = requestID
	}
	return payload
}

func openClawWSPullTimeout(raw *int) (time.Duration, error) {
	if raw == nil {
		return openClawWebSocketPullTimeoutDefault, nil
	}
	if *raw < 0 || *raw > maxPullTimeoutMS {
		return 0, errors.New("timeout_ms must be between 0 and 30000")
	}
	return time.Duration(*raw) * time.Millisecond, nil
}

func normalizeOpenClawSessionKey(raw string) string {
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

func normalizeOpenClawPluginID(raw string) string {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return openClawPluginDefaultID
	}
	if len(candidate) > 120 {
		candidate = candidate[:120]
	}
	return candidate
}

func openClawPluginMetadataKey(pluginID string) string {
	key := strings.ToLower(strings.TrimSpace(pluginID))
	if key == "" {
		key = openClawPluginDefaultID
	}
	key = openClawPluginMetadataKeyPattern.ReplaceAllString(key, "_")
	key = strings.Trim(key, "_")
	if key == "" {
		return "statocyst_openclaw"
	}
	return key
}

func openClawMessageIDFromResult(result map[string]any) string {
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

func (h *Handler) recordOpenClawAdapterUsage(agentUUID, action string, details map[string]any) {
	agentUUID = strings.TrimSpace(agentUUID)
	action = strings.TrimSpace(action)
	if agentUUID == "" || action == "" {
		return
	}
	entry := map[string]any{
		"activity": "openclaw adapter " + action,
		"category": "openclaw_adapter",
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
	_, _ = h.control.RecordAgentSystemActivity(agentUUID, entry, h.now().UTC())
}
