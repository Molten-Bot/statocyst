package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"statocyst/internal/model"
	"statocyst/internal/store"
)

const (
	peerAuthHeaderID        = "X-Statocyst-Peer-Id"
	peerAuthHeaderTS        = "X-Statocyst-Timestamp"
	peerAuthHeaderSignature = "X-Statocyst-Signature"
	peerAuthSkew            = 5 * time.Minute
)

type createPeerRequest struct {
	PeerID           string `json:"peer_id,omitempty"`
	CanonicalBaseURL string `json:"canonical_base_url"`
	DeliveryBaseURL  string `json:"delivery_base_url"`
	SharedSecret     string `json:"shared_secret"`
}

type createRemoteOrgTrustRequest struct {
	LocalOrgID      string `json:"local_org_id"`
	PeerID          string `json:"peer_id"`
	RemoteOrgHandle string `json:"remote_org_handle"`
}

type createRemoteAgentTrustRequest struct {
	LocalAgentUUID string `json:"local_agent_uuid"`
	PeerID         string `json:"peer_id"`
	RemoteAgentURI string `json:"remote_agent_uri"`
}

type peerInboundEnvelope struct {
	Message model.Message `json:"message"`
}

func (h *Handler) requireSuperAdmin(w http.ResponseWriter, r *http.Request) (humanActor, bool) {
	actor, err := h.authenticateHuman(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid human auth")
		return humanActor{}, false
	}
	if !actor.IsSuperAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "statocyst admin required")
		return humanActor{}, false
	}
	return actor, true
}

func peerPayload(peer model.PeerInstance) map[string]any {
	return map[string]any{
		"peer_id":             peer.PeerID,
		"canonical_base_url":  peer.CanonicalBaseURL,
		"delivery_base_url":   peer.DeliveryBaseURL,
		"status":              peer.Status,
		"created_by":          peer.CreatedBy,
		"created_at":          peer.CreatedAt,
		"updated_at":          peer.UpdatedAt,
		"last_successful_at":  peer.LastSuccessfulAt,
		"last_failure_at":     peer.LastFailureAt,
		"last_failure_reason": peer.LastFailureReason,
	}
}

func (h *Handler) handleAdminPeers(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireSuperAdmin(w, r)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodGet:
		peers := h.control.ListPeerInstances()
		out := make([]map[string]any, 0, len(peers))
		for _, peer := range peers {
			out = append(out, peerPayload(peer))
		}
		writeJSON(w, http.StatusOK, map[string]any{"peers": out})
	case http.MethodPost:
		var req createPeerRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
			return
		}
		req.CanonicalBaseURL = normalizeCanonicalBaseURL(req.CanonicalBaseURL)
		req.DeliveryBaseURL = normalizeCanonicalBaseURL(req.DeliveryBaseURL)
		req.PeerID = strings.TrimSpace(req.PeerID)
		req.SharedSecret = strings.TrimSpace(req.SharedSecret)
		if req.CanonicalBaseURL == "" || req.DeliveryBaseURL == "" || req.SharedSecret == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "canonical_base_url, delivery_base_url, and shared_secret are required")
			return
		}
		peerID := req.PeerID
		if peerID == "" {
			var err error
			peerID, err = h.idFactory()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate peer_id")
				return
			}
		}
		peer, err := h.control.CreatePeerInstance(req.CanonicalBaseURL, req.DeliveryBaseURL, req.SharedSecret, actor.Human.HumanID, peerID, h.now().UTC())
		if err != nil {
			switch {
			case errors.Is(err, store.ErrPeerInstanceExists):
				writeError(w, http.StatusConflict, "peer_exists", "peer for canonical_base_url already exists")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to create peer")
			}
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"peer": peerPayload(peer)})
	default:
		writeMethodNotAllowed(w)
	}
}

func (h *Handler) handleAdminPeerByID(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireSuperAdmin(w, r)
	if !ok {
		return
	}
	peerID := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/"), "/v1/admin/peers/"))
	if peerID == "" || strings.Contains(peerID, "/") {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if r.Method != http.MethodDelete {
		writeMethodNotAllowed(w)
		return
	}
	peer, err := h.control.DeletePeerInstance(peerID, "", h.now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrPeerInstanceNotFound) {
			writeError(w, http.StatusNotFound, "unknown_peer", "peer_id is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to delete peer")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"peer": peerPayload(peer), "result": "deleted"})
}

