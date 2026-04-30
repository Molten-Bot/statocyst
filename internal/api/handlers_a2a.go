package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"moltenhub/internal/model"
	"moltenhub/internal/store"
)

const (
	a2aProtocolVersion       = "1.0"
	a2aJSONRPCVersion        = "2.0"
	a2aProtocolAdapter       = "a2a.v1"
	a2aSecuritySchemeBearer  = "moltenhubBearer"
	a2aJSONRPCMethodSend     = "SendMessage"
	a2aJSONRPCMethodStream   = "SendStreamingMessage"
	a2aJSONRPCMethodGetTask  = "GetTask"
	a2aJSONRPCMethodListTask = "ListTasks"
	a2aJSONRPCMethodCancel   = "CancelTask"
	a2aJSONRPCMethodSub      = "SubscribeToTask"
	a2aJSONRPCMethodPushGet  = "GetTaskPushNotificationConfig"
	a2aJSONRPCMethodPushSet  = "CreateTaskPushNotificationConfig"
	a2aJSONRPCMethodPushList = "ListTaskPushNotificationConfigs"
	a2aJSONRPCMethodPushDel  = "DeleteTaskPushNotificationConfig"
	a2aJSONRPCMethodCard     = "GetExtendedAgentCard"
	a2aMIMEJSON              = "application/json"
	a2aMIMEProtocolJSON      = "application/a2a+json"
	a2aMIMEEventStream       = "text/event-stream"
	a2aMIMEText              = "text/plain"
)

const (
	a2aJSONRPCCompatMethodSend     = "message/send"
	a2aJSONRPCCompatMethodStream   = "message/stream"
	a2aJSONRPCCompatMethodGetTask  = "tasks/get"
	a2aJSONRPCCompatMethodCancel   = "tasks/cancel"
	a2aJSONRPCCompatMethodSub      = "tasks/resubscribe"
	a2aJSONRPCCompatMethodPushGet  = "tasks/pushNotificationConfig/get"
	a2aJSONRPCCompatMethodPushSet  = "tasks/pushNotificationConfig/set"
	a2aJSONRPCCompatMethodPushList = "tasks/pushNotificationConfig/list"
	a2aJSONRPCCompatMethodPushDel  = "tasks/pushNotificationConfig/delete"
	a2aJSONRPCCompatMethodCard     = "agent/getAuthenticatedExtendedCard"
)

const (
	a2aCodeParseError        = -32700
	a2aCodeInvalidRequest    = -32600
	a2aCodeMethodNotFound    = -32601
	a2aCodeInvalidParams     = -32602
	a2aCodeInternal          = -32603
	a2aCodeTaskNotFound      = -32001
	a2aCodeTaskNotCancelable = -32002
	a2aCodePushUnsupported   = -32003
	a2aCodeUnsupported       = -32004
	a2aCodeContentType       = -32005
	a2aCodeUnauthenticated   = -31401
	a2aCodeUnauthorized      = -31403
)

type a2aJSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id"`
}

type a2aJSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *a2aWireError `json:"error,omitempty"`
}

type a2aWireError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

type a2aProtocolError struct {
	code       int
	httpStatus int
	status     string
	reason     string
	message    string
	details    map[string]any
}

type a2aSendMessageRequest struct {
	Tenant   string          `json:"tenant,omitempty"`
	Config   *a2aSendConfig  `json:"configuration,omitempty"`
	Message  *a2aMessage     `json:"message"`
	Metadata map[string]any  `json:"metadata,omitempty"`
	raw      json.RawMessage `json:"-"`
}

type a2aSendConfig struct {
	AcceptedOutputModes []string `json:"acceptedOutputModes,omitempty"`
	ReturnImmediately   bool     `json:"returnImmediately,omitempty"`
	HistoryLength       *int     `json:"historyLength,omitempty"`
	PushConfig          any      `json:"pushNotificationConfig,omitempty"`
}

type a2aMessage struct {
	ID             string         `json:"messageId,omitempty"`
	ContextID      string         `json:"contextId,omitempty"`
	Extensions     []string       `json:"extensions,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Parts          []a2aPart      `json:"parts"`
	ReferenceTasks []string       `json:"referenceTaskIds,omitempty"`
	Role           string         `json:"role"`
	TaskID         string         `json:"taskId,omitempty"`
}

type a2aPart struct {
	Text      *string        `json:"text,omitempty"`
	Raw       string         `json:"raw,omitempty"`
	Data      any            `json:"data,omitempty"`
	URL       string         `json:"url,omitempty"`
	Filename  string         `json:"filename,omitempty"`
	MediaType string         `json:"mediaType,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type a2aGetTaskRequest struct {
	Tenant        string `json:"tenant,omitempty"`
	ID            string `json:"id"`
	HistoryLength *int   `json:"historyLength,omitempty"`
}

type a2aListTasksRequest struct {
	Tenant               string     `json:"tenant,omitempty"`
	ContextID            string     `json:"contextId,omitempty"`
	Status               string     `json:"status,omitempty"`
	PageSize             int        `json:"pageSize,omitempty"`
	PageToken            string     `json:"pageToken,omitempty"`
	HistoryLength        *int       `json:"historyLength,omitempty"`
	StatusTimestampAfter *time.Time `json:"statusTimestampAfter,omitempty"`
	IncludeArtifacts     bool       `json:"includeArtifacts,omitempty"`
}