func (h *Handler) handleAdminRemoteOrgTrusts(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireSuperAdmin(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"remote_org_trusts": h.control.ListRemoteOrgTrusts()})
	case http.MethodPost:
		var req createRemoteOrgTrustRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
			return
		}
		req.LocalOrgID = strings.TrimSpace(req.LocalOrgID)
		req.PeerID = strings.TrimSpace(req.PeerID)
		req.RemoteOrgHandle = normalizeHandle(req.RemoteOrgHandle)
		if req.LocalOrgID == "" || req.PeerID == "" || !validateHandle(req.RemoteOrgHandle) {
			writeError(w, http.StatusBadRequest, "invalid_request", "local_org_id, peer_id, and valid remote_org_handle are required")
			return
		}
		trustID, err := h.idFactory()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate trust_id")
			return
		}
		trust, err := h.control.CreateRemoteOrgTrust(req.LocalOrgID, req.PeerID, req.RemoteOrgHandle, actor.Human.HumanID, trustID, h.now().UTC())
		if err != nil {
			switch {
			case errors.Is(err, store.ErrOrgNotFound):
				writeError(w, http.StatusNotFound, "unknown_org", "local_org_id is not registered")
			case errors.Is(err, store.ErrPeerInstanceNotFound):
				writeError(w, http.StatusNotFound, "unknown_peer", "peer_id is not registered")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to create remote org trust")
			}
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"remote_org_trust": trust})
	default:
		writeMethodNotAllowed(w)
	}
}

func (h *Handler) handleAdminRemoteOrgTrustByID(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireSuperAdmin(w, r)
	if !ok {
		return
	}
	trustID := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/"), "/v1/admin/remote-org-trusts/"))
	if trustID == "" || strings.Contains(trustID, "/") {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if r.Method != http.MethodDelete {
		writeMethodNotAllowed(w)
		return
	}
	trust, err := h.control.DeleteRemoteOrgTrust(trustID, "", h.now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrRemoteOrgTrustNotFound) {
			writeError(w, http.StatusNotFound, "unknown_remote_org_trust", "trust_id is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to delete remote org trust")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"remote_org_trust": trust, "result": "deleted"})
}

func (h *Handler) handleAdminRemoteAgentTrusts(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireSuperAdmin(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"remote_agent_trusts": h.control.ListRemoteAgentTrusts()})
	case http.MethodPost:
		var req createRemoteAgentTrustRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
			return
		}
		req.LocalAgentUUID = normalizeUUID(req.LocalAgentUUID)
		req.PeerID = strings.TrimSpace(req.PeerID)
		req.RemoteAgentURI = strings.TrimSpace(req.RemoteAgentURI)
		if !validateUUID(req.LocalAgentUUID) || req.PeerID == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "valid local_agent_uuid and peer_id are required")
			return
		}
		peer, err := h.control.GetPeerInstance(req.PeerID)
		if err != nil {
			writeError(w, http.StatusNotFound, "unknown_peer", "peer_id is not registered")
			return
		}
		agentBase, _, err := splitCanonicalAgentURI(req.RemoteAgentURI)
		if err != nil || agentBase != peer.CanonicalBaseURL {
			writeError(w, http.StatusBadRequest, "invalid_remote_agent_uri", "remote_agent_uri must be a canonical agent URI for the selected peer")
			return
		}
		trustID, err := h.idFactory()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "id_generation_failed", "failed to generate trust_id")
			return
		}
		trust, err := h.control.CreateRemoteAgentTrust(req.LocalAgentUUID, req.PeerID, req.RemoteAgentURI, actor.Human.HumanID, trustID, h.now().UTC())
		if err != nil {
			switch {
			case errors.Is(err, store.ErrAgentNotFound):
				writeError(w, http.StatusNotFound, "unknown_agent", "local_agent_uuid is not registered")
			case errors.Is(err, store.ErrPeerInstanceNotFound):
				writeError(w, http.StatusNotFound, "unknown_peer", "peer_id is not registered")
			default:
				writeError(w, http.StatusInternalServerError, "store_error", "failed to create remote agent trust")
			}
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"remote_agent_trust": trust})
	default:
		writeMethodNotAllowed(w)
	}
}

func (h *Handler) handleAdminRemoteAgentTrustByID(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireSuperAdmin(w, r)
	if !ok {
		return
	}
	trustID := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/"), "/v1/admin/remote-agent-trusts/"))
	if trustID == "" || strings.Contains(trustID, "/") {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if r.Method != http.MethodDelete {
		writeMethodNotAllowed(w)
		return
	}
	trust, err := h.control.DeleteRemoteAgentTrust(trustID, "", h.now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrRemoteAgentTrustNotFound) {
			writeError(w, http.StatusNotFound, "unknown_remote_agent_trust", "trust_id is not registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_error", "failed to delete remote agent trust")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"remote_agent_trust": trust, "result": "deleted"})
}

func (h *Handler) processPeerOutboxes(ctx context.Context, limit int) {
	now := h.now().UTC()
	outbounds := h.control.ListDuePeerOutbounds(now, limit)
	for _, outbound := range outbounds {
		peer, err := h.control.GetPeerInstance(outbound.PeerID)
		if err != nil {
			continue
		}
		body, err := json.Marshal(peerInboundEnvelope{Message: outbound.Message})
		if err != nil {
			_, _ = h.control.MarkPeerOutboundRetry(outbound.OutboundID, err.Error(), nextPeerAttempt(now, outbound.AttemptCount), now)
			continue
		}
		targetURL := strings.TrimRight(peer.DeliveryBaseURL, "/") + "/v1/peer/messages"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(string(body)))
		if err != nil {
			_, _ = h.control.MarkPeerOutboundRetry(outbound.OutboundID, err.Error(), nextPeerAttempt(now, outbound.AttemptCount), now)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		signPeerRequest(req, peer.SharedSecret, peer.PeerID, body, now)
		resp, err := h.peerHTTPClient.Do(req)
		if err != nil {
			h.control.RecordPeerDeliveryFailure(peer.PeerID, err.Error(), now)
			_, _ = h.control.MarkPeerOutboundRetry(outbound.OutboundID, err.Error(), nextPeerAttempt(now, outbound.AttemptCount), now)
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
			reason := strings.TrimSpace(string(data))
			if reason == "" {
				reason = fmt.Sprintf("peer returned %d", resp.StatusCode)
			}
			h.control.RecordPeerDeliveryFailure(peer.PeerID, reason, now)
			_, _ = h.control.MarkPeerOutboundRetry(outbound.OutboundID, reason, nextPeerAttempt(now, outbound.AttemptCount), now)
			continue
		}
		h.control.RecordPeerDeliverySuccess(peer.PeerID, now)
		_, _ = h.control.MarkPeerOutboundDelivered(outbound.OutboundID, now)
		_, _ = h.control.MarkMessageForwarded(outbound.MessageID, now)
	}
}