type a2aCancelTaskRequest struct {
	Tenant   string         `json:"tenant,omitempty"`
	ID       string         `json:"id"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type a2aPushConfigRequest struct {
	Tenant string `json:"tenant,omitempty"`
	TaskID string `json:"taskId,omitempty"`
	ID     string `json:"id,omitempty"`
}

func (h *Handler) handleA2AWellKnownAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeA2AMethodNotAllowed(w, http.MethodGet)
		return
	}
	target, protocolErr := h.a2aTargetAgentFromQuery(r)
	if protocolErr != nil {
		writeA2ARESTError(w, protocolErr, "")
		return
	}
	writeA2AJSON(w, http.StatusOK, h.a2aAgentCard(r, target))
}

func (h *Handler) handleA2ARoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeA2AJSON(w, http.StatusOK, h.a2aAgentCard(r, nil))
	case http.MethodPost:
		h.handleA2AJSONRPC(w, r, "")
	default:
		writeA2AMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func (h *Handler) handleA2ASubroutes(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/a2a/"), "/")
	if path == "" {
		h.handleA2ARoot(w, r)
		return
	}
	targetAgentUUID := ""
	if strings.HasPrefix(path, "agents/") {
		tail := strings.Trim(strings.TrimPrefix(path, "agents/"), "/")
		parts := strings.SplitN(tail, "/", 2)
		targetAgentUUID = normalizeUUID(parts[0])
		if !validateUUID(targetAgentUUID) {
			writeA2ARESTError(w, a2aInvalidParams("invalid_target_agent", "target agent UUID is invalid", nil), "")
			return
		}
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				target, protocolErr := h.a2aLoadTargetAgent(targetAgentUUID)
				if protocolErr != nil {
					writeA2ARESTError(w, protocolErr, "")
					return
				}
				writeA2AJSON(w, http.StatusOK, h.a2aAgentCard(r, &target))
			case http.MethodPost:
				h.handleA2AJSONRPC(w, r, targetAgentUUID)
			default:
				writeA2AMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
			}
			return
		}
		path = strings.Trim(parts[1], "/")
	}

	switch {
	case path == "agent-card" || path == ".well-known/agent-card.json":
		if r.Method != http.MethodGet {
			writeA2AMethodNotAllowed(w, http.MethodGet)
			return
		}
		if targetAgentUUID == "" {
			writeA2AJSON(w, http.StatusOK, h.a2aAgentCard(r, nil))
			return
		}
		target, protocolErr := h.a2aLoadTargetAgent(targetAgentUUID)
		if protocolErr != nil {
			writeA2ARESTError(w, protocolErr, "")
			return
		}
		writeA2AJSON(w, http.StatusOK, h.a2aAgentCard(r, &target))
	case path == "message:send":
		h.handleA2ARESTSendMessage(w, r, targetAgentUUID)
	case path == "message:stream":
		h.handleA2ARESTUnsupported(w, r, "streaming is not enabled for MoltenHub A2A adapter", "")
	case path == "tasks":
		h.handleA2ARESTListTasks(w, r, targetAgentUUID)
	case path == "extendedAgentCard":
		h.handleA2ARESTExtendedAgentCard(w, r, targetAgentUUID)
	case strings.HasPrefix(path, "tasks/"):
		h.handleA2ARESTTaskSubroute(w, r, targetAgentUUID, strings.TrimPrefix(path, "tasks/"))
	default:
		writeA2ARESTError(w, a2aRouteNotFound(), "")
	}
}

func (h *Handler) handleA2AJSONRPC(w http.ResponseWriter, r *http.Request, targetAgentUUID string) {
	if r.Method != http.MethodPost {
		writeA2AJSONRPCError(w, nil, a2aInvalidRequest("invalid_request", "JSON-RPC requests must use POST", nil))
		return
	}
	if protocolErr := requireA2AJSONRequestContentType(r); protocolErr != nil {
		writeA2AJSONRPCError(w, nil, protocolErr)
		return
	}

	var req a2aJSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeA2AJSONRPCError(w, nil, a2aParseError("parse_error", "invalid JSON-RPC request", map[string]any{"error": err.Error()}))
		return
	}
	if req.JSONRPC != a2aJSONRPCVersion {
		writeA2AJSONRPCError(w, req.ID, a2aInvalidRequest("invalid_request", "jsonrpc must be 2.0", nil))
		return
	}

	var result any
	var protocolErr *a2aProtocolError
	method := strings.TrimSpace(req.Method)
	switch method {
	case a2aJSONRPCMethodSend, a2aJSONRPCCompatMethodSend:
		result, protocolErr = h.a2aSendMessage(r.Context(), r, targetAgentUUID, req.Params)
		if protocolErr == nil {
			if method == a2aJSONRPCCompatMethodSend {
				task, _ := result.(map[string]any)
				result = a2aJSONRPCCompatEvent("task", task)
			} else {
				result = map[string]any{"task": result}
			}
		}
	case a2aJSONRPCMethodGetTask, a2aJSONRPCCompatMethodGetTask:
		result, protocolErr = h.a2aGetTask(r, targetAgentUUID, req.Params)
	case a2aJSONRPCMethodListTask:
		result, protocolErr = h.a2aListTasks(r, targetAgentUUID, req.Params)
	case a2aJSONRPCMethodCancel, a2aJSONRPCCompatMethodCancel:
		result, protocolErr = h.a2aCancelTask(r, req.Params)
	case a2aJSONRPCMethodCard, a2aJSONRPCCompatMethodCard:
		result, protocolErr = h.a2aExtendedAgentCard(r, targetAgentUUID)
	case a2aJSONRPCMethodStream, a2aJSONRPCMethodSub, a2aJSONRPCCompatMethodStream, a2aJSONRPCCompatMethodSub:
		protocolErr = a2aUnsupported("unsupported_operation", "streaming is not enabled for MoltenHub A2A adapter", nil)
		if a2aRequestAcceptsEventStream(r) {
			writeA2AJSONRPCSSEError(w, req.ID, protocolErr)
			return
		}
	case a2aJSONRPCMethodPushGet, a2aJSONRPCMethodPushSet, a2aJSONRPCMethodPushList, a2aJSONRPCMethodPushDel,
		a2aJSONRPCCompatMethodPushGet, a2aJSONRPCCompatMethodPushSet, a2aJSONRPCCompatMethodPushList, a2aJSONRPCCompatMethodPushDel:
		protocolErr = a2aPushUnsupported("push_notifications_not_supported", "push notifications are not enabled for MoltenHub A2A adapter", nil)
	case "":
		protocolErr = a2aInvalidRequest("invalid_request", "method is required", nil)
	default:
		protocolErr = &a2aProtocolError{
			code:       a2aCodeMethodNotFound,
			httpStatus: http.StatusNotFound,
			status:     "NOT_FOUND",
			reason:     "METHOD_NOT_FOUND",
			message:    "method not found",
			details:    map[string]any{"method": req.Method},
		}
	}
	if protocolErr != nil {
		writeA2AJSONRPCError(w, req.ID, protocolErr)
		return
	}

	writeA2AJSON(w, http.StatusOK, a2aJSONRPCResponse{
		JSONRPC: a2aJSONRPCVersion,
		ID:      req.ID,
		Result:  result,
	})
}

func a2aJSONRPCCompatEvent(kind string, event map[string]any) map[string]any {
	out := cloneStringAnyMap(event)
	if out == nil {
		out = map[string]any{}
	}
	out["kind"] = kind
	return out
}

func (h *Handler) handleA2ARESTSendMessage(w http.ResponseWriter, r *http.Request, targetAgentUUID string) {
	if r.Method != http.MethodPost {
		writeA2AMethodNotAllowed(w, http.MethodPost)
		return
	}
	if protocolErr := requireA2AJSONRequestContentType(r); protocolErr != nil {
		writeA2ARESTError(w, protocolErr, "")
		return
	}
	var req a2aSendMessageRequest
	raw, err := decodeA2ARequestBody(r, &req)
	if err != nil {
		writeA2ARESTError(w, a2aParseError("parse_error", "invalid JSON request", map[string]any{"error": err.Error()}), "")
		return
	}
	req.raw = raw
	task, protocolErr := h.a2aSendMessageFromRequest(r.Context(), r, targetAgentUUID, req)
	if protocolErr != nil {
		writeA2ARESTError(w, protocolErr, "")
		return
	}
	writeA2AJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (h *Handler) handleA2ARESTListTasks(w http.ResponseWriter, r *http.Request, targetAgentUUID string) {
	if r.Method != http.MethodGet {
		writeA2AMethodNotAllowed(w, http.MethodGet)
		return
	}
	query, protocolErr := a2aListTaskRequestFromQuery(r.URL.Query())
	if protocolErr != nil {
		writeA2ARESTError(w, protocolErr, "")
		return
	}
	result, protocolErr := h.a2aListTasksForRequest(r, targetAgentUUID, query)
	if protocolErr != nil {
		writeA2ARESTError(w, protocolErr, "")
		return
	}
	writeA2AJSON(w, http.StatusOK, result)
}

func (h *Handler) handleA2ARESTTaskSubroute(w http.ResponseWriter, r *http.Request, targetAgentUUID, tail string) {
	tail = strings.Trim(tail, "/")
	if tail == "" {
		writeA2ARESTError(w, a2aInvalidParams("invalid_task_id", "task id is required", nil), "")
		return
	}
	if taskID, ok := strings.CutSuffix(tail, ":cancel"); ok {
		if r.Method != http.MethodPost {
			writeA2AMethodNotAllowed(w, http.MethodPost)
			return
		}
		result, protocolErr := h.a2aCancelTaskByID(r, strings.TrimSpace(taskID))
		if protocolErr != nil {
			writeA2ARESTError(w, protocolErr, taskID)
			return
		}
		writeA2AJSON(w, http.StatusOK, result)
		return
	}
	if taskID, ok := strings.CutSuffix(tail, ":subscribe"); ok {
		h.handleA2ARESTUnsupported(w, r, "streaming is not enabled for MoltenHub A2A adapter", taskID)
		return
	}
	if strings.Contains(tail, "/pushNotificationConfigs") {
		h.handleA2ARESTPushUnsupported(w, r, tail)
		return
	}
	if strings.Contains(tail, "/") {
		writeA2ARESTError(w, a2aRouteNotFound(), tail)
		return
	}
	if r.Method != http.MethodGet {
		writeA2AMethodNotAllowed(w, http.MethodGet)
		return
	}
	historyLength, protocolErr := parseOptionalPositiveIntQuery(r.URL.Query(), "historyLength")
	if protocolErr != nil {
		writeA2ARESTError(w, protocolErr, tail)
		return
	}
	task, protocolErr := h.a2aGetTaskByID(r, targetAgentUUID, tail, historyLength)
	if protocolErr != nil {
		writeA2ARESTError(w, protocolErr, tail)
		return
	}
	writeA2AJSON(w, http.StatusOK, task)
}

func (h *Handler) handleA2ARESTExtendedAgentCard(w http.ResponseWriter, r *http.Request, targetAgentUUID string) {
	if r.Method != http.MethodGet {
		writeA2AMethodNotAllowed(w, http.MethodGet)
		return
	}
	card, protocolErr := h.a2aExtendedAgentCard(r, targetAgentUUID)
	if protocolErr != nil {
		writeA2ARESTError(w, protocolErr, "")
		return
	}
	writeA2AJSON(w, http.StatusOK, card)
}

func (h *Handler) handleA2ARESTUnsupported(w http.ResponseWriter, r *http.Request, message, taskID string) {
	if r.Method != http.MethodPost {
		writeA2AMethodNotAllowed(w, http.MethodPost)
		return
	}
	writeA2ARESTError(w, a2aUnsupported("unsupported_operation", message, nil), taskID)
}

func (h *Handler) handleA2ARESTPushUnsupported(w http.ResponseWriter, r *http.Request, taskID string) {
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodDelete:
		writeA2ARESTError(w, a2aPushUnsupported("push_notifications_not_supported", "push notifications are not enabled for MoltenHub A2A adapter", nil), taskID)
	default:
		writeA2AMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost+", "+http.MethodDelete)
	}
}

func (h *Handler) a2aSendMessage(ctx context.Context, r *http.Request, targetAgentUUID string, raw json.RawMessage) (map[string]any, *a2aProtocolError) {
	var req a2aSendMessageRequest
	if err := decodeA2AParams(raw, &req); err != nil {
		return nil, a2aInvalidParams("invalid_params", "SendMessage params must be a valid SendMessageRequest", map[string]any{"error": err.Error()})
	}
	req.raw = cloneRawJSON(raw)
	return h.a2aSendMessageFromRequest(ctx, r, targetAgentUUID, req)
}

func (h *Handler) a2aSendMessageFromRequest(ctx context.Context, r *http.Request, targetAgentUUID string, req a2aSendMessageRequest) (map[string]any, *a2aProtocolError) {
	senderAgentUUID, err := h.authenticateAgent(r)
	if err != nil {
		return nil, a2aUnauthenticated("unauthenticated", "missing or invalid bearer token", nil)
	}
	if heartbeatErr := h.touchAgentPresenceOnline(senderAgentUUID, "", ""); heartbeatErr != nil {
		return nil, a2aErrorFromRuntimeHandler(heartbeatErr)
	}
	publishReq, protocolErr := a2aPublishRequestFromSendMessage(targetAgentUUID, req)
	if protocolErr != nil {
		return nil, protocolErr
	}
	result, handlerErr := h.publishFromAgent(ctx, senderAgentUUID, publishReq)
	if handlerErr != nil {
		return nil, a2aErrorFromRuntimeHandler(handlerErr)
	}
	status, _ := result["status"].(string)
	if status == "dropped" {
		reason, _ := result["reason"].(string)
		if reason == "" {
			reason = "message dropped"
		}
		return nil, a2aUnauthorized("no_trust_path", "publish dropped: "+reason, map[string]any{"publish_result": result})
	}
	messageID, _ := result["message_id"].(string)
	if messageID == "" {
		return nil, a2aInternal("invalid_agent_response", "publish response did not include message_id", map[string]any{"publish_result": result})
	}
	record, err := h.control.GetMessageRecord(messageID)
	if err != nil {
		return nil, a2aErrorFromStore("task_lookup_failed", "failed to load A2A task after publish", err)
	}
	h.recordA2AAdapterUsage(senderAgentUUID, "send_message", map[string]any{
		"message_id":        messageID,
		"target_agent_uuid": publishReq.ToAgentUUID,
		"target_agent_uri":  publishReq.ToAgentURI,
	})
	return h.a2aTaskFromRecord(record, req.ConfigHistoryLength()), nil
}

func (req a2aSendMessageRequest) ConfigHistoryLength() *int {
	if req.Config == nil {
		return nil
	}
	return req.Config.HistoryLength
}

func a2aPublishRequestFromSendMessage(targetAgentUUID string, req a2aSendMessageRequest) (publishRequest, *a2aProtocolError) {
	if req.Message == nil {
		return publishRequest{}, a2aInvalidParams("invalid_message", "message is required", nil)
	}
	targetAgentUUID = normalizeUUID(targetAgentUUID)
	targetAgentURI := ""
	if targetAgentUUID == "" {
		targetAgentUUID = normalizeUUID(metadataString(req.Metadata, "to_agent_uuid", "toAgentUuid", "target_agent_uuid", "targetAgentUuid"))
		targetAgentURI = strings.TrimSpace(metadataString(req.Metadata, "to_agent_uri", "toAgentUri", "target_agent_uri", "targetAgentUri"))
	}
	if targetAgentUUID == "" && targetAgentURI == "" && req.Message != nil {
		targetAgentUUID = normalizeUUID(metadataString(req.Message.Metadata, "to_agent_uuid", "toAgentUuid", "target_agent_uuid", "targetAgentUuid"))
		targetAgentURI = strings.TrimSpace(metadataString(req.Message.Metadata, "to_agent_uri", "toAgentUri", "target_agent_uri", "targetAgentUri"))
	}
	if targetAgentUUID == "" && targetAgentURI == "" {
		return publishRequest{}, a2aInvalidParams("missing_target_agent", "target agent is required; use /v1/a2a/agents/{agent_uuid}, request metadata.to_agent_uuid/to_agent_uri, or message metadata.to_agent_uuid/to_agent_uri", nil)
	}
	if targetAgentUUID != "" && !validateUUID(targetAgentUUID) {
		return publishRequest{}, a2aInvalidParams("invalid_target_agent", "target agent UUID is invalid", nil)
	}
	contentType, payload, protocolErr := a2aMessagePayload(req)
	if protocolErr != nil {
		return publishRequest{}, protocolErr
	}
	var clientMsgID *string
	if id := strings.TrimSpace(req.Message.ID); id != "" {
		clientMsgID = &id
	}
	return publishRequest{
		ToAgentUUID: targetAgentUUID,
		ToAgentURI:  targetAgentURI,
		ContentType: contentType,
		Payload:     payload,
		ClientMsgID: clientMsgID,
	}, nil
}

func a2aMessagePayload(req a2aSendMessageRequest) (string, string, *a2aProtocolError) {
	if len(req.Message.Parts) == 0 {
		return "", "", a2aInvalidParams("invalid_message_parts", "message.parts must contain at least one part", nil)
	}
	textParts := make([]string, 0, len(req.Message.Parts))
	allText := true
	for _, part := range req.Message.Parts {
		if part.Text == nil || part.Raw != "" || part.Data != nil || part.URL != "" {
			allText = false
			break
		}
		textParts = append(textParts, *part.Text)
	}
	if allText && !a2aMessageNeedsJSONEnvelope(req) {
		return a2aMIMEText, strings.Join(textParts, "\n"), nil
	}
	envelope := map[string]any{
		"protocol": a2aProtocolAdapter,
		"message":  req.Message,
	}
	if len(req.Metadata) > 0 {
		envelope["metadata"] = req.Metadata
	}
	if len(req.raw) > 0 {
		var raw any
		if err := json.Unmarshal(req.raw, &raw); err == nil {
			envelope["request"] = raw
		}
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return "", "", a2aInvalidParams("invalid_message", "message could not be encoded", map[string]any{"error": err.Error()})
	}
	return a2aMIMEJSON, string(raw), nil
}

func a2aMessageNeedsJSONEnvelope(req a2aSendMessageRequest) bool {
	if req.Message == nil {
		return false
	}
	if strings.TrimSpace(req.Message.ContextID) != "" || strings.TrimSpace(req.Message.TaskID) != "" {
		return true
	}
	if len(req.Message.ReferenceTasks) > 0 || len(req.Message.Extensions) > 0 {
		return true
	}
	if len(req.Metadata) > 0 && !a2aMetadataOnlyRouting(req.Metadata) {
		return true
	}
	if len(req.Message.Metadata) > 0 && !a2aMetadataOnlyRouting(req.Message.Metadata) {
		return true
	}
	return false
}

func a2aMetadataOnlyRouting(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return true
	}
	for key := range metadata {
		switch key {
		case "to_agent_uuid", "toAgentUuid", "target_agent_uuid", "targetAgentUuid",
			"to_agent_uri", "toAgentUri", "target_agent_uri", "targetAgentUri":
			continue
		default:
			return false
		}
	}
	return true
}

func (h *Handler) a2aGetTask(r *http.Request, targetAgentUUID string, raw json.RawMessage) (map[string]any, *a2aProtocolError) {
	var req a2aGetTaskRequest
	if err := decodeA2AParams(raw, &req); err != nil {
		return nil, a2aInvalidParams("invalid_params", "GetTask params must be a valid GetTaskRequest", map[string]any{"error": err.Error()})
	}
	return h.a2aGetTaskByID(r, targetAgentUUID, req.ID, req.HistoryLength)
}

func (h *Handler) a2aGetTaskByID(r *http.Request, targetAgentUUID, taskID string, historyLength *int) (map[string]any, *a2aProtocolError) {
	callerAgentUUID, err := h.authenticateAgent(r)
	if err != nil {
		return nil, a2aUnauthenticated("unauthenticated", "missing or invalid bearer token", nil)
	}
	record, err := h.control.GetMessageRecord(strings.TrimSpace(taskID))
	if err != nil {
		return nil, a2aErrorFromStore("task_not_found", "task not found", err)
	}
	targetAgentUUID = normalizeUUID(targetAgentUUID)
	if !a2aCanSeeRecord(callerAgentUUID, targetAgentUUID, record.Message) {
		return nil, a2aUnauthorized("forbidden", "task is not visible to this agent", nil)
	}
	h.recordA2AAdapterUsage(callerAgentUUID, "get_task", map[string]any{
		"message_id":        record.Message.MessageID,
		"target_agent_uuid": targetAgentUUID,
	})
	return h.a2aTaskFromRecord(record, historyLength), nil
}

func (h *Handler) a2aListTasks(r *http.Request, targetAgentUUID string, raw json.RawMessage) (map[string]any, *a2aProtocolError) {
	var req a2aListTasksRequest
	if err := decodeA2AParams(raw, &req); err != nil {
		return nil, a2aInvalidParams("invalid_params", "ListTasks params must be a valid ListTasksRequest", map[string]any{"error": err.Error()})
	}
	return h.a2aListTasksForRequest(r, targetAgentUUID, req)
}

func (h *Handler) a2aListTasksForRequest(r *http.Request, targetAgentUUID string, req a2aListTasksRequest) (map[string]any, *a2aProtocolError) {
	callerAgentUUID, err := h.authenticateAgent(r)
	if err != nil {
		return nil, a2aUnauthenticated("unauthenticated", "missing or invalid bearer token", nil)
	}
	targetAgentUUID = normalizeUUID(targetAgentUUID)
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := 0
	if strings.TrimSpace(req.PageToken) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(req.PageToken))
		if err != nil || parsed < 0 {
			return nil, a2aInvalidParams("invalid_page_token", "pageToken must be a non-negative integer offset", nil)
		}
		offset = parsed
	}
	records := h.a2aVisibleMessageRecords(callerAgentUUID, targetAgentUUID)
	tasks := make([]map[string]any, 0, len(records))
	for _, record := range records {
		task := h.a2aTaskFromRecord(record, req.HistoryLength)
		if req.ContextID != "" && a2aReadStringPath(task, "contextId") != req.ContextID {
			continue
		}
		if req.Status != "" && a2aReadStringPath(task, "status.state") != req.Status {
			continue
		}
		if req.StatusTimestampAfter != nil && !record.UpdatedAt.After(req.StatusTimestampAfter.UTC()) {
			continue
		}
		tasks = append(tasks, task)
	}
	total := len(tasks)
	end := offset + pageSize
	if offset > total {
		offset = total
	}
	if end > total {
		end = total
	}
	nextPageToken := ""
	if end < total {
		nextPageToken = strconv.Itoa(end)
	}
	h.recordA2AAdapterUsage(callerAgentUUID, "list_tasks", map[string]any{
		"target_agent_uuid": targetAgentUUID,
		"total_size":        total,
	})
	return map[string]any{
		"tasks":         tasks[offset:end],
		"totalSize":     total,
		"pageSize":      pageSize,
		"nextPageToken": nextPageToken,
	}, nil
}

func (h *Handler) a2aCancelTask(r *http.Request, raw json.RawMessage) (map[string]any, *a2aProtocolError) {
	var req a2aCancelTaskRequest
	if err := decodeA2AParams(raw, &req); err != nil {
		return nil, a2aInvalidParams("invalid_params", "CancelTask params must be a valid CancelTaskRequest", map[string]any{"error": err.Error()})
	}
	return h.a2aCancelTaskByID(r, req.ID)
}

func (h *Handler) a2aCancelTaskByID(r *http.Request, taskID string) (map[string]any, *a2aProtocolError) {
	callerAgentUUID, err := h.authenticateAgent(r)
	if err != nil {
		return nil, a2aUnauthenticated("unauthenticated", "missing or invalid bearer token", nil)
	}
	record, err := h.control.GetMessageRecord(strings.TrimSpace(taskID))
	if err != nil {
		return nil, a2aErrorFromStore("task_not_found", "task not found", err)
	}
	if !a2aCanSeeRecord(callerAgentUUID, "", record.Message) {
		return nil, a2aUnauthorized("forbidden", "task is not visible to this agent", nil)
	}
	return nil, &a2aProtocolError{
		code:       a2aCodeTaskNotCancelable,
		httpStatus: http.StatusBadRequest,
		status:     "FAILED_PRECONDITION",
		reason:     "TASK_NOT_CANCELABLE",
		message:    "MoltenHub message tasks cannot be canceled after publish",
		details:    map[string]any{"task_id": strings.TrimSpace(taskID), "message_status": record.Status},
	}
}

func (h *Handler) a2aExtendedAgentCard(r *http.Request, targetAgentUUID string) (map[string]any, *a2aProtocolError) {
	callerAgentUUID, err := h.authenticateAgent(r)
	if err != nil {
		return nil, a2aUnauthenticated("unauthenticated", "missing or invalid bearer token", nil)
	}
	var target *model.Agent
	if targetAgentUUID != "" {
		agent, protocolErr := h.a2aLoadTargetAgent(targetAgentUUID)
		if protocolErr != nil {
			return nil, protocolErr
		}
		target = &agent
	}
	card := h.a2aAgentCard(r, target)
	card["metadata"] = map[string]any{
		"authenticated_agent_uuid": callerAgentUUID,
		"control_plane": map[string]any{
			"core_runtime": h.apiBaseURL(r),
			"a2a_adapter":  h.apiBaseURL(r) + "/a2a",
		},
	}
	return card, nil
}

func (h *Handler) a2aAgentCard(r *http.Request, target *model.Agent) map[string]any {
	apiBase := h.apiBaseURL(r)
	a2aURL := apiBase + "/a2a"
	name := "MoltenHub A2A Gateway"
	description := "A2A adapter for MoltenHub agent-to-agent messaging. Use metadata.to_agent_uuid or a target-specific /v1/a2a/agents/{agent_uuid} endpoint."
	version := "1.0.0"
	skills := []map[string]any{{
		"id":          "moltenhub-message",
		"name":        "MoltenHub Message Relay",
		"description": "Relay an A2A message through MoltenHub trust-gated agent messaging.",
		"tags":        []string{"moltenhub", "messaging", "a2a"},
		"inputModes":  []string{a2aMIMEText, a2aMIMEJSON},
		"outputModes": []string{a2aMIMEJSON, a2aMIMEText},
	}}
	if target != nil {
		a2aURL = apiBase + "/a2a/agents/" + url.PathEscape(target.AgentUUID)
		name = a2aAgentDisplayName(*target)
		description = "A2A endpoint for MoltenHub agent " + target.AgentID + ". Messages are delivered through existing MoltenHub trust and queue semantics."
		skills = a2aSkillsFromAgentMetadata(target.Metadata)
		if len(skills) == 0 {
			skills = []map[string]any{{
				"id":          "agent-message",
				"name":        name + " messaging",
				"description": "Deliver text or JSON messages to this MoltenHub agent.",
				"tags":        []string{"moltenhub", "agent", "messaging"},
				"inputModes":  []string{a2aMIMEText, a2aMIMEJSON},
				"outputModes": []string{a2aMIMEJSON, a2aMIMEText},
			}}
		}
	}
	providerURL := h.canonicalBaseURL
	if providerURL == "" {
		providerURL = h.hubBaseURL(r)
	}
	return map[string]any{
		"name":               name,
		"description":        description,
		"version":            version,
		"documentationUrl":   apiBase + "/agents/me/manifest",
		"defaultInputModes":  []string{a2aMIMEText, a2aMIMEJSON},
		"defaultOutputModes": []string{a2aMIMEJSON, a2aMIMEText},
		"provider": map[string]any{
			"organization": "MoltenHub",
			"url":          providerURL,
		},
		"capabilities": map[string]any{
			"streaming":         false,
			"pushNotifications": false,
			"extendedAgentCard": true,
			"collectiveStream":  true,
		},
		"securitySchemes": map[string]any{
			a2aSecuritySchemeBearer: map[string]any{
				"httpAuthSecurityScheme": map[string]any{
					"scheme":       "Bearer",
					"bearerFormat": "MoltenHub agent token",
					"description":  "Use the existing MoltenHub agent bearer token.",
				},
			},
		},
		"securityRequirements": []map[string]any{{
			"schemes": map[string]any{
				a2aSecuritySchemeBearer: []string{},
			},
		}},
		"supportedInterfaces": []map[string]any{
			{
				"url":             a2aURL,
				"protocolBinding": "JSONRPC",
				"protocolVersion": a2aProtocolVersion,
			},
			{
				"url":             a2aURL,
				"protocolBinding": "HTTP+JSON",
				"protocolVersion": a2aProtocolVersion,
			},
			{
				"url":             apiBase + "/collective/stream",
				"protocolBinding": "WebSocket",
				"protocolVersion": a2aProtocolVersion,
			},
		},
		"skills": skills,
	}
}

func a2aAgentDisplayName(agent model.Agent) string {
	if displayName := metadataStringAliasValue(agent.Metadata, "display_name"); displayName != "" {
		return displayName
	}
	if strings.TrimSpace(agent.Handle) != "" {
		return strings.TrimSpace(agent.Handle)
	}
	if strings.TrimSpace(agent.AgentID) != "" {
		return strings.TrimSpace(agent.AgentID)
	}
	return strings.TrimSpace(agent.AgentUUID)
}

func a2aSkillsFromAgentMetadata(metadata map[string]any) []map[string]any {
	summaries := parseAdvertisedSkills(metadata)
	out := make([]map[string]any, 0, len(summaries))
	for _, skill := range summaries {
		tags := []string{"moltenhub", "agent"}
		if skill.Parameters != nil && skill.Parameters.Format != "" {
			tags = append(tags, "parameters:"+skill.Parameters.Format)
		}
		item := map[string]any{
			"id":          skill.Name,
			"name":        skill.Name,
			"description": skill.Description,
			"tags":        tags,
			"inputModes":  []string{a2aMIMEText, a2aMIMEJSON},
			"outputModes": []string{a2aMIMEJSON, a2aMIMEText},
		}
		if skill.Parameters != nil {
			item["examples"] = []string{"Send a skill_request JSON envelope for " + skill.Name}
		}
		out = append(out, item)
	}
	return out
}

func (h *Handler) a2aTaskFromRecord(record model.MessageRecord, historyLength *int) map[string]any {
	contextID := a2aContextIDFromRecord(record)
	task := map[string]any{
		"id":        record.Message.MessageID,
		"contextId": contextID,
		"status": map[string]any{
			"state":     a2aTaskState(record),
			"timestamp": record.UpdatedAt.UTC().Format(time.RFC3339Nano),
		},
		"metadata": a2aTaskMetadataFromRecord(record, contextID),
	}
	if historyLength == nil || *historyLength != 0 {
		task["history"] = []map[string]any{h.a2aMessageFromRecord(record, contextID)}
	}
	if record.Status == model.MessageDeliveryFailed || strings.TrimSpace(record.LastFailureReason) != "" {
		status, _ := task["status"].(map[string]any)
		status["message"] = map[string]any{
			"messageId": record.Message.MessageID + "-failure",
			"contextId": contextID,
			"taskId":    record.Message.MessageID,
			"role":      "ROLE_AGENT",
			"parts": []map[string]any{{
				"text": "Failure: message delivery failed\nError details: " + record.LastFailureReason,
			}},
		}
	}
	return task
}

func a2aTaskMetadataFromRecord(record model.MessageRecord, contextID string) map[string]any {
	moltenhub := map[string]any{
		"message_id":          record.Message.MessageID,
		"task_id":             record.Message.MessageID,
		"context_id":          contextID,
		"status":              record.Status,
		"from_agent_uuid":     record.Message.FromAgentUUID,
		"to_agent_uuid":       record.Message.ToAgentUUID,
		"from_agent_id":       record.Message.FromAgentID,
		"to_agent_id":         record.Message.ToAgentID,
		"from_agent_uri":      record.Message.FromAgentURI,
		"to_agent_uri":        record.Message.ToAgentURI,
		"sender_org_id":       record.Message.SenderOrgID,
		"receiver_org_id":     record.Message.ReceiverOrgID,
		"content_type":        record.Message.ContentType,
		"delivery_attempts":   record.DeliveryAttempts,
		"idempotent_replays":  record.IdempotentReplays,
		"last_failure_reason": record.LastFailureReason,
	}
	if record.Message.ClientMsgID != nil && strings.TrimSpace(*record.Message.ClientMsgID) != "" {
		moltenhub["client_msg_id"] = strings.TrimSpace(*record.Message.ClientMsgID)
	}
	metadata := map[string]any{"moltenhub": moltenhub}
	if a2aMeta := a2aProtocolTaskMetadata(record.Message.Payload); len(a2aMeta) > 0 {
		metadata["a2a"] = a2aMeta
	}
	if openClawMeta := openClawTaskMetadata(record.Message.Payload); len(openClawMeta) > 0 {
		metadata["openclaw"] = openClawMeta
	}
	return metadata
}

func a2aProtocolTaskMetadata(payload string) map[string]any {
	envelope := a2aEnvelopePayload(payload)
	if len(envelope) == 0 || strings.TrimSpace(a2aReadStringPath(envelope, "protocol")) != a2aProtocolAdapter {
		return nil
	}
	msg, _ := envelope["message"].(map[string]any)
	if len(msg) == 0 {
		return nil
	}
	out := map[string]any{}
	copyMetadataString(out, msg, "messageId", "message_id")
	copyMetadataString(out, msg, "contextId", "context_id")
	copyMetadataString(out, msg, "taskId", "task_id")
	copyMetadataString(out, msg, "role", "role")
	if refs := stringSliceFromAny(msg["referenceTaskIds"]); len(refs) > 0 {
		out["reference_task_ids"] = refs
	}
	if request, ok := envelope["request"].(map[string]any); ok {
		if requestMeta, ok := request["metadata"].(map[string]any); ok && len(requestMeta) > 0 {
			out["request_metadata"] = cloneStringAnyMap(requestMeta)
		}
	}
	if envelopeMeta, ok := envelope["metadata"].(map[string]any); ok && len(envelopeMeta) > 0 {
		out["metadata"] = cloneStringAnyMap(envelopeMeta)
	}
	return out
}

func openClawTaskMetadata(payload string) map[string]any {
	envelope := openClawEnvelopePayload(payload)
	if len(envelope) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{
		"protocol",
		"kind",
		"type",
		"request_id",
		"reply_to_request_id",
		"reply_target",
		"skill_name",
		"payload_format",
		"status",
	} {
		copyMetadataString(out, envelope, key, key)
	}
	return out
}

func copyMetadataString(out, source map[string]any, sourceKey, targetKey string) {
	if out == nil || source == nil {
		return
	}
	value := strings.TrimSpace(asStringAny(source[sourceKey]))
	if value != "" {
		out[targetKey] = value
	}
}

func stringSliceFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(asStringAny(item)); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		return nil
	}
}

func (h *Handler) a2aMessageFromRecord(record model.MessageRecord, contextID string) map[string]any {
	if msg := a2aEnvelopeMessage(record.Message.Payload); msg != nil {
		if strings.TrimSpace(a2aReadStringPath(msg, "messageId")) == "" {
			msg["messageId"] = record.Message.MessageID
		}
		if strings.TrimSpace(a2aReadStringPath(msg, "contextId")) == "" {
			msg["contextId"] = contextID
		}
		if strings.TrimSpace(a2aReadStringPath(msg, "taskId")) == "" {
			msg["taskId"] = record.Message.MessageID
		}
		if strings.TrimSpace(a2aReadStringPath(msg, "role")) == "" {
			msg["role"] = "ROLE_USER"
		}
		return msg
	}
	parts := []map[string]any{}
	switch strings.TrimSpace(record.Message.ContentType) {
	case a2aMIMEText:
		parts = append(parts, map[string]any{"text": record.Message.Payload})
	case a2aMIMEJSON:
		var decoded any
		if err := json.Unmarshal([]byte(record.Message.Payload), &decoded); err == nil {
			parts = append(parts, map[string]any{"data": decoded, "mediaType": a2aMIMEJSON})
		} else {
			parts = append(parts, map[string]any{"text": record.Message.Payload, "mediaType": a2aMIMEJSON})
		}
	default:
		parts = append(parts, map[string]any{"text": record.Message.Payload, "mediaType": record.Message.ContentType})
	}
	return map[string]any{
		"messageId": record.Message.MessageID,
		"contextId": contextID,
		"taskId":    record.Message.MessageID,
		"role":      "ROLE_USER",
		"parts":     parts,
		"metadata": map[string]any{
			"moltenhub": map[string]any{
				"content_type": record.Message.ContentType,
			},
		},
	}
}

func a2aEnvelopeMessage(payload string) map[string]any {
	envelope := a2aEnvelopePayload(payload)
	msg, ok := envelope["message"].(map[string]any)
	if ok {
		return cloneStringAnyMap(msg)
	}
	request, ok := envelope["request"].(map[string]any)
	if !ok {
		return nil
	}
	msg, ok = request["message"].(map[string]any)
	if !ok {
		return nil
	}
	return cloneStringAnyMap(msg)
}

func a2aEnvelopePayload(payload string) map[string]any {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return nil
	}
	if strings.TrimSpace(asStringAny(envelope["protocol"])) == a2aProtocolAdapter {
		return envelope
	}
	if _, ok := envelope["message"].(map[string]any); ok {
		return envelope
	}
	if request, ok := envelope["request"].(map[string]any); ok {
		if _, ok := request["message"].(map[string]any); ok {
			return envelope
		}
	}
	return nil
}

func openClawEnvelopePayload(payload string) map[string]any {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return nil
	}
	if strings.TrimSpace(asStringAny(envelope["protocol"])) != openClawHTTPProtocol {
		return nil
	}
	return envelope
}

func a2aContextIDFromRecord(record model.MessageRecord) string {
	if msg := a2aEnvelopeMessage(record.Message.Payload); msg != nil {
		if contextID := strings.TrimSpace(a2aReadStringPath(msg, "contextId")); contextID != "" {
			return contextID
		}
	}
	if openClaw := openClawEnvelopePayload(record.Message.Payload); len(openClaw) > 0 {
		if requestID := strings.TrimSpace(asStringAny(openClaw["request_id"])); requestID != "" {
			return requestID
		}
	}
	if record.Message.ClientMsgID != nil && strings.TrimSpace(*record.Message.ClientMsgID) != "" {
		return strings.TrimSpace(*record.Message.ClientMsgID)
	}
	return record.Message.MessageID
}

func a2aTaskState(record model.MessageRecord) string {
	if strings.TrimSpace(record.LastFailureReason) != "" {
		return "TASK_STATE_FAILED"
	}
	switch record.Status {
	case model.MessageDeliveryQueued:
		return "TASK_STATE_SUBMITTED"
	case model.MessageDeliveryLeased:
		return "TASK_STATE_WORKING"
	case model.MessageDeliveryAcked, model.MessageForwarded:
		return "TASK_STATE_COMPLETED"
	case model.MessageDeliveryFailed:
		return "TASK_STATE_FAILED"
	default:
		return "TASK_STATE_WORKING"
	}
}

func (h *Handler) a2aVisibleMessageRecords(callerAgentUUID, targetAgentUUID string) []model.MessageRecord {
	agentRecords, err := h.control.ListMessageRecordsForAgent(callerAgentUUID)
	if err != nil {
		return nil
	}
	records := make([]model.MessageRecord, 0, len(agentRecords))
	for _, record := range agentRecords {
		if !a2aCanSeeRecord(callerAgentUUID, targetAgentUUID, record.Message) {
			continue
		}
		records = append(records, record)
	}
	return records
}

func a2aCanSeeRecord(callerAgentUUID, targetAgentUUID string, message model.Message) bool {
	if callerAgentUUID != message.FromAgentUUID && callerAgentUUID != message.ToAgentUUID {
		return false
	}
	if targetAgentUUID == "" {
		return true
	}
	return targetAgentUUID == message.FromAgentUUID || targetAgentUUID == message.ToAgentUUID
}

func (h *Handler) a2aLoadTargetAgent(agentUUID string) (model.Agent, *a2aProtocolError) {
	agentUUID = normalizeUUID(agentUUID)
	if !validateUUID(agentUUID) {
		return model.Agent{}, a2aInvalidParams("invalid_target_agent", "target agent UUID is invalid", nil)
	}
	agent, err := h.control.GetAgentByUUID(agentUUID)
	if err != nil {
		return model.Agent{}, a2aErrorFromStore("target_agent_not_found", "target agent not found", err)
	}
	return agent, nil
}

func (h *Handler) a2aTargetAgentFromQuery(r *http.Request) (*model.Agent, *a2aProtocolError) {
	query := r.URL.Query()
	agentUUID := normalizeUUID(query.Get("agent_uuid"))
	agentURI := strings.TrimSpace(query.Get("agent_uri"))
	if agentUUID == "" && agentURI == "" {
		return nil, nil
	}
	if agentUUID == "" {
		resolved, err := h.control.ResolveAgentUUIDByURI(agentURI)
		if err != nil {
			return nil, a2aErrorFromStore("target_agent_not_found", "target agent not found", err)
		}
		agentUUID = resolved
	}
	agent, protocolErr := h.a2aLoadTargetAgent(agentUUID)
	if protocolErr != nil {
		return nil, protocolErr
	}
	return &agent, nil
}

func requireA2AJSONRequestContentType(r *http.Request) *a2aProtocolError {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if mediaType == a2aMIMEJSON || mediaType == a2aMIMEProtocolJSON {
		return nil
	}
	return a2aError(
		a2aCodeContentType,
		http.StatusUnsupportedMediaType,
		"INVALID_ARGUMENT",
		"UNSUPPORTED_CONTENT_TYPE",
		"unsupported_media_type",
		"request content type must be application/json or application/a2a+json",
		map[string]any{"content_type": r.Header.Get("Content-Type")},
	)
}

func decodeA2ARequestBody(r *http.Request, out any) (json.RawMessage, error) {
	defer r.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("empty JSON request")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, err
	}
	return cloneRawJSON(raw), nil
}

func decodeA2AParams(raw json.RawMessage, out any) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}

func metadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		if out := strings.TrimSpace(asStringAny(value)); out != "" {
			return out
		}
	}
	return ""
}

func parseOptionalPositiveIntQuery(query url.Values, key string) (*int, *a2aProtocolError) {
	raw := strings.TrimSpace(query.Get(key))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return nil, a2aInvalidParams("invalid_"+key, key+" must be a non-negative integer", nil)
	}
	return &value, nil
}

func a2aListTaskRequestFromQuery(query url.Values) (a2aListTasksRequest, *a2aProtocolError) {
	req := a2aListTasksRequest{
		ContextID: query.Get("contextId"),
		Status:    query.Get("status"),
		PageToken: query.Get("pageToken"),
	}
	if raw := strings.TrimSpace(query.Get("pageSize")); raw != "" {
		pageSize, err := strconv.Atoi(raw)
		if err != nil || pageSize < 0 {
			return req, a2aInvalidParams("invalid_page_size", "pageSize must be a non-negative integer", nil)
		}
		req.PageSize = pageSize
	}
	historyLength, protocolErr := parseOptionalPositiveIntQuery(query, "historyLength")
	if protocolErr != nil {
		return req, protocolErr
	}
	req.HistoryLength = historyLength
	if raw := strings.TrimSpace(query.Get("lastUpdatedAfter")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return req, a2aInvalidParams("invalid_last_updated_after", "lastUpdatedAfter must be RFC3339", nil)
		}
		req.StatusTimestampAfter = &parsed
	}
	if raw := strings.TrimSpace(query.Get("includeArtifacts")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return req, a2aInvalidParams("invalid_include_artifacts", "includeArtifacts must be a boolean", nil)
		}
		req.IncludeArtifacts = parsed
	}
	return req, nil
}

func writeA2AJSONRPCError(w http.ResponseWriter, id any, protocolErr *a2aProtocolError) {
	if protocolErr == nil {
		protocolErr = a2aInternal("internal_error", "internal error", nil)
	}
	writeA2AJSON(w, http.StatusOK, a2aJSONRPCResponse{
		JSONRPC: a2aJSONRPCVersion,
		ID:      id,
		Error:   protocolErr.wireError(),
	})
}

func writeA2AJSONRPCSSEError(w http.ResponseWriter, id any, protocolErr *a2aProtocolError) {
	if protocolErr == nil {
		protocolErr = a2aInternal("internal_error", "internal error", nil)
	}
	w.Header().Set("Content-Type", a2aMIMEEventStream)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	payload, err := json.Marshal(a2aJSONRPCResponse{
		JSONRPC: a2aJSONRPCVersion,
		ID:      id,
		Error:   protocolErr.wireError(),
	})
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeA2AJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func a2aRequestAcceptsEventStream(r *http.Request) bool {
	for _, item := range strings.Split(r.Header.Get("Accept"), ",") {
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(item, ";")[0]))
		if mediaType == a2aMIMEEventStream {
			return true
		}
	}
	return false
}

func writeA2AMethodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeA2ARESTError(w, a2aError(
		a2aCodeInvalidRequest,
		http.StatusMethodNotAllowed,
		"INVALID_ARGUMENT",
		"METHOD_NOT_ALLOWED",
		"method_not_allowed",
		"method not allowed",
		map[string]any{"allowed": allowed},
	), "")
}

func writeA2ARESTError(w http.ResponseWriter, protocolErr *a2aProtocolError, taskID string) {
	if protocolErr == nil {
		protocolErr = a2aInternal("internal_error", "internal error", nil)
	}
	details := []any{
		map[string]any{
			"@type":    "type.googleapis.com/google.rpc.ErrorInfo",
			"reason":   protocolErr.reason,
			"domain":   "a2a-protocol.org",
			"metadata": a2aRESTErrorMetadata(protocolErr, taskID),
		},
		protocolErr.failureDetails(),
	}
	writeA2AJSON(w, protocolErr.httpStatus, map[string]any{
		"error": map[string]any{
			"code":    protocolErr.httpStatus,
			"status":  protocolErr.status,
			"message": protocolErr.message,
			"details": details,
		},
	})
}

func a2aRESTErrorMetadata(protocolErr *a2aProtocolError, taskID string) map[string]string {
	out := map[string]string{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"code":      detailCode(protocolErr.details),
	}
	if strings.TrimSpace(taskID) != "" {
		out["taskId"] = strings.TrimSpace(taskID)
	}
	return out
}

func a2aRouteNotFound() *a2aProtocolError {
	return a2aError(
		a2aCodeMethodNotFound,
		http.StatusNotFound,
		"NOT_FOUND",
		"ROUTE_NOT_FOUND",
		"route_not_found",
		"route not found",
		nil,
	)
}

func (e *a2aProtocolError) wireError() *a2aWireError {
	return &a2aWireError{
		Code:    e.code,
		Message: e.message,
		Data:    e.failureDetails(),
	}
}

func (e *a2aProtocolError) failureDetails() map[string]any {
	detail := map[string]any{
		"code":    detailCode(e.details),
		"message": e.message,
	}
	for key, value := range e.details {
		if strings.TrimSpace(key) == "" {
			continue
		}
		detail[key] = value
	}
	return map[string]any{
		"failure":        true,
		"Failure":        true,
		"Failure:":       true,
		"Error details":  detail,
		"Error details:": detail,
	}
}

func detailCode(details map[string]any) string {
	if details == nil {
		return "a2a_error"
	}
	if code, _ := details["code"].(string); strings.TrimSpace(code) != "" {
		return strings.TrimSpace(code)
	}
	return "a2a_error"
}

func a2aParseError(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeParseError, http.StatusBadRequest, "INVALID_ARGUMENT", "PARSE_ERROR", code, message, details)
}

func a2aInvalidRequest(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeInvalidRequest, http.StatusBadRequest, "INVALID_ARGUMENT", "INVALID_REQUEST", code, message, details)
}

func a2aInvalidParams(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeInvalidParams, http.StatusBadRequest, "INVALID_ARGUMENT", "INVALID_PARAMS", code, message, details)
}

func a2aUnauthenticated(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeUnauthenticated, http.StatusUnauthorized, "UNAUTHENTICATED", "UNAUTHENTICATED", code, message, details)
}

func a2aUnauthorized(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeUnauthorized, http.StatusForbidden, "PERMISSION_DENIED", "UNAUTHORIZED", code, message, details)
}

func a2aUnsupported(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeUnsupported, http.StatusNotImplemented, "UNIMPLEMENTED", "UNSUPPORTED_OPERATION", code, message, details)
}

func a2aPushUnsupported(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodePushUnsupported, http.StatusNotImplemented, "UNIMPLEMENTED", "PUSH_NOTIFICATION_NOT_SUPPORTED", code, message, details)
}

func a2aTaskNotFound(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeTaskNotFound, http.StatusNotFound, "NOT_FOUND", "TASK_NOT_FOUND", code, message, details)
}

func a2aInternal(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeInternal, http.StatusInternalServerError, "INTERNAL", "INTERNAL_ERROR", code, message, details)
}

func a2aError(jsonrpcCode, httpStatus int, status, reason, code, message string, details map[string]any) *a2aProtocolError {
	if details == nil {
		details = map[string]any{}
	} else {
		details = cloneStringAnyMap(details)
	}
	details["code"] = code
	return &a2aProtocolError{
		code:       jsonrpcCode,
		httpStatus: httpStatus,
		status:     status,
		reason:     reason,
		message:    message,
		details:    details,
	}
}

func a2aErrorFromRuntimeHandler(handlerErr *runtimeHandlerError) *a2aProtocolError {
	if handlerErr == nil {
		return nil
	}
	details := cloneStringAnyMap(handlerErr.extras)
	switch handlerErr.code {
	case "unauthorized":
		return a2aUnauthenticated(handlerErr.code, handlerErr.message, details)
	case "forbidden":
		return a2aUnauthorized(handlerErr.code, handlerErr.message, details)
	case "unknown_receiver", "unknown_agent", "unknown_message", "unknown_delivery":
		return a2aTaskNotFound(handlerErr.code, handlerErr.message, details)
	case "invalid_content_type":
		return a2aError(a2aCodeContentType, http.StatusBadRequest, "INVALID_ARGUMENT", "UNSUPPORTED_CONTENT_TYPE", handlerErr.code, handlerErr.message, details)
	case "invalid_request", "invalid_to_agent_uuid", "invalid_to_agent_uri", "agent_ref_mismatch", "invalid_delivery_id", "invalid_timeout":
		return a2aInvalidParams(handlerErr.code, handlerErr.message, details)
	case "store_error", "id_generation_failed":
		return a2aInternal(handlerErr.code, handlerErr.message, details)
	default:
		if handlerErr.status == http.StatusBadRequest {
			return a2aInvalidParams(handlerErr.code, handlerErr.message, details)
		}
		if handlerErr.status == http.StatusUnauthorized {
			return a2aUnauthenticated(handlerErr.code, handlerErr.message, details)
		}
		if handlerErr.status == http.StatusForbidden {
			return a2aUnauthorized(handlerErr.code, handlerErr.message, details)
		}
		if handlerErr.status == http.StatusNotFound {
			return a2aTaskNotFound(handlerErr.code, handlerErr.message, details)
		}
		return a2aInternal(handlerErr.code, handlerErr.message, details)
	}
}

func a2aErrorFromStore(code, message string, err error) *a2aProtocolError {
	details := map[string]any{"store_error": err.Error()}
	switch {
	case errors.Is(err, store.ErrAgentNotFound), errors.Is(err, store.ErrMessageNotFound):
		return a2aTaskNotFound(code, message, details)
	case errors.Is(err, store.ErrNoTrustPath):
		return a2aUnauthorized(code, message, details)
	default:
		return a2aInternal(code, message, details)
	}
}

func (h *Handler) recordA2AAdapterUsage(agentUUID, action string, details map[string]any) {
	if strings.TrimSpace(agentUUID) == "" {
		return
	}
	entry := map[string]any{
		"adapter": a2aProtocolAdapter,
		"action":  action,
	}
	for key, value := range details {
		entry[key] = value
	}
	now := h.now().UTC()
	agent, err := h.control.RecordAgentSystemActivity(agentUUID, entry, now)
	if err != nil {
		return
	}
	h.publishCollectiveEvent(collectiveStreamEvent{
		At:        now,
		Category:  "a2a_adapter",
		Action:    action,
		AgentUUID: agent.AgentUUID,
		OrgID:     agent.OrgID,
		Details:   details,
	})
}

func a2aReadStringPath(root map[string]any, path string) string {
	if root == nil || strings.TrimSpace(path) == "" {
		return ""
	}
	var current any = root
	for _, segment := range strings.Split(path, ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = obj[segment]
	}
	return strings.TrimSpace(asStringAny(current))
}

func (req a2aSendMessageRequest) String() string {
	if req.Message == nil {
		return "A2A SendMessageRequest<nil>"
	}
	return fmt.Sprintf("A2A SendMessageRequest<message_id=%s>", req.Message.ID)
}