func (h *Handler) handlePeerInboundMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	peerID := strings.TrimSpace(r.Header.Get(peerAuthHeaderID))
	tsRaw := strings.TrimSpace(r.Header.Get(peerAuthHeaderTS))
	signature := strings.TrimSpace(r.Header.Get(peerAuthHeaderSignature))
	if peerID == "" || tsRaw == "" || signature == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing peer authentication headers")
		return
	}
	peer, err := h.control.GetPeerInstance(peerID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unknown peer")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "failed to read request body")
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, tsRaw)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid peer timestamp")
		return
	}
	if delta := h.now().UTC().Sub(ts); delta > peerAuthSkew || delta < -peerAuthSkew {
		writeError(w, http.StatusUnauthorized, "unauthorized", "peer timestamp outside allowed skew")
		return
	}
	if !verifyPeerSignature(peer.SharedSecret, tsRaw, r.Method, r.URL.Path, body, signature) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid peer signature")
		return
	}

	var envelope peerInboundEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	msg := envelope.Message
	if msg.MessageID == "" || msg.FromAgentURI == "" || msg.ToAgentURI == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "message_id, from_agent_uri, and to_agent_uri are required")
		return
	}
	if _, ok := allowedContentTypes[strings.TrimSpace(msg.ContentType)]; !ok {
		writeError(w, http.StatusBadRequest, "invalid_content_type", "content_type must be one of: text/plain, application/json")
		return
	}
	targetBase, _, err := splitCanonicalAgentURI(msg.ToAgentURI)
	if err != nil || targetBase != normalizeCanonicalBaseURL(h.canonicalBaseURL) {
		writeError(w, http.StatusBadRequest, "invalid_to_agent_uri", "to_agent_uri must target this statocyst instance")
		return
	}
	receiverAgentUUID, err := h.control.ResolveAgentUUIDByURI(msg.ToAgentURI)
	if err != nil {
		writeError(w, http.StatusNotFound, "unknown_receiver", "to_agent_uri is not registered")
		return
	}
	receiver, err := h.control.GetAgentByUUID(receiverAgentUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "unknown_receiver", "to_agent_uri is not registered")
		return
	}
	senderBase, senderAgentRef, err := splitCanonicalAgentURI(msg.FromAgentURI)
	if err != nil || senderBase != peer.CanonicalBaseURL {
		writeError(w, http.StatusBadRequest, "invalid_from_agent_uri", "from_agent_uri must match the authenticated peer")
		return
	}
	senderOrgHandle := remoteOrgHandleFromAgentRef(senderAgentRef)
	if senderOrgHandle == "" {
		writeError(w, http.StatusBadRequest, "invalid_from_agent_uri", "from_agent_uri must include a valid org-scoped agent ref")
		return
	}
	if !h.control.HasActiveRemoteOrgTrust(receiver.OrgID, peer.PeerID, senderOrgHandle) || !h.control.HasActiveRemoteAgentTrust(receiver.AgentUUID, peer.PeerID, msg.FromAgentURI) {
		writeError(w, http.StatusForbidden, "forbidden", "no federated trust path")
		return
	}

	inboundMessage := msg
	inboundMessage.ToAgentUUID = receiver.AgentUUID
	inboundMessage.ToAgentID = receiver.AgentID
	inboundMessage.ReceiverOrgID = receiver.OrgID
	inboundMessage.ReceiverPeerID = ""
	inboundMessage.ToAgentURI = h.agentURI(receiver)
	if inboundMessage.CreatedAt.IsZero() {
		inboundMessage.CreatedAt = h.now().UTC()
	}
	record, replay, err := h.control.CreateOrGetMessageRecord(inboundMessage, h.now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to register inbound message")
		return
	}
	if replay {
		writeJSON(w, http.StatusOK, map[string]any{"status": "duplicate", "message_id": record.Message.MessageID})
		return
	}
	if err := h.queue.Enqueue(r.Context(), inboundMessage); err != nil {
		_ = h.control.AbortMessageRecord(inboundMessage.MessageID)
		writeError(w, http.StatusInternalServerError, "store_error", "failed to enqueue inbound message")
		return
	}
	h.waiters.Notify(receiver.AgentUUID)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "message_id": inboundMessage.MessageID})
}

func signPeerRequest(req *http.Request, secret, peerID string, body []byte, now time.Time) {
	ts := now.UTC().Format(time.RFC3339Nano)
	req.Header.Set(peerAuthHeaderID, peerID)
	req.Header.Set(peerAuthHeaderTS, ts)
	req.Header.Set(peerAuthHeaderSignature, computePeerSignature(secret, ts, req.Method, req.URL.Path, body))
}

func verifyPeerSignature(secret, timestamp, method, path string, body []byte, provided string) bool {
	expected := computePeerSignature(secret, timestamp, method, path, body)
	return hmac.Equal([]byte(expected), []byte(strings.TrimSpace(provided)))
}

func computePeerSignature(secret, timestamp, method, path string, body []byte) string {
	sum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.ToUpper(strings.TrimSpace(method))))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(path)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(hex.EncodeToString(sum[:])))
	return hex.EncodeToString(mac.Sum(nil))
}

func nextPeerAttempt(now time.Time, attemptCount int) time.Time {
	backoff := time.Second
	switch {
	case attemptCount >= 4:
		backoff = 30 * time.Second
	case attemptCount == 3:
		backoff = 10 * time.Second
	case attemptCount == 2:
		backoff = 5 * time.Second
	case attemptCount == 1:
		backoff = 2 * time.Second
	}
	return now.Add(backoff)
}

func splitCanonicalAgentURI(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", "", errors.New("missing scheme or host")
	}
	path := strings.Trim(strings.TrimSpace(parsed.Path), "/")
	if path == "" {
		return "", "", errors.New("invalid agent uri path")
	}
	if strings.HasPrefix(path, "orgs/") || strings.HasPrefix(path, "humans/") {
		return "", "", errors.New("invalid agent uri path")
	}
	ref, err := url.PathUnescape(path)
	if err != nil {
		return "", "", err
	}
	if !validateAgentRef(ref) {
		return "", "", errors.New("invalid agent ref")
	}
	return normalizeCanonicalBaseURL(parsed.Scheme + "://" + parsed.Host), ref, nil
}

func remoteOrgHandleFromAgentRef(agentRef string) string {
	parts := strings.Split(strings.Trim(agentRef, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return normalizeHandle(parts[0])
}
